package listening

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/listening/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/signin"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// Signed-in sources (issue #1291): a source read as one of the owner's accounts, X and LinkedIn first, through the
// shared sign-in Muse uses (internal/controlplane/signin). The steps are the issue's, each learned from the references
// it names (docs/research/2026-11-27-social-listening.md §5b):
//
//   - consent: a card with fixed wording, the source's own, agreed to before its first sign-in; until then the source
//     is off and cannot be switched on (last30days references/reddit-session.md:5-18, "It needs its own yes. X consent
//     never implies it", and its wording, :13: automated use "may breach Reddit's terms and put that account at
//     risk"; setup-wizard.md:57-61 and 132: consent is asked before any cookie is read);
//   - sign-in: the owner signs in himself in his tab of the shared browser, and once the site's login cookies are
//     there, they are verified by a real search and only then kept in the vault, the account read in a browser
//     context of its own (the Muse pool's; signin's package comment);
//   - else a pasted cookie header, checked for the same login cookies and verified the same way (last30days-skill-cn
//     crawler_bridge.py:378-410);
//   - one account a source, the one the owner signed in, never switched for another (Agent-Reach sessions.md:20,
//     "choose one, do not switch at random"); a sign-in again replaces it;
//   - expired: a real call that asks for a sign-in marks the account need_login, the card shows "!" and the owner is
//     told once (postiz's refreshNeeded, set where it was not, integration.repository.ts:603-616), and the source's
//     next backend serves meanwhile, the owner's rule in issue #1291 ("the next free backend meanwhile"; Agent-Reach
//     itself does not fall back past a login wall, runtime/dispatch.py:288-299); only a real call's answer does,
//     never a missing cookie (Agent-Reach sessions.md:18, 26);
//   - refused: a challenge or a block marks it refused and stops it until the owner signs in again, never retried by a
//     test and never answered by another account (radar collection-pacing-contract.md:12, "Waiting longer is not
//     permission to retry a safety stop"; research note §3, "a login or block stops that account until a person
//     acts"); a throttling page stops it for the source's Throttle, kept across restarts (linkedin-mcp-server
//     AGENTS.md:209-223);
//   - sign-out clears the account's cookies from the vault, its context and the shared profile, that site's only
//     (Agent-Reach sessions.md:39-41).

// The states of a source's account, as listening_accounts keeps them.
const (
	AccountSignedIn  = "signed_in"
	AccountNeedLogin = "need_login"
	AccountRefused   = "refused"
)

const (
	// maxAccountLabel bounds an account's label, as a Muse account's (muse.maxLabelChars).
	maxAccountLabel = 80
	// maxCause bounds what is kept of a failure, as muse.db keeps a cause (muse/store.go cut 500).
	maxCause = 500
)

// Signed makes a source a signed-in one.
type Signed struct {
	Site signin.Site
	// Consent is the card's fixed wording, agreed to before the first sign-in.
	Consent string
	// Backend is the backend that reads as the account; the source's others read without one.
	Backend string
	// Throttle is how long a throttling page (a rate limit) stops the account, kept across restarts; zero: the
	// backend rests in memory as any other does.
	Throttle time.Duration
}

// Account is a signed-in source's account as the page shows it; its cookies are never part of it.
type Account struct {
	Label      string
	State      string
	Cause      string
	SignedInAt time.Time
	CheckedAt  time.Time
}

// SignedState is what a signed-in source's card shows of it.
type SignedState struct {
	Consent     string
	ConsentedAt time.Time
	Account     *Account
	// StoppedUntil is when the site may be asked again after a throttling page, StopCause what it met; zero when it
	// is not stopped. It holds for a sign-in too.
	StoppedUntil time.Time
	StopCause    string
	// SignInURL is where the owner signs in; empty when the shared browser is off, and only a pasted cookie header
	// signs in.
	SignInURL string
}

// SignIn is how a sign-in stands.
type SignIn struct {
	Done bool
	// Waiting says what is waited for when not done.
	Waiting string
}

// Page is a tab of the shared browser in a context of the control plane's own (browser.Page).
type Page interface {
	Navigate(ctx context.Context, address string) error
	Evaluate(ctx context.Context, expression string, result any) error
	// Responses are the statuses of the requests the tab sent since its last navigation, the page's own fetches
	// among them (browser.Page.Responses).
	Responses(ctx context.Context) ([]browser.Response, error)
}

