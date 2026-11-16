import { z } from "zod"

import type { GitHostKind, NewRepo, Repo } from "@/api/generated/types.gen"

// repoSchema is the form that adds a repository and edits one (issue #647): where it is, the name to call it by,
// the branch work starts from, and how workers clone it. Over SSH it needs the address and the host's key: a host is
// never trusted on first use (issue #625).
export const repoSchema = z
  .object({
    repo: z.string().trim().min(1, "Give the repository's URL, or owner/name for one on GitHub."),
    name: z.string().trim().min(1, "Give the name to call it by."),
    base: z.string(),
    over: z.enum(["https", "ssh"]),
    sshUrl: z.string().trim(),
    sshHostKey: z.string().trim(),
  })
  .superRefine((value, ctx) => {
    if (value.over !== "ssh") return

    if (!/^(ssh:\/\/)?[\w.-]+@[^\s]+$/.test(value.sshUrl)) {
      ctx.addIssue({
        code: "custom",
        path: ["sshUrl"],
        message:
          "Give the address the host shows under Clone with SSH, such as ssh://git@host:222/group/name.git.",
      })
    }
    if (value.sshHostKey === "") {
      ctx.addIssue({
        code: "custom",
        path: ["sshHostKey"],
        message:
          "Give the SSH host's key, as ssh-keyscan prints it, checked against the host's own.",
      })
    }
  })

export type RepoForm = z.infer<typeof repoSchema>

// repoRef is how a registered repository is named to the server again: owner/name on GitHub, its URL elsewhere,
// as the CLI's `repos set` names it (internal/shared/githost/githost.go, Repo.String).
export function repoRef(repo: Pick<Repo, "host" | "path">): string {
  return repo.host === "github.com" ? repo.path : `https://${repo.host}/${repo.path}`
}

// formOf is what the dialog starts from: empty to add one, what the repository is now to edit it.
export function formOf(repo?: Repo): RepoForm {
  if (!repo) return { repo: "", name: "", base: "", over: "https", sshUrl: "", sshHostKey: "" }

  return {
    repo: repoRef(repo),
    name: repo.name,
    base: repo.base ?? "",
    over: repo.ssh_url ? "ssh" : "https",
    sshUrl: repo.ssh_url ?? "",
    sshHostKey: repo.ssh_host_key ?? "",
  }
}

// bodyOf is what is sent: the server registers a name whole (PUT /repos/{name}), so an edit sends what it keeps too,
// the Connection among it, as `repos set` does (internal/controlplane/clientcli/hosts.go, reposSet). Over HTTPS
// the SSH address and the host's keys go.
export function bodyOf(value: RepoForm, connection?: string): NewRepo {
  return {
    repo: value.repo.trim(),
    ...(value.base.trim() ? { base: value.base.trim() } : {}),
    ...(connection ? { connection } : {}),
    ...(value.over === "ssh"
      ? { ssh_url: value.sshUrl.trim(), ssh_host_key: value.sshHostKey.trim() }
      : {}),
  }
}

// sshHostOf is where an SSH address points, its host and port, short enough for a repository's row:
// ssh://git@host:222/group/name.git and git@host:group/name.git give host:222 and host.
export function sshHostOf(url: string): string {
  const scheme = /^ssh:\/\/[^@/]+@([^/]+)/.exec(url)
  if (scheme) return scheme[1]!

  const scp = /^[^@/]+@([^:/]+):/.exec(url)

  return scp ? scp[1]! : url
}

// sshKeysPage is where a git host's user adds an SSH key: GitHub's settings/keys, on github.com or a GitHub
// Enterprise server, and GitLab's user settings (docs.github.com "Adding a new SSH key to your GitHub account",
// docs.gitlab.com "Add an SSH key to your GitLab account").
export function sshKeysPage(host: string, kind: GitHostKind): string {
  if (kind === "github") return `https://${host}/settings/keys`

  return `https://${host}/-/user_settings/ssh_keys`
}

// keyTypes names a key's algorithm as ssh-keygen -l does in its last column.
const keyTypes: Record<string, string> = {
  "ssh-ed25519": "ED25519",
  "ssh-rsa": "RSA",
  "ssh-dss": "DSA",
  "ecdsa-sha2-nistp256": "ECDSA",
  "ecdsa-sha2-nistp384": "ECDSA",
  "ecdsa-sha2-nistp521": "ECDSA",
  "sk-ssh-ed25519@openssh.com": "ED25519-SK",
  "sk-ecdsa-sha2-nistp256@openssh.com": "ECDSA-SK",
}

export type Fingerprint = { type: string; fingerprint: string }

// fingerprints are the SHA256 fingerprints of the SSH host keys pasted, one a key, as `ssh-keygen -l` prints
// them and the CLI does for `repos set` (ARCHITECTURE.md, repositories over SSH): the SHA-256 of the key's
// decoded blob in base64 without its padding. A line is a key as ssh-keyscan prints it, "[host] <type> <base64>";
// a comment, or a line that is not a key, has none.
export async function fingerprints(text: string): Promise<Fingerprint[]> {
  const found: Fingerprint[] = []

  for (const line of text.split("\n")) {
    if (line.trim().startsWith("#")) continue

    const fields = line.trim().split(/\s+/)
    const at = fields.findIndex((field) => field in keyTypes)
    const blob = at >= 0 ? decode(fields[at + 1] ?? "") : undefined
    if (!blob) continue

    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", blob))
    const encoded = btoa(String.fromCharCode(...digest)).replace(/=+$/, "")
    found.push({ type: keyTypes[fields[at]!]!, fingerprint: `SHA256:${encoded}` })
  }

  return found
}

function decode(base64: string): Uint8Array<ArrayBuffer> | undefined {
  if (!/^[A-Za-z0-9+/]+={0,2}$/.test(base64)) return undefined

  try {
    return Uint8Array.from(atob(base64), (c) => c.charCodeAt(0))
  } catch {
    return undefined
  }
}
