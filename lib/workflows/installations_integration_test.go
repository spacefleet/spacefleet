//go:build integration

package workflows

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/component"
	"github.com/spacefleet/spacefleet/lib/helm"
	"github.com/spacefleet/spacefleet/lib/testsupport"
	"github.com/spacefleet/spacefleet/lib/tofu"
)

// TestReplaceWorkflowResolvesInstallations: a saved component stores the
// installation on the account owning its repository, whatever the client sent
// — never another organization's installation, and none for a repository no
// connected account owns.
func TestReplaceWorkflowResolvesInstallations(t *testing.T) {
	client := testsupport.NewEntClient(t)
	svc := NewService(client)
	svc.SetManagedState(true)
	ctx := context.Background()
	org := newOrg(t, client, "Acme")
	other := newOrg(t, client, "Other")
	app := newApp(t, client, org.ID, "web")

	newInstall := func(orgID uuid.UUID, id int64, login string) *ent.GitHubInstallation {
		t.Helper()
		in, err := client.GitHubInstallation.Create().
			SetOrganizationID(orgID).SetInstallationID(id).SetAccountLogin(login).
			Save(ctx)
		if err != nil {
			t.Fatalf("create installation: %v", err)
		}
		return in
	}
	acme := newInstall(org.ID, 1, "acme")
	foreign := newInstall(other.ID, 2, "acme")

	repo := func(name, repoURL string, sent *uuid.UUID) ComponentInput {
		c := tofuComponent(uuid.New(), tofu.BackendSpacefleet)
		c.Name = name
		c.Config[helm.ConfigRepoURL] = repoURL
		c.GitHubInstallationID = sent
		return c
	}
	stages := stagesOf([]ComponentInput{
		repo("typed", "https://github.com/acme/infra", nil),
		repo("foreign", "https://github.com/acme/infra", &foreign.ID),
		repo("public", "https://github.com/initech/infra", &acme.ID),
	})
	if _, err := svc.ReplaceWorkflow(ctx, org.ID, app.ID, stages); err != nil {
		t.Fatalf("save: %v", err)
	}

	comps, err := client.Component.Query().Where(component.ApplicationID(app.ID)).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]uuid.UUID{"typed": acme.ID, "foreign": acme.ID, "public": uuid.Nil}
	for _, c := range comps {
		if c.GithubInstallationID != want[c.Name] {
			t.Errorf("%s: installation = %v, want %v", c.Name, c.GithubInstallationID, want[c.Name])
		}
	}
}
