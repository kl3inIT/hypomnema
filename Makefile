# One gate for the machine and CI, as Omnara's Makefile. Rules: docs/conventions.md (Layout and boundaries).
GO ?= go
CI_TOOL = $(GO) tool -modfile=tools/ci/go.mod
GOLANGCI_LINT = $(CI_TOOL) github.com/golangci/golangci-lint/v2/cmd/golangci-lint
GOVULNCHECK = $(CI_TOOL) golang.org/x/vuln/cmd/govulncheck
MODULE := github.com/kl3inIT/hypomnema
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/shared/platform.Version=$(VERSION)
BINARIES := hypomnema hypomnema-node hypomnema-vault

.PHONY: help verify verify-static fmt-check tidy-check lint deps-check unit race test-integration govulncheck build

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

verify: verify-static unit race ## The gate to run before every push

verify-static: fmt-check tidy-check lint deps-check

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
	check hypomnema-node '^github.com/jackc/pgx|^$(MODULE)/internal/(controlplane|vault|shared/postgres)'; \
	check hypomnema-vault '^$(MODULE)/internal/(controlplane|node)'; \
	check hypomnema '^$(MODULE)/internal/(node|vault)(/|$$)'

unit: ## Unit tests
	$(GO) test -count=1 ./...

race: ## Unit tests with the race detector (needs cgo)
	CGO_ENABLED=1 $(GO) test -race -count=1 ./...

test-integration: ## Tests against real PostgreSQL in containers (needs Docker)
	CGO_ENABLED=1 $(GO) test -race -count=1 -tags=integration ./...

govulncheck: ## Known vulnerabilities in what the code calls
	$(GOVULNCHECK) ./...

build: ## Build the three binaries into bin/
	@mkdir -p bin
	@for b in $(BINARIES); do \
		CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/$$b ./cmd/$$b || exit 1; \
	done
