// Package githubapp is the owner's GitHub App (issue #1262): the manifest the control plane registers it from, the
// exchange of GitHub's code for its credential, the App's own token (a JWT signed with its private key), the
// installations' short-lived tokens, and the webhook deliveries GitHub signs with its secret. It holds no state but a
// cache of installation tokens and never opens the vault: the control plane's own wiring gives it the credential.
//
// It follows Omnara's GitHub App (internal/integration/github: setup.go ConvertManifest, auth.go appJWT and
// mintInstallationToken, installation_token_cache.go, webhooks.go ValidSignature; Apache-2.0,
// third_party/omnara/NOTICE) and GitHub's docs: "Registering a GitHub App from a manifest", "Generating a JSON Web
// Token (JWT) for a GitHub App", "Generating an installation access token for a GitHub App", "Validating webhook
// deliveries".
package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Host is the only host an App is registered on: GitHub Enterprise Server is not built (left out, as Omnara's
// guided setup supports GitHub.com only, docs/integrations/github.mdx:43).
const Host = "github.com"

// The paths of the control plane's own routes the manifest names, under its public URL.
const (
	// EventsPath receives the App's webhook deliveries (the comment's step 1).
	EventsPath = "/api/v1/integrations/github/events"
	// SetupPath is the page the owner registers the App from, and where GitHub sends him back after an install.
	SetupPath = "/api/v1/integrations/github/app/setup"
	// CallbackPath is where GitHub redirects with the manifest's code.
	CallbackPath = "/api/v1/integrations/github/app/callback"
	// InstalledPath is the App's setup URL: GitHub sends the owner there after he installs it.
	InstalledPath = "/api/v1/integrations/github/app/installed"
)

// Events are the events the App subscribes to (the comment's step 1): CI's end, a pull request's change, and the
// conversations #1232 will answer. GitHub sends an App its installation events without a subscription.
var Events = []string{
	"check_suite", "workflow_run", "pull_request", "issue_comment", "pull_request_review",
	"pull_request_review_comment",
}

// Permissions are the App's repository permissions (the comment's step 1): what workers do with gh and git, and what
// reading CI needs (Omnara's manual setup, docs/integrations/github.mdx:62-69).
var Permissions = map[string]string{
	"pull_requests": "write", "issues": "write", "contents": "write",
	"actions": "read", "checks": "read", "statuses": "read",
}

// Manifest is the App's manifest for a control plane served at publicURL (https, without a trailing slash): its
// webhook, the page GitHub sends the owner back to, and the events and permissions above. The App is private, as
// Omnara's registration is (github_app_setup_routes.go:88-107). GitHub asks for the App's name on its page.
func Manifest(publicURL string) map[string]any {
	return map[string]any{
		"url":                      publicURL,
		"hook_attributes":          map[string]any{"url": publicURL + EventsPath, "active": true},
		"redirect_url":             publicURL + CallbackPath,
		"setup_url":                publicURL + InstalledPath,
		"public":                   false,
		"request_oauth_on_install": false,
		"setup_on_update":          true,
		"default_permissions":      Permissions,
		"default_events":           Events,
	}
}

// RegistrationURL is GitHub's page the manifest is posted to, for the owner's own account, with state.
func RegistrationURL(state string) string {
	return "https://github.com/settings/apps/new?" + url.Values{"state": {state}}.Encode()
}

// Credential is the App's: its id and what GitHub said of it, its private key and its webhook secret, kept as one
// secret in the vault, as Omnara keeps the same values as one credential (github_app_setup_callback.go:137-145).
type Credential struct {
	AppID   int64  `json:"app_id"`
	Slug    string `json:"slug"`
	Owner   string `json:"owner"`
	HTMLURL string `json:"html_url"`
	// PrivateKey is the App's RSA key in PEM, and WebhookSecret the secret its deliveries are signed with: never
	// shown, logged or written anywhere but the vault.
	PrivateKey    string `json:"private_key"`
	WebhookSecret string `json:"webhook_secret"`
}

// String keeps a credential out of logs and error messages.
func (c Credential) String() string { return fmt.Sprintf("GitHub App %d (%s)", c.AppID, c.Slug) }

// GoString keeps it out of %#v too.
func (c Credential) GoString() string { return c.String() }

// Encode is the credential as the vault keeps it.
func (c Credential) Encode() (string, error) {
	raw, err := json.Marshal(c) //nolint:gosec // G117: the vault keeps it; nothing else marshals it
	if err != nil {
		return "", errors.New("encoding the GitHub App's credential")
	}

	return string(raw), nil
}

// DecodeCredential reads a credential the vault kept, and checks it can sign.
func DecodeCredential(raw string) (Credential, error) {
	var c Credential
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Credential{}, errors.New("the GitHub App's credential is not readable")
	}

	if c.AppID <= 0 || c.WebhookSecret == "" {
		return Credential{}, errors.New("the GitHub App's credential has no App id or webhook secret")
	}

	if _, err := parsePrivateKey(c.PrivateKey); err != nil {
		return Credential{}, err
	}

	return c, nil
}

