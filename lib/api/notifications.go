package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/lib/notifications"
	"github.com/spacefleet/spacefleet/lib/secrets"
)

// resolveNotificationsRead runs the read preamble for notification-channel
// handlers: the service must be configured, and the caller a member.
func (s *Server) resolveNotificationsRead(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.notifications == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "notifications service not configured"}, nil
	}
	m, aerr, err := s.resolveMembership(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	return m.OrganizationID, nil, nil
}

// resolveNotificationsWrite is the read preamble plus an admin gate:
// channels reach other people's inboxes and external systems, so managing
// them is an organization-admin concern like members and invitations.
func (s *Server) resolveNotificationsWrite(ctx context.Context) (uuid.UUID, *apiError, error) {
	if s.notifications == nil {
		return uuid.Nil, &apiError{http.StatusServiceUnavailable, "unavailable", "notifications service not configured"}, nil
	}
	m, aerr, err := s.resolveAdmin(ctx)
	if err != nil || aerr != nil {
		return uuid.Nil, aerr, err
	}
	return m.OrganizationID, nil, nil
}

func (s *Server) ListNotificationChannels(ctx context.Context, _ ListNotificationChannelsRequestObject) (ListNotificationChannelsResponseObject, error) {
	orgID, aerr, err := s.resolveNotificationsRead(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[ListNotificationChannelsdefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	list, err := s.notifications.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]NotificationChannel, len(list))
	for i, ch := range list {
		out[i] = toAPINotificationChannel(ch)
	}
	return ListNotificationChannels200JSONResponse(out), nil
}

