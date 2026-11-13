package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// An Integration that is an app (KindApp, issue #135). A Connection of it is
// one app of the service under a label (issue #311): its id is on the
// Connection and its secret is the Connection's vault secret, as each of
// OpenClaw's Feishu accounts has its own appId and appSecret
// (extensions/feishu/src/config-schema.ts, FeishuAccountConfigSchema) and a
// tool works as the app of the account it is given (tool-account.ts). Nothing
// is signed in: every token is minted from the app's secret. The token is good
// for two hours at most, so it is kept in memory and minted again when it has
// under two minutes left, as an OAuth Connection's is refreshed.

// appAnswer is the answer of Lark's endpoints: a code that is 0 when the call
// worked, and the fields of the call beside it.
type appAnswer struct {
	Code   int    `json:"code"`
	Token  string `json:"tenant_access_token"`
	Expire int64  `json:"expire"`
	Bot    struct {
		AppName string `json:"app_name"`
	} `json:"bot"`
}

// App is what the owner gives to connect an Integration that is an app: the
// id and the secret the service's developer console shows.
type App struct {
	ID     string
	Secret string
}

// Connect records an app of an Integration as a Connection under a label,
// after checking that its id and secret give a token and reading the app's
// name with it. Nothing is stored when they do not. A label that exists takes
// the new app; given no app, it is checked again with its own.
func (s *Service) Connect(ctx context.Context, integration, label string, app App) (Connection, error) {
	d, err := s.definition(integration)
	if err != nil {
		return Connection{}, err
	}

	if d.IsAccount() {
		return Connection{}, fmt.Errorf("%w: %s is connected with its address, a user name and a password: "+
			"`hypomnema integrations connect %s --url URL --user NAME --password-file FILE`", ErrInvalid, d.ID, d.ID)
	}

	if !d.IsApp() {
		return Connection{}, fmt.Errorf("%w: %s connects an account by signing in, not as an app", ErrInvalid, d.ID)
	}

	if !orchestration.ValidConnectionLabel(label) {
		return Connection{}, fmt.Errorf("%w: a label is lower-case letters, digits and dashes, such as work", ErrInvalid)
	}

	app.ID, app.Secret = strings.TrimSpace(app.ID), strings.TrimSpace(app.Secret)
	if app.ID == "" && app.Secret == "" {
		return s.checkApp(ctx, d, label)
	}

	if app.ID == "" || app.Secret == "" || len(app.ID) > maxClientBytes || len(app.Secret) > maxClientBytes ||
		strings.ContainsAny(app.ID+app.Secret, " \t\r\n") {
		return Connection{}, fmt.Errorf("%w: an app is its App ID and its App Secret, each one line without spaces",
			ErrInvalid)
	}

	token, err := s.mintAppToken(ctx, d, app)
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	name, err := s.appName(ctx, d, token.AccessToken)
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	old, err := s.store.ConnectionOf(ctx, d.ID, label)
	if err != nil && !errors.Is(err, orchestration.ErrNotFound) {
		return Connection{}, err
	}

	secret, payload := connectionSecretName(d.ID, label), vault.Payload{vault.KeyValue: app.Secret}

	_, err = s.vault.Create(ctx, vault.NewSecret{
		Name: secret, Kind: vault.KindGeneric, Payload: payload,
		Metadata: map[string]string{"integration": d.ID, "connection": label, "app": app.ID, "what": "its app's secret"},
	})
	if errors.Is(err, vault.ErrConflict) {
		_, err = s.vault.Rotate(ctx, secret, payload)
	}

	if err != nil {
		return Connection{}, vaultError(err)
	}

	saved, err := s.store.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: d.ID, Label: label, Account: name, Secret: secret, AppID: app.ID,
	})
	if err != nil {
		return Connection{}, err
	}

	s.keepAppToken(d.ID, label, token)

	// A Connection moved before issue #311 named the app the Integration had; that secret goes once nothing names it.
	if old.Secret != "" && old.Secret != secret {
		if err := s.forgetUnnamed(ctx, d, old.Secret); err != nil {
			return Connection{}, err
		}
	}

	return s.view(ctx, d, saved), nil
}

// checkApp checks a Connection's own app again and takes the app's name.
func (s *Service) checkApp(ctx context.Context, d Definition, label string) (Connection, error) {
	conn, err := s.store.ConnectionOf(ctx, d.ID, label)
	if errors.Is(err, orchestration.ErrNotFound) {
		return Connection{}, fmt.Errorf("%w: give the app's App ID and App Secret: "+
			"`hypomnema integrations connect %s --label %s --app-file FILE`", ErrInvalid, d.ID, label)
	}

	if err != nil {
		return Connection{}, err
	}

	app, err := s.appOf(ctx, d, conn)
	if err != nil {
		return Connection{}, err
	}

	token, err := s.mintAppToken(ctx, d, app)
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	name, err := s.appName(ctx, d, token.AccessToken)
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	conn.Account = name

	saved, err := s.store.SaveServiceConnection(ctx, conn)
	if err != nil {
		return Connection{}, err
	}

	s.keepAppToken(d.ID, label, token)

	return s.view(ctx, d, saved), nil
}

