// Package subscription lets the owner log a subscription account in on the
// control plane, once, and keeps its tokens fresh (issue #20, step 2).
//
// The tokens are a secret of the vault, of kind oauth_token_set; the account
// itself, with who it is at the vendor, is orchestration's. One Vendor per
// kind of subscription knows how its login and its refresh are done.
//
// The flows follow the vendors' own CLIs and two gateways that serve the
// same subscriptions, both MIT: CLIProxyAPI (Copyright (c) 2025 Luis Pater,
// Router-For.ME) and 9router-go. Each vendor's file names what it follows,
// with the commit read.
package subscription

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

const (
	// refreshEvery is how often the loop looks for tokens about to end, as
	// 9router-go's background refresh (internal/proxy/oauth/background.go).
	refreshEvery = 5 * time.Minute
	// A refresh that failed for a reason that may pass is tried again after
	// retryFirst, doubling up to retryMax, as 9router-go's
	// db/accounts_oauth_lock.go.
	retryFirst = 5 * time.Minute
	retryMax   = 24 * time.Hour
	// useLead is how close to its end a token is refreshed before it is
	// used for a request, as 9router-go's providers.IsExpired.
	useLead = 60 * time.Second
)

// Errors a caller can tell apart.
var (
	// ErrUnknownVendor is returned for a runtime no Vendor serves.
	ErrUnknownVendor = errors.New("no login for this kind of account")
	// ErrNoTokens is returned for an account that was not logged in here.
	ErrNoTokens = errors.New("the account was not logged in on the control plane")
	// ErrLoginFailed is returned when the vendor did not complete a login.
	ErrLoginFailed = errors.New("the login did not complete")
	// ErrBadPaste is wrapped by a paste that cannot be read (no code, not an
	// address, another login's state): nothing reached the vendor, so the
	// attempt stays open for a corrected paste (issue #749).
	ErrBadPaste = errors.New("the paste cannot be read")
	// ErrNoAccount is returned when no account of a runtime can be used:
	// none was logged in here, or every one is disabled or needs a new login.
	ErrNoAccount = errors.New("no account logged in here can be used")
	// ErrModelNotServed is returned for a model no account's list has, once every usable account's list is known
	// (issue #814).
	ErrModelNotServed = errors.New("the model is not served by any account")
	// ErrNoModel is returned when a token is asked for without the model it will call, of a runtime whose accounts
	// are each served their own models (issue #814).
	ErrNoModel = errors.New("a token is asked for without its model")
)

// CoolingError says every usable account of a runtime waits for a usage
// limit to end; Until is when the first one is free.
type CoolingError struct{ Until time.Time }

func (e *CoolingError) Error() string {
	return "every account waits for its usage limit, until " + e.Until.UTC().Format(time.RFC3339)
}

// Token is an account's access token, for a caller inside the control plane
// that sends it to the vendor. The caller keeps it out of logs and errors.
type Token struct {
	AccountID string
	// ExternalID is the vendor's id of the account.
	ExternalID  string
	AccessToken string
	// Project is the vendor's project the account works in, when it has one.
	Project string
	// Plan is the account's plan as its login read it (Codex: chatgpt_plan_type).
	Plan string
	// Version is the version of the account's secret the token is from.
	Version int
	// Verifying says Google asked for the account to be verified and this is
	// its next try (issue #822): a call that works clears the mark (Verified).
	Verifying bool
	// VerificationRetry holds the claimed deadline across a fallback host or a 401 retry.
	VerificationRetry *atomic.Int64
	checkVerification func(context.Context) error
}

// Identity is who an account is at its vendor. None of it is secret.
type Identity struct {
	// ExternalID is the vendor's own id of the account.
	ExternalID string
	// FormerExternalID is the id an earlier login of the same account may be
	// recorded under, which this login takes over (Claude: the user's, before
	// the organization's).
	FormerExternalID string
	Email            string
	Plan             string
	// Project is the vendor's project the account works in, when it has one.
	Project string
}

// Login is what a completed login gave.
type Login struct {
	Identity

	Tokens vault.Refreshed
}

// Attempt is one login in progress. The owner opens URL in a browser on any
// machine. With a UserCode they type it there and Complete waits for the
// vendor; with Paste the browser ends on an address that does not load, which
// they copy and give to Complete.
type Attempt struct {
	Runtime  string
	URL      string
	UserCode string
	// Paste says Complete needs the address the browser ended on.
	Paste bool
	// ExpiresIn is how long the vendor keeps the attempt open.
	ExpiresIn time.Duration

	finish func(ctx context.Context, pasted string) (Login, error)
}

// Vendor knows one kind of subscription.
type Vendor interface {
	// Runtime names the kind as an account's runtime does: codex.
	Runtime() string
	// Begin starts a login.
	Begin(ctx context.Context) (*Attempt, error)
	// Refresh exchanges a token set's refresh token; it is a
	// vault.RefreshFunc, with that type's rules for errors.
	Refresh(ctx context.Context, current vault.Payload) (vault.Refreshed, error)
	// Identify reads who a token set belongs to.
	Identify(tokens vault.Payload) (Identity, error)
	// RefreshLead is how long before its end an access token is replaced.
	RefreshLead() time.Duration
	// RefreshSpacing is the pause between the refreshes of two accounts in
	// one pass, so they do not reach the vendor together.
	RefreshSpacing() time.Duration
}

