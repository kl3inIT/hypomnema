package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Errors. They wrap orchestration's, so both APIs answer them as they answer
// that package's.
var (
	ErrInvalid  = orchestration.ErrInvalid
	ErrNotFound = orchestration.ErrNotFound
	// ErrSignInRequired: the provider refused the Connection's refresh token
	// for good; the owner signs in again.
	ErrSignInRequired = fmt.Errorf("%w: the account must sign in again", orchestration.ErrConflict)
	// ErrSignInFailed: a sign-in did not complete; nothing was stored.
	ErrSignInFailed = fmt.Errorf("%w: the sign-in did not complete", orchestration.ErrInvalid)
)

const (
	// redirect is where the browser is sent after consent. Nothing listens
	// there: the owner copies the address the browser ends on, as Hermes'
	// google-workspace skill has it ("Google deprecated OOB, so we use a
	// localhost redirect", scripts/setup.py) and as this repository's
	// subscription logins do.
	redirect = "http://localhost:1"
	// signInTimeout is how long a started sign-in can be completed, as the
	// subscription logins.
	signInTimeout = 30 * time.Minute
	// refreshLead: a token asked for with less than this left is refreshed
	// first. Onyx refreshes lazily the same way, without a scheduled job.
	refreshLead = 2 * time.Minute
	httpTimeout = 30 * time.Second
	// maxAnswerBytes bounds a provider's answer.
	maxAnswerBytes = 1 << 20
	// maxClientBytes bounds a client's id or secret.
	maxClientBytes = 1024
)

// Store is what the service needs of the store of Connections
// (orchestration.Service).
type Store interface {
	SaveServiceConnection(ctx context.Context, c orchestration.Connection) (orchestration.Connection, error)
	ConnectionOf(ctx context.Context, integration, label string) (orchestration.Connection, error)
	ConnectionsOf(ctx context.Context, integration string) ([]orchestration.Connection, error)
	RemoveConnection(ctx context.Context, integration, label string) (orchestration.Connection, error)
	SetConnectionChats(ctx context.Context, integration, label string, allowed bool) (orchestration.Connection, error)
	SetIntegrationClient(ctx context.Context, integration, clientID, secret string) error
	IntegrationClientOf(ctx context.Context, integration string) (orchestration.IntegrationClient, error)
}

// Service signs in and refreshes the Connections of the Integrations it was
// given.
type Service struct {
	store  Store
	vault  *vault.Service
	defs   []Definition
	client *http.Client
	clock  platform.Clock
	newID  func(prefix string) string

	mu      sync.Mutex
	signIns map[string]*signIn
}

// signIn is one sign-in the owner started and has not completed.
type signIn struct {
	def      Definition
	label    string
	asked    []Grant
	state    string
	verifier string
	started  time.Time
}

// New returns a Service for the given Integrations.
func New(store Store, secrets *vault.Service, clock platform.Clock, newID func(string) string, defs ...Definition,
) *Service {
	return &Service{
		store: store, vault: secrets, defs: defs, client: &http.Client{Timeout: httpTimeout}, clock: clock,
		newID: newID, signIns: map[string]*signIn{},
	}
}