// AccountBrowser is the shared browser as the signed-in sources use it: the owner's tab to sign in through, and a
// context of their own for each account (browser/contexts.go).
type AccountBrowser interface {
	signin.Profile
	Page(ctx context.Context, key string, cookies []browser.Cookie) (Page, error)
	DisposeContext(ctx context.Context, key string)
}

// Secrets is the vault, where an account's cookie jar is kept.
type Secrets interface {
	Create(ctx context.Context, n vault.NewSecret) (vault.Secret, error)
	Open(ctx context.Context, name string) (vault.Opened, error)
	Rotate(ctx context.Context, name string, p vault.Payload) (vault.Secret, error)
	Delete(ctx context.Context, name string) error
}

// accounts is what the signed-in sources share in the Service.
type accounts struct {
	browser AccountBrowser
	secrets Secrets
	// secretName names the vault secret of a source's account.
	secretName func(source string) string

	mu sync.Mutex
	// checking is the source whose sign-in is being verified by a real search, which a second check does not start;
	// checked is the last jar each verified and how it ended, so a polling check does not search again for the same
	// cookies.
	checking map[string]bool
	checked  map[string]verification
}

type verification struct {
	jar  string
	fail *SourceError
}

// contextKey names a source's account's browser context; a candidate's verification has its own.
func contextKey(source string, candidate bool) string {
	if candidate {
		return "listening-check:" + source
	}

	return "listening:" + source
}

func (s *Service) signedPlatform(source string) (Platform, error) {
	p := s.platform(source)

	switch {
	case p.Name == "":
		return Platform{}, fmt.Errorf("%w: no listening source %q", ErrNotFound, source)
	case p.Signed == nil:
		return Platform{}, fmt.Errorf("%w: %s is read without an account", ErrInvalid, source)
	case s.db == nil || s.acc.secrets == nil:
		return Platform{}, fmt.Errorf("%w: this control plane keeps no listening accounts (no database or vault)",
			ErrInvalid)
	}

	return p, nil
}

// Consent keeps that the owner agreed to a source's consent card, and switches the source on.
func (s *Service) Consent(ctx context.Context, source string) error {
	if _, err := s.signedPlatform(source); err != nil {
		return err
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		return q.ConsentListeningSource(ctx, store.ConsentListeningSourceParams{Source: source, Now: now})
	})
}

func (s *Service) consented(ctx context.Context, source string) (bool, error) {
	rows, err := store.New(s.db).ListListeningSources(ctx)
	if err != nil {
		return false, fmt.Errorf("reading the listening sources: %w", err)
	}

	for _, r := range rows {
		if r.Source == source {
			return r.ConsentedAt.Valid, nil
		}
	}

	return false, nil
}

func (s *Service) needConsent(ctx context.Context, p Platform) error {
	ok, err := s.consented(ctx, p.Name)
	if err != nil {
		return err
	}

	if !ok {
		return fmt.Errorf("%w: agree to %s's consent card first", ErrInvalid, Label(p.Name))
	}

	return nil
}

// SignInStart opens the source's sign-in page in the owner's tab, once he agreed to its consent card.
func (s *Service) SignInStart(ctx context.Context, source string) error {
	p, err := s.signedPlatform(source)
	if err != nil {
		return err
	}

	if err := s.needConsent(ctx, p); err != nil {
		return err
	}

	if s.acc.browser == nil {
		return fmt.Errorf("%w: the shared browser is off; paste a cookie header instead", ErrInvalid)
	}

	return p.Signed.Site.Open(ctx, s.acc.browser)
}

// SignInCheck looks for a finished sign-in in the owner's tab: once the site's login cookies are in the shared
// profile, they are verified by a real search and kept as the source's account, named label (its site's name when
// empty), and cleared from the shared profile.
func (s *Service) SignInCheck(ctx context.Context, source, label string) (SignIn, error) {
	p, err := s.signedPlatform(source)
	if err != nil {
		return SignIn{}, err
	}

	if err := s.needConsent(ctx, p); err != nil {
		return SignIn{}, err
	}

	if s.acc.browser == nil {
		return SignIn{}, fmt.Errorf("%w: the shared browser is off; paste a cookie header instead", ErrInvalid)
	}

	label, err = accountLabel(label, p)
	if err != nil {
		return SignIn{}, err
	}

	site := p.Signed.Site

	jar, err := site.Capture(ctx, s.acc.browser)
	if err != nil {
		return SignIn{}, err
	}

	if missing := site.Missing(jar); missing != "" {
		return SignIn{Waiting: "Sign in to " + site.Name + " in the browser; waiting for its session."}, nil
	}

	waiting, err := s.signIn(ctx, p, jar, label)
	if err != nil || waiting != "" {
		return SignIn{Waiting: waiting}, err
	}

	// The site's cookies leave the shared profile: no agent's tab carries the session.
	if err := site.Clear(ctx, s.acc.browser); err != nil {
		s.logger.WarnContext(ctx, "a listening account's cookies could not be cleared from the shared browser",
			slog.String("event", "listening.profile_uncleared"), slog.String("source", source),
			slog.String("error", err.Error()))
	}

	return SignIn{Done: true}, nil
}

