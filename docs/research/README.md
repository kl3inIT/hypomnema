# Research

What was studied before designing, with sources. One file per topic, named `YYYY-MM-DD-topic.md`, written once and
appended to when the same topic is studied again. Reference repositories are cloned into the git-ignored `.tmp/`
folder; each note names the revision it read.

| File | Topic |
|---|---|
| [2026-11-06-platform.md](2026-11-06-platform.md) | The software-factory pattern; Buzz, Omnara, OpenClaw, Hermes; multi-machine orchestration (Orca, OpenClaw, Omnara); harness drivers and durability (Omnigent, genie, durable-harness, companion); pi as brain; the agreed architecture, vault design, Northstar layer, build order and naming |
| [2026-11-06-headless-cli-drivers.md](2026-11-06-headless-cli-drivers.md) | Spike B: driving Claude Code (stream-JSON with host permission prompts) and Codex (`app-server` JSON-RPC) headless; results and design consequences |
| [2026-11-06-pi-rpc-brain.md](2026-11-06-pi-rpc-brain.md) | Spike A: pi 1.5.0 over RPC from Go with MCP tools served by Go and state in Postgres; brain crash and restart; 9router on zm |
| [2026-11-07-go-repository-layout.md](2026-11-07-go-repository-layout.md) | How large Go repositories enforce boundaries (Kubernetes, Tailscale, Teleport, Grafana, Nomad, Omnara…); agent skill collections and the one-source-per-topic selection |
| [2026-11-07-secrets-for-agents.md](2026-11-07-secrets-for-agents.md) | Infisical Agent Vault (sessions, MITM proxy, placeholders), Omnara's secret manager and envelope encryption, Omnigent's credential proxy |
