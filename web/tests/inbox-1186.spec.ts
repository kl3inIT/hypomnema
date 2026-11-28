import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// Issue #1186: the two kinds of item the owner found hard to read on 2026-11-26, a follow-up with a partner about
// TTS and a KPI item for push notifications, each a running log of dated lines full of message ids. Their words are
// made up here in the same shape (the repository is public); the ids are of the same kinds. "log" is the
// description as the triage wrote it; "split" is after the one-off pass of plan row 4: the description keeps the
// need and the plan, the log lines are comments, and the first comment keeps the old description.

const at = (ms: number) => new Date(Date.now() + ms).toISOString()
const hour = 3_600_000

const pushItem = "tdo_pushpushpushpushpushpushpu"
const teamChat = "2900339445898790001"
const thanhChat = "2900339445898790002"
const lanChat = "2900339445898790003"
const minhChat = "84900000001@s.whatsapp.net"
const larkGroup = "oc_9143bfddcd80cdb37d137257859e5000"

type Message = { service: "zalo" | "whatsapp"; id: string; chat: string; chat_name: string }
const said = (
  m: Message,
  sender: string,
  minutesAgo: number,
  text: string,
  fromMe = false,
): Record<string, unknown> => ({
  ...m,
  sender,
  sender_name: sender,
  at: at(-minutesAgo * 60_000),
  from_me: fromMe,
  text,
  edited: false,
  revoked: false,
  media_kept: false,
})
const zalo = (id: string, chat: string, name: string): Message => ({
  service: "zalo",
  id,
  chat,
  chat_name: name,
})
const wa = (id: string): Message => ({
  service: "whatsapp",
  id,
  chat: minhChat,
  chat_name: "Anh Minh (đối tác TTS)",
})

const kept = [
  said(
    zalo("8527930939001", teamChat, "Nhóm data 96"),
    "Hải",
    24 * 60,
    "Thanh vào dashboard review, define KPI giúp anh để biết khi nào push noti nhé",
  ),
  said(
    zalo("8527929591002", teamChat, "Nhóm data 96"),
    "Hải",
    24 * 60 - 1,
    "Chỉ push được cá nhân thôi, không theo phòng ban",
  ),
  said(zalo("8527934507003", teamChat, "Nhóm data 96"), "Thanh", 24 * 60 - 2, "vâng"),
  said(
    zalo("8529905471004", teamChat, "Nhóm data 96"),
    "Thanh",
    8 * 60,
    "Hôm nay em sửa dashboard, sáng nay em chốt KPI với anh Phong",
  ),
  said(
    zalo("8529917262005", teamChat, "Nhóm data 96"),
    "Thanh",
    8 * 60 - 3,
    "Data hơi lạ anh ạ, tổng user 14K mà NV mới 1.5K, em check lại",
  ),
  said(zalo("8531046704006", thanhChat, "Thanh"), "Thanh", 3 * 60, "(image)"),
  said(
    zalo("8531130269001", lanChat, "Cô Lan"),
    "Phan Đạt",
    2 * 60,
    "Cô ơi mình có làm tiếp TTS không ạ?",
    true,
  ),
  said(zalo("8531149547002", lanChat, "Cô Lan"), "Cô Lan", 2 * 60 - 5, "em cứ test trước nhé"),
  said(
    wa("AC6CB66DE0BCD9613E82CD3AB0FE0001"),
    "Anh Minh",
    17 * 24 * 60,
    "Bên em đã chọn voice chưa em?",
  ),
  said(
    wa("AC760E5BB1B73109326B2558B7210002"),
    "Anh Minh",
    6 * 60,
    "Em ơi bên em có process tiếp vụ TTS không?",
  ),
  said(
    wa("3EB0E056F0533EEF550003"),
    "Phan Đạt",
    90,
    "TTS đang tạm gác để ưu tiên việc khác ạ. Khi nào có update em báo anh nhé ạ.",
    true,
  ),
  said(wa("AC7C8C756650E103A854BF4F1A340004"), "Anh Minh", 85, "Ok thank em nhé!"),
]

