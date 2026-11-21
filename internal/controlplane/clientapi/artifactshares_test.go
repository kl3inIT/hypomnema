package clientapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/shared/artifactpack"
)

// public is an answer to a request sent with no sign-in, as whoever has a link sends it.
type public struct {
	status int
	header http.Header
	body   string
}

// anonymous sends a request with no sign-in from address (the X-Forwarded-For a proxy on this machine names).
func (w *world) anonymous(method, path, address string) public {
	w.t.Helper()

	req, err := http.NewRequestWithContext(w.t.Context(), method, w.url+path, nil)
	require.NoError(w.t, err)

	req.Host = "hyp.example"
	req.Header.Set("X-Forwarded-For", address)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(w.t, err)

	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(resp.Body)
	require.NoError(w.t, err)

	return public{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

func (w *world) shared(token, address string) (openapi.SharedArtifact, public) {
	w.t.Helper()

	got := w.anonymous(http.MethodGet, BasePath+"/shares/"+token, address)

	var sa openapi.SharedArtifact
	if got.status == http.StatusOK {
		require.NoError(w.t, json.Unmarshal([]byte(got.body), &sa))
	}

	return sa, got
}

// sharedPage is a Bot's dashboard folder in a new chat, with the owner's client and the Bot's pi's.
func sharedPage(t *testing.T, w *world) (owner, pi *openapi.ClientWithResponses, chatID string, a *openapi.Artifact) {
	t.Helper()

	owner, _ = w.token("web", bearertoken.Owner)
	_, _ = w.token("pi-coordinator", bearertoken.Coordinator)

	chat, err := w.svc.CreateChat(t.Context(), "", "")
	require.NoError(t, err)

	pi = w.piToken("pi-coordinator", chat.ID, chat.Bot)
	shown := w.show(pi, "Sales", "",
		artifactpack.File{Path: "index.html", Data: []byte("<script src=app.js></script>v1")},
		artifactpack.File{Path: "app.js", Data: []byte("draw(1)")})
	require.Equal(t, http.StatusCreated, shown.StatusCode(), string(shown.Body))

	return owner, pi, chat.ID, shown.JSON201
}

// Scenario: the owner sends Laura a link to a dashboard a Routine rebuilds (issue #745). Without signing in, the
// link shows the newest revision and serves its files under the frame's headers, the page with its policy naming
// the link's own folder; opening the page counts a view, reading what the link shows does not. When the dashboard
// is revised, the same link shows the new revision and a file of the old one answers 404. A Bot's token cannot
// make, list or revoke a link.
func TestALinkShowsTheNewestRevisionWithoutSignIn(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	owner, pi, _, a := sharedPage(t, w)

	made, err := owner.CreateArtifactShareWithResponse(ctx, a.Id, openapi.NewArtifactShare{Label: " Laura "})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, made.StatusCode(), string(made.Body))
	link := made.JSON201
	assert.Equal(t, "Laura", link.Label)
	assert.Len(t, link.Token, 43, "32 random bytes in base64url")
	assert.Equal(t, "/s/"+link.Token, link.Path)

	blank, err := owner.CreateArtifactShareWithResponse(ctx, a.Id, openapi.NewArtifactShare{Label: "  "})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, blank.Body, blank.StatusCode(), http.StatusBadRequest))

	byBot, err := pi.CreateArtifactShareWithResponse(ctx, a.Id, openapi.NewArtifactShare{Label: "x"})
	require.NoError(t, err)
	assert.Equal(t, openapi.Forbidden, errorCode(t, byBot.Body, byBot.StatusCode(), http.StatusForbidden))

	listedByBot, err := pi.ListArtifactSharesWithResponse(ctx, a.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, listedByBot.StatusCode())

	revokedByBot, err := pi.DeleteArtifactShareWithResponse(ctx, link.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, revokedByBot.StatusCode())

	sa, got := w.shared(link.Token, "203.0.113.1")
	require.Equal(t, http.StatusOK, got.status, got.body)
	assert.Equal(t, "Sales", sa.Title)
	assert.Equal(t, openapi.ArtifactHTML, sa.Type)
	assert.Equal(t, "index.html", sa.Entry)
	assert.Equal(t, "text/html; charset=utf-8", sa.MediaType)
	assert.Equal(t, 1, sa.Revision)
	assert.Equal(t, BasePath+"/shares/"+link.Token+"/files/1/", sa.Files)
	assert.Equal(t, "noindex, nofollow", got.header.Get("X-Robots-Tag"))
	assert.Equal(t, "no-referrer", got.header.Get("Referrer-Policy"))
	assert.Equal(t, "private, no-store", got.header.Get("Cache-Control"))
	assert.Equal(t, "frame-ancestors 'none'", got.header.Get("Content-Security-Policy"))

	page := w.anonymous(http.MethodGet, sa.Files+"index.html", "203.0.113.1")
	require.Equal(t, http.StatusOK, page.status, page.body)
	assert.Contains(t, page.body, "v1")
	assert.Equal(t, pagePolicy("hyp.example"+sa.Files), page.header.Get("Content-Security-Policy"))
	assert.Equal(t, "no-referrer", page.header.Get("Referrer-Policy"))
	assert.Equal(t, "private, no-store", page.header.Get("Cache-Control"))
	assert.Equal(t, "noindex, nofollow", page.header.Get("X-Robots-Tag"))

	script := w.anonymous(http.MethodGet, sa.Files+"app.js", "203.0.113.1")
	require.Equal(t, http.StatusOK, script.status)
	assert.Equal(t, fileCSP, script.header.Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", script.header.Get("X-Content-Type-Options"))
	assert.Equal(t, http.StatusMethodNotAllowed, w.anonymous(http.MethodPost, sa.Files+"app.js", "203.0.113.1").status)

	listed, err := owner.ListArtifactSharesWithResponse(ctx, a.Id)
	require.NoError(t, err)
	require.Len(t, listed.JSON200.Data, 1)
	assert.Equal(t, int64(1), listed.JSON200.Data[0].Views, "the page opened once; reading the link is no view")
	require.NotNil(t, listed.JSON200.Data[0].LastViewedAt)
	assert.WithinDuration(t, time.Now(), *listed.JSON200.Data[0].LastViewedAt, time.Minute)

	revised := w.show(pi, "", a.Id,
		artifactpack.File{Path: "index.html", Data: []byte("<script src=app.js></script>v2")},
		artifactpack.File{Path: "app.js", Data: []byte("draw(2)")})
	require.Equal(t, http.StatusCreated, revised.StatusCode(), string(revised.Body))

	sa2, got := w.shared(link.Token, "203.0.113.1")
	require.Equal(t, http.StatusOK, got.status, got.body)
	assert.Equal(t, 2, sa2.Revision)
	assert.Contains(t, w.anonymous(http.MethodGet, sa2.Files+"index.html", "203.0.113.1").body, "v2")
	assert.Equal(t, http.StatusNotFound, w.anonymous(http.MethodGet, sa.Files+"app.js", "203.0.113.1").status,
		"a file of the old revision")
}

