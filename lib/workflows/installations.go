package workflows

import (
	"context"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/spacefleet/spacefleet/ent"
	"github.com/spacefleet/spacefleet/ent/githubinstallation"
	"github.com/spacefleet/spacefleet/lib/helm"
)

// A component's GitHub App installation isn't chosen by hand: it follows from
// the repository the component clones. An installation lives on one GitHub
// account and reaches only that account's repositories, so the owner in a
// github.com repository URL names the installation that can clone it. The
// installation is resolved when the workflow is saved, so the stored id —
// which mints clone tokens and matches webhook deliveries to components — can
// never disagree with the repository URL beside it.

// resolveInstallations sets each component's GitHub installation from its
// repository URLs, against the organization's installations.
func (s *Service) resolveInstallations(ctx context.Context, orgID uuid.UUID, stages []StageInput) error {
	installs, err := s.ent.GitHubInstallation.Query().
		Where(githubinstallation.OrganizationID(orgID)).
		Order(ent.Desc(githubinstallation.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return err
	}
	for i := range stages {
		for j := range stages[i].Components {
			c := &stages[i].Components[j]
			c.GitHubInstallationID = resolveInstallation(*c, installs)
		}
	}
	return nil
}

// resolveInstallation picks the installation a component clones through: the
// one on the GitHub account owning its repository, trying its repositories in
// order (a Helm component's git chart, then its git values sources) since a
// component holds a single installation. installs is newest first, so an
// account connected more than once resolves to its latest installation —
// unless the component already names one of that account's installations
// (the repository picker sets it), which is kept. Nil when no repository is on
// github.com under a connected account: a public repository, or a host the
// GitHub App doesn't cover.
func resolveInstallation(c ComponentInput, installs []*ent.GitHubInstallation) *uuid.UUID {
	for _, repoURL := range componentRepoURLs(c) {
		owner := githubOwner(repoURL)
		if owner == "" {
			continue
		}
		var match *uuid.UUID
		for _, in := range installs {
			if !strings.EqualFold(in.AccountLogin, owner) {
				continue
			}
			if c.GitHubInstallationID != nil && *c.GitHubInstallationID == in.ID {
				id := in.ID
				return &id
			}
			if match == nil {
				id := in.ID
				match = &id
			}
		}
		if match != nil {
			return match
		}
	}
	return nil
}

// componentRepoURLs lists the git repositories a component clones: a manifest
// or OpenTofu component's repo_url; for Helm, the chart's repo_url when the
// chart source is git, then each git values source's.
func componentRepoURLs(c ComponentInput) []string {
	var urls []string
	switch c.Type {
	case TypeManifest, TypeTerraform:
		urls = append(urls, c.Config[helm.ConfigRepoURL])
	case TypeHelm:
		if c.Config[helmConfigChartSource] == helm.SourceGit {
			urls = append(urls, c.Config[helm.ConfigRepoURL])
		}
		// validateWorkflow has already rejected a malformed values_sources.
		sources, _ := decodeValuesSources(c.Config[helmConfigValuesSources])
		for _, src := range sources {
			urls = append(urls, src[helm.ValuesSourceRepoURL])
		}
	}
	return urls
}

// githubOwner returns the account owning the github.com repository a git URL
// names, or "" when it names none.
func githubOwner(repoURL string) string {
	owner, _, _ := strings.Cut(githubRepoPath(repoURL), "/")
	return owner
}

// githubRepoPath returns the owner/name a git URL names on github.com — for
// the https, ssh, and scp-like forms, with or without a trailing .git — or ""
// when the URL isn't a github.com repository.
func githubRepoPath(repoURL string) string {
	var host, path string
	if i := strings.Index(repoURL, "://"); i >= 0 {
		u, err := url.Parse(repoURL)
		if err != nil {
			return ""
		}
		host, path = u.Hostname(), u.Path
	} else if i := strings.Index(repoURL, ":"); i >= 0 && !strings.Contains(repoURL[:i], "/") {
		// scp-like: git@github.com:owner/name.git
		host, path = repoURL[:i], repoURL[i+1:]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	} else {
		return ""
	}
	if !strings.EqualFold(host, "github.com") {
		return ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	owner, name, ok := strings.Cut(path, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return path
}
