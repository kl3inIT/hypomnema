import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { GatewayModel } from "@/api/generated/types.gen"

import { EditGatewayModel } from "./gateway-models"
import { formOf, settingsOf, shortTokens, tokensHint } from "./gateway-models-state"

afterEach(() => vi.restoreAllMocks())

// A Codex model as the server answers it: its window set on the model, the rest from models.dev.
const sol = (): GatewayModel => ({
  name: "gpt-6.1-sol",
  targets: [{ provider: "codex-subscription", upstream_model: "gpt-6.1-sol" }],
  updated_at: new Date().toISOString(),
  attributes: {
    context_window: { value: 272000, source: "set", models_dev: 1050000 },
    max_output: { value: 128000, source: "models_dev", models_dev: 128000 },
    input: { value: ["text", "image"], source: "models_dev", models_dev: ["text", "image"] },
    reasoning: { value: true, source: "models_dev", models_dev: true },
    efforts: {
      value: ["low", "medium", "high", "xhigh"],
      source: "models_dev",
      models_dev: ["low", "medium", "high", "xhigh"],
    },
    price: { input: 2, output: 10, cache_read: 0.1 },
  },
})

describe("a gateway model's form", () => {
  it("holds only what the model sets, and sends nothing for what it leaves to models.dev", () => {
    const form = formOf(sol())
    expect(form).toEqual({
      context_window: "272000",
      max_output: "",
      input: "auto",
      reasoning: "auto",
      efforts: "",
    })
    expect(settingsOf(form)).toEqual({ context_window: 272000 })
  })

  it("sends each value set, the effort levels in their order", () => {
    expect(
      settingsOf({
        context_window: "200,000",
        max_output: " 64000 ",
        input: "text",
        reasoning: "no",
        efforts: "high, LOW medium",
      }),
    ).toEqual({
      context_window: 200000,
      max_output: 64000,
      input: ["text"],
      reasoning: false,
      efforts: ["low", "medium", "high"],
    })
  })

  // Scenario: an Antigravity model's window is the subscription's, from CLIProxyAPI's list, as a Codex model's
  // is Codex's (issue #399); the field shows it with models.dev's beside it. One nobody knows is the client's.
  it("shows a subscription's own window before models.dev's", () => {
    expect(
      tokensHint({ value: 200000, source: "antigravity", models_dev: 1000000 }, 128000),
    ).toEqual({
      placeholder: "200,000",
      description:
        "From CLIProxyAPI's list of Antigravity's models: 200,000; models.dev gives the API's 1,000,000.",
    })
    expect(tokensHint({ value: 272000, source: "codex" }, 128000).description).toBe(
      "From Codex's own list: 272,000; models.dev gives the API's –.",
    )
    expect(tokensHint({ value: 64000, source: "models_dev", models_dev: 64000 }, 16384)).toEqual({
      placeholder: "64,000",
      description: "From models.dev: 64,000",
    })
    expect(tokensHint({ source: "unknown" }, 16384)).toEqual({
      placeholder: "16,384",
      description: "Not known: the client's own default",
    })
  })

  it("writes a number of tokens short on a row", () => {
    expect(shortTokens(1050000)).toBe("1.05M")
    expect(shortTokens(272000)).toBe("272K")
    expect(shortTokens(512)).toBe("512")
  })
})

function renderForm(model: GatewayModel) {
  const sent: { url: string; body: unknown }[] = []
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    sent.push({ url: request.url, body: await request.clone().json() })

    return new Response(JSON.stringify(model), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })
  })

  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={queryClient}>
      <EditGatewayModel model={model} />
    </QueryClientProvider>,
  )

  return sent
}

// Scenario: the owner opens a model's form. An empty field shows models.dev's value; the window the model sets
// is in its field. The owner clears it, so that it comes from models.dev again, and sets a smaller output.
it("the owner clears a window and sets an output", async () => {
  const user = userEvent.setup()
  const sent = renderForm(sol())

  await user.click(screen.getByRole("button", { name: "Edit gpt-6.1-sol" }))

  const window = screen.getByLabelText("Context window (tokens)")
  expect(window).toHaveValue("272000")
  expect(screen.getByText("From models.dev: 1,050,000")).toBeInTheDocument()

  const output = screen.getByLabelText("Largest output (tokens)")
  expect(output).toHaveValue("")
  expect(output).toHaveAttribute("placeholder", "128,000")

  await user.clear(window)
  await user.type(output, "64000")
  await user.click(screen.getByRole("button", { name: "Save" }))

  await waitFor(() => expect(sent).toHaveLength(1))
  expect(decodeURIComponent(sent[0]!.url)).toContain("/gateway/models/gpt-6.1-sol/attributes")
  expect(sent[0]!.body).toEqual({ max_output: 64000 })
})

// Scenario: a value that is not a number of tokens, or a level that is not one, is refused before anything is
// sent.
it("refuses what is not a number of tokens", async () => {
  const user = userEvent.setup()
  const sent = renderForm(sol())

  await user.click(screen.getByRole("button", { name: "Edit gpt-6.1-sol" }))
  await user.type(screen.getByLabelText("Largest output (tokens)"), "lots")
  await user.type(screen.getByLabelText("Effort levels"), "turbo")
  await user.click(screen.getByRole("button", { name: "Save" }))

  expect(await screen.findByText("A number of tokens above 0.")).toBeInTheDocument()
  expect(screen.getByText(/^Levels among/)).toBeInTheDocument()
  expect(sent).toHaveLength(0)
})