const logs = {
  tts: [
    `Nhóm WA "Đối tác <> Tasco | TTS". Anh Minh hỏi lại 09/11 (AC6CB66DE0BCD9613E82CD3AB0FE0001) và hôm nay qua chat riêng (AC760E5BB1B73109326B2558B7210002). Use case: giọng dẫn đường VMAP. Chưa rõ hạn.`,
    `26/11 14:13 anh hỏi cô Lan (Zalo) có làm tiếp TTS không (8531130269001); cô Lan: "em cứ test trước nhé" (8531149547002). Việc: test TTS trước rồi phản hồi anh Minh.`,
    `26/11 14:38 anh báo anh Minh (WA riêng, 3EB0E056F0533EEF550003): TTS đang tạm gác để ưu tiên việc khác. Anh Minh: "Ok thank em nhé!" (AC7C8C756650E103A854BF4F1A340004). Còn nợ: báo lại khi có cập nhật. Không có hạn.`,
  ].join("\n"),
  kpi: [
    `Zalo "Nhóm data 96" 25/11 16:26: anh Hải giao Thanh review dashboard và define KPI để biết khi nào push noti (8527930939001); chỉ push được cá nhân (8527929591002). Thanh: "vâng" (8527934507003). Đây là đầu vào cho backend push service của anh (${pushItem}). Không nói hạn.`,
    `Lark nhóm "VMAP - Data Platform" (${larkGroup}) 25/11 17:32: Thanh sẽ trao đổi với anh Phong về KPI (om_x100b7f5fdbe13ca8edad8575cbee0001).`,
    `26/11 08:57 Zalo nhóm: Thanh sửa dashboard, sáng nay chốt KPI với anh Phong (8529905471004).`,
    `26/11 09:00 Zalo nhóm: Thanh thấy data hơi lạ, sẽ check lại (8529917262005). Không tag anh.`,
    `26/11 13:51 Zalo chat riêng Thanh: gửi anh ảnh 3 dataset (8531046704006), không kèm câu hỏi.`,
  ].join("\n"),
}

// What the one-off pass of plan row 4 leaves: the need and the plan, and the log as comments.
const split = {
  tts: {
    notes: `## Source\nWhatsApp riêng anh Minh, 26/11: anh hứa báo lại khi có cập nhật về TTS (3EB0E056F0533EEF550003).\n\n## Plan\n1. Khi TTS được mở lại: test các voice.\n2. Báo anh Minh kết quả.`,
    comments: [
      `26/11 14:13, Zalo cô Lan: "em cứ test trước nhé" (8531149547002).`,
      `26/11 14:38, WhatsApp anh Minh: ngược với 14:13, TTS đang tạm gác; anh hứa báo lại khi có cập nhật (3EB0E056F0533EEF550003).`,
    ],
  },
  kpi: {
    notes: `## Source\nZalo "Nhóm data 96", 25/11 16:26: anh Hải giao Thanh define KPI push noti (8527930939001), đầu vào cho ${pushItem}.\n\n## Plan\n1. Lấy bảng tạm KPI của Thanh.\n2. Bắn noti cá nhân từ backend push service.`,
    comments: [
      `25/11 17:32, Lark nhóm VMAP - Data Platform (${larkGroup}): Thanh sẽ trao đổi với anh Phong về KPI.`,
      `26/11 08:57, Zalo nhóm: Thanh sửa dashboard, chốt KPI với anh Phong (8529905471004).`,
      `26/11 09:00, Zalo nhóm: Thanh thấy data hơi lạ, sẽ check lại (8529917262005).`,
      `26/11 13:51, Zalo riêng Thanh: gửi anh ảnh 3 dataset (8531046704006), không kèm câu hỏi.`,
    ],
  },
}

const todo = (id: string, title: string, m: object): Record<string, unknown> => ({
  id,
  revision: 4,
  title,
  notes: "",
  done_when: "",
  draft: "",
  due: { kind: "none" },
  assignee: { kind: "me" },
  state: "open",
  created_by: { kind: "bot", id: "bot_main" },
  close_reason: "",
  evidence: [],
  stale: false,
  priority: 3,
  priority_reason: "Đã hứa, chưa có hạn",
  created_at: at(-24 * hour),
  updated_at: at(-hour),
  ...m,
})