// Accounts is what the service needs of the store of accounts
// (orchestration.Service).
type Accounts interface {
	SaveAccountLogin(
		ctx context.Context, in orchestration.AccountLogin, secretRef func(accountID string) string,
	) (orchestration.Account, error)
	ListAccounts(ctx context.Context) ([]orchestration.Account, error)
	VerificationState(ctx context.Context, id string) (*orchestration.AccountVerification, int, error)
	SetVerificationLogin(ctx context.Context, id string, version int) error
	ClaimVerificationRetry(
		ctx context.Context, id string, version int, previous time.Time, wait time.Duration,
	) (time.Time, error)
	SetAccountPlan(ctx context.Context, id, email, plan string) error
	CoolAccount(ctx context.Context, runtime, id string, until time.Time, reason string) error
	RecordAccountRefresh(ctx context.Context, id string, r orchestration.AccountRefresh) error
	RemoveAccountLogin(ctx context.Context, id string) error
	MarkAccountVerification(
		ctx context.Context, id string, version int, url, message string, wait time.Duration,
	) (orchestration.AccountVerification, error)
	ClearAccountVerification(ctx context.Context, id string, version int) (bool, error)
}

// State says whether an account's tokens can be used.
type State string

// The states of an account that was logged in here.
const (
	// StateActive: the tokens are there and the vendor accepts the login.
	StateActive State = "active"
	// StateLoginRequired: the vendor refused the refresh token for good.
	StateLoginRequired State = "login required"
	// StateNoTokens: the account has no secret, after a login that failed
	// halfway or a secret removed by hand.
	StateNoTokens State = "no tokens"
)

// Account is an account with the state of its tokens. State is empty for an
// account that was not logged in here.
type Account struct {
	orchestration.Account

	State State
	// AccessTokenExpiresAt is when the access token ends, if known.
	AccessTokenExpiresAt *time.Time
}

// Service logs accounts in and refreshes their tokens.
type Service struct {
	accounts Accounts
	vault    *vault.Service
	clock    platform.Clock
	logger   *slog.Logger
	vendors  map[string]Vendor
	signInMu sync.Mutex
	// after is time.After outside tests: the pause between two refreshes.
	after func(time.Duration) <-chan time.Time
	// rr is the round-robin cursor per runtime, for a request with no session;
	// rrMu guards it.
	rr   map[string]int
	rrMu sync.Mutex
	// left is the last reading of what remains of each account's quota, in
	// memory as the references keep theirs; leftMu guards it.
	left   map[string]quotaReading
	leftMu sync.Mutex
	// limits is the last reading of each account's limits (limits.go);
	// limitsMu guards it.
	limits   map[string]limitsEntry
	limitsMu sync.Mutex
	// pins is the account each conversation is on (rank.go); pinsMu guards it.
	pins   map[string]pin
	pinsMu sync.Mutex
	// served is the last reading of the models each account is served (served.go); servedMu guards it.
	served   map[string]servedReading
	servedMu sync.Mutex
	// servedWait bounds the wait for an account's first reading in flight (served.go).
	servedWait time.Duration
	// firstGood is, by runtime, the last list answered of a runtime whose list is not each account's own (served.go).
	firstGood map[string]servedView
	// firstList, when set, is told the runtime of an account whose first list of models was just read (served.go).
	firstList func(runtime string)
	// afterAccounts, when set, runs in TokenFor between reading the accounts and what is known of their lists: a
	// test's moment to finish a list.
	afterAccounts func()
}

const (
	// reserveLeft is the part of an account's quota at or below which it is
	// kept in reserve, used only after the accounts that have more: a 10 %
	// reserve in omp (packages/ai/src/auth/types.ts DEFAULT_USAGE_RESERVE_PCT),
	// 90 % used in opencode-antigravity-auth (soft_quota_threshold_percent).
	reserveLeft = 0.10
	// quotaTrusted is how long a reading is believed; an account with none, or
	// with an old one, is used as any other (opencode-antigravity-auth
	// src/plugin/accounts.ts isOverSoftQuotaThreshold and its cache).
	quotaTrusted = 30 * time.Minute
)

// quotaReading is what was left of an account's quota, from 0 to 1, and when
// its vendor said so.
type quotaReading struct {
	at   time.Time
	left float64
}

// New returns a Service over the accounts, the vault and the vendors.
func New(
	accounts Accounts, v *vault.Service, clock platform.Clock, logger *slog.Logger, vendors ...Vendor,
) *Service {
	s := &Service{
		accounts: accounts, vault: v, clock: clock, logger: logger, vendors: map[string]Vendor{}, after: time.After,
		rr: map[string]int{}, left: map[string]quotaReading{}, limits: map[string]limitsEntry{}, pins: map[string]pin{},
		served: map[string]servedReading{}, servedWait: servedWait, firstGood: map[string]servedView{},
	}
	for _, vendor := range vendors {
		s.vendors[vendor.Runtime()] = vendor
	}

	return s
}

