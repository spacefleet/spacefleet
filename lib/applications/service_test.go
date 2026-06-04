//go:build integration

package applications

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/application"
	"github.com/spacefleet/spacefleet/ent/chartcredential"
	"github.com/spacefleet/spacefleet/ent/cluster"
	"github.com/spacefleet/spacefleet/ent/deployment"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/k8s"
	"github.com/spacefleet/spacefleet/lib/testsupport"
)

// stubConns satisfies ConnResolver; the validation/org-scoping tests never reach
// a rollout, so it only needs to exist.
type stubConns struct{}

func (stubConns) ConnForTekton(context.Context, uuid.UUID, uuid.UUID) (k8s.Connection, error) {
	return k8s.Connection{}, nil
}

func newOrg(t *testing.T, client *ent.Client, name string) *ent.Organization {
	t.Helper()
	org, err := client.Organization.Create().SetName(name).Save(context.Background())
	if err != nil {
		t.Fatalf("create org %q: %v", name, err)
	}
	return org
}

func newCluster(t *testing.T, client *ent.Client, orgID uuid.UUID, name string, method cluster.ConnectionMethod, tektonEnabled bool) *ent.Cluster {
	t.Helper()
	ctx := context.Background()
	c, err := client.Cluster.Create().
		SetOrganizationID(orgID).
		SetName(name).
		SetConnectionMethod(method).
		Save(ctx)
	if err != nil {
		t.Fatalf("create cluster %q: %v", name, err)
	}
	if tektonEnabled {
		if _, err := client.TektonInstallation.Create().SetClusterID(c.ID).SetEnabled(true).Save(ctx); err != nil {
			t.Fatalf("enable tekton on %q: %v", name, err)
		}
	}
	return c
}

func httpRepoParams(target, runner uuid.UUID) CreateParams {
	return CreateParams{
		Name:            "web",
		ChartSource:     helm.SourceHTTPRepo,
		Config:          map[string]string{helm.ConfigRepoURL: "https://charts.example.com", helm.ConfigChart: "nginx"},
		TargetNamespace: "apps",
		TargetClusterID: target,
		RunnerClusterID: runner,
	}
}

func TestCreateValidAndOrgScoped(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)

	app, err := svc.Create(ctx, org.ID, httpRepoParams(target.ID, runner.ID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if app.Status != application.StatusPending {
		t.Errorf("status = %q, want pending", app.Status)
	}

	// A different org cannot see it.
	other := newOrg(t, client, "Other")
	if _, err := svc.Get(ctx, other.ID, app.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org Get error = %v, want NotFound", err)
	}
	// The owning org can.
	if _, err := svc.Get(ctx, org.ID, app.ID); err != nil {
		t.Errorf("same-org Get: %v", err)
	}
}

func TestCreateRunnerNotJobRunner(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, false) // tekton not enabled

	_, err := svc.Create(ctx, org.ID, httpRepoParams(target.ID, runner.ID))
	if !IsValidation(err) {
		t.Fatalf("Create error = %v, want ValidationError", err)
	}
}

func TestCreateInClusterRequiresSameRunner(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "in-cluster-target", cluster.ConnectionMethodInCluster, false)
	runner := newCluster(t, client, org.ID, "other-runner", cluster.ConnectionMethodToken, true)

	// in_cluster target + different runner → rejected.
	_, err := svc.Create(ctx, org.ID, httpRepoParams(target.ID, runner.ID))
	if !IsValidation(err) {
		t.Fatalf("mismatched runner error = %v, want ValidationError", err)
	}

	// in_cluster target where the target is itself the (tekton-enabled) runner → ok.
	selfRunner := newCluster(t, client, org.ID, "self", cluster.ConnectionMethodInCluster, true)
	app, err := svc.Create(ctx, org.ID, httpRepoParams(selfRunner.ID, selfRunner.ID))
	if err != nil {
		t.Fatalf("in_cluster self-runner Create: %v", err)
	}
	if app.TargetClusterID != app.RunnerClusterID {
		t.Errorf("expected target == runner")
	}
}

func TestCreateMissingChartFields(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)

	p := httpRepoParams(target.ID, runner.ID)
	p.Config = map[string]string{helm.ConfigRepoURL: "https://c"} // missing chart
	if _, err := svc.Create(ctx, org.ID, p); !IsValidation(err) {
		t.Fatalf("error = %v, want ValidationError for missing chart", err)
	}
}

func TestCreateTargetClusterFromAnotherOrgRejected(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	other := newOrg(t, client, "Other")
	foreignTarget := newCluster(t, client, other.ID, "foreign", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)

	_, err := svc.Create(ctx, org.ID, httpRepoParams(foreignTarget.ID, runner.ID))
	if !IsValidation(err) {
		t.Fatalf("error = %v, want ValidationError for cross-org target", err)
	}
}

