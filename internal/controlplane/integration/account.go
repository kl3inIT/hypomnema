package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// An Integration that is an account at an address of its own (KindAccount,
// issue #203): Superset. A Connection is the address, a user name and a
// password; the password is the Connection's vault secret. The control plane
// signs in with them and keeps the access and refresh tokens in memory, as
// an app's token is kept (app.go): nothing but the password is written. A
// request the service refuses for its token gets a new one, from the refresh
// token first and from the password after, and is sent again.
//
// The sign-in, the token asked for again when a request is refused and the
// CSRF token with its session cookie and Referer follow Winding2020/superset-mcp
// (src/client/base-client.ts, commit ff8e0eb, MIT per its package.json) and
// aptro/superset-mcp (main.py, commit b3dd425, MIT, Copyright (c) 2025 Aptro);
// they were tried against Superset 6.1.0 on 2026-11-12.

const (
	// accountTimeout bounds one request to the service: a chart's query can
	// take long (superset-mcp waits 120 s).
	accountTimeout = 120 * time.Second
	// maxAccountAnswerBytes bounds the service's answer to a relayed request:
	// a chart's rows, an export.
	maxAccountAnswerBytes = 64 << 20
	// maxAccountFieldBytes bounds an address, a user name or a password.
	maxAccountFieldBytes = 1024
)

// Account is what the owner gives to connect an Integration of kind account.
type Account struct {
	// Address is where the service is: https://superset.example.com.
	Address  string
	User     string
	Password string
}

// AccountRequest is one request to a Connection's service. Path and RawQuery
// are as the service reads them; the address, the token and what a request
// that is not a GET needs beside it are added here.
type AccountRequest struct {
	Method      string
	Path        string
	RawQuery    string
	ContentType string
	Body        []byte
}

// AccountAnswer is the service's answer, unchanged.
type AccountAnswer struct {
	Status      int
	ContentType string
	Body        []byte
}

// accountSession is what a Connection is signed in with. It lives in memory
// only.
type accountSession struct {
	access  string
	refresh string
	// csrf and cookie go with a request that is not a GET: the token Superset
	// gave and the session cookie it gave it with.
	csrf   string
	cookie string
}

// newAccountClient is the client for a Connection's own address. It follows
// no redirect: a service that sends a request elsewhere is answered with what
// it said, and the token goes nowhere else.
func newAccountClient() *http.Client {
	return &http.Client{
		Timeout:       accountTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func accountKey(integration, label string) string { return integration + ":" + label }

// accountAddress reads an address as the owner types it: http or https, a
// host, and at most a path the service is under. What would make a request
// read two ways (a query, a fragment, a user) is refused.
func accountAddress(given string) (string, error) {
	given = strings.TrimRight(strings.TrimSpace(given), "/")

	u, err := url.Parse(given)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || len(given) > maxAccountFieldBytes {
		return "", fmt.Errorf("%w: an address is http(s)://host[:port], with a path when the service is under one",
			ErrInvalid)
	}

	return given, nil
}

// ConnectAccount records an Integration of kind account as a Connection under
// a label, after signing in once: that shows the address and the account
// work. A refused sign-in stores nothing. A label that exists takes the new
// address, account and password.
func (s *Service) ConnectAccount(ctx context.Context, integration, label string, a Account) (Connection, error) {
	d, err := s.definition(integration)
	if err != nil {
		return Connection{}, err
	}

	if !d.IsAccount() {
		return Connection{}, fmt.Errorf("%w: %s is not connected with an address and a password", ErrInvalid, d.ID)
	}

	if !orchestration.ValidConnectionLabel(label) {
		return Connection{}, fmt.Errorf("%w: a label is lower-case letters, digits and dashes, such as work", ErrInvalid)
	}

	address, err := accountAddress(a.Address)
	if err != nil {
		return Connection{}, err
	}

	user := strings.TrimSpace(a.User)
	if user == "" || len(user) > maxAccountFieldBytes || strings.ContainsAny(user, "\r\n") ||
		a.Password == "" || len(a.Password) > maxAccountFieldBytes || strings.ContainsAny(a.Password, "\r\n") {
		return Connection{}, fmt.Errorf("%w: an account is its user name and its password, each one line", ErrInvalid)
	}

	session, err := s.signInAccount(ctx, d, address, user, a.Password)
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	name, payload := connectionSecretName(d.ID, label), vault.Payload{vault.KeyValue: a.Password}

	_, err = s.vault.Create(ctx, vault.NewSecret{
		Name: name, Kind: vault.KindGeneric, Payload: payload,
		Metadata: map[string]string{"integration": d.ID, "connection": label, "account": user, "what": "its password"},
	})
	if errors.Is(err, vault.ErrConflict) {
		_, err = s.vault.Rotate(ctx, name, payload)
	}

	if err != nil {
		return Connection{}, vaultError(err)
	}

	saved, err := s.store.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: d.ID, Label: label, Account: user, Secret: name, Address: address,
		Version: s.accountVersion(ctx, d, address, session.access),
	})
	if err != nil {
		return Connection{}, err
	}

	s.mu.Lock()
	s.accounts[accountKey(d.ID, label)] = session
	s.mu.Unlock()

	return s.view(ctx, d, saved), nil
}

