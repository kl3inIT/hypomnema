# The web client against the references: screens and the chat

Asked by the owner on 2026-11-12: what in the UI still looks worse than the references, and whether the chat keeps
up with what the product does today. Two read-only audits, each by a sub-agent that read the references' code; the
lines below give the paths it reported. Hypomnema was read at `a3293b1`. Screenshots of every screen and state,
desktop and phone, were taken from the Playwright fake-server tests the same day and opened one by one.

References read, with their revisions: Orca `e8f5ace6` (`src/renderer/src/components`, `native-chat/`), t3code
`b8c2cfad` (`apps/web/src/components`, `chat/`), Omnigent `2641a4a` (`web/src/pages/ChatPage.tsx`,
`web/src/components`), assistant-ui `27d4bb8` (`packages/ui` elements), AI Elements `ca8be60`
(`packages/elements/src`), Coder `b798061` (`site/src`), Beszel `3392dcc` (`internal/site/src`).

## 1. The chat

### What the server can put in a chat, and what the web shows

The server's parts are `text`, `thinking` and `tool_result` (`api/openapi.yaml`); live frames are `reset`, `delta`,
`tool` and `state` (`internal/controlplane/coordinator/live.go`). The API sends text only and has no edit, retry,
delete, rename, attachment or model choice. Tool output keeps its text only (`coordinator/activity.go`).

| Server side | Web today |
|---|---|
| Text and thinking, streamed and kept | Shown: markdown with copyable code; thinking folds with the tools |
| A tool's call and result | Shown as rows that open; one line of its arguments only; `duration_ms` unused |
| Working state, Stop | Shown |
| The owner pressed Stop | Shown as "pi's model call failed" in red; Lark already tells the two apart (`larkbot/channels.go`) |
| Wake-ups (a Task ended, a worker asks, a Run ended) | Shown as a CLI command, with no link to the Run (`orchestration/chats.go`, `chat-items.tsx`) |
| A message sent while the Bot works, `delivered_at` | Looks delivered at once; `delivered_at` unused |
| Approvals of the chat | Cards above the composer; an answered one leaves no trace |
| A worker's question forwarded to the owner | Only on the board |
| Runs made from the chat (`Run.chat_id`) | Not shown; `ListRuns` has no `chat_id` filter |
| A failed send | Nothing shown: `send()` has no `catch` (`composer.tsx`) |
| The stream dropped or the server restarted | Reconnects with no sign |
| The Bot's name | "pi" written in the placeholder and the reply's heading |
| History | Every message from the first on open; no paging |