// newApp registers a deployable app (token runner with Tekton) for the rollout/
// deployment tests.
func newApp(t *testing.T, svc *Service, client *ent.Client, orgID uuid.UUID) *ent.Application {
	t.Helper()
	ctx := context.Background()
	target := newCluster(t, client, orgID, "target-"+uuid.NewString()[:8], cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, orgID, "runner-"+uuid.NewString()[:8], cluster.ConnectionMethodToken, true)
	app, err := svc.Create(ctx, orgID, httpRepoParams(target.ID, runner.ID))
	if err != nil {
		t.Fatalf("Create app: %v", err)
	}
	return app
}

func TestRecordAndListDeployments(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, svc, client, org.ID)

	first, err := svc.RecordDeployment(ctx, org.ID, app.ID, helm.ActionDeploy, "10")
	if err != nil {
		t.Fatalf("RecordDeployment: %v", err)
	}
	if first.Status != deployment.StatusRunning || first.Action != deployment.ActionDeploy {
		t.Errorf("new run = (%q,%q), want (running,deploy)", first.Status, first.Action)
	}
	if first.FinishedAt != nil {
		t.Errorf("new run finished_at = %v, want nil", first.FinishedAt)
	}
	if _, err := svc.RecordDeployment(ctx, org.ID, app.ID, helm.ActionUpgrade, "11"); err != nil {
		t.Fatalf("RecordDeployment 2: %v", err)
	}

	// Newest-first.
	list, err := svc.ListDeployments(ctx, org.ID, app.ID)
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	if list[0].Action != deployment.ActionUpgrade {
		t.Errorf("list[0].Action = %q, want upgrade (newest first)", list[0].Action)
	}

	// Unknown action is rejected before a row is written.
	if _, err := svc.RecordDeployment(ctx, org.ID, app.ID, "frobnicate", "12"); !IsValidation(err) {
		t.Errorf("RecordDeployment bad action error = %v, want ValidationError", err)
	}
}

func TestGetDeploymentOrgScoped(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, svc, client, org.ID)
	dep, err := svc.RecordDeployment(ctx, org.ID, app.ID, helm.ActionDeploy, "20")
	if err != nil {
		t.Fatalf("RecordDeployment: %v", err)
	}

	if _, err := svc.GetDeployment(ctx, org.ID, app.ID, dep.ID); err != nil {
		t.Errorf("same-org GetDeployment: %v", err)
	}
	other := newOrg(t, client, "Other")
	if _, err := svc.GetDeployment(ctx, other.ID, app.ID, dep.ID); !ent.IsNotFound(err) {
		t.Errorf("cross-org GetDeployment error = %v, want NotFound", err)
	}
}

func TestMarkRolloutUpdatesDeployment(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	// Stub the cluster log-capture seam so the terminal transition stores logs
	// without a live runner.
	svc.captureLogs = func(context.Context, *ent.Application, string) string {
		return "helm: release deployed\n"
	}
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	app := newApp(t, svc, client, org.ID)
	dep, err := svc.RecordDeployment(ctx, org.ID, app.ID, helm.ActionDeploy, "30")
	if err != nil {
		t.Fatalf("RecordDeployment: %v", err)
	}

	// In-flight: status stays running, run name recorded, no finish/logs yet.
	if err := svc.MarkRollout(ctx, org.ID, app.ID, "30", helm.StatusDeploying, "submitted run helm-web-abc", "helm-web-abc"); err != nil {
		t.Fatalf("MarkRollout in-flight: %v", err)
	}
	got, err := svc.GetDeployment(ctx, org.ID, app.ID, dep.ID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.Status != deployment.StatusRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.RunName != "helm-web-abc" {
		t.Errorf("run_name = %q, want helm-web-abc", got.RunName)
	}
	if got.FinishedAt != nil || got.Logs != "" {
		t.Errorf("in-flight finished_at/logs = (%v,%q), want (nil,\"\")", got.FinishedAt, got.Logs)
	}

	// Terminal: succeeded, finished_at stamped, logs captured.
	if err := svc.MarkRollout(ctx, org.ID, app.ID, "30", helm.StatusDeployed, "release deployed", "helm-web-abc"); err != nil {
		t.Fatalf("MarkRollout terminal: %v", err)
	}
	got, err = svc.GetDeployment(ctx, org.ID, app.ID, dep.ID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.Status != deployment.StatusSucceeded {
		t.Errorf("status = %q, want succeeded", got.Status)
	}
	if got.FinishedAt == nil {
		t.Errorf("finished_at = nil, want a timestamp")
	}
	if got.Logs == "" {
		t.Errorf("logs were not captured on terminal")
	}
}

// newChartCredential creates a chart credential row directly for validation
// tests (the applications service checks type↔source compatibility by querying
// the row, so no sealer is needed here).
func newChartCredential(t *testing.T, client *ent.Client, orgID uuid.UUID, name string, typ chartcredential.Type) *ent.ChartCredential {
	t.Helper()
	c, err := client.ChartCredential.Create().
		SetOrganizationID(orgID).
		SetName(name).
		SetType(typ).
		SetUsername("user").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create chart credential %q: %v", name, err)
	}
	return c
}

func TestCreateWithCompatibleCredential(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)
	cred := newChartCredential(t, client, org.ID, "registry", chartcredential.TypeBasicAuth)

	p := httpRepoParams(target.ID, runner.ID)
	p.ChartCredentialID = &cred.ID
	app, err := svc.Create(ctx, org.ID, p)
	if err != nil {
		t.Fatalf("Create with compatible (basic_auth ↔ http_repo) credential: %v", err)
	}
	if app.ChartCredentialID != cred.ID {
		t.Errorf("ChartCredentialID = %v, want %v", app.ChartCredentialID, cred.ID)
	}
}

