import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, expect, test, vi } from "vitest"

import type { Integration, SignIn, SignInAsk } from "@/api/generated/types.gen"
import { SidebarProvider } from "@/components/ui/sidebar"

import { IntegrationsScreen } from "./integrations-screen"

// The owner links his Telegram on the Integrations page (issue #1184): by the QR code his phone accepts, then his
// account's two-step password; or, when the code cannot be scanned, by his phone number and the code Telegram
// sends. Every number, code and password here is made up.

beforeEach(() => {
  vi.spyOn(window, "scrollTo").mockImplementation(() => {})
})
afterEach(() => vi.restoreAllMocks())

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })

const telegram = (over: Partial<Integration> = {}): Integration => ({
  id: "telegram",
  name: "Telegram",
  kind: "device",
  services: [],
  connections: [],
  ...over,
})

// linking plays the control plane's Telegram linking: each answer the owner gives moves it to the step script says
// comes next, and completing waits until a step links the account.
function linking(script: (flow: string, answer: string) => SignInAsk | "linked") {
  let services = [telegram()]
  let step: SignIn = { id: "signin-1", url: "", expires_in_seconds: 900 }
  let flow = "qr"
  let linked: () => void = () => {}
  const done = new Promise<void>((resolve) => (linked = resolve))
  const requests: { call: string; body: unknown }[] = []

  const link = () => {
    const now = new Date().toISOString()
    services = [
      telegram({
        connections: [
          {
            integration: "telegram",
            label: "main",
            account: "+84 912 345 678",
            services: [],
            sign_in_required: false,
            chats: false,
            cannot: [],
            session: { state: "connected" },
            created_at: now,
            updated_at: now,
          },
        ],
      }),
    ]
    step = { id: "signin-1", url: "", expires_in_seconds: 900 }
    linked()
  }

  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const path = decodeURIComponent(new URL(request.url).pathname.replace("/api/v1", ""))
    const call = `${request.method} ${path}`
    if (call === "GET /integrations") return json({ data: services })
    if (call === "GET /integrations/telegram/sign-ins/signin-1") return json(step)
    if (request.method === "GET") return json({ data: [] })

    const body = (await request
      .clone()
      .json()
      .catch(() => undefined)) as Record<string, string> | undefined
    requests.push({ call, body })

    if (call === "POST /integrations/telegram/sign-ins") {
      flow = body?.flow ?? "qr"
      step =
        flow === "phone"
          ? { ...step, ask: { field: "phone", again: false } }
          : { ...step, qr_code: "tg://login?token=AAAA", qr_expires_at: new Date().toISOString() }

      return json(step)
    }
    if (call === "POST /integrations/telegram/sign-ins/signin-1/answer") {
      const next = script(flow, body?.value ?? "")
      if (next === "linked") link()
      else step = { id: "signin-1", url: "", expires_in_seconds: 900, ask: next }

      return json(step)
    }
    if (call === "POST /integrations/telegram/sign-ins/signin-1") {
      await done

      return json(services[0]?.connections[0])
    }

    return json({ code: "not_found", message: "not found" }, 404)
  })

  return {
    requests,
    // accept is the owner's phone accepting the QR code of an account with a two-step password: the linking asks
    // for it.
    accept: () => {
      step = {
        id: "signin-1",
        url: "",
        expires_in_seconds: 900,
        ask: { field: "password", hint: "the cat", again: false },
      }
    },
  }
}

async function open() {
  const router = createRouter({
    routeTree: createRootRoute({ component: IntegrationsScreen }),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  render(
    <QueryClientProvider client={queryClient}>
      <SidebarProvider>
        <RouterProvider router={router} />
      </SidebarProvider>
    </QueryClientProvider>,
  )
  await screen.findByRole("heading", { name: "Integrations", level: 1 })
}

const telegramCard = async () =>
  within(
    (await screen.findByText("Telegram")).closest<HTMLElement>("[data-testid=integration-card]")!,
  )

// The server's words for a Telegram whose api_id and api_hash are not in the vault yet.
const unready =
  "the api_id is missing: make the application at my.telegram.org and keep its api_id and api_hash in the vault as the secrets telegram-api-id and telegram-api-hash"

// Until the owner gave Hypomnema his api_id and api_hash, the card says the api_id is missing and how to give it,
// and offers no link.
test("Telegram says its api_id is missing until it has one", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const request = input as Request
    const path = new URL(request.url).pathname.replace("/api/v1", "")
    if (path === "/integrations") return json({ data: [telegram({ unready })] })

    return json({ data: [] })
  })
  await open()

  const card = await telegramCard()
  expect(card.getByText("Not set up")).toBeInTheDocument()
  expect(card.getByText(/api_id is missing/)).toBeInTheDocument()
  expect(card.getByRole("link", { name: "Open the Vault" })).toHaveAttribute(
    "href",
    "/settings/vault",
  )
  expect(card.queryByRole("button", { name: "Link phone" })).not.toBeInTheDocument()
})

