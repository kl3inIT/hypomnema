# One gate for the machine and CI, as Omnara's Makefile. Rules: docs/conventions.md (Layout and boundaries).
GO ?= go
CI_TOOL = $(GO) tool -modfile=tools/ci/go.mod
GOLANGCI_LINT = $(CI_TOOL) github.com/golangci/golangci-lint/v2/cmd/golangci-lint
GOVULNCHECK = $(CI_TOOL) golang.org/x/vuln/cmd/govulncheck
SQLC = CGO_ENABLED=0 $(CI_TOOL) github.com/sqlc-dev/sqlc/cmd/sqlc
MIGRATIONS := internal/controlplane/orchestration/migrations
MODULE := github.com/kl3inIT/hypomnema
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/shared/platform.Version=$(VERSION)
BINARIES := hypomnema hypomnema-node

.PHONY: help verify verify-static fmt-check tidy-check lint deps-check sqlc-generate sqlc-check migration-check unit race \n	govulncheck build

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

verify: verify-static unit race ## The gate to run before every push

verify-static: fmt-check tidy-check lint deps-check sqlc-check migration-check

fmt-check: ## Fail when a Go file is not gofmt-formatted
	@files="$$(gofmt -l cmd internal)"; \
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
	check hypomnema-node '^$(MODULE)/internal/controlplane(/|$$)'; \
	check hypomnema '^$(MODULE)/internal/node(/|$$)'

sqlc-generate: ## Regenerate the typed queries from the migrations and queries
	$(SQLC) generate

sqlc-check: ## Fail when the generated queries are not what sqlc would generate
	$(SQLC) generate
	@git diff --exit-code -- internal/controlplane/orchestration/internal/store
	@test -z "$$(git status --porcelain -- internal/controlplane/orchestration/internal/store)" || 		{ echo 'sqlc generated files that are not committed'; exit 1; }

# As Omnara's migration-check: a migration that is on main is released and never edited or removed.
migration-check: ## Fail when a migration already on origin/main was changed or removed
	@git rev-parse --verify -q origin/main >/dev/null || { echo 'migration-check needs origin/main'; exit 1; }
	@changed="$$(git diff --name-status origin/main -- $(MIGRATIONS) | grep -v '^A' || true)"; 	test -z "$$changed" || { printf 'released migrations must not change:
%s
' "$$changed"; exit 1; }

unit: ## Unit tests
	$(GO) test -count=1 ./...

race: ## Unit tests with the race detector (needs cgo)
	CGO_ENABLED=1 $(GO) test -race -count=1 ./...

govulncheck: ## Known vulnerabilities in what the code calls
	$(GOVULNCHECK) ./...

build: ## Build the binaries into bin/
	@mkdir -p bin
	@for b in $(BINARIES); do \
		CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/$$b ./cmd/$$b || exit 1; \
	done