// secretName is the vault's name for an account's tokens.
func secretName(accountID string) string {
	return "account-" + strings.ReplaceAll(strings.ToLower(accountID), "_", "-")
}

// Begin starts a login at the vendor of a runtime.
func (s *Service) Begin(ctx context.Context, runtime string) (*Attempt, error) {
	vendor, ok := s.vendors[runtime]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownVendor, runtime)
	}

	a, err := vendor.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("starting a %s login: %w", runtime, err)
	}

	a.Runtime = runtime

	return a, nil
}

// browserLogin is a vendor whose own login asks for a code first, with a
// browser login beside it (Codex).
type browserLogin interface {
	BeginInBrowser(ctx context.Context) (*Attempt, error)
}

// BeginInBrowser starts a login whose last address the owner pastes back: the
// one a web page asks for. 9router's dashboard, CLIProxyAPI's management API
// and T3 Code sign Codex in on the web this way, and keep its device code to a
// terminal, as `accounts login` keeps it here.
func (s *Service) BeginInBrowser(ctx context.Context, runtime string) (*Attempt, error) {
	vendor, ok := s.vendors[runtime]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownVendor, runtime)
	}

	browser, ok := vendor.(browserLogin)
	if !ok {
		return s.Begin(ctx, runtime)
	}

	a, err := browser.BeginInBrowser(ctx)
	if err != nil {
		return nil, fmt.Errorf("starting a %s login: %w", runtime, err)
	}

	a.Runtime = runtime

	return a, nil
}

// Complete finishes a login and stores it: a new account, or new tokens for
// the account the vendor's id already has. pasted is the address the browser
// ended on, for an attempt that asks for it; label names a new account to the
// owner, and is its email when empty.
func (s *Service) Complete(ctx context.Context, a *Attempt, pasted, label string) (Account, error) {
	login, err := a.finish(ctx, pasted)
	if err != nil {
		return Account{}, fmt.Errorf("%w: %w", ErrLoginFailed, err)
	}

	return s.save(ctx, a.Runtime, login, label)
}

// credentialImporter is a vendor whose CLI keeps a session in a file the
// owner can bring to the control plane (Claude: .credentials.json).
type credentialImporter interface {
	ImportCredentials(ctx context.Context, raw []byte) (Login, error)
}

// Import stores the session a vendor's CLI kept in its credentials file, as a
// completed sign-in is stored (issue #828): a new account, or new tokens for
// the account the vendor's id already has. raw is never logged.
func (s *Service) Import(ctx context.Context, runtime string, raw []byte, label string) (Account, error) {
	vendor, ok := s.vendors[runtime]
	if !ok {
		return Account{}, fmt.Errorf("%w: %q", ErrUnknownVendor, runtime)
	}

	importer, ok := vendor.(credentialImporter)
	if !ok {
		return Account{}, fmt.Errorf("%w: %s accounts are not imported from a file", ErrUnknownVendor, runtime)
	}

	login, err := importer.ImportCredentials(ctx, raw)
	if err != nil {
		if errors.Is(err, ErrBadPaste) {
			return Account{}, err
		}

		return Account{}, fmt.Errorf("%w: %w", ErrLoginFailed, err)
	}

	return s.save(ctx, runtime, login, label)
}

// save records a login's account and seals its tokens in the vault.
func (s *Service) save(ctx context.Context, runtime string, login Login, label string) (Account, error) {
	if runtime == antigravityRuntime {
		s.signInMu.Lock()
		defer s.signInMu.Unlock()
	}

	acct, err := s.accounts.SaveAccountLogin(ctx, orchestration.AccountLogin{
		Runtime: runtime, ExternalID: login.ExternalID, FormerExternalID: login.FormerExternalID,
		Email: login.Email, Plan: login.Plan,
		Project: login.Project, Label: label,
	}, func(id string) string { return vault.Ref(secretName(id)) })
	if err != nil {
		return Account{}, err
	}

	name, ok := vault.NameFromRef(acct.SecretRef)
	if !ok {
		return Account{}, fmt.Errorf("%w: account %s", ErrNoTokens, acct.ID)
	}

	secret, err := s.vault.Get(ctx, name)

	switch {
	case err == nil:
		secret, err = s.vault.RotateTokenSet(ctx, name, login.Tokens)
	case errors.Is(err, vault.ErrNotFound):
		secret, err = s.vault.Create(ctx, vault.NewSecret{
			Name: name, Kind: vault.KindOAuthTokenSet, Payload: login.Tokens.Payload,
			Metadata:             map[string]string{"account": acct.ID, "runtime": acct.Runtime},
			AccessTokenExpiresAt: login.Tokens.AccessTokenExpiresAt,
		})
	}

	if err != nil {
		return Account{}, fmt.Errorf("storing the tokens of account %s: %w", acct.ID, err)
	}
	if runtime == antigravityRuntime {
		if err := s.accounts.SetVerificationLogin(ctx, acct.ID, secret.Version); err != nil {
			return Account{}, err
		}
	}

	if err := s.accounts.RecordAccountRefresh(ctx, acct.ID, orchestration.AccountRefresh{}); err != nil {
		return Account{}, err
	}

	return s.Get(ctx, acct.ID)
}

