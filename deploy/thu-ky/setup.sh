#!/bin/sh
# Sets up the Thư ký Bot and its two Routines (issue #1023, part 4 of #838). The owner runs it, with `hypomnema`
# logged in as himself (`hypomnema whoami` says owner); it changes only the control plane it is logged in to, and is
# safe to run again: each command sets what it names and leaves the rest. See README.md beside it.
#
#   deploy/thu-ky/setup.sh --connections lark:me,zalo:main,whatsapp:<label>,google:<label> [--brief-chat <chat id>]
#
# --connections   the owner's accounts the Bot reads (default lark:me,zalo:main): `hypomnema integrations list`
#                 names them.
# --brief-chat    the Hypomnema chat of the owner's Lark chat with the Bot's Lark app, where the brief is told and
#                 where he answers it; without it the brief Routine is left for later (README.md, step 3).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
bot="Thư ký"
connections="lark:me,zalo:main"
brief_chat=""

while [ $# -gt 0 ]; do
  case "$1" in
    --connections) connections=$2; shift 2 ;;
    --brief-chat) brief_chat=$2; shift 2 ;;
    *) echo "usage: $0 [--connections a:b,c:d] [--brief-chat <chat id>]" >&2; exit 2 ;;
  esac
done

# The Bot: Claude Sonnet 5.5, GPT-6.1 Sol when it fails (owner, 2026-11-21; never opencode-go/*, since it reads
# clients' and VMAP's internal messages). It only reads the owner's own accounts (--owner-accounts read; owner,
# 2026-11-21 on #1037): every write and send through his Lark sign-in, his Google, his Zalo and his WhatsApp is
# refused at once in every chat and Routine of the Bot, whatever his approvals say. Its tools: its memory, the
# Inbox, the meetings and the reads of Zalo and WhatsApp; no send tools, no Routines of its own, no web, no
# artifacts, and no workers. In a Routine's read run every write outside the control plane is refused (#956).
hypomnema bots set "$bot" \
  --provider claude-subscription --model claude-sonnet-5-5 --fallbacks codex-subscription/gpt-6.1-sol \
  --persona-file "$here/persona.md" \
  --preset report --tools inbox,meetings,zalo,whatsapp \
  --tools-off zalo_send,whatsapp_send,routines,web,artifacts,superset \
  --connections "$connections" --owner-accounts read --workers ""

# The triage: every 15 minutes, read only. Its check (new-messages.mjs) counts the new messages first and wakes the
# Bot only when some are left after its filters, with a batch of at most 15 conversations (perRun), the rest at the
# next run (issue #1101); its result stays in the run's chat. --timeout is the check's limit only: the Bot's turn has
# the 60 minutes of every prompt run (routinestore.PromptTimeout), which a batch fits. The check of a 7-day first
# look took 96 s on zm (2026-11-23), and the Bot's turn after it 57 s for 23 tool calls.
hypomnema routines set triage --bot "$bot" --access read --every 15 --timeout 300 --quiet \
  --summary "Turn the owner's new messages and meetings into Inbox items, with drafts" \
  --prompt-file "$here/triage.md" --script-file "$here/new-messages.mjs" --script new-messages.mjs

# The brief: 08:00 and 17:00 Hanoi time, told in the owner's Lark chat with the Bot, where he answers it. It reads
# only the Inbox, so it is a full run: a read run's result is told nowhere but its own chat.
if [ -n "$brief_chat" ]; then
  hypomnema routines set brief --bot "$bot" --cron '0 8,17 * * *' --tz Asia/Ho_Chi_Minh \
    --summary "The owner's brief from his Inbox, on Lark" \
    --prompt-file "$here/brief.md" --chat "$brief_chat"
else
  echo "The brief Routine is not set: give --brief-chat once the Bot has a Lark chat with you (README.md, step 3)."
fi

hypomnema routines list --bot "$bot"
