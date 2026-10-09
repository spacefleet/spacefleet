package workflows

import (
	"testing"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
)

func TestGitHubOwner(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://github.com/acme/infra":                        "acme",
		"https://github.com/Acme/Infra.git/":                   "Acme",
		"git@github.com:acme/infra.git":                        "acme",
		"ssh://git@github.com/acme/infra":                      "acme",
		"https://x-access-token:tok@github.com/acme/infra.git": "acme",
		"https://gitlab.com/acme/infra":                        "",
		"https://github.com/acme":                              "",
		"https://github.com/acme/infra/tree/main":              "",
		"oci://ghcr.io/acme/charts":                            "",
		"github.com/acme/infra":                                "",
		"":                                                     "",
	}
	for url, want := range cases {
		if got := githubOwner(url); got != want {
			t.Errorf("githubOwner(%q) = %q, want %q", url, got, want)
		}
	}
}

// TestResolveInstallation: a component's installation is the one on the
// account owning its repository — newest first when an account was connected
// twice, unless the component already names one of that account's; it is
// cleared for a repository no connected account owns, whatever was sent.
func TestResolveInstallation(t *testing.T) {
	t.Parallel()
	acmeNew := &ent.GitHubInstallation{ID: uuid.New(), AccountLogin: "acme"}
	acmeOld := &ent.GitHubInstallation{ID: uuid.New(), AccountLogin: "Acme"}
	globex := &ent.GitHubInstallation{ID: uuid.New(), AccountLogin: "globex"}
	unnamed := &ent.GitHubInstallation{ID: uuid.New()}
	installs := []*ent.GitHubInstallation{acmeNew, unnamed, globex, acmeOld}

	tofu := func(repoURL string, sent *uuid.UUID) ComponentInput {
		return ComponentInput{
			Type:                 TypeTerraform,
			Config:               map[string]string{"repo_url": repoURL},
			GitHubInstallationID: sent,
		}
	}
	cases := []struct {
		name string
		in   ComponentInput
		want *uuid.UUID
	}{
		{"owner's installation", tofu("https://github.com/acme/infra", nil), &acmeNew.ID},
		{"owner matched case-insensitively", tofu("git@github.com:GLOBEX/infra.git", nil), &globex.ID},
		{"sent installation of the owner kept", tofu("https://github.com/acme/infra", &acmeOld.ID), &acmeOld.ID},
		{"stale installation replaced", tofu("https://github.com/acme/infra", &globex.ID), &acmeNew.ID},
		{"unconnected owner cleared", tofu("https://github.com/initech/infra", &globex.ID), nil},
		{"non-GitHub host cleared", tofu("https://gitlab.com/acme/infra", &acmeNew.ID), nil},
		{"no repository", tofu("", nil), nil},
		{
			"manifest",
			ComponentInput{Type: TypeManifest, Config: map[string]string{"repo_url": "https://github.com/globex/k8s"}},
			&globex.ID,
		},
		{
			"helm OCI chart has no repository",
			ComponentInput{Type: TypeHelm, Config: map[string]string{
				"chart_source": "oci",
				"repo_url":     "oci://ghcr.io/acme/charts",
			}, GitHubInstallationID: &acmeNew.ID},
			nil,
		},
		{
			"helm git chart",
			ComponentInput{Type: TypeHelm, Config: map[string]string{
				"chart_source": "git",
				"repo_url":     "https://github.com/acme/charts",
			}},
			&acmeNew.ID,
		},
		{
			"helm git values source",
			ComponentInput{Type: TypeHelm, Config: map[string]string{
				"chart_source":   "http_repo",
				"repo_url":       "https://charts.example.com",
				"values_sources": `[{"repo_url":"https://github.com/initech/cfg","path":"a.yaml"},{"repo_url":"https://github.com/globex/cfg","path":"b.yaml"}]`,
			}},
			&globex.ID,
		},
	}
	for _, tc := range cases {
		got := resolveInstallation(tc.in, installs)
		switch {
		case tc.want == nil && got != nil:
			t.Errorf("%s: got %v, want none", tc.name, *got)
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("%s: got %v, want %v", tc.name, got, *tc.want)
		}
	}
}
