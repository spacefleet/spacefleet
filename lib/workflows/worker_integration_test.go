//go:build integration

package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

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
	"github.com/spacefleet/spacefleet/lib/tofu"
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
	resourcesJSON := `[{"address":"aws_s3_bucket.data","mode":"managed","type":"aws_s3_bucket","name":"data","provider":"registry.opentofu.org/hashicorp/aws","id":"acme-data"},{"address":"module.net.aws_vpc.main","mode":"managed","type":"aws_vpc","name":"main","provider":"registry.opentofu.org/hashicorp/aws","id":"vpc-1"}]`
	w.captureHandover = func(_ context.Context, _ k8s.Connection, namespace, name, key string) ([]byte, error) {
		reads = append(reads, namespace+"/"+name+"#"+key)
		if key == tofu.ResourcesKey {
			return []byte(resourcesJSON), nil
		}
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

	// The capture read exactly the apply pair's Secret: its outputs key and its
	// resources key, once each.
	wantReads := []string{
		tekton.JobsNamespace + "/" + secretName + "#" + tofu.OutputsKey,
		tekton.JobsNamespace + "/" + secretName + "#" + tofu.ResourcesKey,
	}
	if !reflect.DeepEqual(reads, wantReads) {
		t.Errorf("handover reads = %v, want %v", reads, wantReads)
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
	// … with the resource inventory beside them …
	var inventory []tofuResource
	if err := json.Unmarshal([]byte(applyCR.Resources), &inventory); err != nil {
		t.Fatalf("stored resources do not parse: %v (%q)", err, applyCR.Resources)
	}
	if len(inventory) != 2 || inventory[1].Address != "module.net.aws_vpc.main" || string(inventory[1].ID) != `"vpc-1"` {
		t.Errorf("stored resources = %s", applyCR.Resources)
	}
	// … and never on the plan unit.
	if planCR := componentRunFor(t, client, run.ID, tf.ID); planCR.Outputs != "" || planCR.Resources != "" {
		t.Errorf("plan unit outputs/resources = %q/%q, want empty", planCR.Outputs, planCR.Resources)
	}

	// The worker deleted the spent Secret right after reading it; the terminal
	// sweep then deletes it once more (an idempotent backstop that runs for
	// every settled run). Two deletes of the same name, nothing else.
	if len(deletes) != 2 || deletes[0] != secretName || deletes[1] != secretName {
		t.Errorf("handover deletes = %v, want [%s %s] (capture + terminal sweep)", deletes, secretName, secretName)
	}
}

// TestWorkCarriesForwardHalfCapturedState: when an apply's capture brings
// back only one half (here `tofu show` failed, so the resources file is
// empty), the row that becomes the component's latest state carries the
// other half forward from the previous record — a working `tofu output`
// must not hide the last known inventory. With nothing previously recorded
// the missing half simply stays empty.
func TestWorkCarriesForwardHalfCapturedState(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	tf := addTerraformComponent(t, client, org.ID, app.ID, "infra")
	w := newTestWorker(client, succeedingFuncs())
	w.deleteHandover = func(context.Context, k8s.Connection, string, string) error { return nil }

	// First run: `tofu output` fails (empty file), the inventory is captured.
	const inventory = `[{"address":"aws_s3_bucket.data","mode":"managed","type":"aws_s3_bucket","name":"data","provider":"aws","id":"acme-data"}]`
	w.captureHandover = func(_ context.Context, _ k8s.Connection, _, _, key string) ([]byte, error) {
		if key == tofu.ResourcesKey {
			return []byte(inventory), nil
		}
		return nil, nil
	}
	run1, args1 := beginRun(t, svc, org.ID, app.ID)
	if err := w.Work(ctx, workerJob(args1, 1, 3)); err != nil {
		t.Fatalf("Work 1: %v", err)
	}
	first := componentRunFor(t, client, run1.ID, deriveApplyID(tf.ID))
	if first.Outputs != "" || first.Resources == "" {
		t.Fatalf("first apply outputs/resources = %q/%q, want none/inventory", first.Outputs, first.Resources)
	}

	// Second run: the outputs come back, `tofu show` fails this time.
	const outputs = `{"namespace":{"sensitive":false,"type":"string","value":"customer-a"}}`
	w.captureHandover = func(_ context.Context, _ k8s.Connection, _, _, key string) ([]byte, error) {
		if key == tofu.OutputsKey {
			return []byte(outputs), nil
		}
		return []byte("\n"), nil
	}
	run2, args2 := beginRun(t, svc, org.ID, app.ID)
	if err := w.Work(ctx, workerJob(args2, 1, 3)); err != nil {
		t.Fatalf("Work 2: %v", err)
	}
	second := componentRunFor(t, client, run2.ID, deriveApplyID(tf.ID))
	if second.Outputs == "" {
		t.Fatal("second apply has no outputs persisted")
	}
	if second.Resources != first.Resources {
		t.Errorf("second apply resources = %q, want the first run's inventory carried forward", second.Resources)
	}
	latest, err := svc.LatestComponentState(ctx, org.ID, app.ID, tf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != second.ID || latest.Resources != first.Resources {
		t.Errorf("latest state = run %s resources %q, want the second apply carrying the inventory", latest.WorkflowRunID, latest.Resources)
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
		w.captureHandover = func(context.Context, k8s.Connection, string, string, string) ([]byte, error) {
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
	w.captureHandover = func(context.Context, k8s.Connection, string, string, string) ([]byte, error) {
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

// sweepingWorker builds a worker whose handover deletes are recorded rather
// than sent to a cluster, over a resolver whose runner lookups always succeed.
func sweepingWorker(svc *Service, deleted *[]string) *WorkflowRunWorker {
	resolves := 0
	return &WorkflowRunWorker{
		svc:      svc,
		resolver: deploy.NewResolver(countingConns{&resolves}, nil, nil, nil, nil),
		deleteHandover: func(_ context.Context, _ k8s.Connection, namespace, name string) error {
			*deleted = append(*deleted, namespace+"/"+name)
			return nil
		},
	}
}

// TestSweepIfSettled proves the aborted-job path releases a cancelled run's
// planfile-handover Secrets — CancelRun settles the run and then cancels the
// River job, so Work never reaches its terminal sweep — while a run that is
// still in flight (a retry may yet apply its reviewed plan) is left alone.
func TestSweepIfSettled(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	planID := addComponent(t, client, org.ID, app.ID, "infra", nil).ID
	run, err := svc.BeginRun(ctx, org.ID, app.ID, ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := svc.MarkRun(ctx, org.ID, run.ID, string(workflowrun.StatusRunning), "running"); err != nil {
		t.Fatalf("MarkRun running: %v", err)
	}
	// The snapshot the job was driving: one OpenTofu plan unit.
	snapshot := GraphSnapshot{Nodes: []GraphNode{
		{ID: planID, Type: TypeTerraform, Config: map[string]string{terraformConfigCommand: terraformCommandPlan}},
	}}
	a := WorkflowRunArgs{OrgID: org.ID, ApplicationID: app.ID, WorkflowRunID: run.ID, Action: ActionDeploy}

	var deleted []string
	w := sweepingWorker(svc, &deleted)

	// Still in flight: nothing is swept.
	w.sweepIfSettled(ctx, a, app, snapshot)
	if len(deleted) != 0 {
		t.Fatalf("in-flight run swept: %v", deleted)
	}

	// Cancelled (settled by CancelRun): the run's handover Secret is released.
	if _, err := svc.CancelRun(ctx, org.ID, app.ID, run.ID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	w.sweepIfSettled(ctx, a, app, snapshot)
	want := tekton.JobsNamespace + "/" + tofuPlanArtifactSecret(run.ID, planID)
	if len(deleted) != 1 || deleted[0] != want {
		t.Fatalf("deleted = %v, want [%s]", deleted, want)
	}
}

// TestReaperSweepsAbandonedRun proves a run the reaper settles has its
// planfile-handover Secrets released through the worker's reap hook — the
// worker that owned the run died, so nothing else would — rebuilt from the
// run row's own graph snapshot.
func TestReaperSweepsAbandonedRun(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	tf := addComponent(t, client, org.ID, app.ID, "infra", map[string]string{
		"repo_url": "https://example.com/infra.git", "path": ".",
		terraformConfigBackend: "s3", terraformConfigBackendConfig: `{"bucket":"b","key":"k","region":"r"}`,
	})
	if _, err := client.Component.UpdateOneID(tf.ID).SetType(TypeTerraform).Save(ctx); err != nil {
		t.Fatalf("set terraform type: %v", err)
	}
	run, err := svc.BeginRun(ctx, org.ID, app.ID, ActionDeploy)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := svc.SetRunJob(ctx, org.ID, run.ID, "job-dead"); err != nil {
		t.Fatalf("SetRunJob: %v", err)
	}
	if err := svc.MarkRun(ctx, org.ID, run.ID, string(workflowrun.StatusRunning), "running"); err != nil {
		t.Fatalf("MarkRun running: %v", err)
	}
	if err := client.WorkflowRun.UpdateOneID(run.ID).SetStartedAt(time.Now().Add(-2 * reapMaxLifetime)).Exec(ctx); err != nil {
		t.Fatalf("backdate started_at: %v", err)
	}

	var deleted []string
	w := sweepingWorker(svc, &deleted)
	svc.OnReaped(w.sweepReapedRun)

	jobGone := func(context.Context, string) (bool, error) { return false, nil }
	reaped, err := svc.ReapStuckRuns(ctx, reapMaxLifetime, jobGone)
	if err != nil || reaped != 1 {
		t.Fatalf("ReapStuckRuns = %d, %v; want 1 reaped", reaped, err)
	}
	// The plan unit keeps the authored id, so its Secret is keyed off it.
	want := tekton.JobsNamespace + "/" + tofuPlanArtifactSecret(run.ID, tf.ID)
	if len(deleted) != 1 || deleted[0] != want {
		t.Fatalf("deleted = %v, want [%s]", deleted, want)
	}
}

// TestWorkStateOpGatedThenRecordsState drives a state operation end to end
// through the worker: the first attempt parks the single unit (and the run)
// at the approval gate without submitting anything; after approval the
// resume attempt runs the unit — its script is the operation, not a plan or
// apply, running as the handover ServiceAccount — reads the refreshed outputs
// + inventory back through the unit's own handover Secret, persists them on
// the step under the authored component id, and the component-state lookup
// then reports them as the latest state.
func TestWorkStateOpGatedThenRecordsState(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, client, org.ID, "web")
	tf := addTerraformComponent(t, client, org.ID, app.ID, "infra")
	run, err := svc.BeginStateOp(ctx, org.ID, app.ID, tf.ID, tofu.StateOp{Operation: tofu.StateOpRemove, Address: "aws_instance.web"})
	if err != nil {
		t.Fatalf("BeginStateOp: %v", err)
	}
	args := WorkflowRunArgs{WorkflowRunID: run.ID, OrgID: org.ID, ApplicationID: app.ID, Action: ActionStateOp}
	secretName := tofuPlanArtifactSecret(run.ID, tf.ID)

	var submitted []tekton.RunSpec
	funcs := succeedingFuncs()
	funcs.Submit = func(_ context.Context, _ k8s.Connection, _ string, spec tekton.RunSpec) (*tekton.RunStatus, error) {
		submitted = append(submitted, spec)
		return &tekton.RunStatus{Name: spec.Name + "-r1", Phase: "Running"}, nil
	}
	w := newTestWorker(client, funcs)
	var reads []string
	w.captureHandover = func(_ context.Context, _ k8s.Connection, _, name, key string) ([]byte, error) {
		reads = append(reads, name+"#"+key)
		if key == tofu.ResourcesKey {
			return []byte(`[{"address":"aws_s3_bucket.data","mode":"managed","type":"aws_s3_bucket","name":"data","provider":"p","id":"acme-data"}]`), nil
		}
		return []byte(`{"bucket":{"sensitive":false,"type":"string","value":"acme-data"}}`), nil
	}

	// First attempt: parks at the gate, submits nothing.
	if err := w.Work(ctx, workerJob(args, 1, 3)); err != nil {
		t.Fatalf("Work (gated): %v", err)
	}
	if got := runStatus(t, client, run.ID); got != workflowrun.StatusAwaitingApproval {
		t.Fatalf("run = %q, want awaiting_approval", got)
	}
	step := componentRunFor(t, client, run.ID, tf.ID)
	if step.Status != componentrun.StatusAwaitingApproval || len(submitted) != 0 {
		t.Fatalf("step = %q with %d submits, want awaiting_approval and none", step.Status, len(submitted))
	}

	// Approve, then the resume attempt runs the operation.
	if _, open, err := svc.ApproveComponentRun(ctx, org.ID, app.ID, run.ID, step.ID, "ops@example.com", DecisionApprove); err != nil || !open {
		t.Fatalf("approve: err=%v open=%v", err, open)
	}
	if err := w.Work(ctx, workerJob(args, 1, 3)); err != nil {
		t.Fatalf("Work (resumed): %v", err)
	}
	if got := runStatus(t, client, run.ID); got != workflowrun.StatusSucceeded {
		t.Fatalf("run = %q, want succeeded", got)
	}
	if len(submitted) != 1 {
		t.Fatalf("submits = %d, want 1", len(submitted))
	}
	spec := submitted[0]
	if !strings.Contains(spec.Script, "tofu 'state' 'rm' 'aws_instance.web'\n") || strings.Contains(spec.Script, "tofu plan") || strings.Contains(spec.Script, "tofu apply") {
		t.Errorf("script is not the state operation:\n%s", spec.Script)
	}
	if spec.ServiceAccountName != secretName || !strings.Contains(spec.Script, secretName) {
		t.Errorf("unit must run as its handover ServiceAccount %q and store into that Secret (sa=%q)", secretName, spec.ServiceAccountName)
	}
	if want := []string{secretName + "#" + tofu.OutputsKey, secretName + "#" + tofu.ResourcesKey}; !reflect.DeepEqual(reads, want) {
		t.Errorf("handover reads = %v, want %v", reads, want)
	}
	step = componentRunFor(t, client, run.ID, tf.ID)
	if step.Status != componentrun.StatusSucceeded || step.ApprovedBy != "ops@example.com" || !strings.Contains(step.Resources, "aws_s3_bucket.data") || !strings.Contains(step.Outputs, "acme-data") {
		t.Errorf("step = %+v, want succeeded with the refreshed outputs + inventory", step)
	}
	latest, err := svc.LatestComponentState(ctx, org.ID, app.ID, tf.ID)
	if err != nil || latest.ID != step.ID {
		t.Errorf("LatestComponentState = %v (err %v), want the state-op step %s", latest, err, step.ID)
	}
}
