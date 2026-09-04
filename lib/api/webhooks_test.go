package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spacefleet/spacefleet/lib/githubapp"
)

// TestGitHubWebhook covers the handler's own gates without a database: off
// without a secret, a bad signature is refused before parsing, a ping and an
// unhandled event are acknowledged, and a handled event without the
// workflow service or queue is a 503 (TriggerRuns itself is covered in
// lib/workflows).
func TestGitHubWebhook(t *testing.T) {
	deliver := func(srv *Server, event, sig, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", event)
		if sig != "" {
			req.Header.Set("X-Hub-Signature-256", sig)
		}
		rec := httptest.NewRecorder()
		srv.GitHubWebhook(rec, req)
		return rec
	}
	off := NewServer(ServerDeps{})
	if rec := deliver(off, "ping", "", "{}"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no secret: %d, want 503", rec.Code)
	}

	srv := NewServer(ServerDeps{GitHubWebhookSecret: "s3cret"})
	push := `{"ref":"refs/heads/main","after":"abc","repository":{"full_name":"acme/infra","default_branch":"main"},"installation":{"id":42}}`
	if rec := deliver(srv, "push", "", push); rec.Code != http.StatusUnauthorized {
		t.Errorf("no signature: %d, want 401", rec.Code)
	}
	if rec := deliver(srv, "push", githubapp.SignBody("other", []byte(push)), push); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong signature: %d, want 401", rec.Code)
	}
	sign := func(b string) string { return githubapp.SignBody("s3cret", []byte(b)) }
	if rec := deliver(srv, "ping", sign("{}"), "{}"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Errorf("ping: %d %s", rec.Code, rec.Body.String())
	}
	if rec := deliver(srv, "issues", sign("{}"), "{}"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ignored":true`) {
		t.Errorf("unhandled event: %d %s", rec.Code, rec.Body.String())
	}
	if rec := deliver(srv, "push", sign("{"), "{"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad body: %d, want 400", rec.Code)
	}
	if rec := deliver(srv, "push", sign(push), push); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no workflows/queue: %d, want 503", rec.Code)
	}
}
