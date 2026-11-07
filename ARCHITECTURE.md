# Hypomnema architecture

This page states what is implemented today, and nothing else. The target is in [docs/vision.md](docs/vision.md);
planned work is in GitHub issues and never appears here as a fact. Update this page in the change that makes a
statement true or false.

## Binaries

| Binary | Commands | What it does today |
|---|---|---|
| `cmd/hypomnema` (control plane) | `serve`, `version` | Opens its PostgreSQL database and serves `/livez` and `/readyz` |
| `cmd/hypomnema-vault` (vault) | `serve`, `version` | Same, with its own database |
| `cmd/hypomnema-node` (node) | `version` | Prints its version |

Configuration comes from the environment: `HYPOMNEMA_HTTP_ADDR` (default `127.0.0.1:8080`),
`HYPOMNEMA_DATABASE_URL` (required), `HYPOMNEMA_DATABASE_MAX_CONNS` (default 10), `HYPOMNEMA_LOG_LEVEL`,
`HYPOMNEMA_LOG_FORMAT` (`json` or `text`); the vault reads the same names with the prefix `HYPOMNEMA_VAULT_` and
listens on `127.0.0.1:8090` by default. Logs are JSON on stdout; `serve` stops on SIGINT or SIGTERM and lets
requests in flight finish. `/readyz` answers 503 with `Retry-After` while the database does not answer.

## Code

```
internal/controlplane   package controlplane: configuration and serving
internal/vault          package vault: configuration and serving
internal/shared/platform  environment settings, slog logger, HTTP server and probes, version
internal/shared/postgres  PostgreSQL pool (control plane and vault only)
```

There is no `internal/node` yet. Boundaries ([decision](docs/decisions/2026-11-07-three-deployables.md)) are checked
by `make verify`: depguard rules per tree in `.golangci.yml`, and `deps-check`, which refuses PostgreSQL, the control
plane and the vault in the node binary.

## Verification

- `make verify`: gofmt, `go mod tidy -diff` for both modules, golangci-lint (`.golangci.yml`, tools pinned in
  `tools/ci/go.mod`), the dependency check, unit tests and race tests.
- `make test-integration`: tests tagged `integration` against PostgreSQL 18 in containers (testcontainers).
- CI (`.github/workflows/ci.yml`) runs both and `govulncheck` on every pull request and on `main`.
- Local PostgreSQL: `compose.yaml`, one server with the databases `hypomnema` and `hypomnema_vault`.
