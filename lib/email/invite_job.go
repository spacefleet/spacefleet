package email

import (
	"context"
	"fmt"
	"html"

	"github.com/riverqueue/river"
)

// InviteEmailArgs is the River job enqueued by the API when an admin invites
// someone (and email is configured). The worker renders and sends it via the
// Sender. AcceptURL is the fully-built invite link (derived from EXTERNAL_URL by
// the enqueuer), so the worker needs no knowledge of how links are formed.
type InviteEmailArgs struct {
	To        string `json:"to"`
	OrgName   string `json:"org_name"`
	Role      string `json:"role"`
	AcceptURL string `json:"accept_url"`
}

// Kind is the stable River job identifier.
func (InviteEmailArgs) Kind() string { return "invite_email" }

// InviteEmailWorker sends invitation emails. It's registered by the worker
// process with a configured Sender. Spacefleet is multi-tenant and
// self-hostable, so the copy is deliberately operator-neutral.
type InviteEmailWorker struct {
	river.WorkerDefaults[InviteEmailArgs]
	Sender Sender
}

// Work renders the invitation email and sends it.
func (w *InviteEmailWorker) Work(ctx context.Context, job *river.Job[InviteEmailArgs]) error {
	return w.Sender.Send(ctx, renderInvite(job.Args))
}

// renderInvite builds the invitation Message (plain text + HTML) from args.
func renderInvite(a InviteEmailArgs) Message {
	subject := fmt.Sprintf("You've been invited to join %s on Spacefleet", a.OrgName)

	text := fmt.Sprintf(
		"You've been invited to join the organization %q on Spacefleet as a %s.\n\n"+
			"Open this link to accept the invitation and join:\n\n%s\n\n"+
			"This invitation expires in 30 days. If you weren't expecting it, you can ignore this email.\n",
		a.OrgName, a.Role, a.AcceptURL)

	htmlBody := fmt.Sprintf(
		"<p>You've been invited to join the organization <strong>%s</strong> on Spacefleet as a <strong>%s</strong>.</p>"+
			"<p><a href=\"%s\">Accept the invitation and join</a></p>"+
			"<p>Or paste this link into your browser:<br><a href=\"%s\">%s</a></p>"+
			"<p style=\"color:#737373;font-size:12px\">This invitation expires in 30 days. "+
			"If you weren't expecting it, you can ignore this email.</p>",
		html.EscapeString(a.OrgName), html.EscapeString(a.Role),
		html.EscapeString(a.AcceptURL), html.EscapeString(a.AcceptURL), html.EscapeString(a.AcceptURL))

	return Message{To: a.To, Subject: subject, Text: text, HTML: htmlBody}
}
