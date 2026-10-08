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
		{"replace workflow", http.MethodPut, "/api/applications/" + id + "/workflow", `{"stages":[]}`},
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
// secret-bearing `values` key, while an editor (canSee=true) does.
func TestToAPIComponentRedaction(t *testing.T) {
	c := &ent.Component{
		ID:                uuid.New(),
		Name:              "web",
		Type:              component.TypeHelm,
		Config:            map[string]string{"chart_source": "http_repo", "values": "secret: hunter2"},
		ContinueOnFailure: true,
	}

	viewer := toAPIComponent(c, false)
	if _, ok := viewer.Config["values"]; ok {
		t.Fatalf("viewer config still has values: %v", viewer.Config)
	}
	if viewer.Config["chart_source"] != "http_repo" {
		t.Fatalf("non-secret config dropped: %v", viewer.Config)
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

// TestToStageInput proves the API input maps to the service input with the
// client-provided ids, trimmed names, defaulted optionals, and the
// components kept in order.
func TestToStageInput(t *testing.T) {
	stageID, a, b := uuid.New(), uuid.New(), uuid.New()
	cluster := uuid.New()
	ns := "prod"
	cont := true
	in := toStageInput(WorkflowStageInput{
		Id:   stageID,
		Name: "  Deploy  ",
		Components: []ComponentInput{
			{
				Id:                a,
				Name:              "  web  ",
				Type:              "helm",
				Config:            &map[string]string{"chart_source": "oci"},
				ContinueOnFailure: &cont,
				TargetClusterId:   &cluster,
				TargetNamespace:   &ns,
			},
			{Id: b, Name: "cfg", Type: "manifest"},
		},
	})
	if in.ID != stageID || in.Name != "Deploy" {
		t.Fatalf("stage = %v %q, want the id and a trimmed name", in.ID, in.Name)
	}
	if len(in.Components) != 2 || in.Components[0].ID != a || in.Components[1].ID != b {
		t.Fatalf("components not kept in order: %+v", in.Components)
	}
	web := in.Components[0]
	if web.Name != "web" {
		t.Fatalf("name not trimmed: %q", web.Name)
	}
	if !web.ContinueOnFailure || in.Components[1].ContinueOnFailure {
		t.Fatalf("continue_on_failure not mapped")
	}
	if web.TargetClusterID == nil || *web.TargetClusterID != cluster || web.TargetNamespace != "prod" {
		t.Fatalf("target not mapped: %v %q", web.TargetClusterID, web.TargetNamespace)
	}
}

// TestToAPIWorkflow proves stages map in order with their components.
func TestToAPIWorkflow(t *testing.T) {
	web := &ent.Component{ID: uuid.New(), Name: "web", Type: component.TypeHelm, Config: map[string]string{"values": "x"}}
	stages := []*ent.WorkflowStage{
		{ID: uuid.New(), Name: "Empty"},
		{ID: uuid.New(), Name: "Deploy", Edges: ent.WorkflowStageEdges{Components: []*ent.Component{web}}},
	}
	out := toAPIWorkflow(stages, false)
	if len(out.Stages) != 2 || out.Stages[0].Name != "Empty" || out.Stages[1].Name != "Deploy" {
		t.Fatalf("stages = %+v", out.Stages)
	}
	if out.Stages[0].Components == nil || len(out.Stages[0].Components) != 0 {
		t.Fatalf("an empty stage should have [] components, got %v", out.Stages[0].Components)
	}
	if len(out.Stages[1].Components) != 1 || out.Stages[1].Components[0].Id != web.ID {
		t.Fatalf("components = %+v", out.Stages[1].Components)
	}
	if _, ok := out.Stages[1].Components[0].Config["values"]; ok {
		t.Fatal("viewer workflow still has values")
	}
}

// TestIsWorkflowValidation proves the workflow/config/action sentinels
// classify as validation (→ 400) and an unrelated error does not.
func TestIsWorkflowValidation(t *testing.T) {
	for _, err := range []error{
		workflows.ErrDuplicateID, workflows.ErrMissingID, workflows.ErrInvalidStage,
		workflows.ErrInvalidConfig, workflows.ErrInvalidTarget, workflows.ErrInvalidAction,
	} {
		if !isWorkflowValidation(err) {
			t.Fatalf("%v should classify as validation", err)
		}
	}
	if isWorkflowValidation(&ent.NotFoundError{}) {
		t.Fatalf("NotFoundError should not classify as validation")
	}
}
