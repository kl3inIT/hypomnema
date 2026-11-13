# The chat's composer and screen against six references

Date: 2026-11-13. Read in code: t3code `b8c2cfad` (`apps/web/src/components/chat/`), Orca `e8f5ace6`
(`src/renderer/src/components/native-chat/`), Omnigent `2641a4a` (`web/src/`), OpenMausBot `1a4e4da9f`
(`src/components/`, `server/`), assistant-ui `27d4bb8` (`packages/ui/.../elements/`, `packages/react/src/primitives/`),
AI Elements `ca8be60` (`packages/elements/src/`), pi in `.tmp/pi` (`packages/coding-agent/docs/rpc-commands.md`).
Hypomnema read at `origin/main` `17534b6`.

The owner asked what the references' composers and chat screens have, to set one standard for the chat
(2026-11-13).

## What each has

✓ has it, ◐ in part or display only, – not found after a search.

| Feature | t3code | Orca | Omnigent | OpenMausBot | assistant-ui | AI Elements | Hypomnema |
|---|---|---|---|---|---|---|---|
| Attach: button, drop, paste, chips | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | – (Files panel only, #315) |
| Context window meter | ◐ details panel | ✓ ring | ✓ ring | ◐ warning only | ✓ | ✓ ring and cost | – |
| Cost | – | – | ◐ session | – | ◐ | ✓ | – |
| Model picker in the composer | ✓ | ✓ | ✓ | ✓ header | ✓ | ✓ | – (Bot page) |
| Reasoning effort | ✓ | ✓ | ✓ | ✓ | ◐ | – | – |
| @ mentions, `/` commands, skills | ✓ | ✓ | ✓ | ✓ | ✓ | ◐ menu | – |
| Permission / plan / goal mode | ✓ | ✓ | ✓ | ✓ | – | – | – |
| One Send / Stop button | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Queue while working: see, edit, remove, steer | ✓ | ✓ and reorder | ✓ | ✓ | ✓ | ◐ display | ◐ "Queued" notice |
| Enter sends, Shift+Enter breaks, IME guard | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Up-arrow recalls the last prompt | ✓ | ✓ | ✓ | ✓ edits last | – | – | – |
| Draft kept per chat | ✓ localStorage | ✓ IndexedDB | ✓ sessionStorage | ✓ localStorage | ◐ banner | – | – |
| Auto-grow to a cap | ✓ | ✓ 8 lines | ✓ 180 px | ✓ 9 rem | ✓ | ✓ | ✓ |
| Dictation | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | – |
| Copy a message | ✓ | ✓ | ✓ | ✓ | ✓ | ◐ | ✓ |
| Edit and resend / fork / rewind | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | – |
| Retry | – | – | ◐ error rows | ✓ | ✓ | ◐ | ◐ a refused send |
| Reasoning row with duration | ✓ | ✓ | ✓ | ◐ | ✓ | ✓ | ✓ |
| Tool rows: duration, arguments, diffs | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ◐ name and result |
| Changed files of a turn | ✓ | ✓ | – | – | – | – | ◐ Files panel |
| Plan / todo list | ✓ | ✓ | ✓ | – | ✓ | ✓ | ✓ docked Runs (#314) |
| Scroll to the latest | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Find in the chat | ✓ | ✓ | – | – | – | – | – |
| Images and files in messages | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | – |
| Older history on scroll | – | ✓ | – | – | – | – | – (#254 item 6) |

Main paths:

- t3code: `useComposerAttachmentIntake.ts`, `ComposerAttachmentStrip.tsx`, `ComposerCommandMenu.tsx`,
  `ProviderModelPicker.tsx`, `TraitsPicker.tsx`, `ThreadDetailsUsage.tsx`, `QueuedRunsControl.tsx`,
  `composerPromptHistory.ts`, `../../composerDraftStore.ts`, `ThreadFindBar.tsx`; attachments up to 50 MB
  (`packages/contracts/src/chatAttachment.ts:17`).
- Orca: `composer/controls/NativeChatComposerActions.tsx`, `attachments/`, `usage/NativeChatContextUsageRing.tsx`,
  `queue/`, `drafts/native-chat-composer-draft-indexeddb.ts`, `composer/editor/use-native-chat-composer-keydown.ts`,
  `tools/diffs/`.
- Omnigent: `components/composer/ComposerAddMenu.tsx`, `lib/attachments.ts` (image 50, PDF 20, text 10 MB),
  `lib/filesApi.ts`, `components/composer/ComposerContextRing.tsx` (input tokens over the model's window),
  `pages/QueuedMessagesStrip.tsx`, `lib/sessionDrafts.ts`, `components/blocks/ToolCard.tsx`.
- OpenMausBot: `Composer.tsx`, `ComposerAttachments.tsx`, `src/lib/composer-attachments.ts` (an image 10 MB, 10
  images or 40 MB a turn), `ContextWarning.tsx`, `ComposerQueuedMessages.tsx`, `src/lib/drafts.ts`,
  `MentionMenu.tsx`, `SlashCommandMenu.tsx`.
- assistant-ui: `attachment.aui.tsx`, `context-display.tsx`, `../utils/context-usage.ts` (amber at 65 %, red above
  85 %), `model-selector.aui.tsx`, `message-queue.tsx`.
- AI Elements: `prompt-input.tsx` (`accept`, `maxFiles`, `maxFileSize`), `attachments.tsx`, `context.tsx`,
  `model-selector.tsx`, `queue.tsx`, `speech-input.tsx`.

## Where an attachment is kept

- Orca keeps a session's attachments in a folder of its own state, `agent-session-attachments`
  (`src/shared/agent-session/agent-session-attachments.ts:6`,
  `src/main/native-chat/agent-session-attachments/agent-session-attachment-references.ts:15`), and gives the agent
  their paths.
- OpenMausBot keeps them in `DATA_DIR/attachments` (`server/attachments.ts:20`): images are uploaded
  (`uploadDraftImage`), other files are given by path (`pathForFile`).
- Omnigent uploads to the session's resources (`/v1/sessions/{id}/resources/files`, `web/src/lib/filesApi.ts:17`).
- Hypomnema's Files panel already uploads into a Bot's folder (`~/.hypomnema-coordinator/bots/<name>/`, pi's
  working directory) or a Task's worktree, in pieces of 384 KiB up to 50 MiB, after Orca (#315).

## What pi gives

- `get_session_stats` returns `contextUsage {tokens, contextWindow, percent}`, the estimate pi uses for compaction;
  `tokens` and `percent` are null just after a compaction until the next answer (`rpc-commands.md:526-567`).
- `fork` and `clone` exist (`rpc-commands.md:610`, `:638`).

## What the references share

- Attachments by button, drop and paste, shown as chips; large files go by upload or by path, not inline.
- Model, effort and mode are controls of the composer.
- A context meter from provider-reported tokens over the model's window; none shows cost per message.
- A message sent while the agent works is queued, can be edited, and a modifier steers.
- Enter sends, Shift+Enter breaks a line, up-arrow recalls; a draft is kept per conversation.
- Tool rows with duration and diffs, a todo list, a jump to the latest.

## The standard proposed to the owner

In this order, one pull request each:

1. Attach in the composer: button, drop, paste, chips; the file is uploaded into the chat's place in the Bot's
   folder and pi gets its path; images and files show in the message.
2. A context ring in the composer from pi's `get_session_stats`.
3. A draft kept per chat and up-arrow recall.
4. Tool rows in full (#254 item 8) and decided approvals kept as rows (#254 item 5).
5. Later: model and effort in the composer, @ files and `/` skills, the queue (#254 item 2), older history
   (#254 item 6).

Not proposed: cost (four of six have none; a subscription is not billed per token) and a permission mode in the
composer (Approvals decide that).

The owner approved part 1 to start (2026-11-13), one part at a time.
