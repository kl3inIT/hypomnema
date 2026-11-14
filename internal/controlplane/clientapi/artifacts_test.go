package clientapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/artifactpack"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

func tarOf(t *testing.T, files ...artifactpack.File) *bytes.Reader {
	t.Helper()

	body, err := artifactpack.Tar(files)
	require.NoError(t, err)

	return bytes.NewReader(body)
}

func (w *world) show(c *openapi.ClientWithResponses, title, id string, files ...artifactpack.File,
) *openapi.ShowArtifactResponse {
	w.t.Helper()

	p := &openapi.ShowArtifactParams{}
	if title != "" {
		p.Title = &title
	}

	if id != "" {
		p.Id = &id
	}

	resp, err := c.ShowArtifactWithBodyWithResponse(w.t.Context(), p, "application/octet-stream", tarOf(w.t, files...))
	require.NoError(w.t, err)

	return resp
}

// Scenario: a Bot's pi shows a dashboard folder in its chat; the owner and a coordinator list and read it. A pi of
// another chat does not see it, and only a Bot shows one. A folder of 2 MB passes the 1 MB a body usually has;
// one over the archive's bound is refused as too large.
func TestOnlyABotShowsAnArtifactAndOnlyItsChatsPiReadsIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("web", bearertoken.Owner)
	coord, _ := w.token("pi-coordinator", bearertoken.Coordinator)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	other, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi := w.piToken("pi-coordinator", chat.ID, chat.Bot)
	stranger := w.piToken("pi-coordinator", other.ID, other.Bot)

	data := artifactpack.File{Path: "data.json", Data: bytes.Repeat([]byte("1"), 2<<20)}
	shown := w.show(pi, "Sales", "", artifactpack.File{Path: "index.html", Data: []byte("<p>sales")}, data)
	require.Equal(t, http.StatusCreated, shown.StatusCode(), string(shown.Body))
	a := shown.JSON201
	assert.Equal(t, chat.ID, a.ChatId)
	assert.Equal(t, openapi.ArtifactHTML, a.Type)

	listed, err := owner.ListArtifactsWithResponse(ctx, &openapi.ListArtifactsParams{ChatId: &chat.ID})
	require.NoError(t, err)
	require.Len(t, listed.JSON200.Data, 1)
	assert.Equal(t, a.Id, listed.JSON200.Data[0].Id)

	read, err := coord.GetArtifactWithResponse(ctx, a.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, read.StatusCode())
	require.Len(t, *read.JSON200.Revisions, 1)
	assert.Len(t, (*read.JSON200.Revisions)[0].Files, 2)

	hidden, err := stranger.GetArtifactWithResponse(ctx, a.Id)
	require.NoError(t, err)
	assert.Equal(t, openapi.NotFound, errorCode(t, hidden.Body, hidden.StatusCode(), http.StatusNotFound))

	theirs, err := stranger.ListArtifactsWithResponse(ctx, &openapi.ListArtifactsParams{})
	require.NoError(t, err)
	assert.Empty(t, theirs.JSON200.Data, "another chat's pi lists only its own chat's")

	replaced := w.show(stranger, "", a.Id, artifactpack.File{Path: "index.html", Data: []byte("x")})
	assert.Equal(t, http.StatusNotFound, replaced.StatusCode())

	byOwner := w.show(owner, "Mine", "", artifactpack.File{Path: "index.html", Data: []byte("x")})
	assert.Equal(t, openapi.Forbidden, errorCode(t, byOwner.Body, byOwner.StatusCode(), http.StatusForbidden))

	big := artifactpack.File{Path: "big.json", Data: bytes.Repeat([]byte("1"), artifactpack.MaxBody)}
	tooBig := w.show(pi, "Big", "", artifactpack.File{Path: "index.html"}, big)
	assert.Equal(t, http.StatusRequestEntityTooLarge, tooBig.StatusCode())

	over := w.show(pi, "Over", "", artifactpack.File{Path: "p.html", Data: bytes.Repeat([]byte("a"), 300<<10)})
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, over.Body, over.StatusCode(), http.StatusBadRequest))
	assert.Contains(t, string(over.Body), "256 KB")
}

