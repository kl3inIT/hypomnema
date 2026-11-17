package muse

// Sign-in (issue #782, row 6): the owner signs in to muse.ai in his own tab
// of the shared browser, which he watches and controls; once muse.ai's
// POST /api/auth/check answers ok with a viewer_id for the shared profile's
// muse.ai cookies, they are copied into the account's vault secret (its
// context is made from it at its next use) and cleared from the shared
// profile. Meta's own cookies stay there. The check is Muse-Chat-MCP's
// login() (muse-driver.mjs:162-224), made here by the control plane with the
// cookies rather than from the page: the capture is the owner's own path and
// runs while he holds the browser, as his input does (browser/view.go,
// human), which a page step of the hold would not.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// maxLabelChars bounds an account's label.
const maxLabelChars = 80

// SignInStart opens muse.ai in the owner's tab.
func (s *Service) SignInStart(ctx context.Context) error {
	return s.cfg.Browser.OpenOwnerTab(ctx, signInURL)
}

// SignIn is how a sign-in stands.
type SignIn struct {
	// Done says the account was added or signed in again.
	Done    bool     `json:"done"`
	Account *Account `json:"account,omitempty"`
	// Waiting says what is waited for when not done.
	Waiting string `json:"waiting,omitempty"`
}

// SignInCheck looks for a finished sign-in in the owner's tab. With id it
// signs that account in again; else it adds an account named label.
func (s *Service) SignInCheck(ctx context.Context, id, label string) (SignIn, error) {
	label = strings.TrimSpace(label)
	if len([]rune(label)) > maxLabelChars {
		return SignIn{}, fmt.Errorf("%w: a label is at most %d characters", ErrInvalid, maxLabelChars)
	}

	var again orchestration.Account

	if id != "" {
		a, err := s.account(ctx, id)
		if err != nil {
			return SignIn{}, err
		}

		again = a
	}

	profile, err := s.cfg.Browser.ProfileCookies(ctx, Domain)
	if err != nil {
		return SignIn{}, err
	}

	cookies := museCookies(profile)
	if missing := missingCookies(cookies); missing != "" {
		return SignIn{Waiting: "Sign in to Muse in the browser; waiting for its session."}, nil
	}

	viewer, err := s.cfg.Check(ctx, cookies)

	switch {
	case errors.Is(err, ErrNeedsSignIn):
		return SignIn{Waiting: "Muse does not take the session yet; finish signing in."}, nil
	case errors.Is(err, ErrRefused):
		return SignIn{Waiting: "muse.ai refused this session (403: region, network or access)."}, nil
	case err != nil:
		return SignIn{}, err
	}

	release, err := s.take(ctx)
	if err != nil {
		return SignIn{}, err
	}
	defer release()

	raw, err := json.Marshal(cookies)
	if err != nil {
		return SignIn{}, err
	}

	if again.ID != "" {
		// A sign-in is the newest jar: it replaces whatever version is there, and the context made from the
		// old one is closed.
		_, err := s.cfg.Secrets.Rotate(ctx, s.cfg.SecretName(again.ID), vault.Payload{vault.KeyValue: string(raw)})
		if err != nil {
			return SignIn{}, err
		}

		s.cfg.Browser.DisposeContext(ctx, again.ID)
	} else {
		if label == "" {
			label = "Muse " + shortViewer(viewer)
		}

		if again, err = s.add(ctx, label, string(raw)); err != nil {
			return SignIn{}, err
		}
	}

	if err := s.store.setState(ctx, again.ID, StateSignedIn, "", viewer, s.cfg.Clock.Now()); err != nil {
		return SignIn{}, err
	}

	_ = s.store.rest(ctx, again.ID, false, "")

	// muse.ai's cookies leave the shared profile: no agent's tab carries the session.
	if err := s.cfg.Browser.ClearProfileCookies(ctx, cookies); err != nil {
		s.cfg.Logger.WarnContext(ctx, "muse.ai's cookies could not be cleared from the shared browser",
			slog.String("event", "muse.profile_uncleared"), slog.Any("err", err))
	}

	s.cfg.Logger.InfoContext(ctx, "a Muse account was signed in", slog.String("event", "muse.signed_in"),
		slog.String("account", again.ID))

	views, err := s.Accounts(ctx)
	if err != nil {
		return SignIn{}, err
	}

	for i := range views {
		if views[i].ID == again.ID {
			return SignIn{Done: true, Account: &views[i]}, nil
		}
	}

	return SignIn{Done: true}, nil
}

// add makes an account and seals its cookie jar.
func (s *Service) add(ctx context.Context, label, raw string) (orchestration.Account, error) {
	a, err := s.cfg.Accounts.AddTokenAccount(ctx, orchestration.MuseRuntime, label, func(id string) string {
		return vault.Ref(s.cfg.SecretName(id))
	})
	if err != nil {
		return orchestration.Account{}, err
	}

	_, err = s.cfg.Secrets.Create(ctx, vault.NewSecret{
		Name: s.cfg.SecretName(a.ID), Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: raw},
		Metadata: map[string]string{"account": a.ID, "runtime": a.Runtime},
	})
	if err != nil {
		// An account whose jar is not there is taken back.
		if removeErr := s.cfg.Accounts.RemoveAccountLogin(ctx, a.ID); removeErr != nil {
			return orchestration.Account{}, errors.Join(err, removeErr)
		}

		return orchestration.Account{}, fmt.Errorf("keeping the cookies of Muse account %s: %w", a.ID, err)
	}

	return a, nil
}

func shortViewer(viewer string) string {
	if len(viewer) > 6 {
		return viewer[len(viewer)-6:]
	}

	if viewer == "" {
		return "account"
	}

	return viewer
}

// checkSignIn asks site's POST /api/auth/check with the cookies.
func checkSignIn(ctx context.Context, client *http.Client, site string, cookies []browser.Cookie) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, site+"/api/auth/check", strings.NewReader("{}"))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")

	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value}) //nolint:gosec // A request header, not a cookie set.
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: asking muse.ai: %w", ErrUnavailable, err)
	}
	defer resp.Body.Close() //nolint:errcheck // Read or failed.

	var body struct {
		OK     bool `json:"ok"`
		Viewer any  `json:"viewer_id"`
	}

	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)

	switch {
	case resp.StatusCode == http.StatusForbidden:
		return "", ErrRefused
	case resp.StatusCode == http.StatusUnauthorized || (resp.StatusCode == http.StatusOK && !body.OK):
		return "", ErrNeedsSignIn
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%w: muse.ai answered %d", ErrUnavailable, resp.StatusCode)
	case body.Viewer == nil || fmt.Sprint(body.Viewer) == "":
		return "", ErrNeedsSignIn
	}

	return fmt.Sprint(body.Viewer), nil
}