// Scenario: the owner revokes Laura's link while her page is open, and deletes the chat of another dashboard he had
// shared. Each link, and its files, answer 404 at once; the other links of the same artifact still work.
func TestARevokedLinkOrOneOfADeletedChatIsGoneAtOnce(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	owner, _, chatID, a := sharedPage(t, w)

	laura, err := owner.CreateArtifactShareWithResponse(ctx, a.Id, openapi.NewArtifactShare{Label: "Laura"})
	require.NoError(t, err)

	kai, err := owner.CreateArtifactShareWithResponse(ctx, a.Id, openapi.NewArtifactShare{Label: "Kai"})
	require.NoError(t, err)

	sa, got := w.shared(laura.JSON201.Token, "203.0.113.2")
	require.Equal(t, http.StatusOK, got.status, got.body)

	revoked, err := owner.DeleteArtifactShareWithResponse(ctx, laura.JSON201.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, revoked.StatusCode(), string(revoked.Body))

	_, got = w.shared(laura.JSON201.Token, "203.0.113.2")
	assert.Equal(t, http.StatusNotFound, got.status)
	assert.Equal(t, http.StatusNotFound, w.anonymous(http.MethodGet, sa.Files+"app.js", "203.0.113.2").status)

	again, err := owner.DeleteArtifactShareWithResponse(ctx, laura.JSON201.Id)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, again.StatusCode())

	_, got = w.shared(kai.JSON201.Token, "203.0.113.2")
	require.Equal(t, http.StatusOK, got.status, "Kai's link is his own")

	listed, err := owner.ListArtifactSharesWithResponse(ctx, a.Id)
	require.NoError(t, err)
	require.Len(t, listed.JSON200.Data, 1)
	assert.Equal(t, "Kai", listed.JSON200.Data[0].Label)

	_, err = w.svc.DeleteChat(ctx, chatID)
	require.NoError(t, err)

	_, got = w.shared(kai.JSON201.Token, "203.0.113.2")
	assert.Equal(t, http.StatusNotFound, got.status, "the artifact went with its chat, and its links with it")
}