// signInAccount signs a local account in at the service. Its errors hold the
// status only: the answer may repeat what was sent.
func (s *Service) signInAccount(ctx context.Context, d Definition, address, user, password string,
) (accountSession, error) {
	// provider "db" is Superset's own accounts, which sign in here even where its pages sign in elsewhere;
	// refresh asks for a refresh token too (superset-mcp, base-client.ts; tried 2026-11-12).
	body, err := json.Marshal(map[string]any{"username": user, "password": password, "provider": "db", "refresh": true})
	if err != nil {
		return accountSession{}, fmt.Errorf("signing in to %s: %w", d.Name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address+d.LoginPath, bytes.NewReader(body))
	if err != nil {
		return accountSession{}, fmt.Errorf("signing in to %s: the address cannot be called", d.Name)
	}

	req.Header.Set("Content-Type", "application/json")

	status, _, answer, err := s.accountDo(req)
	if err != nil {
		return accountSession{}, fmt.Errorf("signing in to %s: %w", d.Name, err)
	}

	var tokens struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}

	if status != http.StatusOK || json.Unmarshal(answer, &tokens) != nil || tokens.Access == "" {
		return accountSession{}, fmt.Errorf("%s refused the user name and password, or the address is not its API: "+
			"status %d", d.Name, status)
	}

	return accountSession{access: tokens.Access, refresh: tokens.Refresh}, nil
}