func (s *Server) CreateNotificationChannel(ctx context.Context, req CreateNotificationChannelRequestObject) (CreateNotificationChannelResponseObject, error) {
	orgID, aerr, err := s.resolveNotificationsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[CreateNotificationChanneldefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[CreateNotificationChanneldefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	p := notifications.CreateParams{
		Name:   strings.TrimSpace(req.Body.Name),
		Kind:   string(req.Body.Kind),
		Target: req.Body.Target,
		Events: eventKinds(req.Body.Events),
	}
	if req.Body.Secret != nil {
		p.Secret = *req.Body.Secret
	}
	if req.Body.ApplicationId != nil {
		p.ApplicationID = *req.Body.ApplicationId
	}
	ch, err := s.notifications.Create(ctx, orgID, p)
	if err != nil {
		if resp, ok := notificationWriteError[CreateNotificationChanneldefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return CreateNotificationChannel201JSONResponse(toAPINotificationChannel(ch)), nil
}

func (s *Server) UpdateNotificationChannel(ctx context.Context, req UpdateNotificationChannelRequestObject) (UpdateNotificationChannelResponseObject, error) {
	orgID, aerr, err := s.resolveNotificationsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[UpdateNotificationChanneldefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if req.Body == nil {
		return errResp[UpdateNotificationChanneldefaultJSONResponse](http.StatusBadRequest, "bad_request", "request body is required"), nil
	}
	p := notifications.UpdateParams{Name: req.Body.Name, Target: req.Body.Target, Secret: req.Body.Secret}
	if req.Body.Events != nil {
		ev := eventKinds(*req.Body.Events)
		p.Events = &ev
	}
	if req.Body.ApplicationId != nil {
		raw := strings.TrimSpace(*req.Body.ApplicationId)
		id := uuid.Nil
		if raw != "" {
			parsed, err := uuid.Parse(raw)
			if err != nil {
				return errResp[UpdateNotificationChanneldefaultJSONResponse](http.StatusBadRequest, "bad_request", "application_id must be a uuid or empty"), nil
			}
			id = parsed
		}
		p.ApplicationID = &id
	}
	ch, err := s.notifications.Update(ctx, orgID, req.Id, p)
	if err != nil {
		if ent.IsNotFound(err) {
			return errResp[UpdateNotificationChanneldefaultJSONResponse](http.StatusNotFound, "not_found", "notification channel not found"), nil
		}
		if resp, ok := notificationWriteError[UpdateNotificationChanneldefaultJSONResponse](err); ok {
			return resp, nil
		}
		return nil, err
	}
	return UpdateNotificationChannel200JSONResponse(toAPINotificationChannel(ch)), nil
}

func (s *Server) DeleteNotificationChannel(ctx context.Context, req DeleteNotificationChannelRequestObject) (DeleteNotificationChannelResponseObject, error) {
	orgID, aerr, err := s.resolveNotificationsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[DeleteNotificationChanneldefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	if err := s.notifications.Delete(ctx, orgID, req.Id); err != nil {
		if ent.IsNotFound(err) {
			return errResp[DeleteNotificationChanneldefaultJSONResponse](http.StatusNotFound, "not_found", "notification channel not found"), nil
		}
		return nil, err
	}
	return DeleteNotificationChannel204Response{}, nil
}

// TestNotificationChannel enqueues a synthetic test delivery. Admin only;
// 503 without the background worker.
func (s *Server) TestNotificationChannel(ctx context.Context, req TestNotificationChannelRequestObject) (TestNotificationChannelResponseObject, error) {
	orgID, aerr, err := s.resolveNotificationsWrite(ctx)
	if err != nil {
		return nil, err
	}
	if aerr != nil {
		return errResp[TestNotificationChanneldefaultJSONResponse](aerr.status, aerr.code, aerr.msg), nil
	}
	orgName := ""
	if s.orgs != nil {
		if org, err := s.orgs.Get(ctx, orgID); err == nil {
			orgName = org.Name
		}
	}
	if err := s.notifications.Test(ctx, orgID, req.Id, orgName); err != nil {
		switch {
		case ent.IsNotFound(err):
			return errResp[TestNotificationChanneldefaultJSONResponse](http.StatusNotFound, "not_found", "notification channel not found"), nil
		case errors.Is(err, notifications.ErrNoQueue):
			return errResp[TestNotificationChanneldefaultJSONResponse](http.StatusServiceUnavailable, "unavailable", "background job worker not configured; cannot send a test notification"), nil
		default:
			return nil, err
		}
	}
	return TestNotificationChannel202Response{}, nil
}

func notificationWriteError[T defaultResp](err error) (T, bool) {
	switch {
	case notifications.IsValidation(err):
		return errResp[T](http.StatusBadRequest, "bad_request", err.Error()), true
	case errors.Is(err, secrets.ErrDisabled):
		return errResp[T](http.StatusBadRequest, "encryption_unavailable", "cannot store a webhook URL without an encryption key — set SPACEFLEET_SECRET_KEY"), true
	case ent.IsConstraintError(err):
		return errResp[T](http.StatusConflict, "conflict", "a notification channel with that name already exists in this organization"), true
	default:
		var zero T
		return zero, false
	}
}

func eventKinds(in []RunEventKind) []string {
	out := make([]string, 0, len(in))
	for _, e := range in {
		out = append(out, string(e))
	}
	return out
}

// toAPINotificationChannel maps a channel row to the API type. The sealed
// webhook URL is never exposed — only the address (email, or host).
func toAPINotificationChannel(ch *ent.NotificationChannel) NotificationChannel {
	events := make([]RunEventKind, 0, len(ch.Events))
	for _, e := range ch.Events {
		events = append(events, RunEventKind(e))
	}
	out := NotificationChannel{
		Id:        ch.ID,
		Name:      ch.Name,
		Kind:      NotificationChannelKind(ch.Kind),
		Address:   ch.Address,
		Events:    events,
		HasSecret: ch.EncryptedSecret != nil && len(*ch.EncryptedSecret) > 0,
		CreatedAt: ch.CreatedAt,
		UpdatedAt: ch.UpdatedAt,
	}
	if ch.ApplicationID != uuid.Nil {
		id := ch.ApplicationID
		out.ApplicationId = &id
	}
	return out
}