// Connection is one account of an Integration, with whether it still works.
type Connection struct {
	Integration string
	Label       string
	// Account is the account's own name at the service: an email.
	Account string
	Grants  []Grant
	// SignInRequired says the provider refused the refresh token for good.
	SignInRequired bool
	// Chats says the coordinator may use the Connection in chats.
	Chats     bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Integration is a Definition with what the owner set up for it.
type Integration struct {
	Definition

	// ClientID is the id of the OAuth client it signs in with; empty when
	// none was set, and then no account can be connected.
	ClientID    string
	Connections []Connection
}

// SignIn is a sign-in in progress. The owner opens URL in a browser on any
// machine and signs in; the browser ends on an address that does not load,
// which they copy and give to Complete with the ID.
type SignIn struct {
	ID        string
	URL       string
	ExpiresIn time.Duration
}

func (s *Service) definition(id string) (Definition, error) {
	for _, d := range s.defs {
		if d.ID == id {
			return d, nil
		}
	}

	return Definition{}, fmt.Errorf("%w: no integration %q signs in with an account", ErrNotFound, id)
}

// Has says whether id is an Integration of this service.
func (s *Service) Has(id string) bool {
	_, err := s.definition(id)

	return err == nil
}

func clientSecretName(integration string) string { return "integration-" + integration + "-client" }

func connectionSecretName(integration, label string) string {
	return "conn-" + integration + "-" + label
}

// List returns every Integration with its client and its Connections.
func (s *Service) List(ctx context.Context) ([]Integration, error) {
	out := make([]Integration, 0, len(s.defs))

	for _, d := range s.defs {
		in := Integration{Definition: d}

		client, err := s.store.IntegrationClientOf(ctx, d.ID)
		if err != nil && !errors.Is(err, orchestration.ErrNotFound) {
			return nil, err
		}

		in.ClientID = client.ClientID

		conns, err := s.store.ConnectionsOf(ctx, d.ID)
		if err != nil {
			return nil, err
		}

		in.Connections = make([]Connection, 0, len(conns))

		for _, c := range conns {
			in.Connections = append(in.Connections, s.view(ctx, d, c))
		}

		out = append(out, in)
	}

	return out, nil
}

// view is a stored Connection with the state of its token set. A secret that
// cannot be described counts as needing a sign-in: nothing can be done with
// the Connection until there is one.
func (s *Service) view(ctx context.Context, d Definition, c orchestration.Connection) Connection {
	out := Connection{
		Integration: c.Integration, Label: c.Label, Account: c.Account, Chats: c.Chats,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}

	// What is stored was checked when it was written; a service since removed from the catalog is left out.
	out.Grants, _ = d.ParseGrants(c.Services)

	secret, err := s.vault.Get(ctx, c.Secret)
	out.SignInRequired = err != nil || secret.LoginRequired || secret.KeyMissing

	return out
}

// SetClient keeps the OAuth client an Integration signs in with, replacing
// the one it had. Connections made with the old client stop refreshing when
// it is another client: they are signed in again.
func (s *Service) SetClient(ctx context.Context, integration, clientID, clientSecret string) error {
	d, err := s.definition(integration)
	if err != nil {
		return err
	}

	clientID, clientSecret = strings.TrimSpace(clientID), strings.TrimSpace(clientSecret)
	if clientID == "" || clientSecret == "" || len(clientID) > maxClientBytes || len(clientSecret) > maxClientBytes ||
		strings.ContainsAny(clientID+clientSecret, " \t\r\n") {
		return fmt.Errorf("%w: a client is its id and its secret, each one line without spaces", ErrInvalid)
	}

	name, payload := clientSecretName(d.ID), vault.Payload{vault.KeyValue: clientSecret}

	_, err = s.vault.Create(ctx, vault.NewSecret{
		Name: name, Kind: vault.KindGeneric, Payload: payload,
		Metadata: map[string]string{"integration": d.ID, "what": "the secret of its OAuth client"},
	})
	if errors.Is(err, vault.ErrConflict) {
		_, err = s.vault.Rotate(ctx, name, payload)
	}

	if err != nil {
		return vaultError(err)
	}

	return s.store.SetIntegrationClient(ctx, d.ID, clientID, name)
}

// oauthClient is an Integration's client with its secret opened.
type oauthClient struct{ id, secret string }

func (s *Service) oauthClient(ctx context.Context, d Definition) (oauthClient, error) {
	client, err := s.store.IntegrationClientOf(ctx, d.ID)
	if errors.Is(err, orchestration.ErrNotFound) {
		return oauthClient{}, fmt.Errorf("%w: %s has no OAuth client yet; set one with "+
			"`hypomnema integrations client set %s --client-file FILE`", orchestration.ErrConflict, d.ID, d.ID)
	}

	if err != nil {
		return oauthClient{}, err
	}

	opened, err := s.vault.Open(ctx, client.Secret)
	if err != nil {
		return oauthClient{}, vaultError(err)
	}

	return oauthClient{id: client.ClientID, secret: opened.Payload[vault.KeyValue]}, nil
}

// Begin starts a sign-in for a Connection under a label, asking for the given
// services. A label that exists is signed in again and takes the new grants.
func (s *Service) Begin(ctx context.Context, integration, label string, services []string) (SignIn, error) {
	d, err := s.definition(integration)
	if err != nil {
		return SignIn{}, err
	}

	if !orchestration.ValidConnectionLabel(label) {
		return SignIn{}, fmt.Errorf("%w: a label is lower-case letters, digits and dashes, such as work", ErrInvalid)
	}

	asked, err := d.ParseGrants(services)
	if err != nil {
		return SignIn{}, err
	}

	client, err := s.oauthClient(ctx, d)
	if err != nil {
		return SignIn{}, err
	}

	// PKCE and a state, kept here until the owner brings the address back, as Hermes keeps them between its
	// two commands (google_oauth_pending.json) and as the Codex login in this repository does.
	verifier := base64.RawURLEncoding.EncodeToString(randomBytes(64))
	challenge := sha256.Sum256([]byte(verifier))
	pending := &signIn{
		def: d, label: label, asked: asked, state: hex.EncodeToString(randomBytes(16)), verifier: verifier,
		started: s.clock.Now(),
	}

	query := url.Values{
		// offline and consent make Google send a refresh token every time (Hermes; the Antigravity login).
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"client_id":             {client.id},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {strings.Join(d.scopes(asked), " ")},
		"state":                 {pending.state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}

	id := s.newID("signin")

	s.mu.Lock()
	defer s.mu.Unlock()

	for old, p := range s.signIns {
		if pending.started.Sub(p.started) > signInTimeout {
			delete(s.signIns, old)
		}
	}

	s.signIns[id] = pending

	return SignIn{ID: id, URL: d.AuthURL + "?" + query.Encode(), ExpiresIn: signInTimeout}, nil
}

// Complete finishes a sign-in with the address the browser ended on and
// stores it: a new Connection, or new tokens and grants for the label's.
func (s *Service) Complete(ctx context.Context, integration, id, pasted string) (Connection, error) {
	s.mu.Lock()
	pending, ok := s.signIns[id]
	s.mu.Unlock()

	if !ok || pending.def.ID != integration || s.clock.Now().Sub(pending.started) > signInTimeout {
		return Connection{}, fmt.Errorf("%w: no sign-in %q is waiting; it was completed, is older than %d minutes, "+
			"or the control plane restarted. Start it again", ErrNotFound, id, int(signInTimeout.Minutes()))
	}

	d := pending.def

	code, err := subscription.CallbackCode(pasted, pending.state)
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	// A code is spent by its first exchange, so the sign-in is too.
	s.mu.Lock()
	delete(s.signIns, id)
	s.mu.Unlock()

	client, err := s.oauthClient(ctx, d)
	if err != nil {
		return Connection{}, err
	}

	tokens, err := s.token(ctx, d, client, "exchanging the code for tokens", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {pending.verifier},
	})
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	if tokens.RefreshToken == "" {
		return Connection{}, fmt.Errorf("%w: %s gave no refresh token; remove Hypomnema's access in the account's "+
			"security settings, then sign in again", ErrSignInFailed, d.Name)
	}

	grants := d.granted(pending.asked, strings.Fields(tokens.Scope))
	if len(grants) == 0 {
		return Connection{}, fmt.Errorf("%w: none of the services asked for was allowed on the consent page",
			ErrSignInFailed)
	}

	account, err := s.account(ctx, d, tokens.AccessToken)
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	login := vault.Refreshed{
		Payload: vault.Payload{
			vault.KeyAccessToken: tokens.AccessToken, vault.KeyRefreshToken: tokens.RefreshToken,
		},
		AccessTokenExpiresAt: s.expiry(tokens),
	}

	return s.save(ctx, d, pending.label, account, grants, login)
}

func (s *Service) save(
	ctx context.Context, d Definition, label, account string, grants []Grant, login vault.Refreshed,
) (Connection, error) {
	name := connectionSecretName(d.ID, label)

	_, err := s.vault.Get(ctx, name)

	switch {
	case err == nil:
		_, err = s.vault.RotateTokenSet(ctx, name, login)
	case errors.Is(err, vault.ErrNotFound):
		_, err = s.vault.Create(ctx, vault.NewSecret{
			Name: name, Kind: vault.KindOAuthTokenSet, Payload: login.Payload,
			Metadata:             map[string]string{"integration": d.ID, "connection": label, "account": account},
			AccessTokenExpiresAt: login.AccessTokenExpiresAt,
		})
	}

	if err != nil {
		return Connection{}, vaultError(err)
	}

	services := make([]string, 0, len(grants))
	for _, g := range grants {
		services = append(services, g.String())
	}

	saved, err := s.store.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: d.ID, Label: label, Account: account, Secret: name, Services: services,
	})
	if err != nil {
		return Connection{}, err
	}

	return s.view(ctx, d, saved), nil
}

