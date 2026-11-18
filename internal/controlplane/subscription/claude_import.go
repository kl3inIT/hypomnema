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

// identityTries is how often who the account is is asked before an import
// gives up: the refresh has already spent the file's refresh token, so a
// passing failure of the profile should not lose the session.
const identityTries = 3

// identityPause is the wait between two of those tries.
const identityPause = time.Second

// ImportCredentials reads Claude Code's credentials file and returns the
// login it holds, as a completed sign-in returns one.
//
// The file's refresh token is always spent first (issue #828, review of PR
// #831): what is kept is then the newest token set, never an older copy of
// one the control plane already refreshed, and a file whose session was
// already used elsewhere is refused instead of stored. The session on the
// machine the file came from is signed out by it. Who the account is must be
// known, from the refresh's answer or the OAuth profile; an import is not
// stored under an id made up from its token, which could duplicate an account.
func (c *Claude) ImportCredentials(ctx context.Context, raw []byte) (Login, error) {
	if len(raw) > maxCredentialsBytes {
		return Login{}, pasteError("the credentials are larger than Claude Code's file")
	}

	var file struct {
		ClaudeAiOauth *struct {
			RefreshToken string `json:"refreshToken"`
		} `json:"claudeAiOauth"`
	}

	if err := json.Unmarshal(raw, &file); err != nil {
		return Login{}, pasteError("the credentials are not JSON; give the whole .credentials.json")
	}

	if file.ClaudeAiOauth == nil {
		return Login{}, pasteError("the credentials have no claudeAiOauth; give Claude Code's .credentials.json")
	}

	refresh := strings.TrimSpace(file.ClaudeAiOauth.RefreshToken)
	if refresh == "" {
		return Login{}, pasteError("the credentials lack a refresh token")
	}

	answer, err := c.refreshTokens(ctx, refresh)
	if err != nil {
		if errors.Is(err, vault.ErrLoginRejected) {
			return Login{}, fmt.Errorf("anthropic refused this session's refresh token, already used or signed out; "+
				"sign in on that machine again and import the new file: %w", err)
		}

		return Login{}, err
	}

	answer.RefreshToken = firstNonEmpty(answer.RefreshToken, refresh)

	who, err := c.identify(ctx, answer)
	if err != nil {
		return Login{}, err
	}

	return Login{
		Identity: who,
		Tokens: vault.Refreshed{
			Payload: vault.Payload{
				vault.KeyAccessToken: answer.AccessToken, vault.KeyRefreshToken: answer.RefreshToken,
			},
			AccessTokenExpiresAt: c.expiry(answer.ExpiresIn),
		},
	}, nil
}

// identify is who the tokens belong to, asked a few times; an id made up from
// the refresh token means it is not known.
func (c *Claude) identify(ctx context.Context, answer claudeTokens) (Identity, error) {
	for try := 1; ; try++ {
		who := c.whoLoggedIn(ctx, answer)
		if !strings.HasPrefix(who.ExternalID, claudeMadeUpID) {
			return who, nil
		}

		if try == identityTries {
			return Identity{}, errors.New("anthropic did not say whose this session is; the file's session was " +
				"used for the refresh, so sign in on that machine again and import the new file")
		}

		select {
		case <-ctx.Done():
			return Identity{}, ctx.Err()
		case <-time.After(identityPause):
		}
	}
}