// API is GitHub's REST API for github.com.
const API = "https://api.github.com"

// apiVersion is the REST API version every request names.
const apiVersion = "2022-11-28"

// maxAnswer bounds what one answer of GitHub's is read to.
const maxAnswer = 2 << 20

// Client calls GitHub's API for the App.
type Client struct {
	// API is GitHub's REST API; empty is API.
	API  string
	HTTP *http.Client
	// Now is the clock the App's JWT is dated by; nil is time.Now.
	Now func() time.Time
}

func (c Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}

	return time.Now()
}

// Error is a refusal of GitHub's: its status and what it said, never a credential.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("GitHub answered %d", e.Status)
	}

	return fmt.Sprintf("GitHub answered %d: %s", e.Status, e.Message)
}

// do sends one request and decodes its JSON answer into out (nil reads none). token is the bearer, "" for none.
func (c Client) do(ctx context.Context, method, path, token string, in, out any) error {
	base := c.API
	if base == "" {
		base = API
	}

	var body io.Reader

	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return errors.New("encoding a request to GitHub")
		}

		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return fmt.Errorf("asking GitHub: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-Github-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "hypomnema")

	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		// The URL holds no credential; the error is the transport's.
		return fmt.Errorf("asking GitHub: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return fmt.Errorf("reading GitHub's answer: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var refused struct {
			Message string `json:"message"`
		}

		_ = json.Unmarshal(raw, &refused)

		// Only GitHub's own short message, never the body, which may repeat what was sent.
		msg := refused.Message
		if len(msg) > 200 {
			msg = msg[:200]
		}

		return &Error{Status: resp.StatusCode, Message: msg}
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("GitHub's answer is not what was asked")
	}

	return nil
}

// Convert exchanges the code GitHub redirected the owner back with for the App's credential (POST
// /app-manifests/{code}/conversions, which needs no authentication and works once, within an hour of the
// registration), as Omnara's ConvertManifest (setup.go:63-95).
func (c Client) Convert(ctx context.Context, code string) (Credential, error) {
	if code == "" || len(code) > 512 || strings.ContainsFunc(code, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_'
	}) {
		return Credential{}, errors.New("GitHub's code is not one")
	}

	var answer struct {
		ID      int64  `json:"id"`
		Slug    string `json:"slug"`
		HTMLURL string `json:"html_url"`
		Owner   struct {
			Login string `json:"login"`
		} `json:"owner"`
		PEM           string `json:"pem"`
		WebhookSecret string `json:"webhook_secret"`
	}

	if err := c.do(ctx, http.MethodPost, "/app-manifests/"+code+"/conversions", "", nil, &answer); err != nil {
		return Credential{}, err
	}

	cred := Credential{
		AppID: answer.ID, Slug: answer.Slug, HTMLURL: answer.HTMLURL, Owner: answer.Owner.Login,
		PrivateKey: answer.PEM, WebhookSecret: answer.WebhookSecret,
	}
	if cred.AppID <= 0 || cred.WebhookSecret == "" || !validSlug(cred.Slug) {
		return Credential{}, errors.New("GitHub's answer holds no App")
	}

	if _, err := parsePrivateKey(cred.PrivateKey); err != nil {
		return Credential{}, err
	}

	return cred, nil
}

