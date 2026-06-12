//go:build integration

package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/componentrun"
	"github.com/spacefleet/spacefleet/ent/workflowrun"
	"github.com/spacefleet/spacefleet/lib/deploy"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/tekton"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// This file covers the worker's retry semantics against the real status guards
// (H3): a retryable executor failure with River attempts remaining must leave the
// run and the failing component in-flight — MarkRun freezes terminal runs, so a
// premature "failed" would make the retry a no-op forever — while the final
// attempt (or a deterministic failure) settles everything terminal. It needs a
// real Postgres because the in-flight guards live in the service's conditional
// UPDATEs; the tekton executor and the cluster connection are faked through the
// worker's seams.

// workerConns satisfies deploy.ConnResolver without a cluster: every lookup
// resolves to the same static token connection. The fake tekton funcs never dial
// it.
type workerConns struct{}

func (workerConns) ConnForTekton(context.Context, uuid.UUID, uuid.UUID) (k8s.Connection, error) {
	return k8s.Connection{Method: k8s.MethodToken, Endpoint: "https://runner.example.com", Credentials: []byte("tok")}, nil
}

// newTestWorker builds a WorkflowRunWorker over the real service (real Postgres)
// with the tekton executor faked.
func newTestWorker(client *ent.Client, funcs tekton.RunFuncs) *WorkflowRunWorker {
	w := NewWorker(NewService(client), deploy.NewResolver(workerConns{}, nil, nil, nil, nil))
	w.funcs = funcs
	w.captureLogs = func(context.Context, k8s.Connection, string) string { return "" }
	// The handover seams must never dial the fake connection; stub them like the
	// executor (only a terraform node would exercise them).
	w.ensureHandover = func(context.Context, k8s.Connection, string, string, map[string]string) error { return nil }
	w.deleteHandover = func(context.Context, k8s.Connection, string, string) error { return nil }
	return w
}

// workerJob builds the River job for one attempt. attempt is 1-based (River's
// Attempt is incremented before Work runs), so attempt == maxAttempts is the
// final attempt.
func workerJob(a WorkflowRunArgs, attempt, maxAttempts int) *river.Job[WorkflowRunArgs] {
	return &river.Job[WorkflowRunArgs]{
		JobRow: &rivertype.JobRow{ID: 7, Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   a,
	}
}

// addManifestComponent creates a manifest workflow node directly (bypassing
// write-time validation — the fake executor never runs the script).
func addManifestComponent(t *testing.T, client *ent.Client, orgID, appID uuid.UUID, name string, dependsOn ...uuid.UUID) *ent.Component {
	t.Helper()
	c, err := client.Component.Create().
		SetOrganizationID(orgID).
		SetApplicationID(appID).
		SetName(name).
		SetType("manifest").
		SetConfig(map[string]string{"repo_url": "https://github.com/acme/manifests.git", "path": "k8s/"}).
		SetDependsOn(dependsOn).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create component %q: %v", name, err)
	}
	return c
}