// List returns every account, those logged in here with the state of their
// tokens.
func (s *Service) List(ctx context.Context) ([]Account, error) {
	accounts, err := s.accounts.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]Account, 0, len(accounts))

	for _, a := range accounts {
		// A Muse account is the Muse pool's (issue #782), shown on its own page.
		if a.Runtime == orchestration.MuseRuntime {
			continue
		}

		view, err := s.view(ctx, a)
		if err != nil {
			return nil, err
		}

		out = append(out, view)
	}

	return out, nil
}

// Get returns one account by its id.
func (s *Service) Get(ctx context.Context, id string) (Account, error) {
	accounts, err := s.List(ctx)
	if err != nil {
		return Account{}, err
	}

	for _, a := range accounts {
		if a.ID == id {
			return a, nil
		}
	}

	return Account{}, fmt.Errorf("%w: account %s", orchestration.ErrNotFound, id)
}

func (s *Service) view(ctx context.Context, a orchestration.Account) (Account, error) {
	name, ok := vault.NameFromRef(a.SecretRef)
	if !ok {
		return Account{Account: a}, nil
	}

	sec, err := s.vault.Get(ctx, name)

	switch {
	case errors.Is(err, vault.ErrNotFound):
		return Account{Account: a, State: StateNoTokens}, nil
	case err != nil:
		return Account{}, fmt.Errorf("reading the tokens of account %s: %w", a.ID, err)
	case sec.LoginRequired:
		return Account{Account: a, State: StateLoginRequired, AccessTokenExpiresAt: sec.AccessTokenExpiresAt}, nil
	default:
		return Account{Account: a, State: StateActive, AccessTokenExpiresAt: sec.AccessTokenExpiresAt}, nil
	}
}

// Refresh replaces an account's tokens now, whatever their age.
func (s *Service) Refresh(ctx context.Context, id string) (Account, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return Account{}, err
	}

	if err := s.refresh(ctx, a, 0, nil); err != nil {
		return Account{}, err
	}

	return s.Get(ctx, id)
}

// Token returns a valid access token of an account of a runtime that can be
// used: active, logged in here, not cooling, and not among skip. The accounts
// are ordered so one conversation keeps one account and its prompt cache stays
// warm (the cache is per account): a session picks a home account by a hash of
// its id and probes to the next only while the home is cooling or skipped;
// without a session accounts are taken round-robin to spread load. A token
// about to end is refreshed first. (omp `select.ts`/`affinity.ts`, 9router-go
// `session_affinity.go`, CLIProxyAPI `selector.go`.) An account in reserve,
// one whose last reading says a tenth of its quota or less is left, comes
// after the others in that order, a session's home too: it serves only when
// no other can (omp `select.ts`, where a reserve evicts a session's account
// while a sibling is outside it, and `rank.ts` inReserve).
func (s *Service) Token(ctx context.Context, runtime, session string, skip ...string) (Token, error) {
	accounts, err := s.List(ctx)
	if err != nil {
		return Token{}, err
	}

	return s.tokenAmong(ctx, runtime, "", session, usable(accounts, runtime), skip)
}

// TokenFor is Token for a request for a model, which goes only to the accounts whose own list of models has it
// (issue #814), as CLIProxyAPI routes a model only to the auths that registered it
// (sdk/cliproxy/auth/conductor_selection.go:1419-1431 authSupportsRouteModel, applied at :1687). Every rule of Token
// then orders those. Of a runtime whose vendor lists each account's own models (perAccount):
//   - an account whose first list is being read is waited for, at most servedWait; while no account is known to have
//     the model and one's list is not known (still being read, or never read), the answer is a CoolingError to try
//     again soon, never another account in its place;
//   - when every account's list is known and none has the model, the answer is ErrModelNotServed;
//   - a request names its model: an empty one is ErrNoModel.
//
// Another runtime's accounts are taken as Token takes them. The accounts and what is known of their lists are taken
// together, after the lists were read, so that what reading them found of an account decides, and no list read
// between two steps of the pick is half seen (owner, 2026-11-18).
func (s *Service) TokenFor(ctx context.Context, runtime, model, session string, skip ...string) (Token, error) {
	if !perAccount[runtime] {
		return s.Token(ctx, runtime, session, skip...)
	}

	if model == "" {
		return Token{}, fmt.Errorf("%w: %s", ErrNoModel, runtime)
	}

	s.servedOf(ctx, runtime) // reads the lists not read yet, and waits for those being read

	accounts, err := s.List(ctx)
	if err != nil {
		return Token{}, err
	}

	if s.afterAccounts != nil {
		s.afterAccounts()
	}

	ofRuntime := usable(accounts, runtime)
	servers, unknown := s.servers(ofRuntime, model)

	switch {
	case len(servers) > 0:
		return s.tokenAmong(ctx, runtime, model, session, servers, skip)
	case unknown:
		return Token{}, &CoolingError{Until: s.clock.Now().Add(servedUnknownRetry)}
	case len(ofRuntime) == 0:
		return Token{}, fmt.Errorf("%w: %s", ErrNoAccount, runtime)
	default:
		return Token{}, fmt.Errorf("%w: %s by no %s account", ErrModelNotServed, model, runtime)
	}
}