// Scenario (issue #996, review round 3 of #999): the control plane has not yet cleared old links' tokens from the
// database's files. What a link shows and its files alike answer 503, not 404 or 500; once cleared, both work.
func TestALinksFilesWaitLikeWhatItShowsWhileTheFilesAreCleared(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	owner, _, _, a := sharedPage(t, w)

	laura, err := owner.CreateArtifactShareWithResponse(ctx, a.Id, openapi.NewArtifactShare{Label: "Laura"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, laura.StatusCode(), string(laura.Body))

	sa, got := w.shared(laura.JSON201.Token, "203.0.113.3")
	require.Equal(t, http.StatusOK, got.status, got.body)

	w.exec("INSERT INTO share_token_scrub (id, since) VALUES (1, 0)")

	_, got = w.shared(laura.JSON201.Token, "203.0.113.3")
	assert.Equal(t, http.StatusServiceUnavailable, got.status, got.body)
	file := w.anonymous(http.MethodGet, sa.Files+"app.js", "203.0.113.3")
	assert.Equal(t, http.StatusServiceUnavailable, file.status, file.body)

	w.exec("DELETE FROM share_token_scrub")

	file = w.anonymous(http.MethodGet, sa.Files+"app.js", "203.0.113.3")
	assert.Equal(t, http.StatusOK, file.status, file.body)
}

// Scenario: someone tries tokens. After five unknown ones from one address within an hour, that address is locked,
// for what a link shows and for its files alike, with Retry-After, even with a link that exists; another address
// is not, and the lock ends (session.go's lock, LibreChat's per-address limit on its share route).
func TestUnknownTokensAreLimitedPerAddress(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _, _, a := sharedPage(t, w)

	made, err := owner.CreateArtifactShareWithResponse(t.Context(), a.Id, openapi.NewArtifactShare{Label: "Laura"})
	require.NoError(t, err)

	token := made.JSON201.Token

	now := time.Now()
	w.srv.now = func() time.Time { return now }

	guess := strings.Repeat("A", 43)
	for range 4 {
		_, got := w.shared(guess, "198.51.100.7")
		require.Equal(t, http.StatusNotFound, got.status)
	}

	_, fifth := w.shared(guess, "198.51.100.7")
	assert.Equal(t, http.StatusTooManyRequests, fifth.status)
	assert.Equal(t, "30", fifth.header.Get("Retry-After"))

	_, locked := w.shared(token, "198.51.100.7")
	assert.Equal(t, http.StatusTooManyRequests, locked.status, "locked even for a link that exists")

	files := w.anonymous(http.MethodGet, BasePath+"/shares/"+token+"/files/1/app.js", "198.51.100.7")
	assert.Equal(t, http.StatusTooManyRequests, files.status)
	assert.NotEmpty(t, files.header.Get("Retry-After"))

	_, other := w.shared(token, "198.51.100.8")
	assert.Equal(t, http.StatusOK, other.status)

	now = now.Add(31 * time.Second)

	_, later := w.shared(token, "198.51.100.7")
	assert.Equal(t, http.StatusOK, later.status)
}

// Scenario: the owner looks for every link in one place (issue #765). Links to dashboards in two chats are listed
// newest first, each with its artifact's newest title and type, its Bot and its chat; a revoked link leaves the
// list; a Bot's token cannot list them.
func TestEveryLinkIsListedWithWhatItShows(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	owner, pi, chatID, a := sharedPage(t, w)

	other, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	otherChat := other.ID
	shownB := w.show(w.piToken("pi-coordinator", other.ID, other.Bot), "Staff", "",
		artifactpack.File{Path: "index.html", Data: []byte("staff")})
	require.Equal(t, http.StatusCreated, shownB.StatusCode(), string(shownB.Body))
	b := shownB.JSON201

	laura, err := owner.CreateArtifactShareWithResponse(ctx, a.Id, openapi.NewArtifactShare{Label: "Laura"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, laura.StatusCode(), string(laura.Body))

	huy, err := owner.CreateArtifactShareWithResponse(ctx, b.Id, openapi.NewArtifactShare{Label: "Huy"})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, huy.StatusCode(), string(huy.Body))

	revised := w.show(pi, "Sales, November", a.Id, artifactpack.File{Path: "index.html", Data: []byte("v2")})
	require.Equal(t, http.StatusCreated, revised.StatusCode(), string(revised.Body))

	listed, err := owner.ListSharesWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode(), string(listed.Body))

	links := listed.JSON200.Data
	require.Len(t, links, 2)
	assert.False(t, links[0].CreatedAt.Before(links[1].CreatedAt), "newest first")

	byLabel := map[string]openapi.SharedLink{}
	for _, l := range links {
		byLabel[l.Label] = l
	}

	got := byLabel["Laura"]
	assert.Equal(t, laura.JSON201.Id, got.Id)
	assert.Equal(t, a.Id, got.ArtifactId)
	assert.Equal(t, "Sales, November", got.Title, "the newest revision's title")
	assert.Equal(t, openapi.ArtifactHTML, got.Type)
	assert.Equal(t, chatID, got.ChatId)
	assert.Equal(t, a.Bot, got.Bot)
	assert.NotEmpty(t, got.BotName, "the Bot's name to read")
	assert.Equal(t, "/s/"+laura.JSON201.Token, got.Path)
	assert.Equal(t, otherChat, byLabel["Huy"].ChatId)

	byBot, err := pi.ListSharesWithResponse(ctx)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, byBot.StatusCode())

	revoked, err := owner.DeleteArtifactShareWithResponse(ctx, huy.JSON201.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, revoked.StatusCode())

	after, err := owner.ListSharesWithResponse(ctx)
	require.NoError(t, err)
	require.Len(t, after.JSON200.Data, 1)
	assert.Equal(t, "Laura", after.JSON200.Data[0].Label)
}
