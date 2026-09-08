package notifications

import (
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/lib/workflows"
)

func TestRender(t *testing.T) {
	t.Parallel()
	ev := workflows.Event{
		Kind: workflows.EventRunFailed, AppName: "web", Action: "deploy", Status: "failed",
		Message: "workflow failed", StartedBy: "kyle@example.com", RunURL: "https://sf/applications/a/runs/r",
		Steps: []workflows.EventStep{{Name: "api", Status: "failed"}},
	}
	if h := Headline(ev); h != "web: deploy run failed" {
		t.Errorf("headline = %q", h)
	}
	msg := RenderEmail(ev)
	if msg.Subject != "[Spacefleet] web: deploy run failed" {
		t.Errorf("subject = %q", msg.Subject)
	}
	for _, want := range []string{"workflow failed", "Run: deploy (failed)", "Started by: kyle@example.com", "Step api: failed", "Open the run: https://sf/applications/a/runs/r"} {
		if !strings.Contains(msg.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, msg.Text)
		}
	}
	if !strings.Contains(msg.HTML, `<a href="https://sf/applications/a/runs/r">Open the run</a>`) {
		t.Errorf("html lacks the link:\n%s", msg.HTML)
	}
	slack := RenderSlack(ev)
	if !strings.HasPrefix(slack, "*web: deploy run failed*") || !strings.Contains(slack, "<https://sf/applications/a/runs/r|Open the run>") {
		t.Errorf("slack = %q", slack)
	}
	hook := RenderWebhook(ev)
	if hook["headline"] != "web: deploy run failed" || hook["event"].(workflows.Event).RunID != ev.RunID {
		t.Errorf("webhook = %v", hook)
	}

	drift := workflows.Event{Kind: workflows.EventDriftDetected, AppName: "web", Drift: []string{"a.b", "c.d"}}
	if h := Headline(drift); h != "web: drift detected on 2 resources" {
		t.Errorf("drift headline = %q", h)
	}
	if h := Headline(workflows.Event{Kind: workflows.EventAwaitingApproval, AppName: "web", Action: "deploy"}); h != "web: a deploy run is waiting for approval" {
		t.Errorf("approval headline = %q", h)
	}
	// HTML escapes what it echoes.
	msg = RenderEmail(workflows.Event{Kind: EventTest, Message: "<b>hi</b>"})
	if strings.Contains(msg.HTML, "<b>hi</b>") || !strings.Contains(msg.HTML, "&lt;b&gt;hi&lt;/b&gt;") {
		t.Errorf("html not escaped: %s", msg.HTML)
	}
}