// PasteCookies keeps a cookie header the owner copied from a browser signed in to the source's site, once it has the
// site's login cookies and a real search takes it. Its error never carries a cookie's value.
func (s *Service) PasteCookies(ctx context.Context, source, header, label string) error {
	p, err := s.signedPlatform(source)
	if err != nil {
		return err
	}

	if err := s.needConsent(ctx, p); err != nil {
		return err
	}

	label, err = accountLabel(label, p)
	if err != nil {
		return err
	}

	jar, err := p.Signed.Site.FromHeader(header)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	waiting, err := s.signIn(ctx, p, jar, label)
	if err != nil {
		return err
	}

	if waiting != "" {
		return fmt.Errorf("%w: %s", ErrInvalid, waiting)
	}

	return nil
}

func accountLabel(label string, p Platform) (string, error) {
	label = strings.TrimSpace(label)
	if len([]rune(label)) > maxAccountLabel {
		return "", fmt.Errorf("%w: a label is at most %d characters", ErrInvalid, maxAccountLabel)
	}

	if label == "" {
		label = p.Signed.Site.Name + " account"
	}

	return label, nil
}

// signIn verifies a jar by a real search and keeps it as the source's account; waiting says why it was not kept.
func (s *Service) signIn(ctx context.Context, p Platform, jar []browser.Cookie, label string) (string, error) {
	raw, err := signin.Encode(jar)
	if err != nil {
		return "", err
	}

	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	name := p.Signed.Site.Name

	s.acc.mu.Lock()
	if s.acc.checking[p.Name] {
		s.acc.mu.Unlock()

		return "Checking the session with a real search…", nil
	}

	last, seen := s.acc.checked[p.Name]
	// A jar a real search found signed out or refused is not searched with again; one that met a timeout is, in its
	// turn at the site, and one that met a rate limit only after the site's stop (Session.Ready). Inferred, no
	// reference keeps such a memo: the sign-in dialog asks every two seconds (the Muse page's), and each ask must not
	// send the site a search.
	if seen && last.jar == sum && last.fail != nil && (last.fail.Code == CodeNeedLogin || last.fail.Code == CodeRefused) {
		s.acc.mu.Unlock()

		return waitingFor(name, last.fail), nil
	}

	s.acc.checking[p.Name] = true
	s.acc.mu.Unlock()

	f := s.verify(ctx, p, jar)

	s.acc.mu.Lock()
	delete(s.acc.checking, p.Name)
	s.acc.checked[p.Name] = verification{jar: sum, fail: f}
	s.acc.mu.Unlock()

	if f != nil {
		return waitingFor(name, f), nil
	}

	if err := s.keep(ctx, p, raw, label); err != nil {
		return "", err
	}

	s.logger.InfoContext(ctx, "a listening account was signed in", slog.String("event", "listening.signed_in"),
		slog.String("source", p.Name))

	return "", nil
}

// waitingFor is what a sign-in waits for after its verification failed.
func waitingFor(site string, f *SourceError) string {
	switch f.Code {
	case CodeNeedLogin:
		return site + " does not take the session yet; finish signing in."
	case CodeRefused:
		return site + " refused this account (" + f.Detail + "). It is not kept: clear what " + site +
			" asks in your own tab, then sign in again."
	case CodeRateLimited:
		at := "later"
		if !f.Until.IsZero() {
			at = "after " + f.Until.UTC().Format("15:04 UTC, Jan 2")
		}

		return site + " asked to slow down and is stopped (" + f.Detail + "): sign in " + at + "."
	default:
		return site + "'s test search did not answer (" + f.Error() + "); try again in a minute."
	}
}