async function serve(page: Page, kind: "log" | "split") {
  const items = [
    todo("tdo_tts", "Báo anh Minh khi có cập nhật về TTS (đang tạm gác)", {
      why_open: "promised",
      notes: kind === "log" ? logs.tts : split.tts.notes,
      source: {
        kind: "whatsapp",
        id: minhChat,
        account: "main",
        quote: "Khi nào có update em báo anh nhé ạ.",
        ref: { kind: "whatsapp_message", ref: "3EB0E056F0533EEF550003" },
      },
    }),
    todo("tdo_kpi", "Thanh: review dashboard và định nghĩa KPI (khi nào bắn noti)", {
      assignee: { kind: "person", name: "Thanh" },
      why_open: "waiting",
      notes: kind === "log" ? logs.kpi : split.kpi.notes,
      source: {
        kind: "zalo",
        id: teamChat,
        account: "main",
        quote: "Thanh vào dashboard review, define KPI giúp anh để biết khi nào push noti nhé",
        ref: { kind: "zalo_message", ref: "8527930939001" },
      },
    }),
  ]
  const events = (id: string) => {
    if (kind === "log") return []
    const old = id === "tdo_tts" ? logs.tts : logs.kpi
    const comments = [
      `Tách ghi chú (issue #1186): nhật ký cũ thành các bình luận dưới đây.\n\n<details>\n<summary>Ghi chú cũ</summary>\n\n~~~~text\n${old}\n~~~~\n\n</details>`,
      ...(id === "tdo_tts" ? split.tts.comments : split.kpi.comments),
    ]

    return comments.map((text, i) => ({
      id: i + 2,
      at: at(-30 * 60_000 + i * 1000),
      revision: 5 + i,
      actor: { kind: "bot", id: "bot_main" },
      kind: "commented",
      before: {},
      after: { text },
    }))
  }

  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url())
    const path = url.pathname.replace("/api/v1", "")
    const json = (body: unknown) =>
      route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })

    if (path === "/inbox") return json({ data: items, total: items.length })
    if (path === "/inbox/needs-you") return json({ count: 0 })
    if (path === `/inbox/${pushItem}`)
      return json({ ...todo(pushItem, "Backend push service cho VMAP", {}), events: [] })
    const one = items.find((t) => path === `/inbox/${t.id as string}`)
    if (one) {
      return json({
        ...one,
        events: [
          {
            id: 1,
            at: one.created_at,
            revision: 1,
            actor: { kind: "bot", id: "bot_main" },
            kind: "created",
            before: {},
            after: { title: one.title },
          },
          ...events(one.id as string),
        ],
      })
    }
    const messages = /^\/integrations\/(zalo|whatsapp)\/connections\/main\/messages$/.exec(path)
    if (messages) {
      const id = url.searchParams.get("id")
      const chat = url.searchParams.get("chat")
      const before = Date.parse(url.searchParams.get("before") ?? "") || Infinity
      const list = kept
        .filter((m) => m.service === messages[1])
        .filter((m) =>
          id ? m.id === id : (!chat || m.chat === chat) && Date.parse(m.at as string) < before,
        )
        .toSorted((a, b) => Date.parse(b.at as string) - Date.parse(a.at as string))

      return json({ data: list.slice(0, Number(url.searchParams.get("limit") ?? 50)) })
    }
    const chats = /^\/integrations\/(zalo|whatsapp)\/connections\/main\/chats$/.exec(path)
    if (chats) {
      const names = new Map(
        kept.filter((m) => m.service === chats[1]).map((m) => [m.chat, m.chat_name]),
      )

      return json({
        data: [...names].map(([id, name]) => ({
          id,
          name,
          kind: "group",
          last_message_at: at(-hour),
        })),
      })
    }

    switch (path) {
      case "/integrations":
        return json({
          data: ["whatsapp", "zalo"].map((id) => ({
            id,
            name: id === "zalo" ? "Zalo" : "WhatsApp",
            kind: "device",
            services: [],
            connections: [{ integration: id, label: "main", account: "me", services: [] }],
          })),
        })
      case "/me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "/bots":
        return json({
          data: [
            {
              id: "bot_main",
              name: "Hypo",
              main: true,
              instructions: "",
              memory: "",
              memory_limit: 2200,
              skills: [],
              skills_off: [],
              tools: [],
              tools_off: [],
              connections: { limited: false, names: [] },
              workers: { limited: false, names: [] },
              scopes: {},
              created_at: at(-hour),
              updated_at: at(-hour),
            },
          ],
        })
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return json({ data: [] })
    }
  })
}