// SetChats says whether the coordinator may use a Connection in chats.
func (s *Service) SetChats(ctx context.Context, integration, label string, allowed bool) (Connection, error) {
	d, err := s.definition(integration)
	if err != nil {
		return Connection{}, err
	}

	changed, err := s.store.SetConnectionChats(ctx, d.ID, label, allowed)
	if err != nil {
		return Connection{}, err
	}

	return s.view(ctx, d, changed), nil
}

// Token is a Connection's access token, for a caller inside the control plane
// that hands it on. The caller keeps it out of logs and errors.
type Token struct {
	AccessToken string
	// ExpiresAt is when it ends; zero when the provider did not say.
	ExpiresAt time.Time
}

// Token returns a Connection's access token, refreshed first when it has
// less than two minutes left. ErrSignInRequired when the provider refused the
// refresh token for good.
func (s *Service) Token(ctx context.Context, integration, label string) (Token, error) {
	d, err := s.definition(integration)
	if err != nil {
		return Token{}, err
	}

	conn, err := s.store.ConnectionOf(ctx, d.ID, label)
	if err != nil {
		return Token{}, err
	}

	opened, err := s.vault.Open(ctx, conn.Secret)
	if err != nil {
		return Token{}, vaultError(err)
	}

	if fresh(opened, s.clock.Now()) {
		return tokenOf(opened), nil
	}

	if opened.LoginRequired {
		return Token{}, fmt.Errorf("%w: %s:%s", ErrSignInRequired, d.ID, label)
	}

	opened, err = s.vault.Refresh(ctx, conn.Secret, opened.Version, func(ctx context.Context, current vault.Payload,
	) (vault.Refreshed, error) {
		return s.refresh(ctx, d, current)
	})
	if errors.Is(err, vault.ErrLoginRequired) {
		return Token{}, fmt.Errorf("%w: %s:%s", ErrSignInRequired, d.ID, label)
	}

	if err != nil {
		return Token{}, fmt.Errorf("refreshing the token of %s:%s: %w", d.ID, label, err)
	}

	return tokenOf(opened), nil
}

