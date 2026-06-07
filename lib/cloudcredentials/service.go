// Package cloudcredentials holds the cloud-credential use cases: registering a
// named set of cloud-provider credentials (AWS, GCP, Azure) for an
// organization, listing/fetching/updating/deleting them, and resolving
// (decrypting) one for a consumer. Surfaced in the UI as "Cloud Credentials".
// It is the foundation other features build on — cluster registration, pulling
// private packages in workflows, etc. — so the credential itself only stores
// and seals the secret; consumers decrypt it on demand via Resolve.
//
// Like every org-scoped resource, every query is scoped by organization id —
// that scoping, not the handler's membership check, is the security boundary.
// The secret material is envelope-encrypted before it touches the database and
// is never returned to callers: handlers map *ent.CloudCredential to an API type
// that omits it, and only Resolve decrypts it.
package cloudcredentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/cloudcredential"
	"github.com/spacefleet/spacefleet/lib/secrets"
)

// Non-secret config keys (stored in the config map, safe to display) and sealed
// credential keys (stored inside the encrypted blob). Shared so the API field
// builder and future consumers agree on the shape.
const (
	ConfigKeyAWSRegion         = "region"
	ConfigKeyAWSRoleARN        = "role_arn"
	ConfigKeyGCPProject        = "project"
	ConfigKeyAzureTenantID     = "tenant_id"
	ConfigKeyAzureClientID     = "client_id"
	ConfigKeyAzureSubscription = "subscription_id"

	CredKeyAWSAccessKeyID    = "access_key_id"
	CredKeyAWSSecretKey      = "secret_access_key"
	CredKeyAWSSessionToken   = "session_token"
	CredKeyGCPServiceKey     = "service_account_key"
	CredKeyAzureClientSecret = "client_secret"
)

// Service is a thin wrapper over the ent client plus the credential sealer.
type Service struct {
	ent    *ent.Client
	sealer *secrets.Sealer
}

func NewService(entClient *ent.Client, sealer *secrets.Sealer) *Service {
	return &Service{ent: entClient, sealer: sealer}
}

// ValidationError is a client-input error (bad/missing fields) the handler maps
// to 400.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

// IsValidation reports whether err is a ValidationError.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

func validationErr(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// CreateParams describes a cloud credential to register. Config holds the
// non-secret identifiers; Credentials is the raw secret blob (a JSON map) the
// service seals.
type CreateParams struct {
	Name        string
	Description string
	Provider    cloudcredential.Provider
	Config      map[string]string
	Credentials []byte
}

// UpdateParams describes a change to a cloud credential. A nil field is left
// unchanged. The provider is fixed at registration. Supplying Credentials
// re-seals the secret; Config replaces the stored identifiers.
type UpdateParams struct {
	Name        *string
	Description *string
	Config      *map[string]string
	Credentials []byte
}

// Resolved is a decrypted cloud credential, returned only to a consumer (e.g.
// cluster registration or a workflow) — never to an API caller. Secrets is the
// decrypted credential map; Config is the non-secret identifiers.
type Resolved struct {
	Provider cloudcredential.Provider
	Config   map[string]string
	Secrets  map[string]string
}

// List returns the organization's cloud credentials, oldest first.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]*ent.CloudCredential, error) {
	return s.ent.CloudCredential.Query().
		Where(cloudcredential.OrganizationID(orgID)).
		Order(ent.Asc(cloudcredential.FieldCreatedAt)).
		All(ctx)
}

// Get returns one cloud credential scoped to the organization, or ent's
// NotFoundError.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (*ent.CloudCredential, error) {
	return s.ent.CloudCredential.Query().
		Where(cloudcredential.OrganizationID(orgID), cloudcredential.ID(id)).
		Only(ctx)
}

// Create registers a cloud credential, sealing the secret material.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, p CreateParams) (*ent.CloudCredential, error) {
	if len(p.Credentials) == 0 {
		return nil, validationErr("credentials are required")
	}
	create := s.ent.CloudCredential.Create().
		SetOrganizationID(orgID).
		SetName(p.Name).
		SetProvider(p.Provider).
		SetDescription(p.Description)
	if p.Config != nil {
		create.SetConfig(p.Config)
	}
	sealed, err := s.sealer.Seal(p.Credentials)
	if err != nil {
		return nil, err
	}
	create.SetEncryptedCredentials(sealed)
	return create.Save(ctx)
}

// Update changes mutable fields of a cloud credential scoped to the
// organization; the provider is fixed at registration. The secret is re-sealed
// only when new credentials are supplied.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, p UpdateParams) (*ent.CloudCredential, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	upd := c.Update()
	if p.Name != nil {
		if *p.Name == "" {
			return nil, validationErr("name cannot be empty")
		}
		upd.SetName(*p.Name)
	}
	if p.Description != nil {
		upd.SetDescription(*p.Description)
	}
	if p.Config != nil {
		upd.SetConfig(*p.Config)
	}
	if len(p.Credentials) > 0 {
		sealed, err := s.sealer.Seal(p.Credentials)
		if err != nil {
			return nil, err
		}
		upd.SetEncryptedCredentials(sealed)
	}
	return upd.Save(ctx)
}

// Delete removes a cloud credential scoped to the organization.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	return s.ent.CloudCredential.DeleteOne(c).Exec(ctx)
}

// Resolve returns the decrypted credential scoped to the organization, for a
// consumer to authenticate to the cloud. It is the only path that opens the
// sealed blob, and its result never reaches an API response.
func (s *Service) Resolve(ctx context.Context, orgID, id uuid.UUID) (Resolved, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return Resolved{}, err
	}
	secretsMap, err := s.openCreds(c)
	if err != nil {
		return Resolved{}, err
	}
	cfg := c.Config
	if cfg == nil {
		cfg = map[string]string{}
	}
	return Resolved{Provider: c.Provider, Config: cfg, Secrets: secretsMap}, nil
}

// openCreds decrypts and decodes the stored credential blob into its secret
// map, or returns an empty map when none is set. A NULL column can surface as a
// non-nil empty slice, so emptiness — not just nil — means "no credentials".
func (s *Service) openCreds(c *ent.CloudCredential) (map[string]string, error) {
	if c.EncryptedCredentials == nil || len(*c.EncryptedCredentials) == 0 {
		return map[string]string{}, nil
	}
	plain, err := s.sealer.Open(*c.EncryptedCredentials)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(plain, &m); err != nil {
		return nil, err
	}
	return m, nil
}