// usable are the accounts of a runtime that can be used: active and logged in here.
func usable(accounts []Account, runtime string) []Account {
	var out []Account

	for _, a := range accounts {
		if a.usableBy(runtime) {
			out = append(out, a)
		}
	}

	return out
}

// tokenAmong is Token among the given accounts of a runtime. For a model, the account picked is read again right
// before its token is given, and passed over unless it is still usable and its list still has the model: the
// accounts and the lists live apart (the database and memory), so no single read holds both (issue #814).
func (s *Service) tokenAmong(
	ctx context.Context, runtime, model, session string, ofRuntime []Account, skip []string,
) (Token, error) {
	if len(ofRuntime) == 0 {
		return Token{}, fmt.Errorf("%w: %s", ErrNoAccount, runtime)
	}

	// The accounts in a stable order, so the home a session hashes to does not move between its requests.
	slices.SortFunc(ofRuntime, func(a, b Account) int { return strings.Compare(a.ID, b.ID) })

	start := s.startIndex(runtime, session, len(ofRuntime), len(skip) == 0)
	now := s.clock.Now()

	ordered := make([]Account, 0, len(ofRuntime))
	for i := range ofRuntime {
		ordered = append(ordered, ofRuntime[(start+i)%len(ofRuntime)])
	}

	// A warm conversation keeps its account; otherwise the accounts are ranked
	// by their windows, the reserve last (rank.go).
	ordered = s.rank(runtime, session, ordered, now)

	var free *time.Time

	for _, a := range ordered {

		if slices.Contains(skip, a.ID) {
			continue
		}

		if a.CoolingUntil != nil {
			if free == nil || a.CoolingUntil.Before(*free) {
				free = a.CoolingUntil
			}

			continue
		}

		// Read again before a refresh, so no account that cannot serve is refreshed, and again after it, which
		// waits on the vendor: the last check before the token is given (issue #814).
		recheck := func() bool {
			if model == "" {
				return true
			}

			fresh, ok := s.stillServes(ctx, a, model)
			if ok && fresh.CoolingUntil != nil {
				if free == nil || fresh.CoolingUntil.Before(*free) {
					free = fresh.CoolingUntil
				}

				ok = false
			}

			if ok {
				a = fresh
			}

			return ok
		}

		if !recheck() {
			continue
		}

		// An account Google wants verified rests until its next try (issue #822).
		if v := a.Verification; v != nil && v.RetryAt.After(now) {
			if free == nil || v.RetryAt.Before(*free) {
				free = &v.RetryAt
			}

			continue
		}

		token, err := s.usableToken(ctx, a)
		if err != nil {
			if errors.Is(err, vault.ErrLoginRequired) || errors.Is(err, ErrNoTokens) {
				continue
			}

			var verify *VerificationError
			if !errors.As(err, &verify) {
				return Token{}, err
			}

			if free == nil || verify.RetryAt.Before(*free) {
				free = &verify.RetryAt
			}

			continue
		}

		// The refresh waited on the vendor: the last check before the token is given.
		if !recheck() {
			continue
		}

		s.keepPin(runtime, session, a.ID, now)

		return token, nil
	}

	if free != nil {
		return Token{}, &CoolingError{Until: *free}
	}

	// The accounts' lists may have changed while they were tried (a refresh in between): with every list known and
	// none having the model now, the answer is TokenFor's for such a model (issue #814).
	if model != "" && s.notServedNow(ctx, runtime, model) {
		return Token{}, fmt.Errorf("%w: %s by no %s account", ErrModelNotServed, model, runtime)
	}

	return Token{}, fmt.Errorf("%w: %s", ErrNoAccount, runtime)
}

// notServedNow says every usable account of a runtime has a known list and none has model.
func (s *Service) notServedNow(ctx context.Context, runtime, model string) bool {
	accounts, err := s.List(ctx)
	if err != nil {
		return false
	}

	ofRuntime := usable(accounts, runtime)
	servers, unknown := s.servers(ofRuntime, model)

	return len(ofRuntime) > 0 && len(servers) == 0 && !unknown
}

// stillServes reads an account again and says whether it can still be used for model: active, logged in here, and
// its list has the model. Whether it cools is the caller's to see on the account returned.
func (s *Service) stillServes(ctx context.Context, a Account, model string) (Account, bool) {
	fresh, err := s.Get(ctx, a.ID)
	if err != nil || fresh.Runtime != a.Runtime || fresh.Status != orchestration.AccountActive ||
		fresh.State != StateActive {
		return Account{}, false
	}

	return fresh, s.hasModel(a.ID, model)
}

// Serves says an account can still be sent a request for model: active, logged in here, not cooling, and its list has
// the model (issue #814).
func (s *Service) Serves(ctx context.Context, accountID, model string) bool {
	a, err := s.Get(ctx, accountID)
	if err != nil {
		return false
	}

	fresh, ok := s.stillServes(ctx, a, model)

	return ok && fresh.CoolingUntil == nil
}

