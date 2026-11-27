import { expect, test, type Page } from "@playwright/test"

import { shot } from "./shot.js"

// Issue #1219: a worker's decision stands alone. A Gate of two decisions reaches the owner's Inbox as one card: the
// worker's summary above, then each decision with its header, and each option with what it changes and where it
// comes from. He answers both at once. A Gate asked before, with bare labels, reads as it did.

const at = (ms: number) => new Date(Date.now() + ms).toISOString()
const minute = 60_000

const twoDecisions = {
  id: "qst_two",
  dispatch_id: "dsp_1",
  task_id: "tsk_1",
  run_id: "run_1",
  mode: "gate",
  addressed_to: "owner",
  summary:
    "Đã lập kế hoạch cho #1206 (Inbox nhận việc được nhờ). Còn hai việc anh cần quyết; phần còn lại đã ghi trong issue.\n" +
    "Đã đọc: Linear (mỗi yêu cầu một issue), inbox-zero (gom theo luồng thư), Paperclip (mỗi câu hỏi một thẻ).\n" +
    "Chưa sửa gì trên zm.",
  header: "Mục Inbox",
  text: "Mỗi việc người khác nhờ anh (một tin Zalo, một thư) thành một mục Inbox riêng, hay gom theo người nhờ?",
  options: [
    {
      label: "Mỗi việc một mục (Recommended)",
      description:
        "Mỗi lời nhờ có hạn và trạng thái riêng, xong việc nào đóng việc đó. Học từ: Linear, mỗi yêu cầu một issue.",
    },
    {
      label: "Gom theo người nhờ",
      description:
        "Một mục cho mỗi người, các việc là dòng con; ít mục hơn nhưng không đặt hạn riêng được. Học từ: inbox-zero.",
    },
    { label: "Other", allows_text: true },
  ],
  more: [
    {
      header: "Server zm",
      text: "Sửa nhỏ trên server zm (bật đọc tin Zalo cũ hơn 7 ngày) ngay bây giờ, hay để sau khi #1206 xong?",
      options: [
        {
          label: "Để sau (Recommended)",
          description:
            "Không đổi gì trên zm lúc này; làm cùng đợt cập nhật tới. Không tham chiếu nào làm khác.",
        },
        {
          label: "Sửa ngay",
          description: "Tin cũ có ngay hôm nay, nhưng zm khởi động lại khoảng 1 phút giữa giờ làm.",
        },
        { label: "Other", allows_text: true },
      ],
    },
  ],
  status: "open",
  created_at: at(-12 * minute),
}

const oldGate = {
  id: "qst_old",
  dispatch_id: "dsp_2",
  task_id: "tsk_2",
  run_id: "run_1",
  mode: "gate",
  addressed_to: "owner",
  text: "PR #7 is green. Merge it?",
  options: [
    { label: "merge" },
    { label: "change first", allows_text: true },
    { label: "Other", allows_text: true },
  ],
  status: "open",
  created_at: at(-40 * minute),
}

async function serve(page: Page, questions: unknown[]) {
  const answers: { path: string; body: unknown }[] = []

  await page.context().route("**/api/v1/**", async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace("/api/v1", "")
    const json = (body: unknown, status = 200) =>
      route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) })

    if (path === "/questions") return json({ data: questions })
    if (path.startsWith("/questions/") && path.endsWith("/answer")) {
      answers.push({ path, body: request.postDataJSON() })

      return json({ ...twoDecisions, status: "answered" })
    }

    switch (path) {
      case "/me":
        return json({ id: "usr_1", name: "admin", kind: "owner" })
      case "/inbox":
        return json({ data: [], total: 0 })
      case "/inbox/needs-you":
        return json({ count: questions.length })
      case "/tasks":
        return json({ data: [], runs: [], after: 0 })
      case "/events/stream":
        return route.fulfill({ status: 200, contentType: "text/event-stream", body: ": hello\n\n" })
      default:
        return json({ data: [] })
    }
  })

  return answers
}

const card = (page: Page) => page.getByRole("form", { name: "Answer" })