// Scenario: the web shows a folder page in a locked frame. A file is read as an opaque download, never a page of
// this origin. The frame's files load from an address of their own, which needs no sign-in: each answer is the
// file's type, sandboxed, readable from the frame's opaque origin and not cached; a path outside the revision,
// an unknown address and a write get nothing. Only the owner opens a frame.
func TestAFolderPagesFilesLoadFromItsFramesAddressOnly(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()
	owner, _ := w.token("web", bearertoken.Owner)
	coord, _ := w.token("pi-coordinator", bearertoken.Coordinator)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi := w.piToken("pi-coordinator", chat.ID, chat.Bot)
	shown := w.show(pi, "Sales", "",
		artifactpack.File{Path: "index.html", Data: []byte("<script src=app.js></script>")},
		artifactpack.File{Path: "app.js", Data: []byte("draw()")},
		artifactpack.File{Path: "data/sales.csv", Data: []byte("day,total")})
	require.Equal(t, http.StatusCreated, shown.StatusCode(), string(shown.Body))
	a := shown.JSON201

	raw, err := owner.ReadArtifactFile(ctx, a.Id, 1, &openapi.ReadArtifactFileParams{})
	require.NoError(t, err)

	body, _ := io.ReadAll(raw.Body)
	_ = raw.Body.Close()

	assert.Equal(t, "<script src=app.js></script>", string(body))
	assert.Equal(t, "application/octet-stream", raw.Header.Get("Content-Type"))
	assert.Equal(t, "nosniff", raw.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "sandbox; default-src 'none'", raw.Header.Get("Content-Security-Policy"))
	assert.Equal(t, `attachment; filename=index.html`, raw.Header.Get("Content-Disposition"))

	refused, err := coord.OpenArtifactFrameWithResponse(ctx, a.Id, 1)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode())

	frame, err := owner.OpenArtifactFrameWithResponse(ctx, a.Id, 1)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, frame.StatusCode(), string(frame.Body))
	folder := frame.JSON200.Url
	assert.True(t, strings.HasPrefix(folder, BasePath+"/artifact-files/") && strings.HasSuffix(folder, "/"))

	type answer struct {
		status int
		header http.Header
		text   string
	}

	get := func(method, url string) answer {
		t.Helper()

		req, err := http.NewRequestWithContext(ctx, method, w.url+url, nil)
		require.NoError(t, err)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)

		defer func() { _ = resp.Body.Close() }()

		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		return answer{status: resp.StatusCode, header: resp.Header, text: string(b)}
	}

	csv := get(http.MethodGet, folder+"data/sales.csv")
	require.Equal(t, http.StatusOK, csv.status, csv.text)
	assert.Equal(t, "day,total", csv.text)
	assert.Equal(t, "text/csv; charset=utf-8", csv.header.Get("Content-Type"))
	assert.Equal(t, "sandbox allow-scripts; default-src 'none'", csv.header.Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", csv.header.Get("X-Content-Type-Options"))
	assert.Equal(t, "*", csv.header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "no-referrer", csv.header.Get("Referrer-Policy"))
	assert.Equal(t, "private, no-store", csv.header.Get("Cache-Control"))

	assert.Equal(t, "text/javascript; charset=utf-8", get(http.MethodGet, folder+"app.js").header.Get("Content-Type"))
	assert.Equal(t, http.StatusNotFound, get(http.MethodGet, folder+"secret.json").status)
	assert.NotEqual(t, http.StatusOK, get(http.MethodGet, folder+"../app.js").status)

	unknown := get(http.MethodGet, BasePath+"/artifact-files/not-a-token/app.js")
	assert.Equal(t, http.StatusNotFound, unknown.status)
	assert.Equal(t, "sandbox allow-scripts; default-src 'none'", unknown.header.Get("Content-Security-Policy"))

	assert.Equal(t, http.StatusMethodNotAllowed, get(http.MethodPost, folder+"app.js").status)
	assert.Equal(t, http.StatusNoContent, get(http.MethodOptions, folder+"app.js").status)

	zip, err := owner.GetArtifactArchiveWithResponse(ctx, a.Id, 1)
	require.NoError(t, err)
	assert.Equal(t, "application/zip", zip.HTTPResponse.Header.Get("Content-Type"))
	assert.True(t, bytes.HasPrefix(zip.Body, []byte("PK")))
}

// Scenario: the web follows a chat while the Bot revises a dashboard it showed earlier. The stream sends the
// artifact's message again, without an id since its seq is behind the cursor, with the new revision; a stream
// that resumes after it sends it once, as it is now.
func TestAChatsStreamSendsARevisedArtifactAgain(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	ctx := t.Context()

	_, _ = w.token("pi-coordinator", bearertoken.Coordinator)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi := w.piToken("pi-coordinator", chat.ID, chat.Bot)
	first := w.show(pi, "Sales", "", artifactpack.File{Path: "index.html", Data: []byte("v1")})
	require.Equal(t, http.StatusCreated, first.StatusCode(), string(first.Body))

	msgs, err := w.svc.ListChatMessages(ctx, chat.ID, 0, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 1)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url+BasePath+"/chats/"+chat.ID+"/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+bearerOf(t, w, "stream"))
	req.Header.Set("Last-Event-ID", strconv.FormatInt(msgs[0].Seq, 10))

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	lines := make(chan string, 100)

	go func() {
		defer close(lines)

		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if l := sc.Text(); l != "" && !strings.HasPrefix(l, ":") {
				lines <- l
			}
		}
	}()

	next := func() string {
		t.Helper()

		select {
		case l, ok := <-lines:
			require.True(t, ok, "the stream ended")

			return l
		case <-time.After(5 * time.Second):
			t.Fatal("no frame in time")

			return ""
		}
	}

	card := func() openapi.ArtifactCard {
		t.Helper()

		assert.Equal(t, "event: message", next(), "no id: the seq is behind the cursor")

		var m openapi.ChatMessage
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(next(), "data: ")), &m))
		assert.Equal(t, msgs[0].Seq, m.Seq)
		require.NotNil(t, m.Artifact)

		return *m.Artifact
	}

	assert.Equal(t, "event: state", next())
	next()
	assert.Equal(t, 1, card().Revision, "a resumed stream sends it once as it is")

	second := w.show(pi, "Sales by region", first.JSON201.Id, artifactpack.File{Path: "index.html", Data: []byte("v2")})
	require.Equal(t, http.StatusCreated, second.StatusCode(), string(second.Body))

	revised := card()
	assert.Equal(t, 2, revised.Revision)
	assert.Equal(t, "Sales by region", revised.Title)

	close(w.stop)

	for range lines {
		// Drain until the server ends the stream.
	}
}
