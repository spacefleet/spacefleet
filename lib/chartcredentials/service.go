// Package chartcredentials holds the chart-credential use cases: registering a
// named credential set for pulling private Helm charts in an organization,
// listing/fetching/updating/deleting them, and resolving (decrypting) one for a
// rollout. Surfaced in the UI as "Private Charts". It is a thin wrapper over the
// ent client that also owns password encryption (lib/secrets).
//
// Like every org-scoped resource, every query is scoped by organization id —
// that scoping, not the handler's membership check, is the security boundary.
// The password is envelope-encrypted before it touches the database and is never
// returned to callers — handlers map *ent.ChartCredential to an API type that
// omits it; only Resolve (used by the rollout) decrypts it.
package chartcredentials

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/chartcredential"
	"github.com/spacefleet/spacefleet/lib/secrets"
)

// ErrInUse is returned by Delete when the credential is still attached to a
// workflow component: the FK is ON DELETE RESTRICT, so the database refuses the
// delete. The handler maps it to 409. (ent surfaces a delete-time FK violation
// as the raw driver error, not *ent.ConstraintError, so we classify it here.)
var ErrInUse = errors.New("chart credential is attached to a workflow component")

// Service is a thin wrapper over the ent client plus the credential sealer.
type Service struct {
	ent    *ent.Client
	sealer *secrets.Sealer
}

func NewService(entClient *ent.Client, sealer *secrets.Sealer) *Service {
	return &Service{ent: entClient, sealer: sealer}
}

// ValidationError is a client-input error (bad/missing fields, an unknown type)
// the handler maps to 400.
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

// CreateParams describes a chart credential to register.
type CreateParams struct {
	Name     string
	Username string
	Password string
}

// UpdateParams describes a change to a chart credential. A nil field is left
// unchanged. A non-nil empty Password is rejected (a credential always needs
// one); to rotate, pass the new value.
type UpdateParams struct {
	Name     *string
	Username *string
	Password *string
}

// Resolved is a decrypted chart credential, returned only to the rollout (via
// the applications service's resolver seam) — never to an API caller.
type Resolved struct {
	Username string
	Password string
}

// List returns the organization's chart credentials, oldest first.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]*ent.ChartCredential, error) {
	return s.ent.ChartCredential.Query().
		Where(chartcredential.OrganizationID(orgID)).
		Order(ent.Asc(chartcredential.FieldCreatedAt)).
		All(ctx)
}

// Get returns one chart credential scoped to the organization, or ent's
// NotFoundError.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (*ent.ChartCredential, error) {
	return s.ent.ChartCredential.Query().
		Where(chartcredential.OrganizationID(orgID), chartcredential.ID(id)).
		Only(ctx)
}

// Create registers a chart credential, sealing the password.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, p CreateParams) (*ent.ChartCredential, error) {
	if p.Password == "" {
		return nil, validationErr("password is required")
	}
	create := s.ent.ChartCredential.Create().
		SetOrganizationID(orgID).
		SetName(p.Name).
		SetUsername(p.Username)
	sealed, err := s.sealer.Seal([]byte(p.Password))
	if err != nil {
		return nil, err
	}
	create.SetEncryptedPassword(sealed)
	return create.Save(ctx)
}

// Update changes mutable fields of a chart credential scoped to the
// organization; the type is fixed at registration. The password is re-sealed
// only when a new one is supplied.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, p UpdateParams) (*ent.ChartCredential, error) {
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
	if p.Username != nil {
		upd.SetUsername(*p.Username)
	}
	if p.Password != nil {
		if *p.Password == "" {
			return nil, validationErr("password cannot be empty")
		}
		sealed, err := s.sealer.Seal([]byte(*p.Password))
		if err != nil {
			return nil, err
		}
		upd.SetEncryptedPassword(sealed)
	}
	return upd.Save(ctx)
}

// Delete removes a chart credential scoped to the organization. A credential
// still attached to a workflow component fails with an ent constraint error (the
// FK is ON DELETE RESTRICT), which the handler maps to 409.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	if err := s.ent.ChartCredential.DeleteOne(c).Exec(ctx); err != nil {
		if isForeignKeyViolation(err) {
			return ErrInUse
		}
		return err
	}
	return nil
}

// isForeignKeyViolation reports whether err is a Postgres integrity-constraint
// violation from a referencing row — a RESTRICT (23001) or NO ACTION / plain FK
// (23503) rejection — which for a delete means the row is still referenced.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23001" || pgErr.Code == "23503"
}

// Resolve returns the decrypted credential scoped to the organization, for the
// rollout to authenticate the chart pull. It is the only path that opens the
// sealed password, and its result never reaches an API response.
func (s *Service) Resolve(ctx context.Context, orgID, id uuid.UUID) (Resolved, error) {
	c, err := s.Get(ctx, orgID, id)
	if err != nil {
		return Resolved{}, err
	}
	password, err := s.openPassword(c)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Username: c.Username,
		Password: password,
	}, nil
}

// openPassword decrypts the stored password blob, or returns "" when none is
// set. A NULL column can surface as a non-nil empty slice, so emptiness — not
// just nil — means "no password".
func (s *Service) openPassword(c *ent.ChartCredential) (string, error) {
	if c.EncryptedPassword == nil || len(*c.EncryptedPassword) == 0 {
		return "", nil
	}
	plain, err := s.sealer.Open(*c.EncryptedPassword)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
