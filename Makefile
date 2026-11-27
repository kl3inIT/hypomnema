# One gate for the machine and CI, as Omnara's Makefile. Rules: docs/conventions.md (Layout and boundaries).
GO ?= go
CI_TOOL = $(GO) tool -modfile=tools/ci/go.mod
GOLANGCI_LINT = $(CI_TOOL) github.com/golangci/golangci-lint/v2/cmd/golangci-lint
GOVULNCHECK = $(CI_TOOL) golang.org/x/vuln/cmd/govulncheck
SQLC = CGO_ENABLED=0 $(CI_TOOL) github.com/sqlc-dev/sqlc/cmd/sqlc
OAPI_CODEGEN = $(CI_TOOL) github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen
# goose's CLI has a tools module of its own, built with no database driver, as Omnara's: its drivers would move the
# linters' dependencies in tools/ci (issue #954).
GOOSE_TOOL_MOD := tools/goose/go.mod
GOOSE_OFFLINE_BUILD_TAGS := no_clickhouse,no_libsql,no_mssql,no_mysql,no_postgres,no_sqlite3,no_vertica,no_ydb
GOOSE = GOFLAGS='-tags=$(GOOSE_OFFLINE_BUILD_TAGS)' $(GO) tool -modfile=$(GOOSE_TOOL_MOD) github.com/pressly/goose/v3/cmd/goose
OPENAPI_GEN := internal/controlplane/clientapi/openapi
MIGRATIONS := internal/controlplane/orchestration/migrations
SQLC_OUT := internal/controlplane/orchestration/internal/store internal/controlplane/vault/internal/store \
	internal/controlplane/modelgateway/internal/store internal/controlplane/videos/internal/store
MODULE := github.com/kl3inIT/hypomnema
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/shared/platform.Version=$(VERSION)
BINARIES := hypomnema hypomnema-node

.PHONY: help verify verify-static fmt-check tidy-check lint deps-check sqlc-generate sqlc-check migration-check \
	migration-create migration-fix goose-version-check \
	openapi-generate openapi-check unit race govulncheck build web-install web-check web-api-generate \
	web-api-check web-build web-e2e release-node release-controlplane web-doctor \
	zalo-bridge-install zalo-bridge-check zalo-bridge-build pi-extension-check catalogs-generate

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

verify: verify-static unit race ## The Go checks: static, unit, race; before a push, run only those of what changed

verify-static: fmt-check tidy-check lint deps-check sqlc-check openapi-check migration-check goose-version-check

fmt-check: ## Fail when a Go file is not gofmt-formatted
	@files="$$(gofmt -l cmd internal web)"; \
	test -z "$$files" || { printf 'gofmt needed:\n%s\n' "$$files"; exit 1; }

tidy-check: ## Fail when go.mod or go.sum of a module is not tidy
	$(GO) mod tidy -diff
	cd tools/ci && $(GO) mod tidy -diff
	cd tools/goose && $(GO) mod tidy -diff

lint: ## golangci-lint, including integration-tagged files
	$(GOLANGCI_LINT) run ./...

# depguard sees direct imports only; this checks what each binary links, as Nomad's `make check`.
deps-check: ## Fail when a binary links a tree or driver it must not
	@set -e; \
	check() { \
		found="$$($(GO) list -deps ./cmd/$$1 | grep -E "$$2" || true)"; \
		test -z "$$found" || { printf 'cmd/%s must not link:\n%s\n' "$$1" "$$found"; exit 1; }; \
	}; \
	check hypomnema-node '^$(MODULE)/(internal/controlplane|web|bridges)(/|$$)'; \
	check hypomnema '^$(MODULE)/internal/node(/|$$)'

# Fetches each declaration at the revision catalog_sources.json pins, checks its SHA-256 and what the pinned CLI sends,
# and prints what changed (issues #739, #740, #741, #742). Run by hand with a CLI's bump; CI runs only the catalogs' offline
# tests.
SERVICE ?= all

catalogs-generate: ## Regenerate the service proxy's catalogs from their declarations (SERVICE=github|gitlab|google|lark|all; needs network)
	$(GO) run ./internal/controlplane/serviceproxy/catalogsgen -service $(SERVICE)

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

# A new migration is named by the time it was made (goose's default, as Omnara's migration-create), so two open
# branches never take the same version; `make migration-fix` gives it the next number right before it is merged
# (issue #954). Numbers on main never change.
migration-create: ## Make a migration named by its time: make migration-create NAME=what_it_does
	@test -n "$(NAME)" || { echo 'NAME is required: make migration-create NAME=what_it_does'; exit 1; }
	$(GOOSE) -env=none -dir $(MIGRATIONS) create "$(NAME)" sql

migration-fix: ## Number the migrations named by their time after the last numbered one (after merging main)
	$(GOOSE) -env=none -dir $(MIGRATIONS) fix

