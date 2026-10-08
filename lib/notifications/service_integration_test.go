//go:build integration

package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/lib/email"
	"github.com/spacefleet/spacefleet/lib/testsupport"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

type recordingSender struct{ sent []email.Message }

func (r *recordingSender) Send(_ context.Context, m email.Message) error {
	r.sent = append(r.sent, m)
	return nil
}

func seedOrgApp(t *testing.T, client *ent.Client) (*ent.Organization, *ent.Application) {
	t.Helper()
	ctx := context.Background()
	org, err := client.Organization.Create().SetName("Acme").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := client.Cluster.Create().SetOrganizationID(org.ID).SetName("runner").SetConnectionMethod(cluster.ConnectionMethodToken).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app, err := client.Application.Create().SetOrganizationID(org.ID).SetName("web").SetRunnerClusterID(runner.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return org, app
}

// TestChannelsAndDispatch drives the service end to end: create channels
// of each kind (sealed URLs, org-scoped reads, app limits), fan an event out
// to exactly the subscribed channels, run the delivery worker against a
// webhook receiver and a recording email sender, and send a test.
func TestChannelsAndDispatch(t *testing.T) {
	client := testsupport.NewEntClient(t)
	ctx := context.Background()
	org, app := seedOrgApp(t, client)
	other, _ := seedOrgApp(t, client)

	var enqueued []DeliverArgs
	svc := NewService(client, newSealer(t), func(_ context.Context, args river.JobArgs) error {
		enqueued = append(enqueued, args.(DeliverArgs))
		return nil
	})

	var received []map[string]any
	var receivedKinds []string
	var receivedSigs []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		received = append(received, body)
		receivedKinds = append(receivedKinds, r.Header.Get("X-Spacefleet-Event"))
		// A signed delivery must verify against the exact bytes received.
		if sig := r.Header.Get(SignatureHeader); sig != "" {
			if !VerifySignature("hook-secret", b, sig) {
				t.Errorf("signature %q does not verify for body %s", sig, b)
			}
			receivedSigs = append(receivedSigs, sig)
		}
		if strings.HasSuffix(r.URL.Path, "/fail") {
			http.Error(w, "nope", http.StatusBadGateway)
		}
	}))
	defer hook.Close()

	mail, err := svc.Create(ctx, org.ID, CreateParams{Name: "ops", Kind: KindEmail, Target: "ops@example.com", Events: []string{workflows.EventRunFailed, workflows.EventAwaitingApproval}})
	if err != nil {
		t.Fatalf("create email: %v", err)
	}
	slack, err := svc.Create(ctx, org.ID, CreateParams{Name: "slack", Kind: KindSlack, Target: hook.URL + "/slack", Events: []string{workflows.EventRunFailed}, ApplicationID: app.ID})
	if err != nil {
		t.Fatalf("create slack: %v", err)
	}
	if slack.Address != strings.TrimPrefix(hook.URL, "http://") || slack.EncryptedTarget == nil || slack.ApplicationID != app.ID {
		t.Errorf("slack row = %+v", slack)
	}
	generic, err := svc.Create(ctx, org.ID, CreateParams{Name: "hook", Kind: KindWebhook, Target: hook.URL + "/generic", Secret: "hook-secret", Events: []string{workflows.EventDriftDetected}})
	if err != nil {
		t.Fatalf("create webhook: %v", err)
	}
	if generic.EncryptedSecret == nil {
		t.Error("the signing secret must be sealed onto the row")
	}
	if got, _ := svc.secret(generic); got != "hook-secret" {
		t.Errorf("unsealed secret = %q", got)
	}
	// A secret is a webhook-only setting.
	if _, err := svc.Create(ctx, org.ID, CreateParams{Name: "signed-slack", Kind: KindSlack, Target: hook.URL, Secret: "x", Events: []string{workflows.EventRunFailed}}); !IsValidation(err) {
		t.Errorf("secret on slack: err = %v, want validation", err)
	}
	if _, err := svc.Create(ctx, org.ID, CreateParams{Name: "ops", Kind: KindEmail, Target: "x@example.com", Events: []string{workflows.EventRunFailed}}); !ent.IsConstraintError(err) {
		t.Errorf("duplicate name: err = %v, want constraint", err)
	}
	if _, err := svc.Create(ctx, org.ID, CreateParams{Name: "foreign", Kind: KindEmail, Target: "x@example.com", Events: []string{workflows.EventRunFailed}, ApplicationID: uuid.New()}); !IsValidation(err) {
		t.Errorf("unknown app: err = %v, want validation", err)
	}
	if _, err := svc.Get(ctx, other.ID, mail.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org get: err = %v, want NotFound", err)
	}
	list, err := svc.List(ctx, org.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %d (err %v)", len(list), err)
	}

	// Update: rename, re-target, change events, clear the app limit.
	newName, newTarget, nilApp := "slack-ops", hook.URL+"/slack2", uuid.Nil
	events := []string{workflows.EventAwaitingApproval}
	slack, err = svc.Update(ctx, org.ID, slack.ID, UpdateParams{Name: &newName, Target: &newTarget, Events: &events, ApplicationID: &nilApp})
	if err != nil || slack.Name != "slack-ops" || slack.ApplicationID != uuid.Nil || len(slack.Events) != 1 {
		t.Errorf("update = %+v err=%v", slack, err)
	}
	if target, _ := svc.target(slack); target != hook.URL+"/slack2" {
		t.Errorf("re-sealed target = %q", target)
	}
	if _, err := svc.Update(ctx, other.ID, slack.ID, UpdateParams{Name: &newName}); !ent.IsNotFound(err) {
		t.Errorf("cross-org update: err = %v", err)
	}

	// Dispatch: run_failed on app → email (org-wide, subscribed); slack now
	// only listens to awaiting_approval; generic only to drift.
	ev := workflows.Event{Kind: workflows.EventRunFailed, OrgID: org.ID, AppID: app.ID, AppName: "web", RunID: uuid.New(), Action: "deploy", Status: "failed"}
	if err := svc.Dispatch(ctx, ev); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(enqueued) != 1 || enqueued[0].ChannelID != mail.ID || enqueued[0].Event.Kind != workflows.EventRunFailed {
		t.Fatalf("enqueued = %+v", enqueued)
	}
	enqueued = nil
	drift := workflows.Event{Kind: workflows.EventDriftDetected, OrgID: org.ID, AppID: app.ID, AppName: "web", Drift: []string{"a.b"}}
	_ = svc.Dispatch(ctx, drift)
	if len(enqueued) != 1 || enqueued[0].ChannelID != generic.ID {
		t.Errorf("drift enqueued = %+v", enqueued)
	}
	enqueued = nil
	_ = svc.Dispatch(ctx, workflows.Event{Kind: workflows.EventRunFailed, OrgID: other.ID, AppID: uuid.New()})
	if len(enqueued) != 0 {
		t.Errorf("another org's event reached this org's channels: %+v", enqueued)
	}

	// Deliver: the worker sends email through the Sender and posts JSON to
	// webhook kinds; a non-2xx is an error (River retries); a deleted
	// channel is dropped quietly.
	sender := &recordingSender{}
	w := &DeliverWorker{Service: svc, Sender: sender}
	if err := w.Work(ctx, &river.Job[DeliverArgs]{Args: DeliverArgs{ChannelID: mail.ID, OrgID: org.ID, Event: ev}}); err != nil {
		t.Fatalf("deliver email: %v", err)
	}
	if len(sender.sent) != 1 || sender.sent[0].To != "ops@example.com" || sender.sent[0].Subject != "[Spacefleet] web: deploy run failed" {
		t.Errorf("sent = %+v", sender.sent)
	}
	if err := w.Work(ctx, &river.Job[DeliverArgs]{Args: DeliverArgs{ChannelID: generic.ID, OrgID: org.ID, Event: drift}}); err != nil {
		t.Fatalf("deliver webhook: %v", err)
	}
	if len(receivedSigs) != 1 {
		t.Errorf("the signed webhook must carry one signature, got %v", receivedSigs)
	}
	if err := w.Work(ctx, &river.Job[DeliverArgs]{Args: DeliverArgs{ChannelID: slack.ID, OrgID: org.ID, Event: ev}}); err != nil {
		t.Fatalf("deliver slack: %v", err)
	}
	if len(receivedSigs) != 1 {
		t.Errorf("an unsigned channel must not carry a signature, got %v", receivedSigs)
	}
	// Clearing the secret stops signing.
	noSecret := ""
	if generic, err = svc.Update(ctx, org.ID, generic.ID, UpdateParams{Secret: &noSecret}); err != nil || (generic.EncryptedSecret != nil && len(*generic.EncryptedSecret) > 0) {
		t.Errorf("clear secret: row=%+v err=%v", generic, err)
	}
	if err := w.Work(ctx, &river.Job[DeliverArgs]{Args: DeliverArgs{ChannelID: generic.ID, OrgID: org.ID, Event: drift}}); err != nil {
		t.Fatalf("deliver unsigned webhook: %v", err)
	}
	if len(receivedSigs) != 1 {
		t.Errorf("after clearing the secret deliveries must be unsigned, got %v", receivedSigs)
	}
	if len(received) != 3 || receivedKinds[0] != workflows.EventDriftDetected || received[0]["headline"] != "web: drift detected on 1 resource" || !strings.Contains(received[1]["text"].(string), "*web: deploy run failed*") {
		t.Errorf("received = %v kinds=%v", received, receivedKinds)
	}
	failTarget := hook.URL + "/fail"
	if _, err := svc.Update(ctx, org.ID, generic.ID, UpdateParams{Target: &failTarget}); err != nil {
		t.Fatal(err)
	}
	if err := w.Work(ctx, &river.Job[DeliverArgs]{Args: DeliverArgs{ChannelID: generic.ID, OrgID: org.ID, Event: drift}}); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("failing webhook: err = %v, want a 502 error", err)
	}
	if err := w.Work(ctx, &river.Job[DeliverArgs]{Args: DeliverArgs{ChannelID: uuid.New(), OrgID: org.ID, Event: ev}}); err != nil {
		t.Errorf("gone channel: err = %v, want nil", err)
	}

	// Test sends a synthetic event; without a queue it says so.
	enqueued = nil
	if err := svc.Test(ctx, org.ID, mail.ID, "Acme"); err != nil || len(enqueued) != 1 || enqueued[0].Event.Kind != EventTest {
		t.Errorf("test: err=%v enqueued=%+v", err, enqueued)
	}
	noQueue := NewService(client, newSealer(t), nil)
	if err := noQueue.Test(ctx, org.ID, mail.ID, "Acme"); !errors.Is(err, ErrNoQueue) {
		t.Errorf("no queue: err = %v", err)
	}
	if err := noQueue.Dispatch(ctx, ev); err != nil {
		t.Errorf("no queue dispatch: %v", err)
	}

	// Delete, org-scoped.
	if err := svc.Delete(ctx, other.ID, mail.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org delete: err = %v", err)
	}
	if err := svc.Delete(ctx, org.ID, mail.ID); err != nil {
		t.Errorf("delete: %v", err)
	}
	if list, _ := svc.List(ctx, org.ID); len(list) != 2 {
		t.Errorf("after delete: %d channels", len(list))
	}
}
