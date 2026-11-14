import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { useState } from "react"
import { afterEach, describe, expect, it, test, vi } from "vitest"

import type { Bot, GatewayModel, Model } from "@/api/generated/types.gen"

import { ChatModelControls } from "./chat-model-picker"
import {
  botsChoice,
  levelsOf,
  piModels,
  running,
  withModel,
  type ChatChoice,
} from "./chat-model-state"

afterEach(() => vi.restoreAllMocks())

const gatewayModel = (name: string, reasoning: boolean, efforts: string[] = []): GatewayModel =>
  ({
    name,
    targets: [],
    updated_at: new Date().toISOString(),
    attributes: {
      context_window: { source: "unknown" },
      max_output: { source: "unknown" },
      input: { source: "unknown" },
      reasoning: { value: reasoning, source: "models_dev" },
      efforts: { value: efforts, source: "models_dev" },
    },
  }) as GatewayModel

const gateway = [
  gatewayModel("gpt-sol", true, ["high", "low", "medium", "xhigh"]),
  gatewayModel("gpt-luna", true, ["none", "low"]),
  gatewayModel("plain-mini", false),
]

const models: Model[] = [
  { provider: "gateway", name: "gpt-sol", kind: "model_gateway" },
  { provider: "gateway", name: "gpt-luna", kind: "model_gateway" },
  { provider: "gateway", name: "plain-mini", kind: "model_gateway" },
  { provider: "claude-subscription", name: "claude-opus-5-5", kind: "claude_subscription" },
]

describe("a chat's model and effort (issue #369)", () => {
  it("runs with what the chat chose and its Bot's for the rest", () => {
    const bot = { provider: "gateway", model: "gpt-sol", effort: "high" }

    expect(running(botsChoice, bot)).toEqual({
      provider: "gateway",
      model: "gpt-sol",
      effort: "high",
    })
    expect(running({ provider: "", model: "", effort: "low" }, bot)).toEqual({
      provider: "gateway",
      model: "gpt-sol",
      effort: "low",
    })
    expect(running({ provider: "gateway", model: "gpt-luna", effort: "" }, bot).model).toBe(
      "gpt-luna",
    )
  })

  it("offers only the model's levels, in order, and none for a model that does not reason", () => {
    expect(levelsOf("gpt-sol", gateway)).toEqual(["low", "medium", "high", "xhigh"])
    expect(levelsOf("plain-mini", gateway)).toEqual([])
    expect(levelsOf("unknown", gateway)).toEqual([])
  })

  it("keeps the chat's effort when the new model has it, else gives it back to the Bot", () => {
    const chosen: ChatChoice = { provider: "gateway", model: "gpt-sol", effort: "low" }

    expect(withModel(chosen, { provider: "gateway", model: "gpt-luna" }, gateway).effort).toBe(
      "low",
    )
    expect(
      withModel({ ...chosen, effort: "xhigh" }, { provider: "gateway", model: "gpt-luna" }, gateway)
        .effort,
    ).toBe("")
  })

  it("lists the models pi can use", () => {
    expect(piModels(models).map((m) => m.name)).toEqual(["gpt-sol", "gpt-luna", "plain-mini"])
  })
})

const json = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } })

const vmap = {
  name: "vmap",
  provider: "gateway",
  model: "gpt-sol",
  effort: "high",
  persona: "",
  memory: "",
  memory_limit: 2200,
  skills: [],
  skills_off: [],
  connections: { limited: false, names: [] },
  workers: { limited: false, names: [] },
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
} satisfies Bot

function Controls({ onChange }: { onChange: (c: ChatChoice) => void }) {
  const [choice, setChoice] = useState(botsChoice)

  return (
    <ChatModelControls
      bot="vmap"
      choice={choice}
      onChange={(next) => {
        setChoice(next)
        onChange(next)
      }}
    />
  )
}

// Scenario: the composer shows the Bot's model and effort; the owner picks a model from the list, which offers only
// pi's, and its effort button lists that model's levels; a model that does not reason has no effort button.
test("the owner picks a model and an effort in the composer", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const path = new URL((input as Request).url).pathname.replace("/api/v1", "")
    if (path === "/bots") return json({ data: [vmap] })
    if (path === "/models") return json({ data: models })
    if (path === "/gateway/models") return json({ data: gateway })

    return json({})
  })

  const user = userEvent.setup()
  const changes: ChatChoice[] = []
  render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <Controls onChange={(c) => changes.push(c)} />
    </QueryClientProvider>,
  )

  await user.click(await screen.findByRole("combobox", { name: "Model: gpt-sol" }))
  expect(screen.queryByRole("option", { name: /opus/i })).not.toBeInTheDocument()
  await user.click(await screen.findByRole("option", { name: /gpt-luna/i }))
  expect(changes.at(-1)).toEqual({ provider: "gateway", model: "gpt-luna", effort: "" })

  await user.click(await screen.findByRole("button", { name: "Effort: High" }))
  const levels = (await screen.findAllByRole("menuitemradio")).map((o) => o.textContent)
  expect(levels).toEqual(["Bot's: High", "None", "Low"])
  await user.click(screen.getByRole("menuitemradio", { name: "None" }))
  expect(changes.at(-1)).toEqual({ provider: "gateway", model: "gpt-luna", effort: "none" })

  await user.click(screen.getByRole("combobox", { name: "Model: gpt-luna" }))
  await user.click(await screen.findByRole("option", { name: /plain-mini/i }))
  expect(screen.queryByRole("button", { name: /Effort/ })).not.toBeInTheDocument()
})