// verify runs the source's signed-in backend once with the jar, the source's live query and no window, as the
// account would be read: nil when it read, items or a clean zero (Agent-Reach sessions.md:29, "redo the original
// action to verify").
func (s *Service) verify(ctx context.Context, p Platform, jar []browser.Cookie) *SourceError {
	var b *Backend

	for i := range p.Backends {
		if p.Backends[i].Name == p.Signed.Backend {
			b = &p.Backends[i]
		}
	}

	if b == nil {
		return Fail(CodeBroken, "%s has no backend %s", p.Name, p.Signed.Backend)
	}

	query := p.LiveQuery
	if query == "" {
		query = testQuery
	}

	req := s.request(Plan{Topic: query}, Caller{})
	req.Query, req.Limit, req.To = query, testLimit, s.now().UTC()
	req.Session = &Session{s: s, source: p.Name, signed: p.Signed, candidate: jar}

	// The site's stop holds for a verification: nothing is sent to it before the stop ends.
	if f := req.Session.Ready(ctx); f != nil {
		return f
	}

	if s.acc.browser != nil {
		defer s.acc.browser.DisposeContext(context.WithoutCancel(ctx), contextKey(p.Name, true))
	}

	_, err := b.Search(ctx, req)
	if err != nil {
		return AsFailure(err)
	}

	return nil
}

// keep seals a verified jar in the vault as the source's account, replacing any before it, and closes the context made
// from the old one.
func (s *Service) keep(ctx context.Context, p Platform, raw, label string) error {
	name := s.acc.secretName(p.Name)

	_, err := s.acc.secrets.Rotate(ctx, name, vault.Payload{vault.KeyValue: raw})
	if errors.Is(err, vault.ErrNotFound) {
		_, err = s.acc.secrets.Create(ctx, vault.NewSecret{
			Name: name, Kind: vault.KindGeneric, Payload: vault.Payload{vault.KeyValue: raw},
			Metadata: map[string]string{"listening_source": p.Name},
		})
	}

	if err != nil {
		return fmt.Errorf("keeping the cookies of %s's account: %w", p.Name, err)
	}

	if s.acc.browser != nil {
		s.acc.browser.DisposeContext(ctx, contextKey(p.Name, false))
	}

	s.clearRests(p)

	// A new session starts with nothing told of it, so its own end is told (postiz clears refreshNeeded when a
	// channel is connected again, integration.repository.ts:288).
	return s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.PutListeningAccount(ctx, store.PutListeningAccountParams{
			Source: p.Name, Label: label, Secret: name, Now: now,
		}); err != nil {
			return err
		}

		return q.ClearListeningTold(ctx, store.ClearListeningToldParams{Source: p.Name, Now: now})
	})
}

// SignOut removes the source's account: its cookies from the vault, its browser context, and the site's cookies from
// the shared profile, no other site's.
func (s *Service) SignOut(ctx context.Context, source string) error {
	p, err := s.signedPlatform(source)
	if err != nil {
		return err
	}

	if err := s.acc.secrets.Delete(ctx, s.acc.secretName(source)); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return fmt.Errorf("removing the cookies of %s's account: %w", source, err)
	}

	if s.acc.browser != nil {
		s.acc.browser.DisposeContext(ctx, contextKey(source, false))

		if err := p.Signed.Site.Clear(ctx, s.acc.browser); err != nil {
			s.logger.WarnContext(ctx, "a listening site's cookies could not be cleared from the shared browser",
				slog.String("event", "listening.profile_uncleared"), slog.String("source", source),
				slog.String("error", err.Error()))
		}
	}

	s.acc.mu.Lock()
	delete(s.acc.checked, source)
	s.acc.mu.Unlock()

	return s.write(ctx, func(q *store.Queries, _ int64) error {
		_, err := q.DeleteListeningAccount(ctx, source)

		return err
	})
}

// account is a source's account as kept; nil when it has none.
func (s *Service) account(ctx context.Context, source string) (*store.ListeningAccount, error) {
	if s.db == nil {
		return nil, nil //nolint:nilnil // No database: no account.
	}

	a, err := store.New(s.db).GetListeningAccount(ctx, source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // No account signed in.
	}

	if err != nil {
		return nil, fmt.Errorf("reading %s's account: %w", source, err)
	}

	return &a, nil
}

