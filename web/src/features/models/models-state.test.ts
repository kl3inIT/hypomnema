import { describe, expect, it } from "vitest"

import type { Account, Provider } from "@/api/generated/types.gen"

import {
  accountState,
  inUse,
  limitsWhy,
  restLabel,
  subscriptionState,
  subscriptionsOf,
  tryDue,
  until,
} from "./models-state"

const now = new Date("2026-11-12T10:00:00Z").getTime()
const later = "2026-11-12T12:00:00Z"
const earlier = "2026-11-12T09:00:00Z"

const account = (id: string, more: Partial<Account> = {}): Account => ({
  id,
  runtime: "codex",
  label: id,
  status: "active",
  position: 1,
  sign_in: "active",
  ...more,
})

const provider = (name: string, kind: Provider["kind"], models: string[] = []): Provider => ({
  name,
  kind,
  models,
  source: "",
  updated_at: later,
})

describe("why an account rests", () => {
  // Issue #1305, row 6: the code in words, with when it ends; the provider's words stay as the title.
  it("says each code in words", () => {
    const at = until(later, now)
    const cases = [
      ["usage_limit", `Usage limit, resets ${at}`, "warning"],
      ["rate_limit", `Rate limited until ${at}`, "warning"],
      ["billing", `Credits spent until ${at}`, "warning"],
      ["auth", `Key refused until ${at}`, "destructive"],
      ["overloaded", `Provider overloaded until ${at}`, "warning"],
      ["unknown", `Resting until ${at}`, "warning"],
    ] as const

    for (const [code, label, tone] of cases) {
      const words = "5-hour usage limit reached. Resets in 2hr 15min."
      expect(
        accountState(
          account("a", { cooling_until: later, cooling_code: code, cooling_reason: words }),
          now,
        ),
      ).toEqual({ label, tone, why: words })
    }
  })

  it("says only that it rests when the server names no code", () => {
    expect(restLabel(undefined, later, now)).toBe(`Resting until ${until(later, now)}`)
  })
})

describe("an account's state", () => {
  it("is nothing while the gateway can use the account", () => {
    expect(accountState(account("a"), now)).toBeNull()
    // A rest that is over is no rest.
    expect(accountState(account("a", { cooling_until: earlier }), now)).toBeNull()
  })

  it("names the one thing wrong, the worst first", () => {
    const resting = { cooling_until: later, cooling_reason: "rate limit" }

    expect(accountState(account("a", resting), now)).toMatchObject({
      tone: "warning",
      why: "rate limit",
    })
    // An account that needs a sign-in cannot serve even when its rest ends.
    expect(accountState(account("a", { ...resting, sign_in: "login_required" }), now)?.label).toBe(
      "Needs sign-in",
    )
    // One Google wants verified (issue #822) says so, with Google's words, before a rest.
    const verification = {
      url: "https://accounts.google.com/signin/continue?plt=1",
      message: "Verify your account to continue.",
      asked_at: earlier,
      next_try_at: later,
    }
    expect(accountState(account("a", { ...resting, verification }), now)).toEqual({
      label: "Needs verification",
      tone: "destructive",
      why: "Verify your account to continue.",
    })
    expect(
      accountState(account("a", { verification, sign_in: "login_required" }), now)?.label,
    ).toBe("Needs sign-in")
    // Its next try is due once its time has come.
    expect(tryDue(verification, now)).toBe(false)
    expect(tryDue({ next_try_at: earlier }, now)).toBe(true)
    // One the owner turned off says so, whatever else is wrong with it.
    expect(
      accountState(account("a", { ...resting, sign_in: "login_required", status: "disabled" }), now)
        ?.label,
    ).toBe("Off")
  })
})

describe("a subscription", () => {
  it("has the accounts of its runtime, in the gateway's order", () => {
    const [codex, claude] = subscriptionsOf(
      [
        provider("codex-subscription", "codex_subscription"),
        provider("claude-subscription", "claude_subscription"),
        provider("openrouter", "openai_compatible"),
      ],
      [
        account("second", { position: 2 }),
        account("first", { position: 1 }),
        account("other", { runtime: "claude" }),
      ],
    )

    expect(codex?.accounts.map((a) => a.id)).toEqual(["first", "second"])
    expect(claude?.accounts.map((a) => a.id)).toEqual(["other"])
    expect(claude?.name).toBe("Claude")
  })

  it("keeps OpenCode Go's keys and OpenCode Zen's apart", () => {
    const [go, zen] = subscriptionsOf(
      [provider("opencode-go", "opencode_go"), provider("opencode-zen", "opencode_zen")],
      [
        account("go-key", { runtime: "opencode_go" }),
        account("zen-key", { runtime: "opencode_zen" }),
      ],
    )

    expect(go?.accounts.map((a) => a.id)).toEqual(["go-key"])
    expect(zen).toMatchObject({
      name: "OpenCode Zen",
      account: "OpenCode Zen key",
      runtime: "opencode_zen",
    })
    expect(zen?.accounts.map((a) => a.id)).toEqual(["zen-key"])
  })

  it("serves while one account can, and says why when none can", () => {
    const resting = account("r", { cooling_until: later })
    const signedOut = account("s", { sign_in: "login_required" })
    const off = account("o", { status: "disabled" })

    expect(subscriptionState([], now).label).toBe("No account")
    expect(subscriptionState([resting, account("ok")], now).label).toBe("Serving")
    expect(subscriptionState([resting, signedOut], now).label).toBe("Resting")
    expect(subscriptionState([signedOut, off], now).label).toBe("Needs sign-in")
    expect(subscriptionState([off], now).label).toBe("Off")

    const toVerify = account("v", {
      verification: { asked_at: earlier, next_try_at: later, url: "https://accounts.google.com/x" },
    })
    expect(subscriptionState([toVerify, account("ok")], now).label).toBe("Serving")
    expect(subscriptionState([toVerify, signedOut], now).label).toBe("Needs verification")
  })

  it("is in use while any of its accounts is", () => {
    expect(inUse([account("a", { status: "disabled" }), account("b")])).toBe(true)
    expect(inUse([account("a", { status: "disabled" })])).toBe(false)
  })
})

describe("why an account's limits are not fresh", () => {
  const windows = [{ name: "5 hours", used_percent: 40 }]

  it("says nothing of a fresh reading", () => {
    expect(limitsWhy({ windows, read_at: earlier }, now)).toBeNull()
  })

  it("says when a resting account is read again", () => {
    expect(limitsWhy({ windows, unavailable: "resting", retry_at: later }, now)).toBe(
      `The vendor asked to wait: read again at ${until(later, now)}.`,
    )
  })

  it("says to sign in again when the vendor refused the token", () => {
    expect(limitsWhy({ windows, unavailable: "refused" }, now)).toContain("sign it in again")
  })

  it("says the reading failed, beside the last windows or without them", () => {
    expect(limitsWhy({ windows, unavailable: "failed" }, now)).toBe("Reading them again failed.")
    expect(limitsWhy({ windows: [], unavailable: "failed" }, now)).toBe(
      "Its limits could not be read.",
    )
  })
})