// Scenario: the owner opens the Gate #1206's planner stopped at. He reads what the worker did, then each decision
// under its header with what each option changes and where it is learned from, picks both, and sends one answer.
test("a gate of two decisions reaches the owner as one card", async ({ page }) => {
  const answers = await serve(page, [twoDecisions])
  await page.goto("/inbox?item=question%3Aqst_two")

  const form = card(page)
  await expect(form.getByRole("region", { name: "The worker's summary" })).toContainText(
    "Đã lập kế hoạch cho #1206",
  )
  const items = form.getByRole("group", { name: "Mục Inbox" })
  const server = form.getByRole("group", { name: "Server zm" })
  await expect(items).toContainText("1. Mục Inbox")
  await expect(items).toContainText("Học từ: Linear, mỗi yêu cầu một issue.")
  await expect(server).toContainText("2. Server zm")
  await expect(server).toContainText("zm khởi động lại khoảng 1 phút")
  await form.scrollIntoViewIfNeeded()
  await shot(page, "inbox-1219-two-decisions")

  await form.getByRole("button", { name: "Show all" }).click()
  await expect(form.getByRole("button", { name: "Show less" })).toBeVisible()
  await shot(page, "inbox-1219-summary-open")

  await server.getByText("Sửa ngay").click()
  await form.getByRole("button", { name: "Send answer" }).click()
  await expect
    .poll(() => answers)
    .toEqual([{ path: "/questions/qst_two/answer", body: { option: 0, more: [{ option: 1 }] } }])
})

// Scenario: a Gate asked before issue #1219 has no header, no descriptions and no summary: its card shows the
// question and its bare labels, as it did.
test("a gate asked before reads as it did", async ({ page }) => {
  const answers = await serve(page, [oldGate])
  await page.goto("/inbox?item=question%3Aqst_old")

  const form = card(page)
  await expect(form).toContainText("PR #7 is green. Merge it?")
  await expect(form.getByRole("radio")).toHaveCount(3)
  await expect(form.getByRole("region", { name: "The worker's summary" })).toHaveCount(0)
  await expect(form.getByRole("group", { name: "Options" })).toHaveCount(1)
  await shot(page, "inbox-1219-old-gate")

  await form.getByText("merge", { exact: true }).click()
  await form.getByRole("button", { name: "Send answer" }).click()
  await expect
    .poll(() => answers)
    .toEqual([{ path: "/questions/qst_old/answer", body: { option: 0 } }])
})

// Scenario: review of PR #1228. A summary of one long line, 227 characters, wraps past three lines on a phone. It
// shows three lines and "Show all", which opens the whole; on a screen where it fits, no button is shown.
test("a long summary shows three lines and opens to the whole", async ({ page }) => {
  const summary =
    "Planned #1206 from the owner's notes and the three references; the plan is in the issue, the server is " +
    "untouched, and two decisions are left for the owner before the implementer starts on the Inbox item shape this afternoon ok."
  expect(summary).toHaveLength(227)
  await serve(page, [{ ...twoDecisions, summary }])
  await page.goto("/inbox?item=question%3Aqst_two")

  const text = card(page).getByRole("region", { name: "The worker's summary" }).locator("p")
  await expect(text).toContainText("Planned #1206")
  const lines = await text.evaluate((p) => {
    const style = p.ownerDocument.defaultView?.getComputedStyle(p)

    return Math.round(p.clientHeight / Number.parseFloat(style?.lineHeight ?? "1"))
  })
  expect(lines).toBeLessThanOrEqual(3)

  const overflows = await text.evaluate((p) => p.scrollHeight > p.clientHeight + 1)
  if ((page.viewportSize()?.width ?? 1280) < 600) expect(overflows).toBe(true)

  const all = card(page).getByRole("button", { name: "Show all" })
  await expect(all).toHaveCount(overflows ? 1 : 0)
  if (!overflows) return

  await all.click()
  await expect(card(page).getByRole("button", { name: "Show less" })).toBeVisible()
  expect(await text.evaluate((p) => p.scrollHeight <= p.clientHeight + 1)).toBe(true)
  await shot(page, "inbox-1219-long-summary-open")
})

// Scenario: review of PR #1228. A summary holding one long unbroken string, a SHA-256, wraps inside the card on a
// phone instead of running past its edge, and opens to the whole when it runs past three lines.
test("a summary with a long unbroken string wraps inside the card", async ({ page }) => {
  const sha = "9f0fac2c88557f64cdbb4cc92cfd96192389403a" + "e3b0c44298fc1c149afbf4c8996fb924"
  const summary = `Pushed ${sha}${sha} and checked it; the plan is in the issue.`
  await serve(page, [{ ...twoDecisions, summary }])
  await page.goto("/inbox?item=question%3Aqst_two")

  const text = card(page).getByRole("region", { name: "The worker's summary" }).locator("p")
  await expect(text).toContainText("Pushed")
  expect(await text.evaluate((p) => p.scrollWidth <= p.clientWidth + 1)).toBe(true)

  const overflows = await text.evaluate((p) => p.scrollHeight > p.clientHeight + 1)
  await expect(card(page).getByRole("button", { name: "Show all" })).toHaveCount(overflows ? 1 : 0)
  if ((page.viewportSize()?.width ?? 1280) < 600) expect(overflows).toBe(true)
  await shot(page, "inbox-1219-long-string")
})