// refreshAccount asks for a new access token with the refresh token.
func (s *Service) refreshAccount(ctx context.Context, d Definition, address string, old accountSession,
) (accountSession, error) {
	if old.refresh == "" {
		return accountSession{}, errors.New("no refresh token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address+d.RefreshPath, nil)
	if err != nil {
		return accountSession{}, errors.New("the address cannot be called")
	}

	req.Header.Set("Authorization", "Bearer "+old.refresh)

	status, _, answer, err := s.accountDo(req)
	if err != nil {
		return accountSession{}, err
	}

	var tokens struct {
		Access string `json:"access_token"`
	}

	if status != http.StatusOK || json.Unmarshal(answer, &tokens) != nil || tokens.Access == "" {
		return accountSession{}, fmt.Errorf("status %d", status)
	}

	// The CSRF token was given to the old session; the next request that needs one asks again.
	return accountSession{access: tokens.Access, refresh: old.refresh}, nil
}

// accountVersion reads what the service says it runs; empty when it does not
// say.
func (s *Service) accountVersion(ctx context.Context, d Definition, address, access string) string {
	if d.VersionPath == "" {
		return ""
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address+d.VersionPath, nil)
	if err != nil {
		return ""
	}

	req.Header.Set("Authorization", "Bearer "+access)

	status, _, answer, err := s.accountDo(req)
	if err != nil || status != http.StatusOK {
		return ""
	}

	// Superset 6.1.0 answers {"version_string": "6.1.0", "version_sha": "", "build_number": null} (2026-11-12).
	var said struct {
		Version string `json:"version_string"`
	}

	if json.Unmarshal(answer, &said) != nil || len(said.Version) > 64 || strings.ContainsAny(said.Version, " \t\r\n") {
		return ""
	}

	return said.Version
}

// accountSession is what a Connection is signed in with: the one in memory,
// or a new sign-in with its password.
func (s *Service) accountSession(ctx context.Context, d Definition, conn orchestration.Connection,
) (accountSession, error) {
	s.mu.Lock()
	kept, ok := s.accounts[accountKey(d.ID, conn.Label)]
	s.mu.Unlock()

	if ok {
		return kept, nil
	}

	return s.signInAgain(ctx, d, conn)
}

// signInAgain signs a Connection in with its password and records whether
// the service took it, so that the owner sees an account that stopped
// working.
func (s *Service) signInAgain(ctx context.Context, d Definition, conn orchestration.Connection,
) (accountSession, error) {
	opened, err := s.vault.Open(ctx, conn.Secret)
	if err != nil {
		return accountSession{}, vaultError(err)
	}

	session, err := s.signInAccount(ctx, d, conn.Address, conn.Account, opened.Payload[vault.KeyValue])
	if err != nil {
		s.dropAccount(d.ID, conn.Label)
		// What could not be recorded is not worth failing the answer for: the sign-in itself is what failed.
		_ = s.store.SetConnectionSignInFailed(ctx, d.ID, conn.Label, true)

		return accountSession{}, fmt.Errorf("%w: %s:%s: %w", ErrSignInRequired, d.ID, conn.Label, err)
	}

	if conn.SignInFailed {
		_ = s.store.SetConnectionSignInFailed(ctx, d.ID, conn.Label, false)
	}

	s.keepAccount(d.ID, conn.Label, session)

	return session, nil
}

func (s *Service) keepAccount(integration, label string, session accountSession) {
	s.mu.Lock()
	s.accounts[accountKey(integration, label)] = session
	s.mu.Unlock()
}

func (s *Service) dropAccount(integration, label string) {
	s.mu.Lock()
	delete(s.accounts, accountKey(integration, label))
	s.mu.Unlock()
}

// forgetAccount removes a Connection of kind account, its password and what
// it was signed in with. Superset has nothing to end a token with: the one in
// memory is dropped and ends on its own.
func (s *Service) forgetAccount(ctx context.Context, d Definition, conn orchestration.Connection) error {
	if _, err := s.store.RemoveConnection(ctx, d.ID, conn.Label); err != nil {
		return err
	}

	s.dropAccount(d.ID, conn.Label)

	if err := s.vault.Delete(ctx, conn.Secret); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return vaultError(err)
	}

	return nil
}

// Call sends one request to a Connection's service with its token and
// returns the answer unchanged. A request refused for the token is sent
// again with a new one: from the refresh token first, then from the password
// (superset-mcp signs in again on a 401). ErrSignInRequired when the service
// no longer takes the password.
func (s *Service) Call(ctx context.Context, integration, label string, r AccountRequest) (AccountAnswer, error) {
	d, err := s.definition(integration)
	if err != nil {
		return AccountAnswer{}, err
	}

	if !d.IsAccount() {
		return AccountAnswer{}, fmt.Errorf("%w: %s has no address of its own to call", ErrInvalid, d.ID)
	}

	conn, err := s.store.ConnectionOf(ctx, d.ID, label)
	if err != nil {
		return AccountAnswer{}, err
	}

	session, err := s.accountSession(ctx, d, conn)
	if err != nil {
		return AccountAnswer{}, err
	}

	refreshed := false

	for {
		answer, used, err := s.send(ctx, d, conn, session, r)
		if err != nil {
			return AccountAnswer{}, err
		}

		s.keepAccount(d.ID, label, used)

		if !tokenRefused(answer) {
			return answer, nil
		}

		// The refresh token once; when the token it gives is refused too, or there is none, the password once.
		if !refreshed {
			refreshed = true

			if session, err = s.refreshAccount(ctx, d, conn.Address, used); err == nil {
				continue
			}
		}

		if session, err = s.signInAgain(ctx, d, conn); err != nil {
			return AccountAnswer{}, err
		}

		answer, used, err = s.send(ctx, d, conn, session, r)
		if err != nil {
			return AccountAnswer{}, err
		}

		s.keepAccount(d.ID, label, used)

		return answer, nil
	}
}

// tokenRefused says whether an answer refuses the request for its token:
// 401, or the 422 Superset's token library answers a token it cannot read
// with ({"msg": "Signature verification failed"}; its own validation errors
// are under "message").
func tokenRefused(a AccountAnswer) bool {
	if a.Status == http.StatusUnauthorized {
		return true
	}

	if a.Status != http.StatusUnprocessableEntity {
		return false
	}

	var said map[string]json.RawMessage
	if json.Unmarshal(a.Body, &said) != nil {
		return false
	}

	_, token := said["msg"]
	_, own := said["message"]

	return token && !own
}

// send sends a request once with a session, and returns the session as it is
// after: a request that is not a GET may have asked for a CSRF token.
func (s *Service) send(
	ctx context.Context, d Definition, conn orchestration.Connection, session accountSession, r AccountRequest,
) (AccountAnswer, accountSession, error) {
	safe := r.Method == http.MethodGet || r.Method == http.MethodHead
	guarded := !safe && d.CSRFPath != ""

	// Superset wants a CSRF token on every request that is not a GET, with the session cookie it gave the
	// token with and a Referer (tried 2026-11-12: 400 "The CSRF token is missing." without).
	if guarded && session.csrf == "" {
		refused, got, err := s.csrf(ctx, d, conn, &session)
		if err != nil || !got {
			return refused, session, err
		}
	}

	answer, err := s.sendOnce(ctx, conn, session, r, safe)
	if err != nil {
		return AccountAnswer{}, session, err
	}

	// A CSRF token ends with its session. One that is refused is asked for again, once.
	if guarded && answer.Status == http.StatusBadRequest && bytes.Contains(answer.Body, []byte("CSRF")) {
		refused, got, err := s.csrf(ctx, d, conn, &session)
		if err != nil || !got {
			return refused, session, err
		}

		answer, err = s.sendOnce(ctx, conn, session, r, safe)
		if err != nil {
			return AccountAnswer{}, session, err
		}
	}

	return answer, session, nil
}

// csrf asks for a CSRF token and keeps it in the session with the cookie it
// came with. When the answer is not the token, got is false and the answer is
// returned as the request's own: a token refused there is refused for the
// request too.
func (s *Service) csrf(ctx context.Context, d Definition, conn orchestration.Connection, session *accountSession,
) (refused AccountAnswer, got bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, conn.Address+d.CSRFPath, nil)
	if err != nil {
		return AccountAnswer{}, false, errors.New("the address cannot be called")
	}

	req.Header.Set("Authorization", "Bearer "+session.access)

	status, header, body, err := s.accountDo(req)
	if err != nil {
		return AccountAnswer{}, false, err
	}

	var said struct {
		Result string `json:"result"`
	}

	// An answer that is not JSON has no token in it, which is all that is asked of it here.
	_ = json.Unmarshal(body, &said)

	if status != http.StatusOK || said.Result == "" {
		return AccountAnswer{Status: status, ContentType: header.Get("Content-Type"), Body: body}, false, nil
	}

	cookies := []string{}
	for _, c := range (&http.Response{Header: header}).Cookies() {
		cookies = append(cookies, c.Name+"="+c.Value)
	}

	session.csrf, session.cookie = said.Result, strings.Join(cookies, "; ")

	return AccountAnswer{}, true, nil
}

