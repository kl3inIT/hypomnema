package githubapp

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// OfferIdentity gives the node API the bot that commits with the App's token, and the App owner's fallback identity.
// A known bot means the App covers this repository and a token could be made. The offer carries no token itself.
func (a *App) OfferIdentity(ctx context.Context, host, path string) (bot, owner githost.Identity) {
	token, cred, ok, err := a.tokenFor(ctx, host, path)
	if ok && cred.BotUserID <= 0 {
		cred, err = a.backfillBotUser(ctx, cred.AppID, token.Value)
	}

	if err != nil {
		a.logger.WarnContext(ctx, "an offer goes with the owner's token: the GitHub App credential is unavailable",
			slog.String("event", "githubapp.offer_failed"), slog.String("repository", path),
			slog.String("error", err.Error()))

		return bot, owner
	}

	if ok {
		bot = gitHubIdentity(cred.Slug+"[bot]", cred.BotUserID)
		if validSlug(strings.ToLower(cred.Owner)) {
			owner = gitHubIdentity(cred.Owner, cred.OwnerUserID)
		}
	}

	return bot, owner
}

// backfillBotUser updates credentials registered before the bot user ID was kept, or before it was available.
// Recheck under the lock so concurrent offers share the lookup. RotateAt protects a concurrent registration.
func (a *App) backfillBotUser(ctx context.Context, appID int64, token string) (Credential, error) {
	a.botLookup.Lock()
	defer a.botLookup.Unlock()

	cred, version, ok, err := a.credentialVersion(ctx)
	if err != nil {
		return Credential{}, err
	}

	if !ok || cred.AppID != appID {
		return Credential{}, errors.New("the GitHub App changed during the offer")
	}

	if cred.BotUserID > 0 {
		return cred, nil
	}

	lookup, cancel := context.WithTimeout(ctx, gitHubRead)
	defer cancel()

	cred.BotUserID, err = a.client.userID(lookup, cred.Slug+"[bot]", token)
	if err != nil {
		return Credential{}, errors.New(strings.ReplaceAll(err.Error(), token, "[token]"))
	}

	// Older credentials stored only the owner's login. Look up its ID for the fallback co-author address.
	if cred.OwnerUserID == 0 && validSlug(strings.ToLower(cred.Owner)) {
		cred.OwnerUserID, _ = a.client.userID(lookup, cred.Owner, token)
	}

	value, err := cred.Encode()
	if err != nil {
		return Credential{}, err
	}

	if _, err := a.vault.RotateAt(ctx, AppSecret, vault.Payload{vault.KeyValue: value}, version); err != nil {
		return Credential{}, vaultError(err)
	}

	return cred, nil
}

// gitHubIdentity uses the noreply form from claude-code-action's git-config.ts, with the login-only form from
// create-prompt/index.ts when only the owner's login is known.
func gitHubIdentity(login string, id int64) githost.Identity {
	email := login + "@users.noreply.github.com"
	if id > 0 {
		email = strconv.FormatInt(id, 10) + "+" + email
	}

	return githost.Identity{Name: login, Email: email}
}
