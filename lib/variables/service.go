// Package variables holds the workflow-variable use cases: defining named
// key/value pairs that are injected into an application's component jobs as
// environment variables, at two levels — app-level (every component) and
// component-level (one component, overriding an app-level variable of the same
// name) — plus resolving the merged set for a run.
//
// Like every org-scoped resource, every query is scoped by organization id (and
// application id) — that scoping, not the handler's membership check, is the
// security boundary. A sensitive variable's value is envelope-encrypted (see
// lib/secrets) before it touches the database and is never returned to callers:
// handlers map *ent.Variable to an API type that omits it; only ResolveEnv (used
// by a run) decrypts it. A non-secret value is stored and returned in plaintext.
package variables

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/ent/variable"
	"github.com/spacefleet/spacefleet/lib/secrets"
)

// nameRe is a shell-safe environment-variable identifier: a letter or underscore
// followed by letters, digits, or underscores.
var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Service is a thin wrapper over the ent client plus the credential sealer.
type Service struct {
	ent    *ent.Client
	sealer *secrets.Sealer
}

func NewService(entClient *ent.Client, sealer *secrets.Sealer) *Service {
	return &Service{ent: entClient, sealer: sealer}
}

// ValidationError is a client-input error (bad name, duplicate, empty sensitive
// value, missing component) the handler maps to 400.
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

// CreateParams describes a variable to add.
type CreateParams struct {
	Name      string
	Sensitive bool
	Value     string
}

// UpdateParams describes a change to a variable. A nil field is left unchanged;
// in particular omitting Value keeps the existing (possibly sealed) value. The
// sensitive flag is fixed at creation.
type UpdateParams struct {
	Name  *string
	Value *string
}

// List returns the variables in the given scope, oldest first. A nil componentID
// lists the app-level variables (component_id IS NULL); a non-nil one lists that
// component's variables.
func (s *Service) List(ctx context.Context, orgID, appID uuid.UUID, componentID *uuid.UUID) ([]*ent.Variable, error) {
	return s.scopeQuery(orgID, appID, componentID).
		Order(ent.Asc(variable.FieldCreatedAt)).
		All(ctx)
}

// Create adds a variable to the scope, sealing the value when sensitive. A
// component-level variable requires the component to exist in the application.
func (s *Service) Create(ctx context.Context, orgID, appID uuid.UUID, componentID *uuid.UUID, p CreateParams) (*ent.Variable, error) {
	if !nameRe.MatchString(p.Name) {
		return nil, validationErr("variable name %q must be a letter or underscore followed by letters, digits, or underscores", p.Name)
	}
	if p.Sensitive && p.Value == "" {
		return nil, validationErr("a sensitive variable's value cannot be empty")
	}
	if componentID != nil {
		ok, err := s.ent.Component.Query().
			Where(component.OrganizationID(orgID), component.ApplicationID(appID), component.ID(*componentID)).
			Exist(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, validationErr("component not found in this application")
		}
	}

	create := s.ent.Variable.Create().
		SetOrganizationID(orgID).
		SetApplicationID(appID).
		SetNillableComponentID(componentID).
		SetName(p.Name).
		SetSensitive(p.Sensitive)
	if p.Sensitive {
		sealed, err := s.sealer.Seal([]byte(p.Value))
		if err != nil {
			return nil, err
		}
		create.SetEncryptedValue(sealed)
	} else {
		create.SetValue(p.Value)
	}
	v, err := create.Save(ctx)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, validationErr("a variable named %q already exists in this scope", p.Name)
		}
		return nil, err
	}
	return v, nil
}

