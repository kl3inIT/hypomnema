package muse

// The Muse pool (issue #782): the owner's Muse accounts as gateway Accounts
// of runtime muse (orchestration.MuseRuntime), each with its muse.ai cookie
// jar in the vault, asked one question at a time in a browser context of its
// own. Its rules are Muse's own, at the owner's request (2026-11-18), not the
// gateway's subscription picker: a thread stays with its account for good; a
// new thread takes the first account in the owner's order that is signed in
// and not resting; an account out of quota rests until a Usage read shows
// room, read at most every six hours (rows 4, 8, 9).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

const (
	// Domain is the cookies' domain.
	Domain = "muse.ai"
	// usageEvery is how often a resting account's Usage is read again (row 9).
	usageEvery = 6 * time.Hour
	// maxPromptChars bounds a prompt.
	maxPromptChars = 32000
	// signInKey names no account: the context a sign-in is not made in.
	signInURL = Site + "/"
)

// requiredCookies make a signed-in session; optionalCookies are kept when
// there (www's upstream/muse.py:27-31).
// The optional ones are kept as every other muse.ai cookie is (museCookies).
var requiredCookies = []string{"hatch_sess", "hatch_native_auth_device"}

// Errors of the pool, said to the caller as they are.
var (
	ErrInvalid     = errors.New("invalid")
	ErrNoAccount   = errors.New("no Muse account can take the question")
	ErrUnavailable = errors.New("muse is not available")
)

// Accounts are the gateway's Accounts (orchestration).
type Accounts interface {
	ListAccounts(ctx context.Context) ([]orchestration.Account, error)
	AddTokenAccount(ctx context.Context, runtime, label string, secretRef func(string) string) (
		orchestration.Account, error)
	RemoveAccountLogin(ctx context.Context, id string) error
	MoveAccount(ctx context.Context, id string, up bool) error
	DisableAccount(ctx context.Context, id string) error
	EnableAccount(ctx context.Context, id string) error
}

// Secrets is the vault.
type Secrets interface {
	Create(ctx context.Context, n vault.NewSecret) (vault.Secret, error)
	Open(ctx context.Context, name string) (vault.Opened, error)
	Rotate(ctx context.Context, name string, p vault.Payload) (vault.Secret, error)
	RotateAt(ctx context.Context, name string, p vault.Payload, version int) (vault.Secret, error)
	Delete(ctx context.Context, name string) error
}

// Browser is the shared browser's engine.
type Browser interface {
	Page(ctx context.Context, key string, cookies []browser.Cookie) (Page, error)
	DisposeContext(ctx context.Context, key string)
	OpenOwnerTab(ctx context.Context, address string) error
	// EvaluateOwnerTab runs a script in the owner's tab; ErrHeld while he holds his own tab.
	EvaluateOwnerTab(ctx context.Context, expression string, result any) error
	ProfileCookies(ctx context.Context, domain string) ([]browser.Cookie, error)
	ClearProfileCookies(ctx context.Context, cookies []browser.Cookie) error
}

// Config is how the pool is set up.
type Config struct {
	Accounts Accounts
	Secrets  Secrets
	Browser  Browser
	// SecretName names the vault secret of an account's cookie jar.
	SecretName func(accountID string) string
	Logger     *slog.Logger
	Clock      platform.Clock
}

// Service is the pool.
type Service struct {
	cfg   Config
	store *store
	media *media
	close func() error
	t     timings
	// turn is the one Muse tab at a time: a question, a sign-in capture, a Usage read and a cookie save each
	// hold it (row 4), so that two accounts' questions never run at once on the shared Chrome either.
	turn chan struct{}
}

// Open opens muse.db at path, with muse-media beside it.
func Open(ctx context.Context, path string, cfg Config) (*Service, error) {
	db, err := sqlite.Open(ctx, sqlite.Config{Path: path})
	if err != nil {
		return nil, fmt.Errorf("muse: %w", err)
	}

	st, err := openStore(ctx, db)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}

	dir := filepath.Join(filepath.Dir(path), "muse-media")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.Join(err, db.Close())
	}

	s := newService(cfg, st, &media{dir: dir, http: newHTTP(), store: st})
	s.close = db.Close

	return s, nil
}

