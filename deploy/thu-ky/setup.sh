#!/bin/sh
# Sets up the secretary Routines of a Bot, the main Bot by default (issue #1023, part 4 of #838; issue #1107: the
# owner talks to one Bot, which keeps its own persona). It makes and changes no Bot. The owner runs it, with
# `hypomnema` logged in as himself (`hypomnema whoami` says owner); it changes only the control plane it is logged in
# to, and is safe to run again: each command sets what it names and leaves the rest. See README.md beside it.
#
#   deploy/thu-ky/setup.sh [--bot <name or id>] [--brief-chat <chat id>]
#
# --bot           the Bot whose Routines these are (default the main Bot). It needs the inbox, meetings, zalo and
#                 whatsapp tools and the owner's accounts it reads as Connections (README.md, step 1).
# --brief-chat    the Hypomnema chat of the owner's Lark chat with that Bot, where the brief is told and where he
#                 answers it; without it the brief Routine is left for later (README.md, step 3).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
bot=""
brief_chat=""

while [ $# -gt 0 ]; do
  case "$1" in
    --bot) bot=$2; shift 2 ;;
    --brief-chat) brief_chat=$2; shift 2 ;;
    *) echo "usage: $0 [--bot <name or id>] [--brief-chat <chat id>]" >&2; exit 2 ;;
  esac
done

# routine sets one Routine of the Bot; without --bot, `routines set` takes the main Bot.
routine() {
  if [ -n "$bot" ]; then
    hypomnema routines set "$@" --bot "$bot"
  else
    hypomnema routines set "$@"
  fi
}

# Which chats the triage reads is the owner's choice per chat, kept by the control plane (issue #1270). Before the
# check that reads those choices is set, the chat ids the Bot's thu-ky/filters.json named in dropChats become his
# choices, switched off: run again, it changes nothing, and a chat he switched on since stays on. It fails, and
# setup stops here, until every such id is stored: while one of his accounts cannot be listed (its session not open),
# for an id none of his accounts has (take it out of dropChats, or switch the chat off once it is listed), and on a
# control plane released before #1270. So the new check, which reads dropChats no more, never reads a chat it left
# out.
if [ -n "$bot" ]; then
  hypomnema inbox chats move --bot "$bot"
else
  hypomnema inbox chats move
fi

# The triage: every 15 minutes, read only, so every send and every write outside the control plane is refused at
# once in its runs and its check, whatever the Bot may do in its chats (#956). Its prompt carries the secretary's
# rules, since the Bot keeps its own persona. Its check (new-messages.mjs) counts the new messages first and wakes the
# Bot only when some are left after its filters, with a batch of at most 15 conversations (perRun), the rest at the
# next run (issue #1101); its result stays in the run's chat. --timeout is the check's limit only: the Bot's turn has
# the 60 minutes of every prompt run (routinestore.PromptTimeout), which a batch fits. The check of a 7-day first
# look took 96 s on zm (2026-11-23), and the Bot's turn after it 57 s for 23 tool calls.
routine triage --access read --every 15 --timeout 300 --quiet \
  --summary "Turn the owner's new messages and meetings into Inbox items, with drafts" \
  --prompt-file "$here/triage.md" --script-file "$here/new-messages.mjs" --script new-messages.mjs

# The brief: 08:00 and 17:00 Hanoi time, told in the owner's Lark chat with the Bot, where he answers it. It reads
# only the Inbox, so it is a full run: a read run's result is told nowhere but its own chat.
if [ -n "$brief_chat" ]; then
  routine brief --cron '0 8,17 * * *' --tz Asia/Ho_Chi_Minh \
    --summary "The owner's brief from his Inbox, on Lark" \
    --prompt-file "$here/brief.md" --chat "$brief_chat"
else
  echo "The brief Routine is not set: give --brief-chat, the owner's Lark chat with the Bot (README.md, step 3)."
fi

if [ -n "$bot" ]; then
  hypomnema routines list --bot "$bot"
else
  hypomnema routines list
fi
