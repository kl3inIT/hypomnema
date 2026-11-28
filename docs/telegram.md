# The owner's Telegram: making the api_id and linking it

Hypomnema reads and writes the owner's personal Telegram as one more device of his account, a user client of
Telegram's API, never a bot ([issue #1184](https://github.com/kl3inIT/hypomnema/issues/1184); how it works is in
[ARCHITECTURE.md](../ARCHITECTURE.md), "Telegram"). A user client needs the owner's own `api_id` and `api_hash`, which
only he can make, once, for his phone number. Until they are in the vault the Telegram card on the Integrations page
says the `api_id` is missing and offers no link.

Needs: a control plane of a release with #1184 and a vault key (`HYPOMNEMA_VAULT_KEYS`). Telegram watches accounts
that log in with unofficial clients; the owner accepted that risk on his main account (2026-11-28). Hypomnema never
sends in bulk, sends only what he approved, and never marks a chat as read.

1. **Make the application.** Sign in at [my.telegram.org](https://my.telegram.org) with the phone number of the
   account (Telegram sends the code to the Telegram app), open **API development tools**, and fill in the form: an
   app title and a short name of your choice (such as Hypomnema, `hypomnema`), platform Desktop. The page then shows
   the `api_id` (a number) and the `api_hash` (32 hexadecimal characters). There is one per phone number; if the page
   already shows them, use those. Never share them: they are as private as a password.
2. **Put them in the vault.** On Hypomnema's **Settings › Vault** page, add two secrets: `telegram-api-id` with the
   `api_id` as its value, and `telegram-api-hash` with the `api_hash`. From a terminal on the control plane's server
   instead: `hypomnema secrets add telegram-api-id --from-file -` and `hypomnema secrets add telegram-api-hash
   --from-file -`, typing each value and Ctrl-D. The Integrations page's Telegram card then says **Not linked**.
3. **Link.** On **Settings › Integrations**, press **Link phone** on the Telegram card, keep the name `main`, and
   press **Show the code**. On your phone, open Telegram › Settings › Devices › **Link Desktop Device** and scan the
   code. If your account has a two-step password, the dialog then asks for it (with your hint); type it and press
   **Continue**. If the code cannot be scanned, press **Use phone number** instead: type your number with its
   country code, then the code Telegram sends to your app, then the password if asked. A wrong code or password is
   asked again. The card then says **Linked**, and Telegram's list of devices shows "Hypomnema".
4. **Give it to a Bot.** On the Bot's page, allow the Connection `telegram:main`. On your turn in its chat the Bot
   has `telegram_chats`, `telegram_messages`, `telegram_media`, `telegram_history` and `telegram_send`; each send
   waits for your approval (the action `telegram.send`). Which Telegram chats the secretary reads is on the Inbox's
   chats page (gear icon), tab Telegram: direct chats and groups on, bots and channels off by default.

To stop: **Disconnect** the account on the Integrations page, which ends the session at Telegram (it leaves the list
of devices) and forgets what was kept. Changing the `api_id` later: replace both secrets and link again.
