package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/spacefleet/spacefleet/lib/githubapp"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// maxWebhookBody bounds a webhook delivery; GitHub's own limit is 25 MB but a
// push or pull_request payload is well under 1 MB.
const maxWebhookBody = 5 << 20

// GitHubWebhook receives GitHub App webhook deliveries (push and
// pull_request) and starts the runs the matching applications asked for
// (see workflows.TriggerRuns). It is a public route: the delivery is
// authenticated by its HMAC signature (X-Hub-Signature-256) against the
// App's webhook secret, verified over the raw body before anything is
// parsed. Without a configured secret the endpoint is off (503); a bad
// signature is 401; an event Spacefleet does not act on is acknowledged and
// dropped (200, ignored: true) so GitHub never retries it. Requires the
// background worker to enqueue runs (503 otherwise).
func (s *Server) GitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if s.githubWebhookSecret == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "github webhook secret not configured")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil || len(body) > maxWebhookBody {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "could not read the delivery body")
		return
	}
	if err := githubapp.VerifySignature(s.githubWebhookSecret, body, r.Header.Get("X-Hub-Signature-256")); err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "invalid webhook signature")
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if event == "ping" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	ev, ok, err := githubapp.ParseWebhook(event, body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"ignored": true})
		return
	}
	if s.workflows == nil || s.jobQueue == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "background job worker not configured; cannot start triggered runs")
		return
	}
	started, err := s.workflows.TriggerRuns(r.Context(), ev, func(ctx context.Context, args workflows.WorkflowRunArgs) (string, error) {
		res, err := s.jobQueue.Insert(ctx, args)
		if err != nil {
			return "", err
		}
		return strconv.FormatInt(res.Job.ID, 10), nil
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "could not process the delivery")
		return
	}
	runs := make([]string, 0, len(started))
	for _, t := range started {
		runs = append(runs, t.RunID.String())
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": len(runs), "runs": runs})
}

// writeJSON writes a JSON body with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