// Scenario: the QR code first. The phone accepts it; the account has a two-step password, so the dialog asks for it
// with its hint, in a password field; once answered, the account is linked.
test("the owner links Telegram by QR code and types his two-step password", async () => {
  const user = userEvent.setup()
  const server = linking(() => "linked")
  await open()

  await user.click((await telegramCard()).getByRole("button", { name: "Link phone" }))
  const dialog = within(await screen.findByRole("dialog"))
  await user.click(dialog.getByRole("button", { name: "Show the code" }))
  expect(await dialog.findByAltText("QR code to link Telegram")).toBeInTheDocument()
  expect(dialog.getByText(/Link Desktop Device/)).toBeInTheDocument()

  server.accept()
  const password = await dialog.findByLabelText("Two-step password", {}, { timeout: 4000 })
  expect(password).toHaveAttribute("type", "password")
  expect(dialog.getByText("Your hint: the cat")).toBeInTheDocument()
  expect(dialog.queryByAltText("QR code to link Telegram")).not.toBeInTheDocument()

  await user.type(password, "hunter2")
  await user.click(dialog.getByRole("button", { name: "Continue" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  expect(await screen.findByLabelText("Telegram account main")).toBeInTheDocument()

  expect(server.requests).toEqual([
    { call: "POST /integrations/telegram/sign-ins", body: { label: "main" } },
    { call: "POST /integrations/telegram/sign-ins/signin-1", body: {} },
    { call: "POST /integrations/telegram/sign-ins/signin-1/answer", body: { value: "hunter2" } },
  ])
})

// Scenario: the code cannot be scanned, so the owner links by his phone number. The dialog asks the number, then the
// code Telegram sent to his app; a wrong code is refused and asked again, and the right one links the account.
test("the owner links Telegram by phone number, and a wrong code is asked again", async () => {
  const user = userEvent.setup()
  const server = linking((_, answer) => {
    if (answer.startsWith("+")) return { field: "code", hint: "the Telegram app", again: false }
    if (answer === "11111") return { field: "code", hint: "the Telegram app", again: true }

    return "linked"
  })
  await open()

  await user.click((await telegramCard()).getByRole("button", { name: "Link phone" }))
  const dialog = within(await screen.findByRole("dialog"))
  await user.click(dialog.getByRole("button", { name: "Use phone number" }))

  await user.type(await dialog.findByLabelText("Phone number"), "+84912345678")
  await user.click(dialog.getByRole("button", { name: "Continue" }))

  const code = await dialog.findByLabelText("Code")
  expect(dialog.getByText("Sent to the Telegram app.")).toBeInTheDocument()
  await user.type(code, "11111")
  await user.click(dialog.getByRole("button", { name: "Continue" }))
  expect(await dialog.findByText(/That code was wrong; type it again/)).toBeInTheDocument()

  await user.type(dialog.getByLabelText("Code"), "22222")
  await user.click(dialog.getByRole("button", { name: "Continue" }))
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

  expect(server.requests).toEqual([
    { call: "POST /integrations/telegram/sign-ins", body: { label: "main", flow: "phone" } },
    { call: "POST /integrations/telegram/sign-ins/signin-1", body: {} },
    {
      call: "POST /integrations/telegram/sign-ins/signin-1/answer",
      body: { value: "+84912345678" },
    },
    { call: "POST /integrations/telegram/sign-ins/signin-1/answer", body: { value: "11111" } },
    { call: "POST /integrations/telegram/sign-ins/signin-1/answer", body: { value: "22222" } },
  ])
})
