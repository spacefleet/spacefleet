package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/lib/workflows"
)

// These are DB-free handler/mapper tests for the workflow + run endpoints. The
// nil-service paths short-circuit in the resolve preamble (no database needed);
// the redaction + mapping is pure. Role enforcement and the 404/409 paths that
// need real rows live with the integration harness.

// TestWorkflowRoutesNilServiceReturn503 proves the typed workflow/run handlers
// report a clear 503 (not a panic) when the workflows service is unwired.
func TestWorkflowRoutesNilServiceReturn503(t *testing.T) {
	// Applications wired-nil too: resolveWorkflowRead checks workflows first, so a
	// fully-empty deps still 503s on "workflows service not configured".
	handler := newTestHandler(ServerDeps{})

	id := zeroUUID
	cases := []struct {
		name, method, path, body string
	}{
		{"get workflow", http.MethodGet, "/api/applications/" + id + "/workflow", ""},
		{"replace workflow", http.MethodPut, "/api/applications/" + id + "/workflow", `{"components":[]}`},
		{"component outputs", http.MethodGet, "/api/applications/" + id + "/component-outputs", ""},
		{"list runs", http.MethodGet, "/api/applications/" + id + "/runs", ""},
		{"start run", http.MethodPost, "/api/applications/" + id + "/runs", `{"action":"deploy"}`},
		{"get run", http.MethodGet, "/api/applications/" + id + "/runs/" + id, ""},
		{"get component run", http.MethodGet, "/api/applications/" + id + "/runs/" + id + "/components/" + id, ""},
		{"run stream", http.MethodGet, "/api/applications/" + id + "/runs/" + id + "/stream", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := testReq{method: c.method, path: c.path, body: c.body, token: "alice", orgID: id}.do(t, handler)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("got %d, want 503\n%s", rec.Code, rec.Body.String())
			}
			if e := decodeErr(t, rec); e.Code != "unavailable" {
				t.Fatalf("error code = %q, want unavailable", e.Code)
			}
		})
	}
}

// TestToAPIComponentRedaction proves a viewer (canSee=false) never receives the
// secret-bearing `values` key, while an editor (canSee=true) does — and that the
// required array/map fields are non-nil regardless.
func TestToAPIComponentRedaction(t *testing.T) {
	c := &ent.Component{
		ID:                uuid.New(),
		Name:              "web",
		Type:              component.TypeHelm,
		Config:            map[string]string{"chart_source": "http_repo", "values": "secret: hunter2"},
		DependsOn:         nil,
		ContinueOnFailure: true,
	}

	viewer := toAPIComponent(c, false)
	if _, ok := viewer.Config["values"]; ok {
		t.Fatalf("viewer config still has values: %v", viewer.Config)
	}
	if viewer.Config["chart_source"] != "http_repo" {
		t.Fatalf("non-secret config dropped: %v", viewer.Config)
	}
	if viewer.DependsOn == nil {
		t.Fatalf("depends_on should be non-nil ([]), got nil")
	}
	if !viewer.ContinueOnFailure {
		t.Fatalf("continue_on_failure not mapped")
	}

	editor := toAPIComponent(c, true)
	if editor.Config["values"] != "secret: hunter2" {
		t.Fatalf("editor should see values, got %v", editor.Config)
	}
}

// TestRedactGraph proves the snapshot graph has secret config stripped for a
// viewer and passes through verbatim for an editor.
func TestRedactGraph(t *testing.T) {
	snap := workflows.GraphSnapshot{Nodes: []workflows.GraphNode{{
		ID:     uuid.New(),
		Name:   "web",
		Type:   "helm",
		Config: map[string]string{"chart_source": "oci", "values": "secret: x"},
	}}}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}

	// Editor: passes through unchanged.
	if got := redactGraph(string(raw), true); got != string(raw) {
		t.Fatalf("editor graph altered:\n%s", got)
	}

	// Viewer: values removed, chart_source kept.
	var out workflows.GraphSnapshot
	if err := json.Unmarshal([]byte(redactGraph(string(raw), false)), &out); err != nil {
		t.Fatalf("viewer graph not valid json: %v", err)
	}
	if _, ok := out.Nodes[0].Config["values"]; ok {
		t.Fatalf("viewer graph still has values: %v", out.Nodes[0].Config)
	}
	if out.Nodes[0].Config["chart_source"] != "oci" {
		t.Fatalf("viewer graph dropped non-secret config: %v", out.Nodes[0].Config)
	}

	// Empty stays empty; an unparseable snapshot is withheld from a viewer.
	if redactGraph("", false) != "" {
		t.Fatalf("empty graph should stay empty")
	}
	if got := redactGraph("not json", false); got != "" {
		t.Fatalf("unparseable graph should be withheld from viewer, got %q", got)
	}
}

// TestToComponentInput proves the API input maps to the service input with the
// client-provided id, defaulted optionals, and the float32→float64 position
// conversion.
func TestToComponentInput(t *testing.T) {
	id := uuid.New()
	dep := uuid.New()
	cluster := uuid.New()
	ns := "prod"
	cont := true
	deps := []uuid.UUID{dep}
	pos := map[string]float32{"x": 1.5, "y": 2.5}
	in := toComponentInput(ComponentInput{
		Id:                id,
		Name:              "  web  ",
		Type:              "helm",
		Config:            &map[string]string{"chart_source": "oci"},
		ContinueOnFailure: &cont,
		DependsOn:         &deps,
		TargetClusterId:   &cluster,
		TargetNamespace:   &ns,
		Position:          &pos,
	})
	if in.ID != id {
		t.Fatalf("id not preserved: %v", in.ID)
	}
	if in.Name != "web" {
		t.Fatalf("name not trimmed: %q", in.Name)
	}
	if !in.ContinueOnFailure {
		t.Fatalf("continue_on_failure not mapped")
	}
	if len(in.DependsOn) != 1 || in.DependsOn[0] != dep {
		t.Fatalf("depends_on not mapped: %v", in.DependsOn)
	}
	if in.Position["x"] != 1.5 || in.Position["y"] != 2.5 {
		t.Fatalf("position not converted: %v", in.Position)
	}
}

// TestIsWorkflowValidation proves the DAG/config/action sentinels classify as
// validation (→ 400) and an unrelated error does not.
func TestIsWorkflowValidation(t *testing.T) {
	for _, err := range []error{
		workflows.ErrDuplicateID, workflows.ErrMissingID, workflows.ErrUnknownDependency,
		workflows.ErrSelfDependency, workflows.ErrCycle, workflows.ErrInvalidConfig,
		workflows.ErrInvalidTarget, workflows.ErrInvalidAction,
	} {
		if !isWorkflowValidation(err) {
			t.Fatalf("%v should classify as validation", err)
		}
	}
	if isWorkflowValidation(&ent.NotFoundError{}) {
		t.Fatalf("NotFoundError should not classify as validation")
	}
}
