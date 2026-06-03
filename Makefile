.PHONY: run build test test-integration e2e fmt vet tidy dev dev-all worker clean gen \
	ui-install ui-dev ui-build services-up services-down services-logs \
	services-reset migrate-up migrate-status secret-key helm-deps helm-lint helm-template

BINARY := bin/spacefleet
PKG    := ./cmd/spacefleet
CHART  := deploy/charts/spacefleet

run:
	go run $(PKG) serve

# Full production build: UI bundle + Go binary (with UI embedded).
build: ui-build
	go build -o $(BINARY) $(PKG)

test:
	go test ./...

# Integration tests (tagged `integration`) — exercise real handlers against a
# real Postgres. Needs `make services-up`; individual tests skip if the DB is
# unreachable. Override the base DB with TEST_DATABASE_URL.
test-integration:
	go test -tags=integration ./...

# Browser end-to-end tests (Playwright). Needs the full stack up
# (`make services-up && make migrate-up`); the Playwright config starts/reuses
# the Go API and Vite dev server.
e2e:
	cd ui && npx playwright test

fmt:
	go fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy

# Regenerate the ent client, Go server stubs, and TS client types from the
# OpenAPI spec.
gen:
	go generate ./ent/...
	go generate ./lib/api/...
	cd ui && npm run gen:api

# Apply pending migrations from db/migrations/ against $DATABASE_URL.
migrate-up:
	go run $(PKG) migrate up

# Show applied vs pending migrations.
migrate-status:
	go run $(PKG) migrate status

# Generate a base64-encoded 32-byte key for SPACEFLEET_SECRET_KEY (used to
# envelope-encrypt stored credentials, e.g. cluster tokens/kubeconfigs). Copy
# the output into your .env.
secret-key:
	@openssl rand -base64 32

# Dev backend only (port 8080, live reload). Run `make ui-dev` in a second
# terminal for the React dev server.
dev:
	air

# Dev backend AND worker together, both live-reloaded (two Air instances, see
# .air.toml + .air.worker.toml). Use this when you're working on background
# jobs (e.g. the Tekton install flow) so the worker rebuilds on save too. Ctrl-C
# stops both (the trap kills the whole process group). Still run `make ui-dev`
# in a second terminal for the React dev server.
dev-all:
	@trap 'kill 0' EXIT INT TERM; \
		air -c .air.toml & \
		air -c .air.worker.toml & \
		wait

# Long-lived worker process for River-backed background jobs. Run alongside
# `make dev` in a second terminal when you have jobs to process. (For
# live-reload of both server and worker at once, use `make dev-all`.)
worker:
	go run $(PKG) worker

ui-install:
	cd ui && npm install

# Vite dev server on :5173, proxies /api/* to the Go backend on :8080.
ui-dev:
	cd ui && npm run dev

ui-build:
	cd ui && npm run build
	@touch ui/dist/.gitkeep

clean:
	rm -rf bin tmp ui/dist ui/node_modules

# Start Postgres + Dex in the background.
services-up:
	docker compose up -d

services-down:
	docker compose down

services-logs:
	docker compose logs -f

# Wipe Postgres data volumes. Destructive.
services-reset:
	docker compose down -v

# --- Helm chart -------------------------------------------------------------
# The chart depends on the official dexidp/dex subchart (for the optional
# bundled OIDC provider), so the dependency must be fetched before lint/render.

# Fetch chart dependencies (the dex subchart) from Chart.lock into charts/.
# The repo must be registered for `helm dependency build` to resolve it; the
# vendored charts/*.tgz still makes lint/template/package work offline.
helm-deps:
	helm repo add dexidp https://charts.dexidp.io >/dev/null 2>&1 || true
	cd $(CHART) && helm dependency build

# Lint the chart against all CI value sets (bundled + external datastores,
# bundled Dex).
helm-lint: helm-deps
	cd $(CHART) && helm lint . -f ci/default-values.yaml
	cd $(CHART) && helm lint . -f ci/external-values.yaml
	cd $(CHART) && helm lint . -f ci/dex-values.yaml
	cd $(CHART) && helm lint . -f ci/cluster-reader-values.yaml

# Render the chart locally for inspection (bundled-datastore defaults).
helm-template: helm-deps
	helm template spacefleet $(CHART) -f $(CHART)/ci/default-values.yaml
