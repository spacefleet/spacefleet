---
title: Spacefleet Documentation
description: User-facing guides for installing, configuring, and operating Spacefleet.
---

# Spacefleet Documentation

User-facing documentation for operating and using Spacefleet. Each guide is a
plain Markdown file with YAML frontmatter (`title`, `description`, and other
metadata) so it can be rendered by a docs site or read directly on GitHub.

> Looking for contributor/architecture docs? See [`CLAUDE.md`](../CLAUDE.md) and
> [`TESTING.md`](../TESTING.md) in the repo root, and the Helm chart's own
> [`README.md`](../deploy/charts/spacefleet/README.md) for the maintainer-level
> chart reference.

## Guides

### Deployment

- [Install & configure with Helm](deployment/install-with-helm.md) — deploy
  Spacefleet to Kubernetes, from a one-command trial to a production setup with
  external datastores, OIDC, and Ingress.
