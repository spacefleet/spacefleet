// Package users provisions and reads the local user records that mirror OIDC
// identities. The HTTP layer authenticates a request to an OIDC subject (see
// lib/auth); this service turns that subject into a durable *ent.User that
// organization membership can reference.
package users

import (
	"context"
	"time"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/user"
)

// Service is a thin wrapper over the ent client, mirroring the shape of the
// other domain services.
type Service struct {
	ent *ent.Client
}

func NewService(entClient *ent.Client) *Service {
	return &Service{ent: entClient}
}

// EnsureUser returns the local user for an OIDC subject, creating it on first
// sight and keeping the email in sync on subsequent logins. The upsert is
// atomic, so concurrent first-login requests for the same subject can't create
// duplicate rows.
func (s *Service) EnsureUser(ctx context.Context, subject, email string) (*ent.User, error) {
	id, err := s.ent.User.Create().
		SetOidcSubject(subject).
		SetEmail(email).
		OnConflictColumns(user.FieldOidcSubject).
		Update(func(u *ent.UserUpsert) {
			u.SetEmail(email)
			u.SetUpdatedAt(time.Now())
		}).
		ID(ctx)
	if err != nil {
		return nil, err
	}
	return s.ent.User.Get(ctx, id)
}
