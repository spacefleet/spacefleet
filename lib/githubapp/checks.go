package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Check runs: the status a speculative plan reports back onto a pull
// request. A check run is created in_progress when the run starts (linking
// to the run page) and completed with a conclusion and a summary when the
// run settles. Both calls authenticate with an installation token, which
// needs the App's "Checks: read & write" repository permission.

// Check-run conclusions Spacefleet posts.
const (
	CheckSuccess = "success"
	CheckFailure = "failure"
	CheckNeutral = "neutral"
)

// CheckRun is the subset of a check run Spacefleet writes. Status is
// "in_progress" or "completed"; Conclusion is set only with "completed".
// Title and Summary are the check's output (Summary is Markdown).
type CheckRun struct {
	Name       string
	HeadSHA    string
	DetailsURL string
	ExternalID string
	Status     string
	Conclusion string
	Title      string
	Summary    string
}

// CreateCheckRun creates a check run on repo (full name) for the head
// commit and returns its id, to be carried on the run for UpdateCheckRun.
func (a *Authenticator) CreateCheckRun(ctx context.Context, installationID int64, repo string, c CheckRun) (int64, error) {
	token, _, err := a.InstallationToken(ctx, installationID)
	if err != nil {
		return 0, err
	}
	var out struct {
		ID int64 `json:"id"`
	}
	url := fmt.Sprintf("%s/repos/%s/check-runs", a.baseURL, repo)
	if err := a.doTokenJSON(ctx, http.MethodPost, url, token, checkRunBody(c, true), &out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// UpdateCheckRun updates an existing check run — typically to complete it
// with a conclusion and summary.
func (a *Authenticator) UpdateCheckRun(ctx context.Context, installationID int64, repo string, id int64, c CheckRun) error {
	token, _, err := a.InstallationToken(ctx, installationID)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/repos/%s/check-runs/%d", a.baseURL, repo, id)
	return a.doTokenJSON(ctx, http.MethodPatch, url, token, checkRunBody(c, false), nil)
}

// checkRunBody renders the check-run request body. create carries the
// immutable name/head_sha; an update carries only what changes.
func checkRunBody(c CheckRun, create bool) map[string]any {
	body := map[string]any{}
	if create {
		body["name"] = c.Name
		body["head_sha"] = c.HeadSHA
		if c.ExternalID != "" {
			body["external_id"] = c.ExternalID
		}
	}
	if c.DetailsURL != "" {
		body["details_url"] = c.DetailsURL
	}
	if c.Status != "" {
		body["status"] = c.Status
	}
	if c.Conclusion != "" {
		body["conclusion"] = c.Conclusion
	}
	if c.Title != "" || c.Summary != "" {
		body["output"] = map[string]any{"title": c.Title, "summary": c.Summary}
	}
	return body
}

// doTokenJSON performs a JSON request authenticated with an installation
// access token, like doToken but with a method and a body.
func (a *Authenticator) doTokenJSON(ctx context.Context, method, url, token string, in any, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("githubapp: %s %s: status %d: %s", method, url, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
