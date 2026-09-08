package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/notificationchannel"
	"github.com/spacefleet/spacefleet/lib/email"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// DeliverArgs is the River job that delivers one event to one channel. It
// carries the rendered facts of the event (never a secret — the channel's
// URL is unsealed by the worker), so a retry re-sends the same message.
type DeliverArgs struct {
	ChannelID uuid.UUID       `json:"channel_id"`
	OrgID     uuid.UUID       `json:"org_id"`
	Event     workflows.Event `json:"event"`
}

// Kind is the stable River job identifier.
func (DeliverArgs) Kind() string { return "notification_deliver" }

// InsertOpts bounds retries: a destination that keeps refusing is given up
// on after a few attempts rather than River's default twenty-five.
func (DeliverArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 5}
}

// DeliverWorker sends notifications. Registered by the worker process with
// the configured email Sender; webhook kinds post over HTTP.
type DeliverWorker struct {
	river.WorkerDefaults[DeliverArgs]
	Service *Service
	Sender  email.Sender
	// HTTP posts webhook and Slack payloads; nil uses a 15-second client.
	HTTP *http.Client
}

// Work loads the channel (org-scoped), resolves its destination, and
// delivers the event in the channel's format. A channel deleted since the
// event was enqueued is not an error.
func (w *DeliverWorker) Work(ctx context.Context, job *river.Job[DeliverArgs]) error {
	a := job.Args
	ch, err := w.Service.Get(ctx, a.OrgID, a.ChannelID)
	if err != nil {
		if ent.IsNotFound(err) {
			log.Printf("notifications: channel %s is gone; dropping %s", a.ChannelID, a.Event.Kind)
			return nil
		}
		return err
	}
	target, err := w.Service.target(ch)
	if err != nil {
		return err
	}
	return w.deliver(ctx, ch, target, a.Event)
}

// deliver sends one event to a resolved destination.
func (w *DeliverWorker) deliver(ctx context.Context, ch *ent.NotificationChannel, target string, ev workflows.Event) error {
	switch ch.Kind {
	case notificationchannel.KindEmail:
		if w.Sender == nil {
			return fmt.Errorf("notifications: no email sender configured")
		}
		msg := RenderEmail(ev)
		msg.To = target
		return w.Sender.Send(ctx, msg)
	case notificationchannel.KindSlack:
		return w.post(ctx, target, ev.Kind, map[string]any{"text": RenderSlack(ev)})
	case notificationchannel.KindWebhook:
		return w.post(ctx, target, ev.Kind, RenderWebhook(ev))
	default:
		return fmt.Errorf("notifications: unknown channel kind %q", ch.Kind)
	}
}

// post sends a JSON payload to a webhook URL; a non-2xx response is an
// error (River retries).
func (w *DeliverWorker) post(ctx context.Context, target, kind string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Spacefleet")
	req.Header.Set("X-Spacefleet-Event", kind)
	client := w.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("notifications: webhook returned %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}