// signedStates are the signed-in sources' cards' parts, by source.
func (s *Service) signedStates(ctx context.Context, kept keptState) (map[string]*SignedState, error) {
	out := map[string]*SignedState{}

	var rows []store.ListeningAccount

	if s.db != nil {
		var err error
		if rows, err = store.New(s.db).ListListeningAccounts(ctx); err != nil {
			return nil, fmt.Errorf("reading the listening accounts: %w", err)
		}
	}

	for _, p := range s.platforms {
		if p.Signed == nil {
			continue
		}

		src := kept.sources[p.Name]
		st := &SignedState{Consent: p.Signed.Consent, ConsentedAt: at(src.ConsentedAt)}

		if until := at(src.StoppedUntil); until.After(s.now()) {
			st.StoppedUntil, st.StopCause = until, src.StopCause
		}

		if s.acc.browser != nil {
			st.SignInURL = p.Signed.Site.URL
		}

		for _, r := range rows {
			if r.Source == p.Name {
				st.Account = &Account{
					Label: r.Label, State: r.State, Cause: r.Cause, SignedInAt: time.UnixMilli(r.SignedInAt).UTC(),
					CheckedAt: at(r.CheckedAt),
				}
			}
		}

		out[p.Name] = st
	}

	return out, nil
}

// Session is a signed-in source's account as its backend reads with it: the kept account's, or a candidate jar being
// verified before it is kept, whose answers change nothing kept.
type Session struct {
	s         *Service
	source    string
	signed    *Signed
	candidate []browser.Cookie
}

// session is the account a source's backends read with; nil for a source read without one.
func (s *Service) session(source string) *Session {
	p := s.platform(source)
	if p.Signed == nil {
		return nil
	}

	return &Session{s: s, source: source, signed: p.Signed}
}

// Site is the site the account is signed in to.
func (se *Session) Site() signin.Site { return se.signed.Site }

// Ready says, without a call, why the account cannot be read now; nil when it can. A stop after a throttling page is
// the site's and holds for a candidate being verified too (review round 1 of PR #1359).
func (se *Session) Ready(ctx context.Context) *SourceError {
	if f := se.s.stopOf(ctx, se.source); f != nil {
		return f
	}

	if se.candidate != nil {
		return nil
	}

	a, err := se.s.account(ctx, se.source)

	switch {
	case err != nil:
		return Fail(CodeBroken, "%s", err.Error())
	case a == nil:
		return Fail(CodeNeedLogin, "no %s account is signed in", se.signed.Site.Name)
	case a.State == AccountNeedLogin:
		return Fail(CodeNeedLogin, "%s asked its account to sign in again: %s", se.signed.Site.Name, a.Cause)
	case a.State == AccountRefused:
		return Fail(CodeRefused, "%s refused its account, which waits for a new sign-in: %s", se.signed.Site.Name,
			a.Cause)
	}

	return nil
}

