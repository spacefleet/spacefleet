// Package variables holds the workflow-variable use cases: defining named
// key/value pairs that are injected into an application's component jobs as
// environment variables, at three levels — group-level (every application in an
// application group), app-level (every component of one application), and
// component-level (one component) — plus resolving the merged set for a run. The
// three form a precedence chain, lowest to highest: group < app < component, so
// a more specific level overrides a same-named variable from a broader one.
//
// Like every org-scoped resource, every query is scoped by organization id (and
// the group/application id) — that scoping, not the handler's membership check,
// is the security boundary. A sensitive variable's value is envelope-encrypted
// (see lib/secrets) before it touches the database and is never returned to
// callers: handlers map *ent.Variable / *ent.GroupVariable to an API type that
// omits it; only ResolveEnv (used by a run) decrypts it. A non-secret value is
// stored and returned in plaintext.
package variables

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/application"
	"github.com/spacefleet/spacefleet/ent/applicationgroup"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/ent/groupvariable"
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

// ResolveEnv returns the merged environment for one component's job, applying the
// three variable levels in precedence order — group (the application's group, if
// any), then app-level, then the component's own — each overriding a same-named
// variable from a broader level. The two returned maps are disjoint — non-secret
// values in plain, sensitive (decrypted) values in secret — and a name appears in
// exactly one, taking the winning level's definition (value and sensitivity). It
// is the only path that opens a sealed value, and its result never reaches an API
// response.
func (s *Service) ResolveEnv(ctx context.Context, orgID, appID, componentID uuid.UUID) (plain map[string]string, secret map[string]string, err error) {
	plain = map[string]string{}
	secret = map[string]string{}
	// set places a resolved variable into the right map and clears it from the
	// other, so a higher-priority override that flips sensitivity leaves the name
	// in exactly one map.
	set := func(name, value string, sensitive bool) {
		if sensitive {
			secret[name] = value
			delete(plain, name)
		} else {
			plain[name] = value
			delete(secret, name)
		}
	}

	// Level 1 (lowest): the variables of the application's group, if it is in one.
	groupID, err := s.appGroupID(ctx, orgID, appID)
	if err != nil {
		return nil, nil, err
	}
	if groupID != uuid.Nil {
		grows, err := s.ent.GroupVariable.Query().
			Where(groupvariable.OrganizationID(orgID), groupvariable.GroupID(groupID)).
			Order(ent.Asc(groupvariable.FieldCreatedAt)).
			All(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range grows {
			val, serr := s.resolveGroupValue(r)
			if serr != nil {
				return nil, nil, serr
			}
			set(r.Name, val, r.Sensitive)
		}
	}

	// Levels 2 and 3: app-level then component-level, from the variables table.
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
	// Apply app-level rows first (component_id NULL), then component-level rows,
	// so a component variable overrides an app one — which in turn overrode any
	// group variable — of the same name.
	for _, r := range rows {
		if r.ComponentID != nil {
			continue
		}
		val, serr := s.resolveValue(r)
		if serr != nil {
			return nil, nil, serr
		}
		set(r.Name, val, r.Sensitive)
	}
	for _, r := range rows {
		if r.ComponentID == nil {
			continue
		}
		val, serr := s.resolveValue(r)
		if serr != nil {
			return nil, nil, serr
		}
		set(r.Name, val, r.Sensitive)
	}
	return plain, secret, nil
}

// resolveValue returns a Variable's effective value: the plaintext for a
// non-secret one, or the decrypted blob for a sensitive one.
func (s *Service) resolveValue(v *ent.Variable) (string, error) {
	if !v.Sensitive {
		return v.Value, nil
	}
	return s.open(v.EncryptedValue)
}

// resolveGroupValue is resolveValue for a group-level GroupVariable.
func (s *Service) resolveGroupValue(v *ent.GroupVariable) (string, error) {
	if !v.Sensitive {
		return v.Value, nil
	}
	return s.open(v.EncryptedValue)
}

// appGroupID returns the group id of the org-scoped application, or uuid.Nil when
// the application sits at the org root (or no longer exists — a run with no group
// variables to merge).
func (s *Service) appGroupID(ctx context.Context, orgID, appID uuid.UUID) (uuid.UUID, error) {
	a, err := s.ent.Application.Query().
		Where(application.OrganizationID(orgID), application.ID(appID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return uuid.Nil, nil
		}
		return uuid.Nil, err
	}
	return a.GroupID, nil
}

// --- Group-level variables ---
//
// The group level mirrors the app level (List/Create/Update/Delete) but scopes
// by group id instead of application id and has no component dimension; the
// validation, sealing, and uniqueness handling are identical.

// ListGroup returns a group's variables, oldest first.
func (s *Service) ListGroup(ctx context.Context, orgID, groupID uuid.UUID) ([]*ent.GroupVariable, error) {
	return s.groupScopeQuery(orgID, groupID).
		Order(ent.Asc(groupvariable.FieldCreatedAt)).
		All(ctx)
}

// CreateGroup adds a variable to the group, sealing the value when sensitive. The
// group must exist in the organization.
func (s *Service) CreateGroup(ctx context.Context, orgID, groupID uuid.UUID, p CreateParams) (*ent.GroupVariable, error) {
	if !nameRe.MatchString(p.Name) {
		return nil, validationErr("variable name %q must be a letter or underscore followed by letters, digits, or underscores", p.Name)
	}
	if p.Sensitive && p.Value == "" {
		return nil, validationErr("a sensitive variable's value cannot be empty")
	}
	ok, err := s.ent.ApplicationGroup.Query().
		Where(applicationgroup.OrganizationID(orgID), applicationgroup.ID(groupID)).
		Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, validationErr("application group not found")
	}

	create := s.ent.GroupVariable.Create().
		SetOrganizationID(orgID).
		SetGroupID(groupID).
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
			return nil, validationErr("a variable named %q already exists in this group", p.Name)
		}
		return nil, err
	}
	return v, nil
}

