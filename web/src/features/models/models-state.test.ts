import { describe, expect, it } from "vitest"

import type { Account, GatewayModel, Provider } from "@/api/generated/types.gen"

import { accountState, inUse, routesOf, subscriptionState, subscriptionsOf } from "./models-state"

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

  it("serves while one account can, and says why when none can", () => {
    const resting = account("r", { cooling_until: later })
    const signedOut = account("s", { sign_in: "login_required" })
    const off = account("o", { status: "disabled" })

    expect(subscriptionState([], now).label).toBe("No account")
    expect(subscriptionState([resting, account("ok")], now).label).toBe("Serving")
    expect(subscriptionState([resting, signedOut], now).label).toBe("Resting")
    expect(subscriptionState([signedOut, off], now).label).toBe("Needs sign-in")
    expect(subscriptionState([off], now).label).toBe("Off")
  })

  it("is in use while any of its accounts is", () => {
    expect(inUse([account("a", { status: "disabled" }), account("b")])).toBe(true)
    expect(inUse([account("a", { status: "disabled" })])).toBe(false)
  })
})

describe("the gateway's models", () => {
  it("carry each target's kind of provider and whether an agent may name the model", () => {
    const models: GatewayModel[] = [
      {
        name: "claude-sonnet-5-5",
        updated_at: later,
        targets: [
          { provider: "claude-subscription", upstream_model: "claude-sonnet-5-5" },
          { provider: "gone", upstream_model: "x" },
        ],
      },
      { name: "fable", updated_at: later, targets: [] },
    ]

    const [sonnet, fable] = routesOf(models, [
      provider("claude-subscription", "claude_subscription"),
      provider("model-gateway", "model_gateway", ["claude-sonnet-5-5"]),
    ])

    expect(sonnet?.offered).toBe(true)
    expect(sonnet?.targets).toEqual([
      {
        provider: "claude-subscription",
        kind: "claude_subscription",
        upstream: "claude-sonnet-5-5",
      },
      // A target whose provider is not listed keeps its name and has no kind.
      { provider: "gone", kind: "", upstream: "x" },
    ])
    expect(fable?.offered).toBe(false)
  })
})
