// Package notifications delivers run events to an organization's
// notification channels: an email address, a Slack incoming webhook, or a
// generic webhook, each subscribed to a set of event kinds and optionally
// limited to one application. Events come from lib/workflows (see
// workflows.OnEvent); Dispatch fans one out into a River delivery job per
// matching channel so each destination retries on its own.
//
// Like every org-scoped resource, every query is scoped by organization id
// — that scoping, not the handler's membership check, is the security
// boundary. A webhook URL is a secret (whoever holds it can post as the
// integration): it is sealed before it touches the database and is never
// returned to a caller; the API sees only the address (an email, or the
// webhook's host).
package notifications

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/notificationchannel"
	"github.com/spacefleet/spacefleet/lib/secrets"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// Channel kinds.
const (
	KindEmail   = "email"
	KindSlack   = "slack"
	KindWebhook = "webhook"
)

// EventTest is the synthetic event kind a "send a test" delivery carries;
// channels cannot subscribe to it.
const EventTest = "test"

// EnqueueFunc inserts a River job (the worker's or API's queue client).
type EnqueueFunc func(ctx context.Context, args river.JobArgs) error

// ErrNoQueue is returned by Test when no job queue is wired.
var ErrNoQueue = errors.New("notifications: background job worker not configured")

// Service is a thin wrapper over the ent client plus the sealer (for webhook
// URLs) and the queue (for deliveries).
type Service struct {
	ent     *ent.Client
	sealer  *secrets.Sealer
	enqueue EnqueueFunc
}

// NewService builds the service. enqueue may be nil on a process that only
// reads channels.
func NewService(entClient *ent.Client, sealer *secrets.Sealer, enqueue EnqueueFunc) *Service {
	return &Service{ent: entClient, sealer: sealer, enqueue: enqueue}
}

// ValidationError is a client-input error the handler maps to 400.
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

// CreateParams describes a channel to create. Target is the destination —
// an email address, or a webhook URL (sealed).
type CreateParams struct {
	Name          string
	Kind          string
	Target        string
	Events        []string
	ApplicationID uuid.UUID
}

// UpdateParams describes a change. A nil field is unchanged; a new Target
// re-seals the URL (or replaces the email address); ApplicationID set to
// uuid.Nil clears the application limit.
type UpdateParams struct {
	Name          *string
	Target        *string
	Events        *[]string
	ApplicationID *uuid.UUID
}

// List returns the organization's channels, oldest first.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]*ent.NotificationChannel, error) {
	return s.ent.NotificationChannel.Query().
		Where(notificationchannel.OrganizationID(orgID)).
		Order(ent.Asc(notificationchannel.FieldCreatedAt)).
		All(ctx)
}

// Get returns one channel scoped to the organization, or ent's NotFoundError.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (*ent.NotificationChannel, error) {
	return s.ent.NotificationChannel.Query().
		Where(notificationchannel.OrganizationID(orgID), notificationchannel.ID(id)).
		Only(ctx)
}

// Create validates and stores a channel, sealing a webhook URL.
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, p CreateParams) (*ent.NotificationChannel, error) {
	if strings.TrimSpace(p.Name) == "" {
		return nil, validationErr("name is required")
	}
	if err := validateEvents(p.Events); err != nil {
		return nil, err
	}
	address, sealed, err := s.prepareTarget(p.Kind, p.Target)
	if err != nil {
		return nil, err
	}
	create := s.ent.NotificationChannel.Create().
		SetOrganizationID(orgID).
		SetName(strings.TrimSpace(p.Name)).
		SetKind(notificationchannel.Kind(p.Kind)).
		SetAddress(address).
		SetEvents(p.Events)
	if sealed != nil {
		create.SetEncryptedTarget(sealed)
	}
	if p.ApplicationID != uuid.Nil {
		if err := s.assertApp(ctx, orgID, p.ApplicationID); err != nil {
			return nil, err
		}
		create.SetApplicationID(p.ApplicationID)
	}
	return create.Save(ctx)
}

// Update changes mutable fields of a channel scoped to the organization.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, p UpdateParams) (*ent.NotificationChannel, error) {
	ch, err := s.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	upd := ch.Update()
	if p.Name != nil {
		if strings.TrimSpace(*p.Name) == "" {
			return nil, validationErr("name cannot be empty")
		}
		upd.SetName(strings.TrimSpace(*p.Name))
	}
	if p.Events != nil {
		if err := validateEvents(*p.Events); err != nil {
			return nil, err
		}
		upd.SetEvents(*p.Events)
	}
	if p.Target != nil {
		address, sealed, err := s.prepareTarget(string(ch.Kind), *p.Target)
		if err != nil {
			return nil, err
		}
		upd.SetAddress(address)
		if sealed != nil {
			upd.SetEncryptedTarget(sealed)
		}
	}
	if p.ApplicationID != nil {
		if *p.ApplicationID == uuid.Nil {
			upd.ClearApplicationID()
		} else {
			if err := s.assertApp(ctx, orgID, *p.ApplicationID); err != nil {
				return nil, err
			}
			upd.SetApplicationID(*p.ApplicationID)
		}
	}
	return upd.Save(ctx)
}