func validSlug(s string) bool {
	return s != "" && len(s) <= 100 && !strings.ContainsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-'
	})
}

// parsePrivateKey reads the App's RSA key, as Omnara's parsePrivateKey (auth.go:31-55).
func parsePrivateKey(raw string) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode([]byte(raw))
	if block == nil || strings.TrimSpace(string(rest)) != "" || len(block.Headers) != 0 {
		return nil, errors.New("the GitHub App's private key is not one RSA key in PEM")
	}

	var (
		key *rsa.PrivateKey
		err error
	)

	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var parsed any

		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = parsed.(*rsa.PrivateKey)
	default:
		return nil, errors.New("the GitHub App's private key is not an RSA key")
	}

	if err != nil || key == nil || key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, errors.New("the GitHub App's private key is not a valid RSA key of at least 2048 bits")
	}

	return key, nil
}

// jwt is the App's own token: RS256, issued a minute ago to allow for clock drift and good for nine minutes, under
// GitHub's ten (Omnara's appJWT, auth.go:57-77; GitHub's "Generating a JSON Web Token (JWT) for a GitHub App").
func (c Client) jwt(cred Credential) (string, error) {
	key, err := parsePrivateKey(cred.PrivateKey)
	if err != nil {
		return "", err
	}

	now := c.now()

	claims, err := json.Marshal(struct {
		IssuedAt int64  `json:"iat"`
		Expires  int64  `json:"exp"`
		Issuer   string `json:"iss"`
	}{now.Add(-time.Minute).Unix(), now.Add(9 * time.Minute).Unix(), strconv.FormatInt(cred.AppID, 10)})
	if err != nil {
		return "", errors.New("encoding the App's token")
	}

	encode := base64.RawURLEncoding.EncodeToString
	unsigned := encode([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + encode(claims)
	digest := sha256.Sum256([]byte(unsigned))

	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("signing the App's token")
	}

	return unsigned + "." + encode(signature), nil
}

// Token is an installation's access token and when it ends (an hour after it was made).
type Token struct {
	Value     string
	ExpiresAt time.Time
}

// String keeps a token out of logs.
func (t Token) String() string { return "installation token until " + t.ExpiresAt.Format(time.RFC3339) }

// mint makes a new token of an installation (POST /app/installations/{id}/access_tokens) for the repositories named
// by id, as Omnara's mintInstallationToken asks for one repository (auth.go:138-162): without repository_ids GitHub
// gives a token of every repository the installation covers. It keeps every permission the installation was
// granted, without Omnara's narrowing: a worker's gh and git need all of them. Only the listing of an
// installation's repositories, which no one is given, asks for none.
func (c Client) mint(ctx context.Context, cred Credential, installation int64, repositoryIDs []int64) (Token, error) {
	jwt, err := c.jwt(cred)
	if err != nil {
		return Token{}, err
	}

	var answer struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}

	var body any
	if len(repositoryIDs) > 0 {
		body = map[string]any{"repository_ids": repositoryIDs}
	}

	path := "/app/installations/" + strconv.FormatInt(installation, 10) + "/access_tokens"
	if err := c.do(ctx, http.MethodPost, path, jwt, body, &answer); err != nil {
		return Token{}, err
	}

	if answer.Token == "" || strings.ContainsAny(answer.Token, " \r\n") || !answer.ExpiresAt.After(c.now()) {
		return Token{}, errors.New("GitHub's answer holds no installation token")
	}

	return Token{Value: answer.Token, ExpiresAt: answer.ExpiresAt}, nil
}