// stopOf is a source's stop after a throttling page while it lasts; nil when it is not stopped.
func (s *Service) stopOf(ctx context.Context, source string) *SourceError {
	if s.db == nil {
		return nil
	}

	row, err := store.New(s.db).ListeningSourceStop(ctx, source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	if err != nil {
		return Fail(CodeBroken, "reading %s's stop: %s", source, err.Error())
	}

	if until := at(row.StoppedUntil); s.now().Before(until) {
		return &SourceError{Code: CodeRateLimited, Detail: "stopped after a throttling page: " + row.StopCause,
			Until: until, transient: true}
	}

	return nil
}

// Jar is the account's cookies.
func (se *Session) Jar(ctx context.Context) ([]browser.Cookie, error) {
	if se.candidate != nil {
		return se.candidate, nil
	}

	if se.s.acc.secrets == nil {
		return nil, Fail(CodeBroken, "this control plane has no vault")
	}

	opened, err := se.s.acc.secrets.Open(ctx, se.s.acc.secretName(se.source))
	if errors.Is(err, vault.ErrNotFound) {
		return nil, Fail(CodeNeedLogin, "no %s account is signed in", se.signed.Site.Name)
	}

	if err != nil {
		return nil, Fail(CodeBroken, "opening %s's account: %s", se.source, err.Error())
	}

	jar, err := signin.Decode(opened.Payload[vault.KeyValue])
	if err != nil {
		return nil, Fail(CodeBroken, "%s's account: %s", se.source, err.Error())
	}

	return jar, nil
}

// Page is the account's tab, in its own browser context, made with its cookies.
func (se *Session) Page(ctx context.Context) (Page, error) {
	if se.s.acc.browser == nil {
		return nil, Fail(CodeBroken, "this control plane's shared browser is off")
	}

	jar, err := se.Jar(ctx)
	if err != nil {
		return nil, err
	}

	page, err := se.s.acc.browser.Page(ctx, contextKey(se.source, se.candidate != nil), jar)
	if err != nil {
		return nil, Fail(CodeBroken, "opening the account's tab: %s", err.Error())
	}

	return page, nil
}

// met keeps what a real call's failure says of the account: a sign-in asked marks it need_login, a refusal refused,
// and a throttling page stops the site for its source's Throttle, a candidate's call too, since it went to the same
// site. The owner is told of each session's change into need_login or refused, once: postiz notifies where it sets
// refreshNeeded (integration.repository.ts:603-616), which a new connection clears (:288); the change also sets the source's
// told flag, so the broken reading of the same search tells nothing more (health.go record), nor any later reading
// whose code is the account's own state (review rounds 1 and 2 of PR #1359). A sign-in clears the flag (keep).
func (se *Session) met(ctx context.Context, f *SourceError) {
	if f == nil || f.resting || se.candidate != nil && f.Code != CodeRateLimited {
		return
	}

	ctx = context.WithoutCancel(ctx)

	switch f.Code {
	case CodeNeedLogin, CodeRefused:
		var flagged bool

		err := se.s.write(ctx, func(q *store.Queries, now int64) error {
			changed, err := q.MarkListeningAccount(ctx, store.MarkListeningAccountParams{
				Source: se.source, State: string(f.Code), Cause: cut(f.Detail, maxCause), Now: now,
			})
			if err != nil || changed == 0 {
				return err
			}

			flagged = true
			_, err = q.FlagListeningTold(ctx, store.FlagListeningToldParams{Source: se.source, Now: now})

			return err
		})
		if err != nil {
			se.s.logger.ErrorContext(ctx, "a listening account's state was not kept", slog.String("event",
				"listening.account_unkept"), slog.String("source", se.source), slog.String("error", err.Error()))

			return
		}

		if flagged && se.s.tell != nil {
			se.s.tell(ctx, Broken{Source: se.source, Code: f.Code, Detail: f.Detail, Fix: Fix(se.source, f.Code,
				time.Time{})})
		}
	case CodeRateLimited:
		se.stop(ctx, f)
	default:
		// Any other failure is the read's, not the account's.
	}
}

// stop keeps a throttle's stop of the source's site for its Throttle, or longer when the site named a later time; an
// earlier stop is never shortened (StopListeningSource). A backend calls it before its turn at the site ends, so a
// read queued behind it reads the stop once its turn comes, as call.do rests a path before its turn ends
// (http.go:72-101).
func (se *Session) stop(ctx context.Context, f *SourceError) {
	if f == nil || f.resting || f.Code != CodeRateLimited || se.signed.Throttle == 0 {
		return
	}

	ctx = context.WithoutCancel(ctx)

	until := se.s.now().Add(se.signed.Throttle)
	if f.Until.After(until) {
		until = f.Until
	}

	err := se.s.write(ctx, func(q *store.Queries, now int64) error {
		return q.StopListeningSource(ctx, store.StopListeningSourceParams{
			Source: se.source, Until: until.UnixMilli(), Cause: cut(f.Detail, maxCause), Now: now,
		})
	})
	if err != nil {
		se.s.logger.ErrorContext(ctx, "a listening account's stop was not kept", slog.String("event",
			"listening.account_unkept"), slog.String("source", se.source), slog.String("error", err.Error()))
	}
}

// worked notes that a real call read with the account.
func (se *Session) worked(ctx context.Context) {
	if se.candidate != nil {
		return
	}

	ctx = context.WithoutCancel(ctx)

	_ = se.s.write(ctx, func(q *store.Queries, now int64) error {
		return q.ConfirmListeningAccount(ctx, store.ConfirmListeningAccountParams{Source: se.source, Now: now})
	})
}

// sessionBackend is a backend that reads as the source's account: its probe is the account's state, and what each of
// its reads meets is kept on the account.
func sessionBackend(b Backend) Backend {
	search := b.Search
	b.Probe = func(r Request) *SourceError {
		if r.Session == nil {
			return Fail(CodeBroken, "this source's account cannot be read here")
		}

		// A probe has no context of its own: it only reads the database.
		return r.Session.Ready(context.Background())
	}
	b.Search = func(ctx context.Context, r Request) (Result, error) {
		if r.Session == nil {
			return Result{}, Fail(CodeBroken, "this source's account cannot be read here")
		}

		res, err := search(ctx, r)

		switch {
		case err != nil:
			r.Session.met(ctx, AsFailure(err))
		case res.Failed != nil:
			r.Session.met(ctx, res.Failed)
		default:
			r.Session.worked(ctx)
		}

		return res, err
	}

	return b
}