// TokenAfterRefusalFor is TokenAfterRefusal for a request for a model (issue #814): of a runtime whose accounts are
// each served their own models, the account is refreshed only while it is still usable and its list still has the
// model; otherwise the answer is ErrModelNotServed, and the request goes on to another account.
func (s *Service) TokenAfterRefusalFor(ctx context.Context, refused Token, model string) (Token, error) {
	a, err := s.Get(ctx, refused.AccountID)
	if err != nil {
		return Token{}, err
	}

	if perAccount[a.Runtime] {
		if model == "" {
			return Token{}, fmt.Errorf("%w: %s", ErrNoModel, a.Runtime)
		}

		if fresh, ok := s.stillServes(ctx, a, model); !ok || fresh.CoolingUntil != nil {
			return Token{}, fmt.Errorf("%w: %s by account %s", ErrModelNotServed, model, a.ID)
		}
	}

	token, err := s.TokenAfterRefusal(ctx, refused)
	if err != nil || !perAccount[a.Runtime] {
		return token, err
	}

	// The refresh waited on the vendor: the account is read again, the last check before its token is given.
	if fresh, ok := s.stillServes(ctx, a, model); !ok || fresh.CoolingUntil != nil {
		return Token{}, fmt.Errorf("%w: %s by account %s", ErrModelNotServed, model, a.ID)
	}

	return token, nil
}

func (a Account) usableBy(runtime string) bool {
	return a.Runtime == runtime && a.Status == orchestration.AccountActive && a.State == StateActive
}

func (s *Service) FirstFree(ctx context.Context, runtime string, modelReset func(string) time.Time) (time.Time, error) {
	accounts, err := s.List(ctx)
	if err != nil {
		return time.Time{}, err
	}
	now := s.clock.Now()
	var first time.Time
	for _, account := range accounts {
		if !account.usableBy(runtime) {
			continue
		}
		var reset time.Time
		if account.CoolingUntil != nil {
			reset = *account.CoolingUntil
		}
		if modelReset != nil {
			if until := modelReset(account.ID); until.After(reset) {
				reset = until
			}
		}
		if !reset.After(now) {
			return time.Time{}, nil
		}
		if first.IsZero() || reset.Before(first) {
			first = reset
		}
	}
	return first, nil
}

// NoteQuota records what an account's vendor said is left of its quota, from
// 0 (used up) to 1, as read from an answer served under the account.
func (s *Service) NoteQuota(accountID string, left float64) {
	s.leftMu.Lock()
	defer s.leftMu.Unlock()

	s.left[accountID] = quotaReading{at: s.clock.Now(), left: left}
}

// inReserve reports whether an account's quota is known, fresh and nearly
// used up. An account with no reading is not in reserve: it is used.
func (s *Service) inReserve(accountID string, now time.Time) bool {
	s.leftMu.Lock()
	defer s.leftMu.Unlock()

	reading, known := s.left[accountID]

	return known && now.Sub(reading.at) < quotaTrusted && reading.left <= reserveLeft
}

// cmpBool orders false before true.
func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}

// startIndex is where to begin scanning a runtime's accounts. A session hashes
// to a stable home, so every request of one conversation starts at the same
// account. A request with no session takes the next round-robin slot, advanced
// once per request (when nothing has been skipped yet) so a retry does not
// rotate again; a retry then scans from the front, since skip already holds
// what it tried.
func (s *Service) startIndex(runtime, session string, n int, fresh bool) int {
	if session != "" {
		h := fnv.New32a()
		_, _ = h.Write([]byte(session))

		return int(h.Sum32() % uint32(n)) //nolint:gosec // an index in [0,n), not a size
	}

	if !fresh {
		return 0
	}

	s.rrMu.Lock()
	defer s.rrMu.Unlock()

	start := s.rr[runtime] % n
	s.rr[runtime] = (start + 1) % n

	return start
}

// TokenAfterRefusal returns a new token for an account whose token the vendor
// refused. The refresh token is exchanged unless the account's tokens were
// replaced since the refused one was read: then those are returned.
func (s *Service) TokenAfterRefusal(ctx context.Context, refused Token) (Token, error) {
	a, err := s.Get(ctx, refused.AccountID)
	if err != nil {
		return Token{}, err
	}

	retry := refused.VerificationRetry
	if retry == nil && a.Runtime == antigravityRuntime {
		retry = new(atomic.Int64)
	}
	if err := s.refresh(ctx, a, refused.Version, retry); err != nil {
		return Token{}, err
	}

	a, err = s.Get(ctx, refused.AccountID)
	if err != nil {
		return Token{}, err
	}

	return s.tokenOf(ctx, a.Account, retry)
}

// ServedModel is a model a vendor serves an account, with what is left of
// its quota when the vendor says.
type ServedModel struct {
	Name string
	// Remaining is the part of the model's quota that is left, from 0 to 1;
	// negative when the vendor did not say.
	Remaining float64
	// ResetsAt is when the quota is whole again; zero when not known.
	ResetsAt time.Time
	// Window and Output are the model's context window and largest output in tokens, as the vendor gives them;
	// zero when it does not.
	Window, Output int64
	// Wire is the API the vendor serves the model on, when it serves its models on several (OpenCode Go:
	// chat-completions, responses or messages); empty otherwise.
	Wire string
}

// ModelLister is a Vendor that can say which models it serves an account.
type ModelLister interface {
	Models(ctx context.Context, token Token) ([]ServedModel, error)
}