func (s *Service) sendOnce(
	ctx context.Context, conn orchestration.Connection, session accountSession, r AccountRequest, safe bool,
) (AccountAnswer, error) {
	address := conn.Address + r.Path
	if r.RawQuery != "" {
		address += "?" + r.RawQuery
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, address, bytes.NewReader(r.Body))
	if err != nil {
		return AccountAnswer{}, errors.New("the request cannot be sent to the connection's address")
	}

	if r.ContentType != "" {
		req.Header.Set("Content-Type", r.ContentType)
	}

	req.Header.Set("Authorization", "Bearer "+session.access)

	if !safe && session.csrf != "" {
		req.Header.Set("X-Csrftoken", session.csrf)
		req.Header.Set("Referer", conn.Address)

		if session.cookie != "" {
			req.Header.Set("Cookie", session.cookie)
		}
	}

	status, header, body, err := s.accountDo(req)
	if err != nil {
		return AccountAnswer{}, err
	}

	return AccountAnswer{Status: status, ContentType: header.Get("Content-Type"), Body: body}, nil
}

// accountDo sends a request to a Connection's address. Its errors hold the
// host and the cause only, never the request or the answer.
func (s *Service) accountDo(req *http.Request) (int, http.Header, []byte, error) {
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := s.accountClient.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // net/http would repeat the address.
		}

		return 0, nil, nil, fmt.Errorf("%w: calling %s: %w", orchestration.ErrUnavailable, req.URL.Host, err)
	}
	defer resp.Body.Close() //nolint:errcheck // The answer was read or the read failed; closing adds nothing.

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAccountAnswerBytes+1))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: reading the answer of %s: %w", orchestration.ErrUnavailable, req.URL.Host, err)
	}

	if len(body) > maxAccountAnswerBytes {
		return 0, nil, nil, fmt.Errorf("%w: the answer of %s is larger than %d MB", orchestration.ErrInvalid,
			req.URL.Host, maxAccountAnswerBytes>>20)
	}

	return resp.StatusCode, resp.Header, body, nil
}