Already as the references do: scroll to the bottom (AI Elements `conversation.tsx`), the streaming shimmer,
reasoning and tools folded in groups (t3code's work log), markdown with code copy, Enter that waits for an input
method.

### What the references have that the chat lacks

| Gap | Size | References |
|---|---|---|
| A stopped turn said as stopped | Small | t3code `MessagesTimeline.logic.ts`; Orca `native-chat/messages/notices/`; assistant-ui `elements/stopped-run.tsx` |
| Wake-ups as cards that link a Run, a Task, a question | Medium, server | Orca `native-chat/tools/runs/`, `tools/tasks/NativeChatTaskList.tsx`; t3code `TimelineSystemDivider.tsx`, `MarkdownThreadLink.tsx` |
| A queue of messages sent while the Bot works | Medium | Omnigent `pages/QueuedMessagesStrip.tsx`, `hooks/QueueFlushProvider.tsx`; Orca `native-chat/queue/`; AI Elements `queue.tsx`; assistant-ui `elements/message-queue.tsx` |
| The owner's message shown at once, a failed send with a retry | Small | t3code `optimisticUserMessages.ts`, `ThreadErrorBanner.tsx`; assistant-ui `elements/error-state.tsx` |
| The connection's state | Small | Omnigent `pages/ChatIndicators.tsx` (ConnectionIndicator); assistant-ui `elements/connection-state.tsx` |
| The chat's Runs in the chat | Medium, server | t3code `ThreadDetailsPanel.tsx`; Orca `tools/tasks/NativeChatDockedTaskList.tsx` |
| A worker's question answered in the chat | Medium, server | t3code `ComposerPendingUserInputPanel.tsx`; AI Elements `question.tsx`; assistant-ui `elements/question-flow.tsx` |
| Older history loaded on scroll, a virtual list | Medium | Orca `native-chat/viewport/history/`, `viewport/virtualization/`; Omnigent `components/InfiniteScrollSentinel.tsx` |
| Tools in full: duration, arguments, diffs, the browser page | Medium | Orca `tools/lines/NativeChatToolAnnotations.tsx`, `tools/runs/NativeChatBrowserPageButton.tsx`, `tools/diffs/`; AI Elements `tool.tsx`; assistant-ui `elements/code-diff.tsx` |
| Keys: approve, decline, Esc to stop | Small | Omnigent `hooks/useApproveHotkey.ts`; t3code `useChatShortcuts.ts` |
| Answered approvals kept in the history | Small, server | t3code `TimelinePendingApprovalRow.tsx`; AI Elements `confirmation.tsx` |
| A draft kept per chat | Small | Orca `native-chat/drafts/`; assistant-ui `elements/draft-restore.tsx` |
| Files and images | Large, server | Omnigent `components/ComposerAttachments.tsx`; Orca `native-chat/attachments/`; AI Elements `prompt-input.tsx` |
| The Bot's name; the Bot and model in the composer | Small; medium | t3code `ProviderModelPicker.tsx`, `ComposerFooterControls.tsx`; Omnigent `components/ModelValueCombobox.tsx` |
| Retry, edit and resend, fork | Large, server | Orca `messages/actions/NativeChatRewindAction.tsx`; AI Elements `message.tsx`; assistant-ui `elements/edit-message.tsx` |
| "Working for" counted from the turn's start | Small | t3code `AgentElapsed.tsx` |
| A new-messages marker, find in the chat | Small to medium | Orca `viewport/new-rows/`, `viewport/find/`; t3code `ThreadFindBar.tsx` |

## 2. The screens

Read against a reference's code:

| Finding (picture) | Reference |
|---|---|
| A tall dialog scrolls its title away (channels-add) | t3code `components/ui/dialog.tsx`: the popup is bounded, the header and footer stay, a `DialogPanel` scrolls |
| A reply shows raw `**run_1**` while the lazily loaded markdown renderer arrives (chat-approval) | t3code imports its markdown renderer directly |
| Wide tables cut their last column at 1280 px (machines-table, agents-table) | Beszel `systems-table.tsx`: a View menu shows or hides columns, and a filter |
| The approval card's three equal buttons wrap on a phone (chat-approval, task-approval) | t3code `ComposerPendingApprovalActions.tsx`: Decline and Approve, the rest in a menu |
| The sign-in page has no mark and no card (sign-in-refused) | Coder `pages/LoginPage/LoginPageView.tsx`, `SignInForm.tsx` |
| Empty states differ: Browser says a normal state in red, Models shows a dashed box with a command | t3code `components/ui/empty.tsx`, one component for all |
| "0m ago" for a moment ago | t3code `timestampFormat.ts`: "just now" |
| The composer is a bare box; the Bot picker sits above it | t3code `ComposerFooterControls.tsx`, `ComposerModelEffort.tsx` |

The sub-agent's own judgement, with no reference read: the agent builder's Save below the fold and its errors far
from their field; two rows of tabs on an agent's page; the model list opening over the fields above it; the code
font used for sentences ("Runtime default"); raw argument names in an approval's details ("emailAddress"); a large
red Cancel Task above the task panel's content and unlabelled chips; card titles squeezed to four lines on a phone;
the Settings tabs cut at the phone's edge; "New Bot" against "New agent".

Screens found fine: the board, the machines grid, the agents grid, the Integrations list and permissions sheet,
Channels, the Vault, the Models page on a desktop, the Codex sign-in code, the Run page's question card, an agent's
versions, the four-chat grid.

## 3. What the owner decided

On 2026-11-12 the owner approved fixing the web-only items first, as one change: a stopped turn said as stopped, a
failed send shown with a retry and the owner's message shown at once, the connection's state, the Bot's own name,
dialogs with a fixed header and footer, one empty state, two approval buttons, a sign-in page with the mark, "just
now", the code font and raw argument names, the phone layouts, the markdown renderer loaded with the chat, and a
View menu for wide tables. The items that need the server (wake-up cards, the queue, Runs and questions in the
chat, answered approvals, history paging, fuller tools) each get a design for approval; files, retry and edit, and
a model in the composer come later.
