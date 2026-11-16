import { expect, test } from "vitest"

import {
  bodyOf,
  fingerprints,
  formOf,
  repoRef,
  repoSchema,
  sshHostOf,
  sshKeysPage,
} from "./repositories"

const hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB8TL1wZn5vEveB+nHTVCX490uF9kH8g6jcBT6BgFep8"

// The fingerprints are what `ssh-keygen -lf` prints for the same keys.
test("a host key's fingerprint is the one ssh-keygen prints", async () => {
  expect(await fingerprints(hostKey)).toEqual([
    { type: "ED25519", fingerprint: "SHA256:Ls1IQVqyE39d8qi/ZFNrZbP7SsHmSqWVrJiVjUjpJ/M" },
  ])
  // As ssh-keyscan prints them: a comment, then the host before each key.
  expect(
    await fingerprints(
      `# 167.254.68.172:222 SSH-2.0-OpenSSH_9.6\n[167.254.68.172]:222 ${hostKey}\n`,
    ),
  ).toHaveLength(1)
  expect(await fingerprints("")).toEqual([])
  expect(await fingerprints("ssh-ed25519 not*base64")).toEqual([])
  expect(await fingerprints("not a key")).toEqual([])
})

test("an SSH address is shortened to its host and port", () => {
  expect(sshHostOf("ssh://git@167.254.68.172:222/mobility/platform.git")).toBe("167.254.68.172:222")
  expect(sshHostOf("git@gitlab.company.example:team/app.git")).toBe("gitlab.company.example")
})

test("a host's SSH keys page is its product's", () => {
  expect(sshKeysPage("github.com", "github")).toBe("https://github.com/settings/keys")
  expect(sshKeysPage("git.company.example", "gitlab")).toBe(
    "https://git.company.example/-/user_settings/ssh_keys",
  )
})

// Over HTTPS the SSH fields are neither checked nor sent; over SSH both are needed.
test("the SSH address and host key are asked for only over SSH", () => {
  const https = { ...formOf(), repo: "owner/app", name: "app", sshUrl: "typed then left" }
  expect(repoSchema.safeParse(https).success).toBe(true)
  expect(bodyOf(https)).toEqual({ repo: "owner/app" })

  const ssh = { ...https, over: "ssh" as const, sshUrl: "https://host/x.git" }
  const errors = repoSchema.safeParse(ssh).error?.issues.map((issue) => issue.path[0])
  expect(errors).toEqual(["sshUrl", "sshHostKey"])

  const good = {
    ...ssh,
    sshUrl: "git@host:team/app.git",
    sshHostKey: hostKey,
    base: " main ",
  }
  expect(repoSchema.safeParse(good).success).toBe(true)
  expect(bodyOf(good, "work")).toEqual({
    repo: "owner/app",
    base: "main",
    connection: "work",
    ssh_url: "git@host:team/app.git",
    ssh_host_key: hostKey,
  })
})

test("a registered repository is named again as the server took it", () => {
  const created_at = "2026-11-16T00:00:00Z"
  expect(repoRef({ host: "github.com", path: "owner/app" })).toBe("owner/app")
  expect(
    formOf({ name: "app", host: "git.company.example", path: "team/app", created_at }),
  ).toEqual({
    repo: "https://git.company.example/team/app",
    name: "app",
    base: "",
    over: "https",
    sshUrl: "",
    sshHostKey: "",
  })
})
