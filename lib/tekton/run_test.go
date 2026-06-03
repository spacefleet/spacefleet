package tekton

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// taskRunObj builds a minimal TaskRun unstructured with the given Succeeded
// condition status ("" = no conditions yet) plus an optional pod name.
func taskRunObj(succeededStatus, message, podName string) *unstructured.Unstructured {
	status := map[string]any{}
	if podName != "" {
		status["podName"] = podName
	}
	if succeededStatus != "" {
		status["conditions"] = []any{
			map[string]any{"type": "Succeeded", "status": succeededStatus, "message": message},
		}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": TektonGroup + "/v1",
		"kind":       "TaskRun",
		"metadata":   map[string]any{"name": "run-abc", "namespace": "default"},
		"status":     status,
	}}
}

func TestToRunStatusPhases(t *testing.T) {
	cases := []struct {
		name       string
		succeeded  string
		wantPhase  string
		wantTermnl bool
	}{
		{"pending", "", "Pending", false},
		{"running", "Unknown", "Running", false},
		{"succeeded", "True", "Succeeded", true},
		{"failed", "False", "Failed", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := toRunStatus(taskRunObj(tc.succeeded, "msg", "run-abc-pod"))
			if r.Phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q", r.Phase, tc.wantPhase)
			}
			if r.Terminal() != tc.wantTermnl {
				t.Errorf("Terminal() = %v, want %v", r.Terminal(), tc.wantTermnl)
			}
			if r.Name != "run-abc" || r.Namespace != "default" {
				t.Errorf("name/namespace = %q/%q", r.Name, r.Namespace)
			}
			if tc.succeeded != "" && r.Message != "msg" {
				t.Errorf("message = %q, want %q", r.Message, "msg")
			}
		})
	}
}

func TestToRunStatusPodName(t *testing.T) {
	r := toRunStatus(taskRunObj("Unknown", "", "run-abc-pod"))
	if r.PodName != "run-abc-pod" {
		t.Errorf("PodName = %q, want %q", r.PodName, "run-abc-pod")
	}
}