// ErrNoModelList is returned for an account whose vendor lists no models.
var ErrNoModelList = errors.New("this kind of account does not list its models")

// Models asks the vendor which models it serves an account, by name.
func (s *Service) Models(ctx context.Context, id string) ([]ServedModel, error) {
	lister, token, err := vendorToken[ModelLister](ctx, s, id)
	if err != nil {
		return nil, err
	}

	models, err := lister.Models(ctx, token)
	if err := s.noteVerification(ctx, token, err); err != nil {
		return nil, err
	}

	return models, nil
}

// RosterRow is one row of the list a vendor answers an account's models with, as it gives it, before the rows no
// request can use are left out: one the vendor keeps for itself (Internal), one it does not serve the account now
// (Disabled), or one the references leave out (Denied).
type RosterRow struct {
	Name                       string
	Internal, Disabled, Denied bool
}

// RosterLister is a Vendor that can show every row of its list of an account's models.
type RosterLister interface {
	Roster(ctx context.Context, token Token) ([]RosterRow, error)
}

// Roster asks the vendor for every row of its list of an account's models, those Models leaves out with them, so the
// owner can see why a model is not served (issue #814).
func (s *Service) Roster(ctx context.Context, id string) ([]RosterRow, error) {
	lister, token, err := vendorToken[RosterLister](ctx, s, id)
	if err != nil {
		return nil, err
	}

	rows, err := lister.Roster(ctx, token)
	if err := s.noteVerification(ctx, token, err); err != nil {
		return nil, err
	}

	return rows, nil
}

// vendorToken returns an account's vendor as a T, when it is one, and a token of the account fresh enough to use.
func vendorToken[T any](ctx context.Context, s *Service, id string) (T, Token, error) {
	var none T

	a, err := s.Get(ctx, id)
	if err != nil {
		return none, Token{}, err
	}

	vendor, ok := s.vendors[a.Runtime].(T)
	if !ok {
		return none, Token{}, fmt.Errorf("%w: %s", ErrNoModelList, a.Runtime)
	}

	token, err := s.usableToken(ctx, a)
	if err != nil {
		return none, Token{}, err
	}

	return vendor, token, nil
}

func cmpState(state State) string {
	if state == "" {
		return "not logged in here"
	}

	return string(state)
}

// Quota is what is left of an account's quota for each model, from 0 to 1,
// as far as its vendor says.
func (s *Service) Quota(ctx context.Context, id string) (map[string]float64, error) {
	models, err := s.Models(ctx, id)
	if err != nil {
		return nil, err
	}

	left := map[string]float64{}

	for _, m := range models {
		if m.Remaining >= 0 {
			left[m.Name] = m.Remaining
		}
	}

	return left, nil
}

// CoolDown keeps an account out of use until its usage limit ends.
func (s *Service) CoolDown(ctx context.Context, runtime, accountID string, until time.Time, reason string) error {
	return s.accounts.CoolAccount(ctx, runtime, accountID, until, reason)
}

func (s *Service) tokenOf(ctx context.Context, a orchestration.Account, retry *atomic.Int64) (Token, error) {
	name, ok := vault.NameFromRef(a.SecretRef)
	if !ok {
		return Token{}, fmt.Errorf("%w: account %s", ErrNoTokens, a.ID)
	}

	opened, err := s.vault.Open(ctx, name)
	if err != nil {
		return Token{}, fmt.Errorf("opening the tokens of account %s: %w", a.ID, err)
	}

	// An OAuth login keeps the token under KeyAccessToken; an account added by
	// hand with its own token (accounts add --token) keeps it under KeyValue.
	// The gateway serves either, so a Claude token pasted in by hand works too.
	access := opened.Payload[vault.KeyAccessToken]
	if access == "" {
		access = opened.Payload[vault.KeyValue]
	}

	token := Token{
		AccountID: a.ID, ExternalID: a.ExternalID, AccessToken: access,
		Project: a.Project, Plan: a.Plan, Version: opened.Version,
	}
	if a.Runtime == antigravityRuntime {
		token.VerificationRetry = retry
		mark, err := s.currentVerification(ctx, token)
		if err != nil {
			return Token{}, err
		}

		token.Verifying = mark != nil
		token.checkVerification = func(ctx context.Context) error { return s.CheckVerification(ctx, token) }
	}

	return token, nil
}

// RefreshDue refreshes every account whose access token ends within its
// vendor's lead. An account that needs a new login, is marked for verification,
// or whose last refresh failed and is not due for another try is left alone.
// It returns what failed, joined.
func (s *Service) RefreshDue(ctx context.Context) error {
	accounts, err := s.List(ctx)
	if err != nil {
		return err
	}

	var failed []error

	now := s.clock.Now()
	refreshed := false

	for _, a := range accounts {
		vendor, ok := s.vendors[a.Runtime]
		if !ok || a.State != StateActive || a.AccessTokenExpiresAt == nil {
			continue
		}

		if a.AccessTokenExpiresAt.Sub(now) > vendor.RefreshLead() {
			continue
		}

		if a.Refresh.RetryAt != nil && now.Before(*a.Refresh.RetryAt) {
			continue
		}
		if a.Verification != nil {
			continue
		}

		// Accounts are refreshed one after another with a pause between
		// them, as 9router-go's background refresh does.
		if refreshed {
			select {
			case <-ctx.Done():
				return errors.Join(append(failed, ctx.Err())...)
			case <-s.after(vendor.RefreshSpacing()):
			}
		}

		if err := s.refresh(ctx, a, 0, nil); err != nil {
			var verify *VerificationError
			if errors.As(err, &verify) {
				continue
			}

			failed = append(failed, err)
		}
		refreshed = true
	}

	return errors.Join(failed...)
}