func fresh(o vault.Opened, now time.Time) bool {
	return o.Payload[vault.KeyAccessToken] != "" && o.AccessTokenExpiresAt != nil &&
		o.AccessTokenExpiresAt.Sub(now) > refreshLead
}

func tokenOf(o vault.Opened) Token {
	t := Token{AccessToken: o.Payload[vault.KeyAccessToken]}
	if o.AccessTokenExpiresAt != nil {
		t.ExpiresAt = *o.AccessTokenExpiresAt
	}

	return t
}

// refresh is the vault.RefreshFunc of an Integration: a provider that sends
// no new refresh token keeps the old one, as Google does.
func (s *Service) refresh(ctx context.Context, d Definition, current vault.Payload) (vault.Refreshed, error) {
	client, err := s.oauthClient(ctx, d)
	if err != nil {
		return vault.Refreshed{}, err
	}

	tokens, err := s.token(ctx, d, client, "refreshing the token", url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {current[vault.KeyRefreshToken]},
	})
	if err != nil {
		return vault.Refreshed{}, err
	}

	kept := tokens.RefreshToken
	if kept == "" {
		kept = current[vault.KeyRefreshToken]
	}

	return vault.Refreshed{
		Payload:              vault.Payload{vault.KeyAccessToken: tokens.AccessToken, vault.KeyRefreshToken: kept},
		AccessTokenExpiresAt: s.expiry(tokens),
	}, nil
}

// Disconnect forgets a Connection and its tokens, after asking the provider
// to end them (Hermes' revoke). A provider that cannot be reached does not
// keep the Connection here: the owner can still remove the access in the
// account's own settings, and the answer says so.
func (s *Service) Disconnect(ctx context.Context, integration, label string) (revoked bool, err error) {
	d, err := s.definition(integration)
	if err != nil {
		return false, err
	}

	conn, err := s.store.ConnectionOf(ctx, d.ID, label)
	if err != nil {
		return false, err
	}

	if opened, err := s.vault.Open(ctx, conn.Secret); err == nil {
		revoked = s.revoke(ctx, d, opened.Payload[vault.KeyRefreshToken]) == nil
	}

	if _, err := s.store.RemoveConnection(ctx, d.ID, label); err != nil {
		return false, err
	}

	if err := s.vault.Delete(ctx, conn.Secret); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return revoked, vaultError(err)
	}

	return revoked, nil
}

type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

func (s *Service) expiry(t tokenAnswer) time.Time {
	if t.ExpiresIn <= 0 {
		return time.Time{}
	}

	return s.clock.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
}

