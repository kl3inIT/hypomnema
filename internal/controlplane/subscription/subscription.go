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
	"log/slog"
	"strings"
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
	// ErrNoAccount is returned when no account of a runtime can be used:
	// none was logged in here, or every one is disabled or needs a new login.
	ErrNoAccount = errors.New("no account logged in here can be used")
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
	// Version is the version of the account's secret the token is from.
	Version int
}

// Identity is who an account is at its vendor. None of it is secret.
type Identity struct {
	// ExternalID is the vendor's own id of the account.
	ExternalID string
	Email      string
	Plan       string
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
}

// Accounts is what the service needs of the store of accounts
// (orchestration.Service).
type Accounts interface {
	SaveAccountLogin(
		ctx context.Context, in orchestration.AccountLogin, secretRef func(accountID string) string,
	) (orchestration.Account, error)
	ListAccounts(ctx context.Context) ([]orchestration.Account, error)
	SetAccountPlan(ctx context.Context, id, email, plan string) error
	CoolAccount(ctx context.Context, runtime, id string, until time.Time, reason string) error
	RecordAccountRefresh(ctx context.Context, id string, r orchestration.AccountRefresh) error
	RemoveAccountLogin(ctx context.Context, id string) error
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
}

// New returns a Service over the accounts, the vault and the vendors.
func New(
	accounts Accounts, v *vault.Service, clock platform.Clock, logger *slog.Logger, vendors ...Vendor,
) *Service {
	s := &Service{accounts: accounts, vault: v, clock: clock, logger: logger, vendors: map[string]Vendor{}}
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

// Complete finishes a login and stores it: a new account, or new tokens for
// the account the vendor's id already has. pasted is the address the browser
// ended on, for an attempt that asks for it; label names a new account to the
// owner, and is its email when empty.
func (s *Service) Complete(ctx context.Context, a *Attempt, pasted, label string) (Account, error) {
	login, err := a.finish(ctx, pasted)
	if err != nil {
		return Account{}, fmt.Errorf("%w: %w", ErrLoginFailed, err)
	}

	acct, err := s.accounts.SaveAccountLogin(ctx, orchestration.AccountLogin{
		Runtime: a.Runtime, ExternalID: login.ExternalID, Email: login.Email, Plan: login.Plan,
		Project: login.Project, Label: label,
	}, func(id string) string { return vault.Ref(secretName(id)) })
	if err != nil {
		return Account{}, err
	}

	name, ok := vault.NameFromRef(acct.SecretRef)
	if !ok {
		return Account{}, fmt.Errorf("%w: account %s", ErrNoTokens, acct.ID)
	}

	_, err = s.vault.Get(ctx, name)

	switch {
	case err == nil:
		_, err = s.vault.RotateTokenSet(ctx, name, login.Tokens)
	case errors.Is(err, vault.ErrNotFound):
		_, err = s.vault.Create(ctx, vault.NewSecret{
			Name: name, Kind: vault.KindOAuthTokenSet, Payload: login.Tokens.Payload,
			Metadata:             map[string]string{"account": acct.ID, "runtime": acct.Runtime},
			AccessTokenExpiresAt: login.Tokens.AccessTokenExpiresAt,
		})
	}

	if err != nil {
		return Account{}, fmt.Errorf("storing the tokens of account %s: %w", acct.ID, err)
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

	if err := s.refresh(ctx, a, 0); err != nil {
		return Account{}, err
	}

	return s.Get(ctx, id)
}

// Token returns a valid access token of the first account of a runtime that
// can be used: active, logged in here, and not waiting for a usage limit. A
// token about to end is refreshed first.
func (s *Service) Token(ctx context.Context, runtime string) (Token, error) {
	accounts, err := s.List(ctx)
	if err != nil {
		return Token{}, err
	}

	var free *time.Time

	for _, a := range accounts {
		if a.Runtime != runtime || a.Status != orchestration.AccountActive || a.State != StateActive {
			continue
		}

		if a.CoolingUntil != nil {
			if free == nil || a.CoolingUntil.Before(*free) {
				free = a.CoolingUntil
			}

			continue
		}

		if a.AccessTokenExpiresAt != nil && a.AccessTokenExpiresAt.Sub(s.clock.Now()) < useLead {
			if err := s.refresh(ctx, a, 0); err != nil {
				return Token{}, err
			}
		}

		return s.tokenOf(ctx, a.Account)
	}

	if free != nil {
		return Token{}, &CoolingError{Until: *free}
	}

	return Token{}, fmt.Errorf("%w: %s", ErrNoAccount, runtime)
}

// TokenAfterRefusal returns a new token for an account whose token the vendor
// refused. The refresh token is exchanged unless the account's tokens were
// replaced since the refused one was read: then those are returned.
func (s *Service) TokenAfterRefusal(ctx context.Context, refused Token) (Token, error) {
	a, err := s.Get(ctx, refused.AccountID)
	if err != nil {
		return Token{}, err
	}

	if err := s.refresh(ctx, a, refused.Version); err != nil {
		return Token{}, err
	}

	return s.tokenOf(ctx, a.Account)
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
}

// ModelLister is a Vendor that can say which models it serves an account.
type ModelLister interface {
	Models(ctx context.Context, token Token) ([]ServedModel, error)
}

// ErrNoModelList is returned for an account whose vendor lists no models.
var ErrNoModelList = errors.New("this kind of account does not list its models")

// Models asks the vendor which models it serves an account, by name.
func (s *Service) Models(ctx context.Context, id string) ([]ServedModel, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	lister, ok := s.vendors[a.Runtime].(ModelLister)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoModelList, a.Runtime)
	}

	if a.State != StateActive {
		return nil, fmt.Errorf("%w: account %s is %s", ErrNoTokens, id, cmpState(a.State))
	}

	if a.AccessTokenExpiresAt != nil && a.AccessTokenExpiresAt.Sub(s.clock.Now()) < useLead {
		if err := s.refresh(ctx, a, 0); err != nil {
			return nil, err
		}
	}

	token, err := s.tokenOf(ctx, a.Account)
	if err != nil {
		return nil, err
	}

	return lister.Models(ctx, token)
}