// appOf is a Connection's app with its secret opened.
func (s *Service) appOf(ctx context.Context, d Definition, conn orchestration.Connection) (App, error) {
	if conn.AppID == "" {
		return App{}, fmt.Errorf("%w: %s:%s has no app; connect it again with its App ID and App Secret",
			orchestration.ErrConflict, d.ID, conn.Label)
	}

	opened, err := s.vault.Open(ctx, conn.Secret)
	if err != nil {
		return App{}, vaultError(err)
	}

	return App{ID: conn.AppID, Secret: opened.Payload[vault.KeyValue]}, nil
}

// appToken is a Connection's access token, minted from its own app when the
// one in memory has under two minutes left or is another app's.
func (s *Service) appToken(ctx context.Context, d Definition, conn orchestration.Connection) (Token, error) {
	s.mu.Lock()
	kept, ok := s.appTokens[accountKey(d.ID, conn.Label)]
	s.mu.Unlock()

	if ok && kept.ClientID == conn.AppID && kept.ExpiresAt.Sub(s.clock.Now()) > refreshLead {
		return kept, nil
	}

	app, err := s.appOf(ctx, d, conn)
	if err != nil {
		return Token{}, err
	}

	token, err := s.mintAppToken(ctx, d, app)
	if err != nil {
		return Token{}, err
	}

	s.keepAppToken(d.ID, conn.Label, token)

	return token, nil
}

func (s *Service) keepAppToken(integration, label string, token Token) {
	s.mu.Lock()
	s.appTokens[accountKey(integration, label)] = token
	s.mu.Unlock()
}

func (s *Service) dropAppToken(integration, label string) {
	s.mu.Lock()
	delete(s.appTokens, accountKey(integration, label))
	s.mu.Unlock()
}

// mintAppToken asks the service for a token with an app's id and secret. Its
// errors hold the status and the service's code only.
func (s *Service) mintAppToken(ctx context.Context, d Definition, app App) (Token, error) {
	body, err := json.Marshal(map[string]string{"app_id": app.ID, "app_secret": app.Secret})
	if err != nil {
		return Token{}, fmt.Errorf("asking %s for the app's token: %w", d.Name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.AppTokenURL, bytes.NewReader(body))
	if err != nil {
		return Token{}, fmt.Errorf("asking %s for the app's token: %w", d.Name, err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	answer, status, err := s.appCall(req)
	if err != nil {
		return Token{}, fmt.Errorf("asking %s for the app's token: %w", d.Name, err)
	}

	if answer.Code != 0 || answer.Token == "" || answer.Expire <= 0 {
		return Token{}, fmt.Errorf("%s refused the app's id and secret: status %d, code %d", d.Name, status, answer.Code)
	}

	return Token{
		AccessToken: answer.Token, ExpiresAt: s.clock.Now().Add(secondsOf(answer.Expire)), ClientID: app.ID,
	}, nil
}

// forgetApp removes a Connection that is an app and its token in memory, and
// its secret once no other Connection names it: one moved before issue #311
// may share the secret of the app the Integration had. Nothing is left at the
// service to end: the app itself is removed in its developer console.
func (s *Service) forgetApp(ctx context.Context, d Definition, conn orchestration.Connection) error {
	if _, err := s.store.RemoveConnection(ctx, d.ID, conn.Label); err != nil {
		return err
	}

	s.dropAppToken(d.ID, conn.Label)

	return s.forgetUnnamed(ctx, d, conn.Secret)
}

// forgetUnnamed deletes a vault secret no Connection of the Integration names.
func (s *Service) forgetUnnamed(ctx context.Context, d Definition, secret string) error {
	conns, err := s.store.ConnectionsOf(ctx, d.ID)
	if err != nil {
		return err
	}

	for _, c := range conns {
		if c.Secret == secret {
			return nil
		}
	}

	if err := s.vault.Delete(ctx, secret); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return vaultError(err)
	}

	return nil
}

// appName reads the name of the app a token is.
func (s *Service) appName(ctx context.Context, d Definition, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.AppInfoURL, nil)
	if err != nil {
		return "", fmt.Errorf("reading which app it is: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	answer, status, err := s.appCall(req)
	if err != nil {
		return "", fmt.Errorf("reading which app it is: %w", err)
	}

	if answer.Code != 0 || answer.Bot.AppName == "" {
		return "", fmt.Errorf("reading which app it is: status %d, code %d, and no name in the answer; the app "+
			"needs the bot feature switched on", status, answer.Code)
	}

	return answer.Bot.AppName, nil
}

// appCall sends a request and reads the answer's code and fields. Lark
// answers a refusal with a body too, under a status that is not 200.
func (s *Service) appCall(req *http.Request) (appAnswer, int, error) {
	status, body, err := s.do(req)
	if err != nil {
		return appAnswer{}, 0, err
	}

	var answer appAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return appAnswer{}, status, fmt.Errorf("status %d, and an answer that is not JSON", status)
	}

	return answer, status, nil
}
