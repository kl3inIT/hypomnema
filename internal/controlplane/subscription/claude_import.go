package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// Importing the session Claude Code keeps in `.credentials.json` (issue
// #828), so an account signed in on a machine moves to the control plane
// without a new browser sign-in. OmniRoute imports the same file
// (ImportClaudeAuthModal.tsx, src/lib/oauth/utils/claudeAuthFile.ts) and Hermes
// reads it (agent/anthropic_credentials.py, _claude_oauth_record): both take
// `claudeAiOauth`'s access and refresh tokens, and the e-mail from a call, not
// from the file.

// maxCredentialsBytes bounds an imported credentials file; Claude Code's is
// well under a kilobyte.
const maxCredentialsBytes = 64 << 10

// importRefreshLead is how close to its end an imported access token is
// replaced before it is used to read who the account is.
const importRefreshLead = 5 * time.Minute

// ImportCredentials reads Claude Code's credentials file and returns the
// login it holds, as a completed sign-in returns one. The control plane
// refreshes the tokens from then on, so the session on the machine the file
// came from may be signed out.
func (c *Claude) ImportCredentials(ctx context.Context, raw []byte) (Login, error) {
	if len(raw) > maxCredentialsBytes {
		return Login{}, pasteError("the credentials are larger than Claude Code's file")
	}

	var file struct {
		ClaudeAiOauth *struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			// ExpiresAt is in milliseconds since the epoch.
			ExpiresAt int64 `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}

	if err := json.Unmarshal(raw, &file); err != nil {
		return Login{}, pasteError("the credentials are not JSON; give the whole .credentials.json")
	}

	oauth := file.ClaudeAiOauth
	if oauth == nil {
		return Login{}, pasteError("the credentials have no claudeAiOauth; give Claude Code's .credentials.json")
	}

	tokens := claudeTokens{
		AccessToken: strings.TrimSpace(oauth.AccessToken), RefreshToken: strings.TrimSpace(oauth.RefreshToken),
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		return Login{}, pasteError("the credentials lack an access or a refresh token")
	}

	refreshed := vault.Refreshed{
		Payload: vault.Payload{vault.KeyAccessToken: tokens.AccessToken, vault.KeyRefreshToken: tokens.RefreshToken},
	}
	if oauth.ExpiresAt > 0 {
		refreshed.AccessTokenExpiresAt = time.UnixMilli(oauth.ExpiresAt)
	}

	// An access token about to end cannot read the profile: refresh it first,
	// as a sign-in's tokens are fresh.
	if refreshed.AccessTokenExpiresAt.IsZero() ||
		refreshed.AccessTokenExpiresAt.Before(c.clock.Now().Add(importRefreshLead)) {
		next, err := c.Refresh(ctx, refreshed.Payload)
		if err != nil {
			if errors.Is(err, vault.ErrLoginRejected) {
				return Login{}, fmt.Errorf("the refresh token was refused by Anthropic; sign in on that machine again: %w", err)
			}

			return Login{}, err
		}

		refreshed = next
		tokens.AccessToken = next.Payload[vault.KeyAccessToken]
		tokens.RefreshToken = next.Payload[vault.KeyRefreshToken]
	}

	return Login{Identity: c.whoLoggedIn(ctx, tokens), Tokens: refreshed}, nil
}
