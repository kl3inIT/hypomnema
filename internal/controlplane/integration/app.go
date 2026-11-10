package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// An Integration that is an app (KindApp, issue #135). Its client is the
// app's id and secret, kept as an OAuth client's is. A Connection of it is the
// app itself under a label: nothing is signed in and nothing is stored for it
// beyond the row, because every token is minted from the client's secret. The
// token is good for two hours at most, so it is kept in memory and minted
// again when it has under two minutes left, as an OAuth Connection's is
// refreshed.

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

// Connect records an app Integration as a Connection under a label, after
// checking that its id and secret give a token and reading the app's name
// with it. A label that exists is checked again and takes the app's name.
func (s *Service) Connect(ctx context.Context, integration, label string) (Connection, error) {
	d, err := s.definition(integration)
	if err != nil {
		return Connection{}, err
	}

	if !d.IsApp() {
		return Connection{}, fmt.Errorf("%w: %s connects an account by signing in, not as an app", ErrInvalid, d.ID)
	}

	if !orchestration.ValidConnectionLabel(label) {
		return Connection{}, fmt.Errorf("%w: a label is lower-case letters, digits and dashes, such as work", ErrInvalid)
	}

	token, err := s.mintAppToken(ctx, d)
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	name, err := s.appName(ctx, d, token.AccessToken)
	if err != nil {
		return Connection{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	// The Connection has no secret of its own, so it names the client's.
	saved, err := s.store.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: d.ID, Label: label, Account: name, Secret: clientSecretName(d.ID),
	})
	if err != nil {
		return Connection{}, err
	}

	return s.view(ctx, d, saved), nil
}

// appToken is an app Integration's access token, minted when the one in
// memory has under two minutes left.
func (s *Service) appToken(ctx context.Context, d Definition) (Token, error) {
	s.mu.Lock()
	kept, ok := s.appTokens[d.ID]
	s.mu.Unlock()

	if ok && kept.ExpiresAt.Sub(s.clock.Now()) > refreshLead {
		return kept, nil
	}

	return s.mintAppToken(ctx, d)
}

// mintAppToken asks the service for a token with the app's id and secret and
// keeps it in memory. Its errors hold the status and the service's code only.
func (s *Service) mintAppToken(ctx context.Context, d Definition) (Token, error) {
	client, err := s.oauthClient(ctx, d)
	if err != nil {
		return Token{}, err
	}

	body, err := json.Marshal(map[string]string{"app_id": client.id, "app_secret": client.secret})
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

	token := Token{
		AccessToken: answer.Token, ExpiresAt: s.clock.Now().Add(secondsOf(answer.Expire)), ClientID: client.id,
	}

	s.mu.Lock()
	s.appTokens[d.ID] = token
	s.mu.Unlock()

	return token, nil
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
