# Secrets for agents: Infisical Agent Vault, Omnara, Omnigent

Date: 2026-11-07. Read from the clones in `.tmp/`; what was inferred is marked.

## Infisical Agent Vault (`.tmp/infisical`, `81e981d`, `docs/documentation/platform/agent-vault/`)

Agent Vault replaces Infisical's deprecated Agent Proxy. `why-agent-vault.mdx` separates two kinds of consumer:
applications run deterministic, trusted code, so a secret can be returned to them (`infisical run -- cmd` puts it in
the environment); agents are nondeterministic and the code they write is untrusted. Five risks are named: prompt
injection exfiltrating a credential, the credential reaching the model provider when the agent prints an environment
variable or opens `.env`, access wider than the task, long-lived keys, and no record of the agent's requests. The
answer: credentials never enter the agent's environment.

- **Pieces** (`how-it-works.mdx`): an orchestrator creates a session and starts the agent with the session token; the
  agent holds only that token; a proxy fetches the real credentials and attaches them; Infisical stores them. The
  orchestrator's own credentials must stay out of the agent's environment.
- **Access model**: services (an HTTP API or MCP server with how to attach its credential), access bundles (services
  granted together), sessions (time-bound, revoked by the orchestrator when the task ends), proxies.
- **Attaching** (`http-apis.mdx`): `Authorization: Bearer`, Basic or a custom header; optional allowed methods and
  paths per service; substitutions that replace a placeholder the agent sends (`GITHUB_TOKEN=__GITHUB_PAT__`) in the
  path, query, headers or body, for tools that refuse to run without a token or APIs that take the key outside a
  header. Bodies over 10 MB or compressed are forwarded unchanged.
- **Proxy** (`proxies.mdx`): agents reach it through `HTTPS_PROXY` with the session token as the proxy password. It
  decrypts HTTPS with a self-signed CA per proxy; the agent trusts it through `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`,
  `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`. Traffic policy: any host (credentials only on covered hosts)
  or covered hosts only; a blocked request gets 403 after the TLS handshake. It polls Infisical every 60 seconds, so a
  revoked session stops within a minute.
- **Placement**: the proxy runs apart from the agent (its own container or machine), "otherwise the agent can read the
  proxy token and the private key of the proxy's certificate authority"; the agent's network should reach only the
  proxy. One proxy per agent on managed sandboxes, since session tokens travel over plain HTTP.
- **Session logs** (`session-logs.mdx`): every request with method, host, path, status and whether a credential was
  attached, encrypted before it leaves; self-hosted session logs need an enterprise license.

## Omnara (`.tmp/omnara`, `0ef1711`)

- **Secret manager** (`docs/organization/secrets.mdx`): kinds `generic`, `oauth_token_set`, `aws_credentials`,
  `github_app_credentials`, each validated; reads return metadata, never the value; rotation adds a version under the
  same ID; delete is refused while a model provider, pool or integration uses the secret; grants share a secret with a
  project. API, CLI and dashboard.
- **Encryption** (`internal/secrets/secrets.go`): `aes-256-gcm-envelope-v1`, a 32-byte data key per secret version
  wrapped by a root key chosen by ID from `OMNARA_SECRET_ENCRYPTION_KEYS` (`internal/config/config.go:329`), associated
  data binding organisation, secret, version and kind; `RewrapSecretVersionsByKeyID` rotates the root key without
  touching values; values capped at 64 KiB; a lease so two processes never refresh one OAuth token at once.
- **Delivery**: secrets reach a machine as environment variables (`secret_env` of machines and pools), so a command the
  agent runs can read them. Omnara treats the machine as the isolation boundary. No separate vault process.

## Omnigent (`.tmp/omnigent`, `7230afa`, `omnigent/inner/credential_proxy.py`, `omnigent/inner/egress/`)

Default "swap-on-access": nothing credential-shaped enters the sandbox; the MITM proxy attaches the credential for one
host. Optional `oa_cred_*` placeholders in the environment for tools such as `gh`; a placeholder sent to another host
is refused with 403. The CA is generated and bundled with the system roots (`egress/ca.py`).

## What Hypomnema takes

| From | What |
|---|---|
| Omnara | The secret manager's operations and its envelope encryption, keyring and rewrap; secrets stored in the control plane's database; no separate vault process |
| Infisical Agent Vault | Sessions per task opened and revoked by the orchestrator (the control plane, per Dispatch); services with allowed hosts, methods and paths; header attachment and placeholder substitution; a MITM proxy with its own CA reached through `HTTPS_PROXY`; the proxy apart from the agent; a log of every request |
| Omnigent | Placeholders refused on the wrong host |

Departures: the proxy is part of `hypomnema-node` and runs as another OS user than the worker (cheaper than a
separate container on a trusted VM, and enough to keep the proxy's token and CA key from the worker; inferred from
Linux file and `/proc` permissions, not yet tested); no organisations, projects or grants (one owner); the secret
manager is reached by a CLI on the control plane host first, never through chat, whose history would keep the value.
Recorded in `docs/conventions.md` (Data and security) and the [decision](../decisions/2026-11-07-two-deployables.md).