// token calls the provider's token endpoint. A grant refused for good
// (invalid_grant: revoked, expired, or the password changed) wraps
// vault.ErrLoginRejected. Its errors hold the status and the error code only:
// the body may repeat a token.
func (s *Service) token(ctx context.Context, d Definition, client oauthClient, doing string, form url.Values,
) (tokenAnswer, error) {
	form.Set("client_id", client.id)
	form.Set("client_secret", client.secret)

	status, body, err := s.post(ctx, d.TokenURL, form)
	if err != nil {
		return tokenAnswer{}, fmt.Errorf("%s: %w", doing, err)
	}

	if status != http.StatusOK {
		code := errorCode(body)

		err := fmt.Errorf("%s: status %d %s", doing, status, code)
		if code == "invalid_grant" {
			return tokenAnswer{}, fmt.Errorf("%w: %w", vault.ErrLoginRejected, err)
		}

		return tokenAnswer{}, err
	}

	var answer tokenAnswer
	if err := json.Unmarshal(body, &answer); err != nil || answer.AccessToken == "" {
		return tokenAnswer{}, fmt.Errorf("%s: the answer lacks an access token", doing)
	}

	return answer, nil
}

// account reads whose account signed in.
func (s *Service) account(ctx context.Context, d Definition, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.UserInfoURL, nil)
	if err != nil {
		return "", fmt.Errorf("reading who signed in: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	status, body, err := s.do(req)
	if err != nil {
		return "", fmt.Errorf("reading who signed in: %w", err)
	}

	var user struct {
		Email string `json:"email"`
	}

	if status != http.StatusOK || json.Unmarshal(body, &user) != nil || user.Email == "" {
		return "", fmt.Errorf("reading who signed in: status %d, and no email in the answer", status)
	}

	return user.Email, nil
}

func (s *Service) revoke(ctx context.Context, d Definition, token string) error {
	if d.RevokeURL == "" || token == "" {
		return errors.New("nothing to revoke")
	}

	status, _, err := s.post(ctx, d.RevokeURL, url.Values{"token": {token}})
	if err != nil {
		return err
	}

	if status != http.StatusOK {
		return fmt.Errorf("revoking: status %d", status)
	}

	return nil
}

func (s *Service) post(ctx context.Context, address string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, fmt.Errorf("building the request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return s.do(req)
}

// do sends a request and returns the status and the answer. Its errors hold
// neither the request nor the answer.
func (s *Service) do(req *http.Request) (int, []byte, error) {
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // net/http would repeat the address, and with it a query.
		}

		return 0, nil, fmt.Errorf("calling %s: %w", req.URL.Host, err)
	}
	defer resp.Body.Close() //nolint:errcheck // The answer was read or the read failed; closing adds nothing.

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("reading the answer of %s: %w", req.URL.Host, err)
	}

	return resp.StatusCode, body, nil
}

// errorCode is the "error" of an OAuth error body when it is a short word,
// and nothing otherwise: a body is not trusted to hold no token.
func errorCode(body []byte) string {
	var parsed struct {
		Error string `json:"error"`
	}

	if json.Unmarshal(body, &parsed) != nil || len(parsed.Error) > 64 || strings.ContainsAny(parsed.Error, " \t\r\n") {
		return ""
	}

	return parsed.Error
}

// vaultError turns the vault's errors into the ones the APIs answer with. The
// vault's errors never carry a value.
func vaultError(err error) error {
	switch {
	case errors.Is(err, vault.ErrNotConfigured):
		return fmt.Errorf("%w: the vault has no key, so no sign-in can be kept; set HYPOMNEMA_VAULT_KEYS "+
			"(see `hypomnema secrets keygen`)", orchestration.ErrUnavailable)
	case errors.Is(err, vault.ErrInvalid):
		return fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	default:
		return fmt.Errorf("the vault: %w", err)
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms.

	return b
}

// Tools are the tools a node of a system is told to have: the tool of each
// Integration that has a Connection (internal/shared/agenttool), when it has
// a release for that system. An Integration nobody signed in to asks nothing
// of a node. A list that cannot be read is left out: the node keeps what it
// has.
func (s *Service) Tools(ctx context.Context, os, arch string) []nodeproto.Tool {
	var out []nodeproto.Tool

	for _, d := range s.defs {
		def, ok := agenttool.Of(d.ID)
		if !ok {
			continue
		}

		tool, ok := def.For(os, arch)
		if !ok {
			continue
		}

		conns, err := s.store.ConnectionsOf(ctx, d.ID)
		if err != nil || len(conns) == 0 {
			continue
		}

		out = append(out, tool)
	}

	return out
}