# As Omnara's goose-version-check: the CLI that makes the files is the goose the binary migrates with.
goose-version-check: ## Fail when tools/goose and go.mod name different goose versions
	@root="$$(GOFLAGS= $(GO) list -m -f '{{.Version}}' github.com/pressly/goose/v3)" || exit $$?; \
	tool="$$(GOFLAGS= $(GO) list -m -modfile=$(GOOSE_TOOL_MOD) -f '{{.Version}}' github.com/pressly/goose/v3)" || exit $$?; \
	test -n "$$root" && test -n "$$tool" || { echo 'cannot resolve goose versions'; exit 1; }; \
	test "$$root" = "$$tool" || { printf 'goose version mismatch: go.mod=%s tools/goose=%s; in tools/goose run go get -tool %s@%s\n' \
		"$$root" "$$tool" github.com/pressly/goose/v3/cmd/goose "$$root"; exit 1; }

# As Omnara's migration-check: a numbered migration that is on main is released and never edited or removed.
# MIGRATION_BASE is what the migrations are checked against: origin/main for a branch; CI passes its own (issue #334).
# A migration named by its time (14 digits) is never released: main refuses it with MIGRATION_TIMESTAMPS=refuse,
# which CI sets on a push to main, so one that reached main may be renamed there (issue #954).
MIGRATION_BASE ?= origin/main
MIGRATION_TIMESTAMPS ?= allow
TIMESTAMPED := [0-9]\{14\}_[^/]*\.sql$$

migration-check: ## Fail when a migration on main changed, two share a number, or a new one is not after main's
	@git rev-parse --verify -q $(MIGRATION_BASE) >/dev/null || { echo 'migration-check needs $(MIGRATION_BASE)'; exit 1; }
	@changed="$$(git diff --no-renames --name-status $(MIGRATION_BASE) -- $(MIGRATIONS) | grep -v '^A' | \
		grep -v '/$(TIMESTAMPED)' || true)"; \
	test -z "$$changed" || { printf 'migrations on main must not change (behind main? merge it):\n%s\n' "$$changed"; exit 1; }
	@dup="$$(ls $(MIGRATIONS) | sed -n 's/^\([0-9]*\)_.*\.sql$$/\1/p' | sort | uniq -d)"; \
	test -z "$$dup" || { printf 'two migrations share the number %s\n' "$$dup"; exit 1; }
	@last="$$(git ls-tree --name-only $(MIGRATION_BASE) $(MIGRATIONS)/ | grep -v '/$(TIMESTAMPED)' | \
		sed -n 's|.*/\([0-9]*\)_.*\.sql$$|\1|p' | sort | tail -1)"; \
	for f in $$(git diff --name-only --diff-filter=A $(MIGRATION_BASE) -- $(MIGRATIONS) | grep -v '/$(TIMESTAMPED)'); do \
	  n="$$(basename $$f | sed -n 's/^\([0-9]*\)_.*/\1/p')"; \
	  test "$$n" \> "$$last" || { printf '%s must be numbered after %s, the last on main\n' "$$f" "$$last"; exit 1; }; \
	done
	@test "$(MIGRATION_TIMESTAMPS)" != refuse || { \
	  stamped="$$(ls $(MIGRATIONS) | grep '^$(TIMESTAMPED)' || true)"; \
	  test -z "$$stamped" || { printf 'migrations named by their time; run make migration-fix:\n%s\n' "$$stamped"; exit 1; }; \
	}

UNIT_PACKAGES ?= ./...

unit: ## Unit tests
	$(GO) test -count=1 $(UNIT_PACKAGES)

RACE_PACKAGES ?= ./...

# -race also turns on checkptr in every package. In modernc.org/sqlite, C translated to Go, it took a third of a
# race run's time and checks code that is not ours, so it is off there only (issue #517).
race: ## Unit tests with the race detector (needs cgo)
	CGO_ENABLED=1 $(GO) test -race -count=1 -gcflags='modernc.org/...=-d=checkptr=0' $(RACE_PACKAGES)

govulncheck: ## Known vulnerabilities in what the code calls
	$(GOVULNCHECK) ./...

# The web client and the Zalo bridge are built first, so that hypomnema embeds them (web/embed.go,
# bridges/zalo/embed.go).
build: web-build zalo-bridge-build ## Build the web client, the Zalo bridge and the binaries into bin/
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

ZALO := pnpm --dir bridges/zalo

zalo-bridge-install: ## Install the Zalo bridge's packages from the lockfile
	$(ZALO) install --frozen-lockfile

zalo-bridge-check: ## Unit tests of the Zalo bridge's protocol
	$(ZALO) test

zalo-bridge-build: ## Bundle the Zalo bridge into bridges/zalo/dist, which hypomnema embeds (issue #657)
	$(ZALO) build

# pi loads the extension as TypeScript (jiti); Node 24 runs its tests without a build, as the Zalo bridge's run.
pi-extension-check: ## Tests of pi's extensions and the Thư ký Bot's check (needs Node 24)
	mkdir -p .tmp/pi-extension-check
	cp deploy/coordinator/package*.json deploy/coordinator/pi-*.mts .tmp/pi-extension-check/
	npm ci --prefix .tmp/pi-extension-check
	node --test .tmp/pi-extension-check/pi-*.test.mts
	node --test deploy/thu-ky/*.test.mjs

# Not a gate: its findings are read, then fixed or ignored in web/doctor.config.json. It runs local checks only
# (no score API, no crash reports, no Socket.dev lookups).
web-doctor: ## React Doctor's report on the web client
	$(WEB) run doctor

web-e2e: ## Browser tests of the built web client (needs Playwright's Chromium)
	$(WEB) test:e2e