const item = (page: Page) => page.getByRole("article", { name: "Item" })
const notes = (page: Page) => item(page).locator("[data-slot=notes]")

// Scenario: the owner opens the follow-up with the partner about TTS. Each line of its description is a paragraph
// with space between; every WhatsApp and Zalo message it names is a link named "message", which opens it in its
// chat, the messages before it and it; no message id is left in its text.
test("an item's log reads as paragraphs, its message ids as links that open the message", async ({
  page,
}) => {
  await serve(page, "log")
  await page.goto("/inbox?item=todo%3Atdo_tts")
  await expect(item(page).getByRole("heading", { name: /Báo anh Minh/ })).toBeVisible()
  await expect(notes(page).locator("p")).toHaveCount(3)
  await expect(notes(page).getByRole("button", { name: /^Message from / })).toHaveCount(6)
  await expect(notes(page)).not.toContainText("3EB0E056F0533EEF550003")
  await expect(notes(page)).not.toContainText("8531149547002")
  await shot(page, "inbox-1186-tts-log")

  await notes(page)
    .getByRole("button", { name: /^Message from Cô Lan/ })
    .click()
  const chat = page.getByRole("list", { name: "Cô Lan" })
  await expect(chat.getByRole("listitem")).toHaveText([/You.*làm tiếp TTS/, /Cô Lan.*test trước/])
  await expect(chat.locator("[aria-current=true]")).toContainText("em cứ test trước nhé")
  await shot(page, "inbox-1186-tts-message")
})

// Scenario: the KPI item names Zalo messages of two chats, a Lark group and the item it feeds: each is a link, the
// Lark group opens in Lark, and the item reads as its title. The Lark message of a chat the item was not read in
// stays as written.
test("an item's Lark chat and another item are links by what they name", async ({ page }) => {
  await serve(page, "log")
  await page.goto("/inbox?item=todo%3Atdo_kpi")
  await expect(notes(page).locator("p")).toHaveCount(5)
  await expect(notes(page).getByRole("button", { name: /^Message from / })).toHaveCount(6)
  await expect(notes(page).getByRole("link", { name: "Lark chat" })).toHaveAttribute(
    "href",
    `https://applink.larksuite.com/client/chat/open?openChatId=${larkGroup}`,
  )
  await expect(
    notes(page).getByRole("link", { name: "Backend push service cho VMAP" }),
  ).toHaveAttribute("href", `/inbox?item=todo%3A${pushItem}`)
  await expect(notes(page)).toContainText("om_x100b7f5fdbe13ca8edad8575cbee0001")
  await shot(page, "inbox-1186-kpi-log")
})

// Scenario: after the one-off pass, each item's description is its need and its plan, and what happened since is
// its comments, the old description folded in the first.
test("after the split, the description is the need and the log is comments", async ({ page }) => {
  await serve(page, "split")
  for (const [id, count] of [
    ["tdo_tts", 2],
    ["tdo_kpi", 4],
  ] as const) {
    await page.goto(`/inbox?item=todo%3A${id}`)
    await expect(notes(page).getByRole("heading", { name: "Plan" })).toBeVisible()
    await expect(item(page).getByRole("button", { name: "Ghi chú cũ" })).toBeVisible()
    await expect(item(page).getByText(/^\d\d\/11 \d\d:\d\d, /)).toHaveCount(count)
    await item(page)
      .getByText(/^\d\d\/11 \d\d:\d\d, /)
      .first()
      .scrollIntoViewIfNeeded()
    await shot(page, `inbox-1186-${id.slice(4)}-split`)
  }
})
