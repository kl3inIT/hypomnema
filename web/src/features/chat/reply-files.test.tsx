import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, test, vi } from "vitest"

import type { ChatMessage } from "@/api/generated/types.gen"

import { ChatItemView } from "./chat-items"
import { chatItems, chatReducer, emptyChat } from "./chat-state"
import { mediaOf } from "./media"

const readWorkspaceRaw = vi.hoisted(() => vi.fn<(options: unknown) => Promise<unknown>>())
vi.mock("@/api/generated/sdk.gen", async (actual) => ({
  ...(await actual<object>()),
  readWorkspaceRaw,
}))

// reply draws a Bot's message, given as its text or as the text parts the server keeps.
const reply = (text: string | string[], readOnly = false) => {
  const parts = typeof text === "string" ? undefined : text
  const message: ChatMessage = {
    seq: 3,
    chat_id: "cht_1",
    role: "coordinator",
    text: parts ? parts.join("\n\n") : (text as string),
    ...(parts && { parts: parts.map((t) => ({ type: "text" as const, text: t })) }),
    created_at: "2026-11-17T09:30:00Z",
  }
  const items = chatItems(chatReducer(emptyChat, { type: "message", message }))
  if (items.length === 0) throw new Error("nothing drawn")
  render(
    <>
      {items.map((item) => (
        <ChatItemView
          key={item.key}
          item={item}
          working={false}
          botName="reporter"
          readOnly={readOnly}
        />
      ))}
    </>,
  )
}

test("a read-only run does not load images or turn MEDIA lines into downloads", () => {
  reply("![Logo](https://example.org/pixel.png)\n\nMEDIA:out/private.txt", true)
  expect(screen.queryByRole("img")).not.toBeInTheDocument()
  expect(screen.getByText("MEDIA:out/private.txt")).toBeInTheDocument()
  expect(screen.queryByRole("link")).not.toBeInTheDocument()
})

// link is the Files panel's download of a file of the Bot's folder, after the client's base address.
const link = (path: string) =>
  expect.stringMatching(
    new RegExp(
      `/api/v1/chats/cht_1/workspaces/bot/raw\\?path=${encodeURIComponent(path).replaceAll(".", "\\.")}$`,
    ),
  )

// The lines are read by the rules a Channel reads them by (internal/controlplane/channels/media.go).
describe("mediaOf", () => {
  test("takes MEDIA: lines out, in any case, and keeps the text around them", () => {
    expect(mediaOf("Here it is.\nMEDIA:out/a.xlsx\n  media: `out/b.zip`\nTell me.")).toMatchObject({
      shown: "Here it is.\nTell me.",
      paths: ["out/a.xlsx", "out/b.zip"],
    })
  })

  test("leaves a line in a fenced code block as text", () => {
    const text = "Write:\n```\nMEDIA:out/a.xlsx\n```\n~~~\nMEDIA:out/b.xlsx\n~~~"
    expect(mediaOf(text)).toMatchObject({ shown: text, paths: [] })
  })

  test("leaves MEDIA: in the middle of a line as text", () => {
    const text = "Put MEDIA:out/a.xlsx on its own line."
    expect(mediaOf(text)).toMatchObject({ shown: text, paths: [] })
  })

  test("cleans the quotes, brackets and file:// a model wraps a path in", () => {
    expect(mediaOf('MEDIA: "file://out/a b.xlsx"').paths).toEqual(["out/a b.xlsx"])
    expect(mediaOf("MEDIA:<out/a.xlsx>,").paths).toEqual(["out/a.xlsx"])
  })

  test("a fence open where the text starts goes on, and the fence open at its end is returned", () => {
    expect(mediaOf("MEDIA:out/a.xlsx\n```", "```")).toEqual({
      shown: "MEDIA:out/a.xlsx\n```",
      paths: [],
      fence: "",
    })
    expect(mediaOf("Example:\n~~~").fence).toBe("~~~")
  })

  test("drops an empty MEDIA: line without a file", () => {
    expect(mediaOf("Done.\nMEDIA:  ")).toMatchObject({ shown: "Done.", paths: [] })
  })
})

// A Bot's reply in the web chat (issue #699): each MEDIA: line is a file to download, not text.
describe("a reply's files", () => {
  test("a MEDIA: line becomes a chip that downloads the file from the Bot's folder", () => {
    reply("The sheet is ready.\nMEDIA:out/tuan/dong-gop.xlsx\nIt has every column.")

    const chip = screen.getByRole("link", { name: "Download dong-gop.xlsx" })
    expect(chip).toHaveAttribute("href", link("out/tuan/dong-gop.xlsx"))
    expect(chip).toHaveAttribute("download", "dong-gop.xlsx")
    expect(screen.getByText("The sheet is ready.", { exact: false })).toBeInTheDocument()
    expect(screen.getByText("It has every column.", { exact: false })).toBeInTheDocument()
    expect(screen.queryByText(/MEDIA:/)).not.toBeInTheDocument()
  })

  test("two lines make two chips", () => {
    reply("Both:\nMEDIA:./out/a.xlsx\nMEDIA:out/b.zip")

    const files = within(screen.getByRole("list", { name: "Files" })).getAllByRole("link")
    expect(files.map((a) => a.getAttribute("href"))).toEqual([
      link("out/a.xlsx"),
      link("out/b.zip"),
    ])
  })

  test("a line in a code fence stays text", () => {
    reply("Write it so:\n```\nMEDIA:out/a.xlsx\n```")

    expect(screen.queryByRole("list", { name: "Files" })).not.toBeInTheDocument()
    expect(screen.getByText(/MEDIA:out\/a\.xlsx/)).toBeInTheDocument()
  })

  test("a line in a fence another text part of the message opened stays text", () => {
    reply(["Example:\n```", "MEDIA:out/sample.xlsx\n```", "MEDIA:out/real.xlsx"])

    const files = within(screen.getByRole("list", { name: "Files" })).getAllByRole("link")
    expect(files.map((a) => a.getAttribute("href"))).toEqual([link("out/real.xlsx")])
    expect(screen.getByText(/MEDIA:out\/sample\.xlsx/)).toBeInTheDocument()
  })

  test("a file the server refuses says the server's message", async () => {
    readWorkspaceRaw.mockRejectedValueOnce({
      code: "not_found",
      message: "out/a.xlsx is not there.",
    })
    reply("MEDIA:out/a.xlsx")

    await userEvent.setup().click(screen.getByRole("link", { name: "Download a.xlsx" }))

    await waitFor(() => expect(screen.getByText("out/a.xlsx is not there.")).toBeInTheDocument())
    expect(readWorkspaceRaw).toHaveBeenCalledWith(
      expect.objectContaining({
        path: { chat_id: "cht_1", workspace_id: "bot" },
        query: { path: "out/a.xlsx" },
      }),
    )
  })
})
