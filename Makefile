# One gate for the machine and CI, as Omnara's Makefile. Rules: docs/conventions.md (Layout and boundaries).
GO ?= go
CI_TOOL = $(GO) tool -modfile=tools/ci/go.mod
GOLANGCI_LINT = $(CI_TOOL) github.com/golangci/golangci-lint/v2/cmd/golangci-lint
GOVULNCHECK = $(CI_TOOL) golang.org/x/vuln/cmd/govulncheck
SQLC = CGO_ENABLED=0 $(CI_TOOL) github.com/sqlc-dev/sqlc/cmd/sqlc
OAPI_CODEGEN = $(CI_TOOL) github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen
OPENAPI_GEN := internal/controlplane/clientapi/openapi
MIGRATIONS := internal/controlplane/orchestration/migrations
SQLC_OUT := internal/controlplane/orchestration/internal/store internal/controlplane/vault/internal/store \
	internal/controlplane/modelgateway/internal/store
MODULE := github.com/kl3inIT/hypomnema
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/shared/platform.Version=$(VERSION)
BINARIES := hypomnema hypomnema-node

.PHONY: help verify verify-static fmt-check tidy-check lint deps-check sqlc-generate sqlc-check migration-check \
	openapi-generate openapi-check unit race govulncheck build web-install web-check web-api-generate \
	web-api-check web-build web-e2e release-node release-controlplane web-doctor

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

verify: verify-static unit race ## The gate to run before every push

verify-static: fmt-check tidy-check lint deps-check sqlc-check openapi-check migration-check

fmt-check: ## Fail when a Go file is not gofmt-formatted
	@files="$$(gofmt -l cmd internal web)"; \
	test -z "$$files" || { printf 'gofmt needed:\n%s\n' "$$files"; exit 1; }

tidy-check: ## Fail when go.mod or go.sum of a module is not tidy
	$(GO) mod tidy -diff
	cd tools/ci && $(GO) mod tidy -diff

lint: ## golangci-lint, including integration-tagged files
	$(GOLANGCI_LINT) run ./...

# depguard sees direct imports only; this checks what each binary links, as Nomad's `make check`.
deps-check: ## Fail when a binary links a tree or driver it must not
	@set -e; \
	check() { \
		found="$$($(GO) list -deps ./cmd/$$1 | grep -E "$$2" || true)"; \
		test -z "$$found" || { printf 'cmd/%s must not link:\n%s\n' "$$1" "$$found"; exit 1; }; \
	}; \
	check hypomnema-node '^$(MODULE)/(internal/controlplane|web)(/|$$)'; \
	check hypomnema '^$(MODULE)/internal/node(/|$$)'

sqlc-generate: ## Regenerate the typed queries from the migrations and queries
	$(SQLC) generate

sqlc-check: ## Fail when the generated queries are not what sqlc would generate
	$(SQLC) generate
	@git diff --exit-code -- $(SQLC_OUT)
	@test -z "$$(git status --porcelain -- $(SQLC_OUT))" || \
		{ echo 'sqlc generated files that are not committed'; exit 1; }

openapi-generate: ## Regenerate the client API's types, server and client from api/openapi.yaml
	$(OAPI_CODEGEN) -config api/oapi-codegen.yaml api/openapi.yaml

# As Omnara's openapi-check: the generated code is committed and must match the contract.
openapi-check: ## Fail when the generated client API code is not what the contract generates
	$(OAPI_CODEGEN) -config api/oapi-codegen.yaml api/openapi.yaml
	@git diff --exit-code -- $(OPENAPI_GEN)
	@test -z "$$(git status --porcelain -- $(OPENAPI_GEN))" || \
		{ echo 'oapi-codegen generated files that are not committed'; exit 1; }

# As Omnara's migration-check: a migration that is on main is released and never edited or removed.
migration-check: ## Fail when a migration already on origin/main was changed or removed
	@git rev-parse --verify -q origin/main >/dev/null || { echo 'migration-check needs origin/main'; exit 1; }
	@changed="$$(git diff --name-status origin/main -- $(MIGRATIONS) | grep -v '^A' || true)"; \
	test -z "$$changed" || { printf 'released migrations must not change:\n%s\n' "$$changed"; exit 1; }

unit: ## Unit tests
	$(GO) test -count=1 ./...

race: ## Unit tests with the race detector (needs cgo)
	CGO_ENABLED=1 $(GO) test -race -count=1 ./...

govulncheck: ## Known vulnerabilities in what the code calls
	$(GOVULNCHECK) ./...

# The web client is built first, so that hypomnema embeds it (web/embed.go).
build: web-build ## Build the web client and the binaries into bin/
	@mkdir -p bin
	@for b in $(BINARIES); do \
		CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/$$b ./cmd/$$b || exit 1; \
	done

RELEASE_REPO ?= kl3inIT/hypomnema

release-node: ## Build the node for every platform into dist/, with the manifests the install script reads
	sh deploy/release/release-node.sh "$(VERSION)" "$(RELEASE_REPO)" "$(LDFLAGS)"

release-controlplane: ## Build the control plane for Linux into dist/, with the manifests `hypomnema update` reads
	sh deploy/release/release-controlplane.sh "$(VERSION)" "$(RELEASE_REPO)" "$(LDFLAGS)"

WEB := pnpm --dir web

web-install: ## Install the web client's packages from the lockfile
	$(WEB) install --frozen-lockfile

web-check: web-api-check ## Lint, format check, typecheck and unit tests of the web client
	$(WEB) check

web-api-generate: ## Regenerate the web client's typed API client from api/openapi.yaml
	$(WEB) generate:api

# As openapi-check, for the web client's generated client; it runs in the web job, which has pnpm.
web-api-check: ## Fail when the web client's generated API client is not what the contract generates
	$(WEB) generate:api
	@git diff --exit-code -- web/src/api/generated
	@test -z "$$(git status --porcelain -- web/src/api/generated)" || 		{ echo 'the web API client has generated files that are not committed'; exit 1; }

web-build: ## Build the web client into web/dist
	$(WEB) build

# Not a gate: its findings are read, then fixed or ignored in web/doctor.config.json. It runs local checks only
# (no score API, no crash reports, no Socket.dev lookups).
web-doctor: ## React Doctor's report on the web client
	$(WEB) run doctor

web-e2e: ## Browser tests of the built web client (needs Playwright's Chromium)
	$(WEB) test:e2e