func TestCreateWithIncompatibleCredentialRejected(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)
	// An OCI credential attached to an http_repo app is a type mismatch.
	cred := newChartCredential(t, client, org.ID, "registry", chartcredential.TypeOci)

	p := httpRepoParams(target.ID, runner.ID)
	p.ChartCredentialID = &cred.ID
	if _, err := svc.Create(ctx, org.ID, p); !IsValidation(err) {
		t.Fatalf("error = %v, want ValidationError for incompatible credential type", err)
	}
}

func TestCreateWithCredentialFromAnotherOrgRejected(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	other := newOrg(t, client, "Other")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)
	foreignCred := newChartCredential(t, client, other.ID, "foreign", chartcredential.TypeBasicAuth)

	p := httpRepoParams(target.ID, runner.ID)
	p.ChartCredentialID = &foreignCred.ID
	if _, err := svc.Create(ctx, org.ID, p); !IsValidation(err) {
		t.Fatalf("error = %v, want ValidationError for cross-org credential", err)
	}
}

// newInstallation creates a GitHub installation row directly, for the
// validateInstallation tests.
func newInstallation(t *testing.T, client *ent.Client, orgID uuid.UUID, installationID int64) *ent.GitHubInstallation {
	t.Helper()
	inst, err := client.GitHubInstallation.Create().
		SetOrganizationID(orgID).
		SetInstallationID(installationID).
		SetAccountLogin("acme").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create github installation: %v", err)
	}
	return inst
}

func gitParams(target, runner uuid.UUID) CreateParams {
	return CreateParams{
		Name:            "git-app",
		ChartSource:     helm.SourceGit,
		Config:          map[string]string{helm.ConfigRepoURL: "https://github.com/org/charts.git"},
		TargetNamespace: "apps",
		TargetClusterID: target,
		RunnerClusterID: runner,
	}
}

func TestCreateGitWithInstallation(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)
	inst := newInstallation(t, client, org.ID, 12345)

	p := gitParams(target.ID, runner.ID)
	p.GitHubInstallationID = &inst.ID
	app, err := svc.Create(ctx, org.ID, p)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if app.GithubInstallationID != inst.ID {
		t.Errorf("GithubInstallationID = %v, want %v", app.GithubInstallationID, inst.ID)
	}
}

func TestCreateInstallationOnlyAllowedForGit(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)
	inst := newInstallation(t, client, org.ID, 12345)

	// An installation on an http_repo (non-git) app is rejected.
	p := httpRepoParams(target.ID, runner.ID)
	p.GitHubInstallationID = &inst.ID
	if _, err := svc.Create(ctx, org.ID, p); !IsValidation(err) {
		t.Fatalf("error = %v, want ValidationError for installation on non-git source", err)
	}
}

func TestCreateGitWithInstallationFromAnotherOrgRejected(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client, stubConns{}, nil, nil)
	ctx := context.Background()

	org := newOrg(t, client, "Acme")
	other := newOrg(t, client, "Other")
	target := newCluster(t, client, org.ID, "target", cluster.ConnectionMethodToken, false)
	runner := newCluster(t, client, org.ID, "runner", cluster.ConnectionMethodToken, true)
	foreign := newInstallation(t, client, other.ID, 999)

	p := gitParams(target.ID, runner.ID)
	p.GitHubInstallationID = &foreign.ID
	if _, err := svc.Create(ctx, org.ID, p); !IsValidation(err) {
		t.Fatalf("error = %v, want ValidationError for cross-org installation", err)
	}
}
