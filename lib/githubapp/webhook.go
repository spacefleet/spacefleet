package githubapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Webhook deliveries. GitHub signs every delivery with the App's webhook
// secret (X-Hub-Signature-256: an HMAC-SHA256 of the raw body); the payload
// of the two events Spacefleet acts on — push and pull_request — is reduced
// to WebhookEvent, the few fields the run-trigger matching needs.

// ErrBadSignature is returned by VerifySignature for a missing, malformed, or
// mismatching signature. A handler maps it to 401.
var ErrBadSignature = errors.New("githubapp: webhook signature is missing or invalid")

// VerifySignature checks the X-Hub-Signature-256 header value against the
// raw request body using the webhook secret, in constant time. The header
// is "sha256=<hex>".
func VerifySignature(secret string, body []byte, header string) error {
	if secret == "" {
		return errors.New("githubapp: no webhook secret configured")
	}
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return ErrBadSignature
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return ErrBadSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrBadSignature
	}
	return nil
}

// SignBody renders the X-Hub-Signature-256 value for a body — what GitHub
// sends. Exported for tests and tooling that simulate a delivery.
func SignBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// The webhook events Spacefleet acts on.
const (
	EventPush        = "push"
	EventPullRequest = "pull_request"
)

// WebhookEvent is a push or pull_request delivery reduced to what run
// triggering needs. Branch is the branch the event is about: the pushed
// branch for a push; for a pull request, BaseBranch is the branch the pull
// request targets and Branch its head branch. SameRepo reports whether a
// pull request's head lives in the base repository (not a fork).
type WebhookEvent struct {
	Event          string
	Action         string
	InstallationID int64
	// Repo is the repository's full name, e.g. "acme/infra".
	Repo          string
	DefaultBranch string
	Branch        string
	BaseBranch    string
	SHA           string
	Sender        string
	PRNumber      int
	PRTitle       string
	SameRepo      bool
}

// pullRequestActions are the pull_request actions that start a speculative
// plan: a new pull request, new commits on it, or a reopen.
var pullRequestActions = map[string]bool{"opened": true, "synchronize": true, "reopened": true}

// ParseWebhook decodes a delivery of the named event (the X-GitHub-Event
// header) into a WebhookEvent. ok is false for an event or action Spacefleet
// does not act on (any other event; a branch deletion; a tag push; a
// pull_request action other than opened/synchronize/reopened) — those are
// acknowledged and dropped. An undecodable body of a handled event is an
// error.
func ParseWebhook(event string, body []byte) (ev WebhookEvent, ok bool, err error) {
	switch event {
	case EventPush:
		var p struct {
			Ref     string `json:"ref"`
			After   string `json:"after"`
			Deleted bool   `json:"deleted"`
			Repo    struct {
				FullName      string `json:"full_name"`
				DefaultBranch string `json:"default_branch"`
			} `json:"repository"`
			Sender       struct{ Login string } `json:"sender"`
			Installation struct{ ID int64 }     `json:"installation"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return WebhookEvent{}, false, fmt.Errorf("githubapp: decode push: %w", err)
		}
		const heads = "refs/heads/"
		if p.Deleted || !strings.HasPrefix(p.Ref, heads) {
			return WebhookEvent{}, false, nil
		}
		return WebhookEvent{
			Event:          EventPush,
			InstallationID: p.Installation.ID,
			Repo:           p.Repo.FullName,
			DefaultBranch:  p.Repo.DefaultBranch,
			Branch:         strings.TrimPrefix(p.Ref, heads),
			SHA:            p.After,
			Sender:         p.Sender.Login,
		}, true, nil
	case EventPullRequest:
		var p struct {
			Action      string `json:"action"`
			Number      int    `json:"number"`
			PullRequest struct {
				Title string `json:"title"`
				Head  struct {
					Ref  string `json:"ref"`
					SHA  string `json:"sha"`
					Repo struct {
						FullName string `json:"full_name"`
					} `json:"repo"`
				} `json:"head"`
				Base struct {
					Ref string `json:"ref"`
				} `json:"base"`
			} `json:"pull_request"`
			Repo struct {
				FullName      string `json:"full_name"`
				DefaultBranch string `json:"default_branch"`
			} `json:"repository"`
			Sender       struct{ Login string } `json:"sender"`
			Installation struct{ ID int64 }     `json:"installation"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return WebhookEvent{}, false, fmt.Errorf("githubapp: decode pull_request: %w", err)
		}
		if !pullRequestActions[p.Action] {
			return WebhookEvent{}, false, nil
		}
		return WebhookEvent{
			Event:          EventPullRequest,
			Action:         p.Action,
			InstallationID: p.Installation.ID,
			Repo:           p.Repo.FullName,
			DefaultBranch:  p.Repo.DefaultBranch,
			Branch:         p.PullRequest.Head.Ref,
			BaseBranch:     p.PullRequest.Base.Ref,
			SHA:            p.PullRequest.Head.SHA,
			Sender:         p.Sender.Login,
			PRNumber:       p.Number,
			PRTitle:        p.PullRequest.Title,
			SameRepo:       strings.EqualFold(p.PullRequest.Head.Repo.FullName, p.Repo.FullName),
		}, true, nil
	default:
		return WebhookEvent{}, false, nil
	}
}