// Delete removes a channel scoped to the organization.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	n, err := s.ent.NotificationChannel.Delete().
		Where(notificationchannel.OrganizationID(orgID), notificationchannel.ID(id)).
		Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return &ent.NotFoundError{}
	}
	return nil
}

// assertApp checks an application belongs to the organization (a 400 on a
// foreign or unknown id, not a 404 — the channel is what is being written).
func (s *Service) assertApp(ctx context.Context, orgID, appID uuid.UUID) error {
	app, err := s.ent.Application.Get(ctx, appID)
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	if err != nil || app.OrganizationID != orgID {
		return validationErr("application not found")
	}
	return nil
}

// validateEvents checks every subscribed event kind is a real one and the
// list is not empty.
func validateEvents(events []string) error {
	if len(events) == 0 {
		return validationErr("subscribe to at least one event (%s)", strings.Join(workflows.EventKinds, ", "))
	}
	seen := map[string]bool{}
	for _, e := range events {
		if !workflows.ValidEventKind(e) {
			return validationErr("unknown event %q (one of %s)", e, strings.Join(workflows.EventKinds, ", "))
		}
		if seen[e] {
			return validationErr("event %q is listed twice", e)
		}
		seen[e] = true
	}
	return nil
}

// prepareTarget validates a destination for the kind and returns the
// display address plus, for a webhook kind, the sealed URL.
func (s *Service) prepareTarget(kind, target string) (address string, sealed []byte, err error) {
	target = strings.TrimSpace(target)
	switch kind {
	case KindEmail:
		addr, err := mail.ParseAddress(target)
		if err != nil || addr.Address != target {
			return "", nil, validationErr("target must be a plain email address")
		}
		return target, nil, nil
	case KindSlack, KindWebhook:
		u, err := url.Parse(target)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return "", nil, validationErr("target must be an http(s) URL")
		}
		if s.sealer == nil {
			return "", nil, secrets.ErrDisabled
		}
		sealed, err := s.sealer.Seal([]byte(target))
		if err != nil {
			return "", nil, err
		}
		return u.Host, sealed, nil
	default:
		return "", nil, validationErr("kind must be %s, %s, or %s", KindEmail, KindSlack, KindWebhook)
	}
}

// target returns the channel's destination: the address for email, the
// unsealed URL for a webhook kind.
func (s *Service) target(ch *ent.NotificationChannel) (string, error) {
	if ch.Kind == notificationchannel.KindEmail {
		return ch.Address, nil
	}
	if ch.EncryptedTarget == nil || s.sealer == nil {
		return "", errors.New("notifications: channel has no usable webhook URL")
	}
	raw, err := s.sealer.Open(*ch.EncryptedTarget)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Dispatch fans a run event out to the organization's channels that
// subscribe to its kind and are not limited to another application: one
// delivery job per channel, so each destination retries independently. A
// no-op without a queue. Errors are returned for the caller to log; a
// failure to enqueue one channel does not stop the others.
func (s *Service) Dispatch(ctx context.Context, ev workflows.Event) error {
	if s.enqueue == nil {
		return nil
	}
	channels, err := s.ent.NotificationChannel.Query().
		Where(
			notificationchannel.OrganizationID(ev.OrgID),
			notificationchannel.Or(
				notificationchannel.ApplicationIDIsNil(),
				notificationchannel.ApplicationID(ev.AppID),
			),
		).
		All(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, ch := range channels {
		if !subscribed(ch, ev.Kind) {
			continue
		}
		if err := s.enqueue(ctx, DeliverArgs{ChannelID: ch.ID, OrgID: ev.OrgID, Event: ev}); err != nil {
			errs = append(errs, fmt.Errorf("channel %s: %w", ch.ID, err))
		}
	}
	return errors.Join(errs...)
}

// subscribed reports whether a channel receives the event kind.
func subscribed(ch *ent.NotificationChannel, kind string) bool {
	for _, e := range ch.Events {
		if e == kind {
			return true
		}
	}
	return false
}

// Test enqueues a synthetic "test" delivery to one channel so a user can
// confirm it reaches its destination. ErrNoQueue without a queue.
func (s *Service) Test(ctx context.Context, orgID, id uuid.UUID, orgName string) error {
	ch, err := s.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	if s.enqueue == nil {
		return ErrNoQueue
	}
	ev := workflows.Event{
		Kind:    EventTest,
		OrgID:   orgID,
		AppName: orgName,
		Message: "This is a test notification from Spacefleet for the channel " + ch.Name + ".",
	}
	return s.enqueue(ctx, DeliverArgs{ChannelID: ch.ID, OrgID: orgID, Event: ev})
}
