package githost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// Identity is the account a host's token belongs to, as git names it in a
// commit (issue #991): a worker given the token commits as that account, not
// as whoever the node user's ~/.gitconfig names. Paperclip does the same for
// each run, from the GitHub account whose token the run uses
// (server/src/services/git-credentials.ts:116-153).
type Identity struct {
	Name  string
	Email string
}

// Known says whether the identity can be given to git: both its name and its
// address are known.
func (i Identity) Known() bool { return i.Name != "" && i.Email != "" }

// Env is the environment that makes git write the identity as a commit's
// author and committer: GIT_AUTHOR_* and GIT_COMMITTER_*, which git takes over
// user.name and user.email of any configuration file, as Coder's workspace
// agent sets them (examples/templates/docker/main.tf:45-53). It is empty for an
// identity that is not known, so git keeps what the machine says.
func (i Identity) Env() []string { return i.EnvWithCommitter(i) }

// EnvWithCommitter also supports App-backed commits whose author and committer can differ. If either identity is
// unknown, git uses the other for both fields.
func (i Identity) EnvWithCommitter(committer Identity) []string {
	if !i.Known() {
		i = committer
	}

	if !committer.Known() {
		committer = i
	}

	if !i.Known() {
		return nil
	}

	return []string{
		"GIT_AUTHOR_NAME=" + i.Name, "GIT_AUTHOR_EMAIL=" + i.Email,
		"GIT_COMMITTER_NAME=" + committer.Name, "GIT_COMMITTER_EMAIL=" + committer.Email,
	}
}

// ErrNoIdentity says the host gives no identity for the token: its kind or
// host has no address to use, or the account shows none.
var ErrNoIdentity = errors.New("no git identity is known for the token")

// maxRedirects bounds the redirects followed to the host's /user, as Go's client does.
const maxRedirects = 10

// maxUserBytes bounds the host's answer about the token's user; GitHub's and
// GitLab's are a few kilobytes.
const maxUserBytes = 1 << 20

// ReadIdentity asks host, of kind, which account token belongs to (GET /user)
// and returns it as git names it in a commit. It is read once a Connection,
// and kept with it, by the control plane.
//
// No error it returns holds what the host answered beyond its status code, so
// a host or a proxy that repeats the token cannot put it in a log.
func ReadIdentity(ctx context.Context, client *http.Client, kind Kind, host, token string) (Identity, error) {
	if token == "" {
		return Identity{}, fmt.Errorf("%w: there is no token", ErrNoIdentity)
	}

	adapter, ok := For(kind)
	if !ok {
		return Identity{}, fmt.Errorf("%w: unknown kind of host %q", ErrNoIdentity, kind)
	}

	userURL, err := adapter.userURL(host)
	if err != nil {
		return Identity{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userURL, nil)
	if err != nil {
		return Identity{}, fmt.Errorf("asking %s for the token's user: %w", host, err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	// The token is sent to the host alone: a redirect is followed only within the same https origin, never to
	// http, where it would cross the network in the clear, nor to another host.
	sameOrigin := *client
	sameOrigin.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if next.URL.Scheme != "https" || next.URL.Host != via[0].URL.Host || len(via) >= maxRedirects {
			return http.ErrUseLastResponse
		}

		return nil
	}

	resp, err := sameOrigin.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("asking %s for the token's user: %w", host, transportError(err))
	}
	defer resp.Body.Close() //nolint:errcheck // a read-only answer; nothing is left to report

	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("%s answered HTTP %d about the token's user", host, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUserBytes))
	if err != nil {
		return Identity{}, fmt.Errorf("reading %s's answer about the token's user: %w", host, transportError(err))
	}

	id, err := adapter.identity(host, body)
	if err != nil {
		return Identity{}, err
	}

	if !plainIdentityValue(id.Name) || !plainIdentityValue(id.Email) || !strings.Contains(id.Email, "@") {
		return Identity{}, fmt.Errorf("%w: %s named the token's user in a form git cannot take", ErrNoIdentity, host)
	}

	// An identity is written in every commit and kept in the clear: one that holds the token is refused.
	if strings.Contains(id.Name, token) || strings.Contains(id.Email, token) {
		return Identity{}, fmt.Errorf("%w: %s named the token's user with the token", ErrNoIdentity, host)
	}

	return id, nil
}

// transportError is what is said of a request that failed before an answer:
// a timeout, a cancel or the kind of network error, never its text, which may
// hold a proxy's answer.
func transportError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	default:
		return errors.New("the request failed")
	}
}

// plainIdentityValue reports whether s can be an environment variable's value
// git writes in a commit: one short line, without the angle brackets that
// delimit the address.
func plainIdentityValue(s string) bool {
	return s != "" && len(s) <= 256 && !strings.ContainsAny(s, "<>") &&
		!strings.ContainsFunc(s, unicode.IsControl)
}

// userURL on GitHub is api.github.com for github.com, and the REST API under
// /api/v3 of a GitHub Enterprise host, as claude-code-action reads it
// (src/github/api/config.ts:1-14) and gh does.
func (github) userURL(host string) (string, error) {
	if host == GitHubHost {
		return "https://api.github.com/user", nil
	}

	return (&url.URL{Scheme: "https", Host: host, Path: "/api/v3/user"}).String(), nil
}

// identity on GitHub is the login and the noreply address
// <id>+<login>@users.noreply.<host>: users.noreply.github.com, as Paperclip
// makes it (git-credentials.ts:118), and users.noreply.<hostname> on a GitHub
// Enterprise host, as claude-code-action does (src/github/operations/
// git-config.ts:29-42), without the host's port.
func (github) identity(host string, body []byte) (Identity, error) {
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}

	if err := json.Unmarshal(body, &user); err != nil {
		return Identity{}, fmt.Errorf("reading GitHub's user: %w", err)
	}

	if user.ID <= 0 || user.Login == "" {
		return Identity{}, fmt.Errorf("%w: GitHub's user has no id or login", ErrNoIdentity)
	}

	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}

	return Identity{
		Name:  user.Login,
		Email: strconv.FormatInt(user.ID, 10) + "+" + user.Login + "@users.noreply." + hostname,
	}, nil
}

// userURL on GitLab is the host's REST API, for gitlab.com and a company's
// server alike.
func (gitlab) userURL(host string) (string, error) {
	return (&url.URL{Scheme: "https", Host: host, Path: "/api/v4/user"}).String(), nil
}

// identity on GitLab is the user's username and the address GitLab itself
// writes in the commits made on its web pages, commit_email, else the public
// one. An account that shows neither gives none.
func (gitlab) identity(_ string, body []byte) (Identity, error) {
	var user struct {
		Username    string `json:"username"`
		CommitEmail string `json:"commit_email"`
		PublicEmail string `json:"public_email"`
	}

	if err := json.Unmarshal(body, &user); err != nil {
		return Identity{}, fmt.Errorf("reading GitLab's user: %w", err)
	}

	email := user.CommitEmail
	if email == "" {
		email = user.PublicEmail
	}

	if user.Username == "" || email == "" {
		return Identity{}, fmt.Errorf("%w: GitLab's user shows no username or address", ErrNoIdentity)
	}

	return Identity{Name: user.Username, Email: email}, nil
}