// Update changes a variable scoped to the org/app (and component, if any). The
// value is re-sealed (sensitive) or replaced (non-secret) only when supplied;
// omitting it keeps the existing value. The sensitive flag is immutable.
func (s *Service) Update(ctx context.Context, orgID, appID uuid.UUID, componentID *uuid.UUID, id uuid.UUID, p UpdateParams) (*ent.Variable, error) {
	v, err := s.get(ctx, orgID, appID, componentID, id)
	if err != nil {
		return nil, err
	}
	upd := v.Update()
	if p.Name != nil {
		if !nameRe.MatchString(*p.Name) {
			return nil, validationErr("variable name %q must be a letter or underscore followed by letters, digits, or underscores", *p.Name)
		}
		upd.SetName(*p.Name)
	}
	if p.Value != nil {
		if v.Sensitive {
			if *p.Value == "" {
				return nil, validationErr("a sensitive variable's value cannot be empty")
			}
			sealed, err := s.sealer.Seal([]byte(*p.Value))
			if err != nil {
				return nil, err
			}
			upd.SetEncryptedValue(sealed)
		} else {
			upd.SetValue(*p.Value)
		}
	}
	out, err := upd.Save(ctx)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, validationErr("a variable named %q already exists in this scope", *p.Name)
		}
		return nil, err
	}
	return out, nil
}

// Delete removes a variable scoped to the org/app (and component, if any).
func (s *Service) Delete(ctx context.Context, orgID, appID uuid.UUID, componentID *uuid.UUID, id uuid.UUID) error {
	v, err := s.get(ctx, orgID, appID, componentID, id)
	if err != nil {
		return err
	}
	return s.ent.Variable.DeleteOne(v).Exec(ctx)
}

// ResolveEnv returns the merged environment for one component's job: the
// app-level variables overlaid by the component's own (which win on name). The
// two maps are disjoint — non-secret values in plain, sensitive (decrypted)
// values in secret — and a name appears in exactly one, taking the component's
// definition (value and sensitivity) when it overrides an app-level one. It is
// the only path that opens a sealed value, and its result never reaches an API
// response.
func (s *Service) ResolveEnv(ctx context.Context, orgID, appID, componentID uuid.UUID) (plain map[string]string, secret map[string]string, err error) {
	rows, err := s.ent.Variable.Query().
		Where(
			variable.OrganizationID(orgID),
			variable.ApplicationID(appID),
			variable.Or(variable.ComponentIDIsNil(), variable.ComponentID(componentID)),
		).
		Order(ent.Asc(variable.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Merge: app-level first, then component-level overrides by name. A
	// component-scoped row is kept over an app-scoped one of the same name.
	merged := make(map[string]*ent.Variable, len(rows))
	for _, r := range rows {
		if r.ComponentID == nil {
			if _, ok := merged[r.Name]; !ok {
				merged[r.Name] = r
			}
			continue
		}
		merged[r.Name] = r // component-scoped always wins
	}

	plain = map[string]string{}
	secret = map[string]string{}
	for name, r := range merged {
		if !r.Sensitive {
			plain[name] = r.Value
			continue
		}
		val, err := s.openValue(r)
		if err != nil {
			return nil, nil, err
		}
		secret[name] = val
	}
	return plain, secret, nil
}

// get fetches one variable scoped to the org/app (and component, if any), or
// ent's NotFoundError.
func (s *Service) get(ctx context.Context, orgID, appID uuid.UUID, componentID *uuid.UUID, id uuid.UUID) (*ent.Variable, error) {
	return s.scopeQuery(orgID, appID, componentID).
		Where(variable.ID(id)).
		Only(ctx)
}

// scopeQuery builds the org/app/component-scoped base query. A nil componentID
// scopes to app-level rows (component_id IS NULL).
func (s *Service) scopeQuery(orgID, appID uuid.UUID, componentID *uuid.UUID) *ent.VariableQuery {
	q := s.ent.Variable.Query().
		Where(variable.OrganizationID(orgID), variable.ApplicationID(appID))
	if componentID == nil {
		return q.Where(variable.ComponentIDIsNil())
	}
	return q.Where(variable.ComponentID(*componentID))
}

// openValue decrypts the stored value blob of a sensitive variable, or returns
// "" when none is set. A NULL column can surface as a non-nil empty slice, so
// emptiness — not just nil — means "no value".
func (s *Service) openValue(v *ent.Variable) (string, error) {
	if v.EncryptedValue == nil || len(*v.EncryptedValue) == 0 {
		return "", nil
	}
	plain, err := s.sealer.Open(*v.EncryptedValue)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (23505) — a duplicate variable name within a scope.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505"
}
