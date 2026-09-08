package notifications

import (
	"fmt"
	"html"
	"strings"

	"github.com/spacefleet/spacefleet/lib/email"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// Headline is the one-line reading of an event, shared by every format.
func Headline(ev workflows.Event) string {
	switch ev.Kind {
	case workflows.EventAwaitingApproval:
		return fmt.Sprintf("%s: a %s run is waiting for approval", ev.AppName, ev.Action)
	case workflows.EventRunFailed:
		return fmt.Sprintf("%s: %s run %s", ev.AppName, ev.Action, ev.Status)
	case workflows.EventDriftDetected:
		n := len(ev.Drift)
		return fmt.Sprintf("%s: drift detected on %d resource%s", ev.AppName, n, plural(n))
	case EventTest:
		return "Test notification from Spacefleet"
	default:
		return fmt.Sprintf("%s: %s", ev.AppName, ev.Kind)
	}
}

// lines renders the event's detail as plain-text lines (no headline).
func lines(ev workflows.Event) []string {
	var out []string
	if ev.Message != "" {
		out = append(out, ev.Message)
	}
	if ev.Kind != EventTest {
		out = append(out, fmt.Sprintf("Run: %s (%s)", ev.Action, ev.Status))
	}
	if ev.StartedBy != "" {
		out = append(out, "Started by: "+ev.StartedBy)
	}
	for _, s := range ev.Steps {
		out = append(out, fmt.Sprintf("Step %s: %s", s.Name, s.Status))
	}
	for _, d := range ev.Drift {
		out = append(out, "Drifted: "+d)
	}
	if ev.RunURL != "" {
		out = append(out, "Open the run: "+ev.RunURL)
	}
	return out
}

// RenderEmail renders an event as an email (To is filled by the caller).
// Spacefleet is self-hostable, so the copy is operator-neutral.
func RenderEmail(ev workflows.Event) email.Message {
	head := Headline(ev)
	text := head + "\n\n" + strings.Join(lines(ev), "\n") + "\n"
	var b strings.Builder
	fmt.Fprintf(&b, "<p><strong>%s</strong></p>", html.EscapeString(head))
	for _, l := range lines(ev) {
		if ev.RunURL != "" && strings.HasPrefix(l, "Open the run: ") {
			fmt.Fprintf(&b, "<p><a href=\"%s\">Open the run</a></p>", html.EscapeString(ev.RunURL))
			continue
		}
		fmt.Fprintf(&b, "<p>%s</p>", html.EscapeString(l))
	}
	return email.Message{Subject: "[Spacefleet] " + head, Text: text, HTML: b.String()}
}

// RenderSlack renders an event as a Slack (mrkdwn) message text.
func RenderSlack(ev workflows.Event) string {
	var b strings.Builder
	b.WriteString("*" + Headline(ev) + "*")
	for _, l := range lines(ev) {
		if ev.RunURL != "" && strings.HasPrefix(l, "Open the run: ") {
			fmt.Fprintf(&b, "\n<%s|Open the run>", ev.RunURL)
			continue
		}
		b.WriteString("\n" + l)
	}
	return b.String()
}

// RenderWebhook renders an event as the generic webhook's JSON body: the
// headline plus the event's fields as they are.
func RenderWebhook(ev workflows.Event) map[string]any {
	return map[string]any{
		"headline": Headline(ev),
		"event":    ev,
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