func newService(cfg Config, st *store, m *media) *Service {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	if cfg.Clock == nil {
		cfg.Clock = platform.SystemClock{}
	}

	return &Service{cfg: cfg, store: st, media: m, t: defaultTimings, turn: make(chan struct{}, 1)}
}

// Close closes muse.db.
func (s *Service) Close() error {
	if s.close == nil {
		return nil
	}

	return s.close()
}

// take waits for the Muse tab's turn.
func (s *Service) take(ctx context.Context) (func(), error) {
	select {
	case s.turn <- struct{}{}:
		return func() { <-s.turn }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for Muse's turn: %w", ctx.Err())
	}
}

// Account is a Muse account as the Muse page shows it.
type Account struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Position int    `json:"position"`
	On       bool   `json:"on"`
	// State and Cause are what the pool last saw (row 7); Resting says it is out of quota.
	State   string `json:"state"`
	Cause   string `json:"cause,omitempty"`
	Resting bool   `json:"resting"`
	// Usage is Settings → Usage's text, or "unknown".
	Usage       string    `json:"usage"`
	UsageReadAt time.Time `json:"usage_read_at,omitzero"`
	CheckedAt   time.Time `json:"checked_at,omitzero"`
}

// accounts are the Muse accounts in the owner's order.
func (s *Service) accounts(ctx context.Context) ([]orchestration.Account, error) {
	all, err := s.cfg.Accounts.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}

	var out []orchestration.Account

	for _, a := range all {
		if a.Runtime == orchestration.MuseRuntime {
			out = append(out, a)
		}
	}

	slices.SortStableFunc(out, func(a, b orchestration.Account) int { return a.Position - b.Position })

	return out, nil
}

// Accounts lists the Muse accounts with their states.
func (s *Service) Accounts(ctx context.Context) ([]Account, error) {
	accounts, err := s.accounts(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]Account, 0, len(accounts))

	for _, a := range accounts {
		st, err := s.store.state(ctx, a.ID)
		if err != nil {
			return nil, err
		}

		view := Account{
			ID: a.ID, Label: a.Label, Position: a.Position, On: a.Status == orchestration.AccountActive,
			State: st.State, Cause: st.Cause, Resting: st.Resting, Usage: "unknown",
			UsageReadAt: st.UsageReadAt, CheckedAt: st.CheckedAt,
		}
		if view.State == StateNew {
			view.State = StateSignedIn
		}

		if st.Usage != nil {
			view.Usage = st.Usage.Text()
		}

		out = append(out, view)
	}

	return out, nil
}

// ToolsOn says whether agents get the Muse tools.
func (s *Service) ToolsOn(ctx context.Context) (bool, error) { return s.store.toolsOn(ctx) }

// SetToolsOn switches the Muse tools on or off.
func (s *Service) SetToolsOn(ctx context.Context, on bool) error { return s.store.setToolsOn(ctx, on) }

// Move moves an account one place up or down the owner's order.
func (s *Service) Move(ctx context.Context, id string, up bool) error {
	if _, err := s.account(ctx, id); err != nil {
		return err
	}

	return s.cfg.Accounts.MoveAccount(ctx, id, up)
}

// SetOn puts an account in use or out of it.
func (s *Service) SetOn(ctx context.Context, id string, on bool) error {
	if _, err := s.account(ctx, id); err != nil {
		return err
	}

	if on {
		return s.cfg.Accounts.EnableAccount(ctx, id)
	}

	return s.cfg.Accounts.DisableAccount(ctx, id)
}

// Remove removes an account, its cookie jar and its context. Its questions
// stay in muse.db; its threads can no longer be continued.
func (s *Service) Remove(ctx context.Context, id string) error {
	if _, err := s.account(ctx, id); err != nil {
		return err
	}

	release, err := s.take(ctx)
	if err != nil {
		return err
	}
	defer release()

	s.cfg.Browser.DisposeContext(ctx, id)

	if err := s.cfg.Accounts.RemoveAccountLogin(ctx, id); err != nil {
		return err
	}

	if err := s.cfg.Secrets.Delete(ctx, s.cfg.SecretName(id)); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return err
	}

	return s.store.forget(ctx, id)
}