// beginRun starts a run and returns it plus its args for the worker job.
func beginRun(t *testing.T, svc *Service, orgID, appID uuid.UUID) (*ent.WorkflowRun, WorkflowRunArgs) {
	t.Helper()
	run, err := svc.BeginRun(context.Background(), orgID, appID, ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	return run, WorkflowRunArgs{WorkflowRunID: run.ID, OrgID: orgID, ApplicationID: appID, Action: ActionDeploy}
}

// runStatus re-reads a run's status.
func runStatus(t *testing.T, client *ent.Client, runID uuid.UUID) workflowrun.Status {
	t.Helper()
	run, err := client.WorkflowRun.Get(context.Background(), runID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	return run.Status
}

// componentRunFor re-reads the ComponentRun for one component of a run.
func componentRunFor(t *testing.T, client *ent.Client, runID, componentID uuid.UUID) *ent.ComponentRun {
	t.Helper()
	cr, err := client.ComponentRun.Query().
		Where(componentrun.WorkflowRunID(runID), componentrun.ComponentID(componentID)).
		Only(context.Background())
	if err != nil {
		t.Fatalf("get component run: %v", err)
	}
	return cr
}

// succeedingFuncs is an executor whose submits and watches all succeed: each
// submit assigns a run name and the watch settles Succeeded immediately.
func succeedingFuncs() tekton.RunFuncs {
	return tekton.RunFuncs{
		Submit: func(_ context.Context, _ k8s.Connection, _ string, spec tekton.RunSpec) (*tekton.RunStatus, error) {
			return &tekton.RunStatus{Name: spec.Name + "-r1", Phase: "Running"}, nil
		},
		Get: func(_ context.Context, _ k8s.Connection, _, name string) (*tekton.RunStatus, error) {
			return &tekton.RunStatus{Name: name, Phase: "Running"}, nil
		},
		Watch: func(_ context.Context, _ k8s.Connection, _, name string) (*tekton.RunStream, error) {
			return &tekton.RunStream{Snapshot: tekton.RunStatus{Name: name, Phase: "Succeeded", Message: "ok"}}, nil
		},
		List: func(context.Context, k8s.Connection, string, string) ([]*tekton.RunStatus, error) {
			return nil, nil
		},
	}
}

// addTerraformComponent creates a terraform workflow node directly (bypassing
// write-time validation — the fake executor never runs the script). BeginRun
// expands it into a plan unit (the authored id) and an apply unit
// (deriveApplyID).
func addTerraformComponent(t *testing.T, client *ent.Client, orgID, appID uuid.UUID, name string) *ent.Component {
	t.Helper()
	c, err := client.Component.Create().
		SetOrganizationID(orgID).
		SetApplicationID(appID).
		SetName(name).
		SetType("terraform").
		SetConfig(map[string]string{"repo_url": "https://github.com/acme/infra", "path": "envs/prod", "backend": "s3"}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create terraform component %q: %v", name, err)
	}
	return c
}

// TestWorkCapturesTofuOutputs: when a terraform apply unit settles succeeded on
// a deploy run, the worker reads the outputs the pod handed back through the
// pair's handover Secret, persists the canonical JSON on the apply unit's
// component run (only there — the plan unit captures nothing), and deletes the
// Secret.
func TestWorkCapturesTofuOutputs(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	tf := addTerraformComponent(t, client, org.ID, app.ID, "infra")
	run, args := beginRun(t, svc, org.ID, app.ID)

	secretName := tofuPlanArtifactSecret(run.ID, tf.ID)
	outputsJSON := `{"namespace": {"sensitive": false, "type": "string", "value": "customer-a"}, "db_password": {"sensitive": true, "type": "string", "value": "hunter2"}}`

	w := newTestWorker(client, succeedingFuncs())
	var reads, deletes []string
	w.captureOutputs = func(_ context.Context, _ k8s.Connection, namespace, name string) ([]byte, error) {
		reads = append(reads, namespace+"/"+name)
		return []byte(outputsJSON), nil
	}
	w.deleteHandover = func(_ context.Context, _ k8s.Connection, _, name string) error {
		deletes = append(deletes, name)
		return nil
	}
	if err := w.Work(ctx, workerJob(args, 1, 3)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if got := runStatus(t, client, run.ID); got != workflowrun.StatusSucceeded {
		t.Fatalf("run = %q, want %q", got, workflowrun.StatusSucceeded)
	}

	// The capture read exactly the apply pair's Secret, once.
	wantRead := tekton.JobsNamespace + "/" + secretName
	if len(reads) != 1 || reads[0] != wantRead {
		t.Errorf("outputs reads = %v, want exactly [%s]", reads, wantRead)
	}

	// Persisted on the apply unit (canonicalized but content-identical) …
	applyCR := componentRunFor(t, client, run.ID, deriveApplyID(tf.ID))
	if applyCR.Outputs == "" {
		t.Fatal("apply unit has no outputs persisted")
	}
	var stored map[string]tofuOutput
	if err := json.Unmarshal([]byte(applyCR.Outputs), &stored); err != nil {
		t.Fatalf("stored outputs do not parse: %v", err)
	}
	if string(stored["namespace"].Value) != `"customer-a"` || !stored["db_password"].Sensitive {
		t.Errorf("stored outputs = %s, want namespace + sensitive db_password", applyCR.Outputs)
	}
	// … and never on the plan unit.
	if planCR := componentRunFor(t, client, run.ID, tf.ID); planCR.Outputs != "" {
		t.Errorf("plan unit outputs = %q, want empty", planCR.Outputs)
	}

	// The worker deleted the spent Secret right after reading it; the terminal
	// sweep then deletes it once more (an idempotent backstop that runs for
	// every settled run). Two deletes of the same name, nothing else.
	if len(deletes) != 2 || deletes[0] != secretName || deletes[1] != secretName {
		t.Errorf("handover deletes = %v, want [%s %s] (capture + terminal sweep)", deletes, secretName, secretName)
	}
}

// TestResolveComponentOutputs: the ${{ components.* }} lookup behind the
// planner — the referencing run's own succeeded-with-outputs row wins over a
// newer run's outputs; a run without one (a preview, a skipped upstream) falls
// back to the latest successful outputs; everything is org-scoped; nothing
// recorded resolves to "".
func TestResolveComponentOutputs(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	tf := addTerraformComponent(t, client, org.ID, app.ID, "infra")
	applyID := deriveApplyID(tf.ID)

	// Nothing recorded yet: resolves empty, not an error.
	if got, err := svc.ResolveComponentOutputs(ctx, org.ID, uuid.New(), applyID); err != nil || got != "" {
		t.Fatalf("no outputs anywhere: got (%q, %v), want empty", got, err)
	}

	// Two successive deploy runs, each capturing different outputs.
	deployWithOutputs := func(outputs string) *ent.WorkflowRun {
		t.Helper()
		run, args := beginRun(t, svc, org.ID, app.ID)
		w := newTestWorker(client, succeedingFuncs())
		w.captureOutputs = func(context.Context, k8s.Connection, string, string) ([]byte, error) {
			return []byte(outputs), nil
		}
		if err := w.Work(ctx, workerJob(args, 1, 3)); err != nil {
			t.Fatalf("Work: %v", err)
		}
		return run
	}
	v1 := `{"namespace": {"sensitive": false, "type": "string", "value": "team-v1"}}`
	v2 := `{"namespace": {"sensitive": false, "type": "string", "value": "team-v2"}}`
	run1 := deployWithOutputs(v1)
	run2 := deployWithOutputs(v2)

	// A run with its own succeeded-with-outputs row resolves that row — run1
	// still sees v1 even though run2's outputs are newer.
	if got, err := svc.ResolveComponentOutputs(ctx, org.ID, run1.ID, applyID); err != nil || !strings.Contains(got, "team-v1") {
		t.Errorf("run1 (this-run row): got (%q, %v), want v1", got, err)
	}
	if got, err := svc.ResolveComponentOutputs(ctx, org.ID, run2.ID, applyID); err != nil || !strings.Contains(got, "team-v2") {
		t.Errorf("run2 (this-run row): got (%q, %v), want v2", got, err)
	}

	// A run with no row of its own (a preview's plan applies nothing) falls back
	// to the LATEST successful outputs: v2.
	if got, err := svc.ResolveComponentOutputs(ctx, org.ID, uuid.New(), applyID); err != nil || !strings.Contains(got, "team-v2") {
		t.Errorf("fallback: got (%q, %v), want the latest (v2)", got, err)
	}

	// Org scoping is the tenancy boundary: another org resolves nothing, even
	// with the right run and component ids.
	other := newOrg(t, client, "Other")
	if got, err := svc.ResolveComponentOutputs(ctx, other.ID, run2.ID, applyID); err != nil || got != "" {
		t.Errorf("cross-org: got (%q, %v), want empty", got, err)
	}
}

// TestWorkOutputsCaptureFailureDoesNotFailRun: outputs capture is best-effort —
// a read failure leaves the outputs column empty, keeps the Secret for the
// sweep, and the step (whose apply already succeeded on the cluster) still
// settles succeeded.
func TestWorkOutputsCaptureFailureDoesNotFailRun(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	tf := addTerraformComponent(t, client, org.ID, app.ID, "infra")
	run, args := beginRun(t, svc, org.ID, app.ID)

	w := newTestWorker(client, succeedingFuncs())
	w.captureOutputs = func(context.Context, k8s.Connection, string, string) ([]byte, error) {
		return nil, errors.New("secret read: cluster API unreachable")
	}
	if err := w.Work(ctx, workerJob(args, 1, 3)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if got := runStatus(t, client, run.ID); got != workflowrun.StatusSucceeded {
		t.Fatalf("run = %q, want %q (capture is best-effort)", got, workflowrun.StatusSucceeded)
	}
	if cr := componentRunFor(t, client, run.ID, deriveApplyID(tf.ID)); cr.Status != componentrun.StatusSucceeded || cr.Outputs != "" {
		t.Fatalf("apply unit = (%q, outputs %q), want succeeded with empty outputs", cr.Status, cr.Outputs)
	}
}

// TestWorkRetryableFailureLeavesRunInFlight is the H3 regression: a watch that
// dies after submit (a transient API blip) must NOT settle the run or the
// component — MarkRun's terminal guard would freeze the run at failed and the
// retry's short-circuit would never re-attach. The retried attempt must
// re-attach to the in-flight TaskRun (no resubmit) and recover to succeeded.
func TestWorkRetryableFailureLeavesRunInFlight(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	comp := addManifestComponent(t, client, org.ID, app.ID, "site")
	run, args := beginRun(t, svc, org.ID, app.ID)

	// Attempt 1 of 3: submit succeeds (the TaskRun is now live on the cluster),
	// then the watch fails — the canonical transient blip.
	attempt1 := succeedingFuncs()
	attempt1.Watch = func(context.Context, k8s.Connection, string, string) (*tekton.RunStream, error) {
		return nil, errors.New("watch: cluster API unreachable")
	}
	err := newTestWorker(client, attempt1).Work(ctx, workerJob(args, 1, 3))
	if err == nil {
		t.Fatal("attempt 1: expected an error so River retries")
	}
	if !strings.Contains(err.Error(), "retryable") {
		t.Fatalf("attempt 1 error = %v, want retryable", err)
	}

	if got := runStatus(t, client, run.ID); got != workflowrun.StatusRunning {
		t.Fatalf("run after retryable failure = %q, want %q (must stay in flight for the retry)", got, workflowrun.StatusRunning)
	}
	cr := componentRunFor(t, client, run.ID, comp.ID)
	if cr.Status != componentrun.StatusRunning {
		t.Fatalf("component after retryable failure = %q, want %q", cr.Status, componentrun.StatusRunning)
	}
	if cr.RunName == "" {
		t.Fatal("component lost its submitted run name — the retry could not re-attach")
	}
	if !strings.Contains(cr.Message, "transient failure") {
		t.Errorf("component message = %q, want the transient-failure note", cr.Message)
	}

	// Attempt 2: the persisted run name is still active — the executor must
	// re-attach (never resubmit) and the run recovers to succeeded.
	attempt2 := succeedingFuncs()
	attempt2.Submit = func(context.Context, k8s.Connection, string, tekton.RunSpec) (*tekton.RunStatus, error) {
		t.Error("attempt 2 resubmitted instead of re-attaching to the in-flight run")
		return nil, errors.New("unexpected submit")
	}
	if err := newTestWorker(client, attempt2).Work(ctx, workerJob(args, 2, 3)); err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if got := runStatus(t, client, run.ID); got != workflowrun.StatusSucceeded {
		t.Fatalf("run after retry = %q, want %q", got, workflowrun.StatusSucceeded)
	}
	if got := componentRunFor(t, client, run.ID, comp.ID).Status; got != componentrun.StatusSucceeded {
		t.Fatalf("component after retry = %q, want %q", got, componentrun.StatusSucceeded)
	}
}

// TestWorkRetryableFailureDefersDependentSkips proves a dependent of the
// retryably-failed node is NOT persisted skipped while a retry remains — a
// terminal skip would short-circuit it on the retried attempt even when its
// dependency then succeeds. It stays pending and runs on the retry.
func TestWorkRetryableFailureDefersDependentSkips(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	a := addManifestComponent(t, client, org.ID, app.ID, "base")
	b := addManifestComponent(t, client, org.ID, app.ID, "overlay", a.ID)
	run, args := beginRun(t, svc, org.ID, app.ID)

	// Attempt 1 of 3: even the submit fails — nothing reached the cluster.
	attempt1 := succeedingFuncs()
	attempt1.Submit = func(context.Context, k8s.Connection, string, tekton.RunSpec) (*tekton.RunStatus, error) {
		return nil, errors.New("submit: connection refused")
	}
	if err := newTestWorker(client, attempt1).Work(ctx, workerJob(args, 1, 3)); err == nil {
		t.Fatal("attempt 1: expected an error so River retries")
	}

	if got := runStatus(t, client, run.ID); got != workflowrun.StatusRunning {
		t.Fatalf("run after retryable failure = %q, want %q", got, workflowrun.StatusRunning)
	}
	if got := componentRunFor(t, client, run.ID, b.ID).Status; got != componentrun.StatusPending {
		t.Fatalf("dependent after retryable failure = %q, want %q (a persisted skip would never re-run)", got, componentrun.StatusPending)
	}

	// Attempt 2: everything works; the whole DAG — including the dependent — runs.
	if err := newTestWorker(client, succeedingFuncs()).Work(ctx, workerJob(args, 2, 3)); err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if got := runStatus(t, client, run.ID); got != workflowrun.StatusSucceeded {
		t.Fatalf("run after retry = %q, want %q", got, workflowrun.StatusSucceeded)
	}
	for _, comp := range []*ent.Component{a, b} {
		if got := componentRunFor(t, client, run.ID, comp.ID).Status; got != componentrun.StatusSucceeded {
			t.Fatalf("component %q after retry = %q, want %q", comp.Name, got, componentrun.StatusSucceeded)
		}
	}
}

// TestWorkRetryableFailureFinalAttemptSettles proves the in-flight grace ends
// with the retry budget: on the final attempt a retryable failure settles the
// run failed, the failing component failed, and its dependents skipped — nothing
// is left dangling for the reaper.
func TestWorkRetryableFailureFinalAttemptSettles(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	a := addManifestComponent(t, client, org.ID, app.ID, "base")
	b := addManifestComponent(t, client, org.ID, app.ID, "overlay", a.ID)
	run, args := beginRun(t, svc, org.ID, app.ID)

	funcs := succeedingFuncs()
	funcs.Submit = func(context.Context, k8s.Connection, string, tekton.RunSpec) (*tekton.RunStatus, error) {
		return nil, errors.New("submit: connection refused")
	}
	err := newTestWorker(client, funcs).Work(ctx, workerJob(args, 3, 3))
	if err == nil {
		t.Fatal("final attempt: expected the failure to surface on the job")
	}

	if got := runStatus(t, client, run.ID); got != workflowrun.StatusFailed {
		t.Fatalf("run after final attempt = %q, want %q", got, workflowrun.StatusFailed)
	}
	crA := componentRunFor(t, client, run.ID, a.ID)
	if crA.Status != componentrun.StatusFailed {
		t.Fatalf("failing component after final attempt = %q, want %q", crA.Status, componentrun.StatusFailed)
	}
	if !strings.Contains(crA.Message, "connection refused") {
		t.Errorf("failing component message = %q, want the executor error", crA.Message)
	}
	if got := componentRunFor(t, client, run.ID, b.ID).Status; got != componentrun.StatusSkipped {
		t.Fatalf("dependent after final attempt = %q, want %q", got, componentrun.StatusSkipped)
	}
}

// TestWorkDeterministicFailureSettlesWithoutRetry proves the other side of the
// gate: a TaskRun that ran to a terminal Failed phase is a settled result, so
// even with retries remaining the run settles failed and Work returns nil (no
// retry burn re-running the same failing script).
func TestWorkDeterministicFailureSettlesWithoutRetry(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	comp := addManifestComponent(t, client, org.ID, app.ID, "site")
	run, args := beginRun(t, svc, org.ID, app.ID)

	funcs := succeedingFuncs()
	funcs.Watch = func(_ context.Context, _ k8s.Connection, _, name string) (*tekton.RunStream, error) {
		return &tekton.RunStream{Snapshot: tekton.RunStatus{Name: name, Phase: "Failed", Message: "kubectl apply exited 1"}}, nil
	}
	if err := newTestWorker(client, funcs).Work(ctx, workerJob(args, 1, 3)); err != nil {
		t.Fatalf("deterministic failure must not return an error (no retry): %v", err)
	}

	if got := runStatus(t, client, run.ID); got != workflowrun.StatusFailed {
		t.Fatalf("run = %q, want %q", got, workflowrun.StatusFailed)
	}
	cr := componentRunFor(t, client, run.ID, comp.ID)
	if cr.Status != componentrun.StatusFailed {
		t.Fatalf("component = %q, want %q", cr.Status, componentrun.StatusFailed)
	}
	if !strings.Contains(cr.Message, "kubectl apply exited 1") {
		t.Errorf("component message = %q, want the script failure", cr.Message)
	}
}