// UpdateGroup changes a group variable. The value is re-sealed (sensitive) or
// replaced (non-secret) only when supplied; the sensitive flag is immutable.
func (s *Service) UpdateGroup(ctx context.Context, orgID, groupID, id uuid.UUID, p UpdateParams) (*ent.GroupVariable, error) {
	v, err := s.getGroup(ctx, orgID, groupID, id)
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
			return nil, validationErr("a variable named %q already exists in this group", *p.Name)
		}
		return nil, err
	}
	return out, nil
}

// DeleteGroup removes a variable scoped to the org/group.
func (s *Service) DeleteGroup(ctx context.Context, orgID, groupID, id uuid.UUID) error {
	v, err := s.getGroup(ctx, orgID, groupID, id)
	if err != nil {
		return err
	}
	return s.ent.GroupVariable.DeleteOne(v).Exec(ctx)
}

// getGroup fetches one group variable scoped to the org/group, or ent's
// NotFoundError.
func (s *Service) getGroup(ctx context.Context, orgID, groupID, id uuid.UUID) (*ent.GroupVariable, error) {
	return s.groupScopeQuery(orgID, groupID).
		Where(groupvariable.ID(id)).
		Only(ctx)
}

// groupScopeQuery builds the org/group-scoped base query.
func (s *Service) groupScopeQuery(orgID, groupID uuid.UUID) *ent.GroupVariableQuery {
	return s.ent.GroupVariable.Query().
		Where(groupvariable.OrganizationID(orgID), groupvariable.GroupID(groupID))
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

// open decrypts a sensitive variable's stored value blob, or returns "" when
// none is set. A NULL column can surface as a non-nil empty slice, so emptiness —
// not just nil — means "no value". Shared by the app/component Variable and the
// group-level GroupVariable, whose encrypted_value columns are identical.
func (s *Service) open(encrypted *[]byte) (string, error) {
	if encrypted == nil || len(*encrypted) == 0 {
		return "", nil
	}
	plain, err := s.sealer.Open(*encrypted)
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