func (s *Service) account(ctx context.Context, id string) (orchestration.Account, error) {
	accounts, err := s.accounts(ctx)
	if err != nil {
		return orchestration.Account{}, err
	}

	for _, a := range accounts {
		if a.ID == id {
			return a, nil
		}
	}

	return orchestration.Account{}, fmt.Errorf("%w: no Muse account %s", orchestration.ErrNotFound, id)
}

// jar is an account's cookie jar as the vault keeps it, with its version.
type jar struct {
	cookies []browser.Cookie
	version int
}

func (s *Service) openJar(ctx context.Context, id string) (jar, error) {
	opened, err := s.cfg.Secrets.Open(ctx, s.cfg.SecretName(id))
	if err != nil {
		return jar{}, fmt.Errorf("opening the cookies of Muse account %s: %w", id, err)
	}

	var cookies []browser.Cookie
	if err := json.Unmarshal([]byte(opened.Payload[vault.KeyValue]), &cookies); err != nil {
		return jar{}, fmt.Errorf("the cookies of Muse account %s do not decode", id)
	}

	return jar{cookies: cookies, version: opened.Version}, nil
}

// saveJar saves the context's cookies back when they changed, only over the
// version they were read at (row 3): a newer sign-in is never overwritten.
func (s *Service) saveJar(ctx context.Context, id string, read jar, page Page) {
	got, err := page.Cookies(ctx)
	if err != nil {
		s.cfg.Logger.WarnContext(ctx, "a Muse account's cookies could not be read back",
			slog.String("event", "muse.cookies_unread"), slog.String("account", id), slog.Any("err", err))

		return
	}

	kept := museCookies(got)
	if missingCookies(kept) != "" || sameCookies(kept, read.cookies) {
		return
	}

	raw, err := json.Marshal(kept)
	if err != nil {
		return
	}

	_, err = s.cfg.Secrets.RotateAt(ctx, s.cfg.SecretName(id), vault.Payload{vault.KeyValue: string(raw)}, read.version)

	switch {
	case errors.Is(err, vault.ErrConflict):
		s.cfg.Logger.InfoContext(ctx, "a Muse account was signed in again meanwhile; its older cookies are dropped",
			slog.String("event", "muse.cookies_superseded"), slog.String("account", id))
	case err != nil:
		s.cfg.Logger.WarnContext(ctx, "a Muse account's cookies could not be saved",
			slog.String("event", "muse.cookies_unsaved"), slog.String("account", id), slog.Any("err", err))
	}
}

// museCookies are the cookies of muse.ai among cookies.
func museCookies(cookies []browser.Cookie) []browser.Cookie {
	var out []browser.Cookie

	for _, c := range cookies {
		d := strings.TrimPrefix(c.Domain, ".")
		if d == Domain || strings.HasSuffix(d, "."+Domain) {
			out = append(out, c)
		}
	}

	return out
}

// missingCookies names the required cookies a jar lacks, empty when none.
func missingCookies(cookies []browser.Cookie) string {
	var missing []string

	for _, name := range requiredCookies {
		if !slices.ContainsFunc(cookies, func(c browser.Cookie) bool { return c.Name == name && c.Value != "" }) {
			missing = append(missing, name)
		}
	}

	return strings.Join(missing, ", ")
}

func sameCookies(a, b []browser.Cookie) bool {
	key := func(cs []browser.Cookie) []string {
		out := make([]string, 0, len(cs))
		for _, c := range cs {
			out = append(out, c.Domain+"\x00"+c.Path+"\x00"+c.Name+"\x00"+c.Value)
		}

		slices.Sort(out)

		return out
	}

	return slices.Equal(key(a), key(b))
}

// stateOf is the account's state an error shows, and whether it is one.
func stateOf(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrNeedsSignIn):
		return StateNeedsSignIn, true
	case errors.Is(err, ErrRefused):
		return StateRefused, true
	case errors.Is(err, ErrWorkspace):
		return StateWorkspace, true
	case errors.Is(err, ErrPageNotReady):
		return StatePageNotReady, true
	}

	return "", false
}