func cmpState(state State) string {
	if state == "" {
		return "not logged in here"
	}

	return string(state)
}

// CoolDown keeps an account out of use until its usage limit ends.
func (s *Service) CoolDown(ctx context.Context, runtime, accountID string, until time.Time, reason string) error {
	return s.accounts.CoolAccount(ctx, runtime, accountID, until, reason)
}

func (s *Service) tokenOf(ctx context.Context, a orchestration.Account) (Token, error) {
	name, ok := vault.NameFromRef(a.SecretRef)
	if !ok {
		return Token{}, fmt.Errorf("%w: account %s", ErrNoTokens, a.ID)
	}

	opened, err := s.vault.Open(ctx, name)
	if err != nil {
		return Token{}, fmt.Errorf("opening the tokens of account %s: %w", a.ID, err)
	}

	return Token{
		AccountID: a.ID, ExternalID: a.ExternalID, AccessToken: opened.Payload[vault.KeyAccessToken],
		Project: a.Project, Version: opened.Version,
	}, nil
}

// RefreshDue refreshes every account whose access token ends within its
// vendor's lead. An account that needs a new login, or whose last refresh
// failed and is not due for another try, is left alone. It returns what
// failed, joined.
func (s *Service) RefreshDue(ctx context.Context) error {
	accounts, err := s.List(ctx)
	if err != nil {
		return err
	}

	var failed []error

	now := s.clock.Now()

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

		if err := s.refresh(ctx, a, 0); err != nil {
			failed = append(failed, err)
		}
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
func (s *Service) refresh(ctx context.Context, a Account, stale int) error {
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

	refreshed, err := s.vault.Refresh(ctx, name, stale, vendor.Refresh)
	if err != nil {
		return errors.Join(
			fmt.Errorf("refreshing account %s: %w", a.ID, err),
			s.accounts.RecordAccountRefresh(ctx, a.ID, s.failed(a, err)))
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
