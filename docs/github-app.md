# The GitHub App: registering and installing it

Hypomnema's workers and coordinator use a GitHub App of the owner's for the repositories it is installed on, so
their calls count against the App's own rate limit instead of the owner's token, and GitHub tells Hypomnema when CI
ends instead of being polled ([issue #1262](https://github.com/kl3inIT/hypomnema/issues/1262); how it works is in
[ARCHITECTURE.md](../ARCHITECTURE.md), "The owner's GitHub App"). Until these steps are done nothing changes: work
goes on with the owner's token.

Needs: a control plane of a release with #1262, reachable over HTTPS at `HYPOMNEMA_PUBLIC_URL` (GitHub sends the
App's events there), and a vault key (`HYPOMNEMA_VAULT_KEYS`). Do the steps in one browser signed in to both
Hypomnema and GitHub as the owner.

1. **Register.** Open `https://<control plane>/api/v1/integrations/github/app/setup` and press **Create GitHub
   App**. GitHub shows the App it will make (private, on your account, with the permissions and events Hypomnema
   uses); give it a name and confirm. GitHub sends you back, and the page says the App is registered. Its private
   key and webhook secret went straight into the vault (secret `github-app`) and are never shown. Finish within an
   hour of opening the page, and without restarting the control plane in between; otherwise open it again.
2. **Install.** On the same page, follow **Install it on repositories** and choose the repositories Runs use
   (hypomnema, BeyondPilot, MemoryOS). GitHub sends you back and the page lists them. `hypomnema hosts app` shows
   the same; `hypomnema hosts app sync` reads them again after you change them on GitHub.
3. **Test a delivery.** Check that the page's *Last delivery* shows one (GitHub sends the App an `installation`
   event when you install it). Then press **Send a test delivery** (or `hypomnema hosts app test`): GitHub sends
   that latest delivery again, with the same id, so Hypomnema answers it 200 as handled before and *Last
   delivery* stays as it was. GitHub's list, under the App's settings, **Advanced**, **Recent Deliveries**, shows the
   redelivery with that 200 answer.

After this, a worker on one of those repositories gets the App's token and waits for its pull request's CI with
`hypomnema ci wait <url>`; the coordinator can use the same command. Offers made before the App was installed keep
the owner's token. Each token reaches its Task's repository alone. To stop using the App, uninstall it from the
repositories on GitHub when no Run is working on them: new work goes back to the owner's token, while a worker that
started as the App's would get no token of it and run gh with its machine's own login.