// Installation is one of the App's installations and the repositories it covers.
type Installation struct {
	ID           int64
	Account      string
	Repositories []Repository
}

// Repository is a repository an installation covers: owner/name in lower case, and GitHub's id of it.
type Repository struct {
	Name string
	ID   int64
}

// Installations lists the App's installations (GET /app/installations, with the App's token) and each one's
// repositories (GET /installation/repositories, with a token of the installation), 100 a page.
func (c Client) Installations(ctx context.Context, cred Credential) ([]Installation, error) {
	jwt, err := c.jwt(cred)
	if err != nil {
		return nil, err
	}

	var out []Installation

	for page := 1; ; page++ {
		var installs []struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
			} `json:"account"`
		}

		if err := c.do(ctx, http.MethodGet, "/app/installations?per_page=100&page="+strconv.Itoa(page), jwt, nil,
			&installs); err != nil {
			return nil, err
		}

		for _, i := range installs {
			out = append(out, Installation{ID: i.ID, Account: i.Account.Login})
		}

		if len(installs) < 100 {
			break
		}
	}

	for n := range out {
		token, err := c.mint(ctx, cred, out[n].ID, nil)
		if err != nil {
			return nil, err
		}

		repos, err := c.repositories(ctx, token.Value)
		if err != nil {
			return nil, err
		}

		out[n].Repositories = repos
	}

	return out, nil
}

func (c Client) repositories(ctx context.Context, token string) ([]Repository, error) {
	var out []Repository

	for page := 1; ; page++ {
		var answer struct {
			Repositories []struct {
				ID       int64  `json:"id"`
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}

		if err := c.do(ctx, http.MethodGet, "/installation/repositories?per_page=100&page="+strconv.Itoa(page), token,
			nil, &answer); err != nil {
			return nil, err
		}

		for _, r := range answer.Repositories {
			if name, ok := RepositoryName(r.FullName); ok && r.ID > 0 {
				out = append(out, Repository{Name: name, ID: r.ID})
			}
		}

		if len(answer.Repositories) < 100 {
			return out, nil
		}
	}
}

// RepositoryName is a repository's owner/name in lower case, as GitHub's names are compared, and whether it is one.
func RepositoryName(fullName string) (string, bool) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || !namePart(owner) || !namePart(name) {
		return "", false
	}

	return strings.ToLower(owner + "/" + name), true
}

func namePart(s string) bool {
	return s != "" && s != "." && s != ".." && len(s) <= 100 && !strings.ContainsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' &&
			r != '.'
	})
}

// Delivery is one of the App's webhook deliveries, as GitHub lists them.
type Delivery struct {
	ID         int64  `json:"id"`
	GUID       string `json:"guid"`
	Event      string `json:"event"`
	Redelivery bool   `json:"redelivery"`
}

// Redeliver asks GitHub to send the App's latest delivery again (GET /app/hook/deliveries, then POST
// /app/hook/deliveries/{id}/attempts, with the App's token): the test delivery of the comment's step 3. GitHub keeps
// the delivery's id (X-GitHub-Delivery), so the control plane answers it 200 as handled before; GitHub's Recent
// Deliveries shows that answer. With none sent yet it says so.
func (c Client) Redeliver(ctx context.Context, cred Credential) (Delivery, error) {
	jwt, err := c.jwt(cred)
	if err != nil {
		return Delivery{}, err
	}

	var deliveries []Delivery
	if err := c.do(ctx, http.MethodGet, "/app/hook/deliveries?per_page=1", jwt, nil, &deliveries); err != nil {
		return Delivery{}, err
	}

	if len(deliveries) == 0 {
		return Delivery{}, errors.New("GitHub has sent the App no delivery yet to send again")
	}

	d := deliveries[0]
	if err := c.do(ctx, http.MethodPost, "/app/hook/deliveries/"+strconv.FormatInt(d.ID, 10)+"/attempts", jwt, nil,
		nil); err != nil {
		return Delivery{}, err
	}

	return d, nil
}
