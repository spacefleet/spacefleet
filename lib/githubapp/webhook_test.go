package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	t.Parallel()
	body := []byte(`{"ref":"refs/heads/main"}`)
	sig := SignBody("s3cret", body)
	if err := VerifySignature("s3cret", body, sig); err != nil {
		t.Errorf("valid: %v", err)
	}
	for name, h := range map[string]string{
		"wrong secret": SignBody("other", body),
		"no prefix":    "abc",
		"bad hex":      "sha256=zz",
		"empty":        "",
	} {
		if err := VerifySignature("s3cret", body, h); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: err = %v, want ErrBadSignature", name, err)
		}
	}
	if err := VerifySignature("", body, sig); err == nil || errors.Is(err, ErrBadSignature) {
		t.Errorf("no secret: err = %v, want a configuration error", err)
	}
}

const pushPayload = `{"ref":"refs/heads/main","after":"abc123","deleted":false,
"repository":{"full_name":"Acme/Infra","default_branch":"main"},
"sender":{"login":"kyle"},"installation":{"id":42}}`

const prPayload = `{"action":"synchronize","number":12,
"pull_request":{"title":"Add bucket","head":{"ref":"feature/x","sha":"def456","repo":{"full_name":"acme/infra"}},"base":{"ref":"main"}},
"repository":{"full_name":"acme/infra","default_branch":"main"},
"sender":{"login":"kyle"},"installation":{"id":42}}`

func TestParseWebhook(t *testing.T) {
	t.Parallel()
	ev, ok, err := ParseWebhook(EventPush, []byte(pushPayload))
	if err != nil || !ok {
		t.Fatalf("push: ok=%v err=%v", ok, err)
	}
	want := WebhookEvent{Event: EventPush, InstallationID: 42, Repo: "Acme/Infra", DefaultBranch: "main", Branch: "main", SHA: "abc123", Sender: "kyle"}
	if ev != want {
		t.Errorf("push = %+v, want %+v", ev, want)
	}

	ev, ok, err = ParseWebhook(EventPullRequest, []byte(prPayload))
	if err != nil || !ok {
		t.Fatalf("pull_request: ok=%v err=%v", ok, err)
	}
	want = WebhookEvent{Event: EventPullRequest, Action: "synchronize", InstallationID: 42, Repo: "acme/infra", DefaultBranch: "main", Branch: "feature/x", BaseBranch: "main", SHA: "def456", Sender: "kyle", PRNumber: 12, PRTitle: "Add bucket", SameRepo: true}
	if ev != want {
		t.Errorf("pull_request = %+v, want %+v", ev, want)
	}

	// Dropped: other events, tag pushes, deletions, other PR actions, forks
	// are flagged (not dropped — the caller decides).
	for name, in := range map[string][2]string{
		"ping":      {"ping", `{}`},
		"tag push":  {EventPush, `{"ref":"refs/tags/v1","repository":{"full_name":"a/b"}}`},
		"deletion":  {EventPush, `{"ref":"refs/heads/main","deleted":true,"repository":{"full_name":"a/b"}}`},
		"pr closed": {EventPullRequest, `{"action":"closed","pull_request":{"head":{"repo":{}},"base":{}},"repository":{}}`},
	} {
		if _, ok, err := ParseWebhook(in[0], []byte(in[1])); ok || err != nil {
			t.Errorf("%s: ok=%v err=%v, want dropped", name, ok, err)
		}
	}
	fork := `{"action":"opened","number":1,"pull_request":{"head":{"ref":"x","sha":"s","repo":{"full_name":"someone/infra"}},"base":{"ref":"main"}},"repository":{"full_name":"acme/infra"}}`
	if ev, ok, _ := ParseWebhook(EventPullRequest, []byte(fork)); !ok || ev.SameRepo {
		t.Errorf("fork PR: ok=%v same=%v, want ok and not same", ok, ev.SameRepo)
	}
	if _, _, err := ParseWebhook(EventPush, []byte(`{`)); err == nil {
		t.Error("bad body: want an error")
	}
}

func TestCheckRuns(t *testing.T) {
	var created, updated map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/42/access_tokens":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"token":"ghs_minted","expires_at":"2026-06-03T12:00:00Z"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/infra/check-runs":
			if r.Header.Get("Authorization") != "token ghs_minted" {
				t.Errorf("create: auth = %q", r.Header.Get("Authorization"))
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &created)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":777}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/acme/infra/check-runs/777":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &updated)
			_, _ = w.Write([]byte(`{"id":777}`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()
	a, err := New(1, testKeyPEM(t), "client-id", "client-secret")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	a.baseURL = srv.URL

	id, err := a.CreateCheckRun(context.Background(), 42, "acme/infra", CheckRun{Name: "Spacefleet · web", HeadSHA: "def456", DetailsURL: "https://sf/run", ExternalID: "run-1", Status: "in_progress"})
	if err != nil || id != 777 {
		t.Fatalf("create: id=%d err=%v", id, err)
	}
	if created["name"] != "Spacefleet · web" || created["head_sha"] != "def456" || created["status"] != "in_progress" || created["details_url"] != "https://sf/run" || created["external_id"] != "run-1" {
		t.Errorf("create body = %v", created)
	}
	if _, ok := created["conclusion"]; ok {
		t.Error("create must not carry a conclusion")
	}
	if err := a.UpdateCheckRun(context.Background(), 42, "acme/infra", 777, CheckRun{Status: "completed", Conclusion: CheckSuccess, Title: "Plan: 2 to add", Summary: "| step |"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated["status"] != "completed" || updated["conclusion"] != "success" {
		t.Errorf("update body = %v", updated)
	}
	if _, ok := updated["name"]; ok {
		t.Error("update must not resend the name")
	}
	out, _ := updated["output"].(map[string]any)
	if out["title"] != "Plan: 2 to add" || out["summary"] != "| step |" {
		t.Errorf("update output = %v", out)
	}
}
