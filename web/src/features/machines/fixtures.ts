import type { Node } from "@/api/generated/types.gen"

// Test data shaped as the client API returns it: a connected node of 4 CPUs, 16 GiB of memory and a 100 GiB disk.

const GiB = 2 ** 30

export const node = (id: string, m: Partial<Node> = {}): Node => ({
  id,
  name: id,
  status: "active",
  max_dispatches: 4,
  runtimes: [
    { runtime: "claude", path: "/usr/bin/claude", version: "2.1.0" },
    { runtime: "codex", path: "/usr/bin/codex", version: "0.50.0" },
  ],
  system: {
    daemon_version: "0.4.0",
    os: "linux",
    arch: "amd64",
    cpus: 4,
    memory_bytes: 16 * GiB,
    disk_bytes: 100 * GiB,
  },
  stats: {
    cpu_percent: 12.5,
    memory_used_bytes: 4 * GiB,
    disk_used_bytes: 50 * GiB,
    load1: 0.4,
    at: "2026-11-10T10:00:00Z",
  },
  last_seen_at: "2026-11-10T10:00:00Z",
  connected: true,
  active: 1,
  created_at: "2026-11-09T10:00:00Z",
  ...m,
})

export const used = (memory: number, disk: number, cpu = 10): Node["stats"] => ({
  cpu_percent: cpu,
  memory_used_bytes: memory * GiB,
  disk_used_bytes: disk * GiB,
  load1: 0.4,
  at: "2026-11-10T10:00:00Z",
})