// Loop runs RefreshDue now and then every five minutes, until ctx ends.
func (s *Service) Loop(ctx context.Context) {
	ticker := time.NewTicker(refreshEvery)
	defer ticker.Stop()

	for {
		if err := s.RefreshDue(ctx); err != nil && ctx.Err() == nil {
			s.logger.WarnContext(ctx, "refreshing subscription accounts",
				slog.String("event", "subscription.refresh_failed"), slog.Any("error", err))
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// refresh exchanges the account's refresh token through the vault and
// records how it went. stale is the version of the tokens found too old; zero
// means the ones in the vault now.
func (s *Service) refresh(ctx context.Context, a Account, stale int, retry *atomic.Int64) error {
	vendor, ok := s.vendors[a.Runtime]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownVendor, a.Runtime)
	}

	name, ok := vault.NameFromRef(a.SecretRef)
	if !ok || a.State == StateNoTokens {
		return fmt.Errorf("%w: account %s", ErrNoTokens, a.ID)
	}

	if stale == 0 {
		opened, err := s.vault.Open(ctx, name)
		if err != nil {
			return fmt.Errorf("opening the tokens of account %s: %w", a.ID, err)
		}

		stale = opened.Version
	}
	check := Token{AccountID: a.ID, Version: stale, VerificationRetry: retry}
	exchange := vendor.Refresh
	if a.Runtime == antigravityRuntime {
		if _, err := s.currentVerification(ctx, check); err != nil {
			return err
		}

		exchange = func(ctx context.Context, payload vault.Payload) (vault.Refreshed, error) {
			var err error
			if retry == nil {
				_, err = s.currentVerification(ctx, check)
			} else {
				err = s.CheckVerification(ctx, check)
			}
			if err != nil {
				return vault.Refreshed{}, err
			}

			return vendor.Refresh(ctx, payload)
		}
	}

	refreshed, err := s.vault.Refresh(ctx, name, stale, exchange)
	if err != nil {
		var verify *VerificationError
		if errors.As(err, &verify) {
			return verify
		}

		failed := errors.Join(
			fmt.Errorf("refreshing account %s: %w", a.ID, err),
			s.accounts.RecordAccountRefresh(ctx, a.ID, s.failed(a, err)))
		if a.Runtime == antigravityRuntime {
			if _, rest := s.currentVerification(ctx, check); rest != nil {
				if errors.As(rest, &verify) {
					return verify
				}

				return errors.Join(rest, failed)
			}
		}

		return failed
	}

	if err := s.accounts.RecordAccountRefresh(ctx, a.ID, orchestration.AccountRefresh{}); err != nil {
		return err
	}

	// The plan can change between two refreshes; the id cannot.
	who, err := vendor.Identify(refreshed.Payload)
	if err != nil || (who.Plan == a.Plan && who.Email == a.Email) {
		return nil //nolint:nilerr // A token set that names nobody keeps what the login recorded.
	}

	return s.accounts.SetAccountPlan(ctx, a.ID, who.Email, who.Plan)
}

// failed is what to record after a refresh that did not work. A login the
// vendor refused is not tried again; anything else is, later and later.
func (s *Service) failed(a Account, cause error) orchestration.AccountRefresh {
	if errors.Is(cause, vault.ErrLoginRequired) {
		return orchestration.AccountRefresh{Error: cause.Error()}
	}

	failures := a.Refresh.Failures + 1

	wait := retryMax
	if failures <= 9 { // 5 minutes doubled 9 times is past a day.
		wait = min(retryFirst<<(failures-1), retryMax)
	}

	retryAt := s.clock.Now().Add(wait)

	return orchestration.AccountRefresh{Failures: failures, RetryAt: &retryAt, Error: cause.Error()}
}

// Remove forgets an account that was logged in here, and its tokens.
func (s *Service) Remove(ctx context.Context, id string) error {
	a, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	name, ok := vault.NameFromRef(a.SecretRef)
	if !ok {
		return fmt.Errorf("%w: account %s", ErrNoTokens, id)
	}

	// The account goes first: the vault refuses to delete a secret an
	// account still names.
	if err := s.accounts.RemoveAccountLogin(ctx, id); err != nil {
		return err
	}

	if err := s.vault.Delete(ctx, name); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return fmt.Errorf("removing the tokens of account %s: %w", id, err)
	}

	return nil
}

// pasteError is a paste that cannot be read: its own words, and ErrBadPaste
// for errors.Is.
type pasteError string

func (e pasteError) Error() string { return string(e) }

func (pasteError) Is(target error) bool { return target == ErrBadPaste }
