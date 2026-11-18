package clientapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/artifactpack"
)

// Artifacts (issue #402): a Bot shows one with `hypomnema artifact show`, its pi's token naming the chat; every
// token reads them, a Bot's only its own chat's (its Bot's for a Routine's script), as a pi's token reads only its
// own chat's Runs (issue #260). A file is sent as /raw sends one, never as a page of this origin. A page and its
// files load in its locked frame from a short-lived address of their own (OpenMausBot's server/page-files.ts,
// Onyx's static_preview.py), which the frame reaches without the owner's session.

// ShowArtifact keeps the files a Bot shows as a new artifact in its chat, or as the next revision of one.
func (s *Server) ShowArtifact(ctx context.Context, req openapi.ShowArtifactRequestObject,
) (openapi.ShowArtifactResponseObject, error) {
	tok := principal(ctx)
	if tok.Kind != bearertoken.Bot {
		return nil, fmt.Errorf("%w: only a Bot shows an artifact, with `hypomnema artifact show` in its chat",
			errForbidden)
	}

	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("reading the files: %w", err)
	}

	pack, err := artifactpack.FromTar(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	}

	a, changed, err := s.svc.ShowArtifact(ctx, orchestration.ArtifactMaker{Bot: tok.Bot, Chat: tok.Chat},
		orchestration.ShowArtifact{ID: val(req.Params.Id), Title: val(req.Params.Title), Pack: pack})
	if err != nil {
		return nil, err
	}

	if changed {
		return openapi.ShowArtifact201JSONResponse(toArtifact(a)), nil
	}

	return openapi.ShowArtifact200JSONResponse(toArtifact(a)), nil
}

// ListArtifacts lists artifacts, a chat's when one is named.
func (s *Server) ListArtifacts(ctx context.Context, req openapi.ListArtifactsRequestObject,
) (openapi.ListArtifactsResponseObject, error) {
	tok := principal(ctx)
	f := orchestration.ArtifactFilter{ChatID: val(req.Params.ChatId), Limit: val(req.Params.Limit)}

	if tok.Kind == bearertoken.Bot {
		if tok.Chat == "" {
			// A Routine's script, which has no chat, reads its Bot's.
			if f.ChatID != "" {
				return nil, fmt.Errorf("%w: a Routine's run has no chat", errForbidden)
			}

			f.Bot = tok.Bot
		} else {
			chat, err := chatOf(tok, f.ChatID)
			if err != nil {
				return nil, err
			}

			f.ChatID = chat
		}
	}

	list, err := s.svc.ListArtifacts(ctx, f)
	if err != nil {
		return nil, err
	}

	out := openapi.ListArtifacts200JSONResponse{Data: make([]openapi.Artifact, 0, len(list))}
	for _, a := range list {
		out.Data = append(out.Data, toArtifact(a))
	}

	return out, nil
}

// readableArtifact returns an artifact the caller may read: any, but a Bot's token only its own chat's, or its
// Bot's for a Routine's script. Another's is not there for it.
func (s *Server) readableArtifact(ctx context.Context, id string) (orchestration.Artifact, error) {
	a, err := s.svc.GetArtifact(ctx, id)
	if err != nil {
		return orchestration.Artifact{}, err
	}

	tok := principal(ctx)
	if tok.Kind == bearertoken.Bot && (a.Bot != tok.Bot || tok.Chat != "" && a.ChatID != tok.Chat) {
		return orchestration.Artifact{}, fmt.Errorf("%w: artifact %s", orchestration.ErrNotFound, id)
	}

	return a, nil
}

// GetArtifact returns an artifact with its revisions.
func (s *Server) GetArtifact(ctx context.Context, req openapi.GetArtifactRequestObject,
) (openapi.GetArtifactResponseObject, error) {
	a, err := s.readableArtifact(ctx, req.ArtifactId)
	if err != nil {
		return nil, err
	}

	return openapi.GetArtifact200JSONResponse(toArtifact(a)), nil
}

// ReadArtifactFile sends a file of a revision as an opaque download, as /raw does.
func (s *Server) ReadArtifactFile(ctx context.Context, req openapi.ReadArtifactFileRequestObject,
) (openapi.ReadArtifactFileResponseObject, error) {
	if _, err := s.readableArtifact(ctx, req.ArtifactId); err != nil {
		return nil, err
	}

	f, err := s.svc.ReadArtifactFile(ctx, req.ArtifactId, req.Revision, val(req.Params.Path))
	if err != nil {
		return nil, err
	}

	name := f.Path[strings.LastIndex(f.Path, "/")+1:]

	return download{name: name, data: f.Data}, nil
}

// GetArtifactArchive sends every file of a revision as a zip.
func (s *Server) GetArtifactArchive(ctx context.Context, req openapi.GetArtifactArchiveRequestObject,
) (openapi.GetArtifactArchiveResponseObject, error) {
	a, err := s.readableArtifact(ctx, req.ArtifactId)
	if err != nil {
		return nil, err
	}

	files, err := s.svc.ReadArtifactRevision(ctx, req.ArtifactId, req.Revision)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Path, Method: zip.Deflate, Modified: a.UpdatedAt})
		if err != nil {
			return nil, fmt.Errorf("packing %s: %w", f.Path, err)
		}

		if _, err := w.Write(f.Data); err != nil {
			return nil, fmt.Errorf("packing %s: %w", f.Path, err)
		}
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("packing artifact %s: %w", a.ID, err)
	}

	return download{name: a.ID + "-" + strconv.Itoa(req.Revision) + ".zip", data: buf.Bytes(),
		contentType: "application/zip"}, nil
}

// download is a file sent to be saved, never shown as a page of this origin: an opaque type, nosniff, and a
// sandboxing policy should it be opened (as /raw, workspaces.go).
type download struct {
	name        string
	data        []byte
	contentType string
}

func (d download) write(w http.ResponseWriter) error {
	h := w.Header()

	ct := d.contentType
	if ct == "" {
		ct = "application/octet-stream"
	}

	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.name}))
	h.Set("Content-Length", strconv.Itoa(len(d.data)))
	h.Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)

	_, err := w.Write(d.data)

	return err
}

func (d download) VisitReadArtifactFileResponse(w http.ResponseWriter) error { return d.write(w) }

func (d download) VisitGetArtifactArchiveResponse(w http.ResponseWriter) error { return d.write(w) }

// OpenArtifactFrame gives the address a page and its files load from in its frame.
func (s *Server) OpenArtifactFrame(ctx context.Context, req openapi.OpenArtifactFrameRequestObject,
) (openapi.OpenArtifactFrameResponseObject, error) {
	a, err := s.readableArtifact(ctx, req.ArtifactId)
	if err != nil {
		return nil, err
	}

	kind, ok := artifactRevisionType(a, req.Revision)
	if !ok {
		return nil, fmt.Errorf("%w: revision %d of artifact %s", orchestration.ErrNotFound, req.Revision, a.ID)
	}

	token, err := s.frames.add(a.ID, req.Revision, kind, s.now())
	if err != nil {
		return nil, err
	}

	return openapi.OpenArtifactFrame200JSONResponse{Url: artifactFilesPath + token + "/"}, nil
}

func artifactRevisionType(a orchestration.Artifact, rev int) (artifactpack.Type, bool) {
	for _, r := range a.Revisions {
		if r.Revision == rev {
			return r.Type, true
		}
	}

	return "", false
}

func toArtifact(a orchestration.Artifact) openapi.Artifact {
	out := openapi.Artifact{
		Id: a.ID, ChatId: a.ChatID, Bot: a.Bot, Revision: a.Revision, Title: a.Title,
		Type: openapi.ArtifactType(a.Type), Entry: a.Entry, Size: a.Size, CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}

	if a.Revisions != nil {
		revs := make([]openapi.ArtifactRevision, 0, len(a.Revisions))
		for _, r := range a.Revisions {
			files := make([]openapi.ArtifactFileInfo, 0, len(r.Files))
			for _, f := range r.Files {
				files = append(files, openapi.ArtifactFileInfo{Path: f.Path, Size: f.Size, MediaType: f.MediaType})
			}

			revs = append(revs, openapi.ArtifactRevision{
				Revision: r.Revision, Title: r.Title, Type: openapi.ArtifactType(r.Type), Entry: r.Entry,
				Size: r.Size, Files: files, CreatedAt: r.CreatedAt,
			})
		}

		out.Revisions = &revs
	}

	return out
}

func toArtifactCard(c *orchestration.ArtifactCard) *openapi.ArtifactCard {
	if c == nil {
		return nil
	}

	return &openapi.ArtifactCard{
		Id: c.ID, Revision: c.Revision, Title: c.Title, Type: openapi.ArtifactType(c.Type), UpdatedAt: c.UpdatedAt,
	}
}

// artifactFilesPath is where a page and its files load from: the frame's own address, as OpenMausBot's
// /api/page-files/<token>/ and Onyx's /build/static-preview/<token>/.
const artifactFilesPath = BasePath + "/artifact-files/"

// frameTTL is how long a frame's address lasts after its last use: Onyx's preview token lasts an hour.
const frameTTL = time.Hour

// maxFrames bounds the addresses kept; the oldest goes first (OpenMausBot's MAX_TOKENS).
const maxFrames = 5000

// artifactFrames are the addresses of folder pages' files, kept in memory as the terminals' tickets are: the
// control plane is one process, and a page opened again asks for a new one.
type artifactFrames struct {
	mu     sync.Mutex
	frames map[string]*artifactFrame
}

type artifactFrame struct {
	artifact string
	revision int
	kind     artifactpack.Type
	used     time.Time
}

func (f *artifactFrames) add(artifact string, revision int, kind artifactpack.Type, now time.Time) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("making a frame's address: %w", err)
	}

	token := base64.RawURLEncoding.EncodeToString(raw)

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.frames == nil {
		f.frames = map[string]*artifactFrame{}
	}

	var oldest string

	for t, fr := range f.frames {
		if now.Sub(fr.used) > frameTTL {
			delete(f.frames, t)
		} else if oldest == "" || fr.used.Before(f.frames[oldest].used) {
			oldest = t
		}
	}

	if len(f.frames) >= maxFrames {
		delete(f.frames, oldest)
	}

	f.frames[token] = &artifactFrame{artifact: artifact, revision: revision, kind: kind, used: now}

	return token, nil
}

// take returns what a token serves, and marks it used; false when it is unknown or has lapsed.
func (f *artifactFrames) take(token string, now time.Time) (artifactFrame, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fr, ok := f.frames[token]
	if !ok {
		return artifactFrame{}, false
	}

	if now.Sub(fr.used) > frameTTL {
		delete(f.frames, token)

		return artifactFrame{}, false
	}

	fr.used = now

	return *fr, true
}

// frameHeaders are on every answer of the files route (OpenMausBot's SHARED_HEADERS): read from the frame's opaque
// origin, never a page of this origin if opened directly, never cached. A page itself is sent with its own policy
// instead (pagePolicy).
var frameHeaders = map[string]string{
	"Access-Control-Allow-Origin":   "*",
	"Access-Control-Expose-Headers": "Content-Length",
	"Cross-Origin-Resource-Policy":  "cross-origin",
	"X-Content-Type-Options":        "nosniff",
	"Referrer-Policy":               "no-referrer",
	"Cache-Control":                 "private, no-store",
	"Content-Security-Policy":       fileCSP,
}

// fileCSP is the policy of every answer but a page's: none of it runs, opened on its own.
const fileCSP = "sandbox allow-scripts; default-src 'none'"

// A page is its frame's document, loaded from this route by its address (issue #737), so that its policy is this
// answer's header and nothing else: a srcdoc frame took the web client's own policy on top of its meta, which
// refused its inline and CDN scripts and its base. Onyx's static preview sends a page so (PREVIEW_HEADERS in
// backend/onyx/server/features/build/static_preview.py), as LibreChat's MCP app sandbox does (buildSandboxResponse
// in packages/api/src/mcp/sandbox.ts). The directives are OpenMausBot's BOT_HTML_CSP and folderPageCsp
// (shared/app-frame/csp.ts), which the frame's meta carried until then.

// pageCDNs are where a page's scripts, styles and fonts load from: three package CDNs. Its images come from them,
// its own files, data: and blob: only, so no tracking pixel reaches another host.
const pageCDNs = "https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com"

// duckdbExtensions is where DuckDB-WASM fetches its readers, Parquet's among them, when a query first needs one.
const duckdbExtensions = "https://extensions.duckdb.org"

// pagePolicy is a page's policy: sandboxed as its frame is (the sandbox attribute's flags, so that a form's submit
// event still reaches its script), its own files under files (its address, by http and https as Onyx names it,
// never the rest of this origin, the API included) and the CDNs, framed only by the web client.
func pagePolicy(files string) string {
	own := "http://" + files + " https://" + files

	return strings.Join([]string{
		"sandbox allow-scripts allow-forms allow-downloads",
		"default-src 'none'",
		"script-src 'unsafe-inline' 'unsafe-eval' 'wasm-unsafe-eval' " + pageCDNs + " " + own,
		"style-src 'unsafe-inline' " + pageCDNs + " https://fonts.googleapis.com " + own,
		"font-src data: " + pageCDNs + " https://fonts.gstatic.com " + own,
		"img-src data: blob: " + pageCDNs + " " + own,
		"media-src data: blob: " + own,
		"connect-src " + pageCDNs + " " + own + " " + duckdbExtensions,
		"worker-src blob:",
		"frame-src 'none'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'none'",
		"manifest-src 'none'",
		"frame-ancestors 'self'",
	}, "; ")
}

// plainHost is a Host a policy may name: a name or an address, and a port. Anything else would be read as more of
// the policy (LibreChat's FRAME_ANCESTOR_RE, packages/api/src/mcp/sandbox.ts).
var plainHost = regexp.MustCompile(`^([a-zA-Z0-9][a-zA-Z0-9.-]*|\[[0-9a-fA-F:.]+\])(:[0-9]{1,5})?$`)

// artifactFiles serves a page and its files to its frame by the token in the address, which is its whole
// authority: the frame sends no sign-in. Only the files of the revision the token names are served, by their exact
// path.
func (s *Server) artifactFiles(w http.ResponseWriter, r *http.Request) {
	if !frameRequest(w, r) {
		return
	}

	fr, ok := s.frames.take(r.PathValue("token"), s.now())
	if !ok {
		answerText(w, http.StatusNotFound, "Not found.")

		return
	}

	s.serveFrameFile(w, r, fr.artifact, fr.revision, fr.kind, artifactFilesPath+r.PathValue("token")+"/")
}

// frameRequest sets the frame's headers on an answer of a files route, and answers what is not a read; false
// when it answered.
func frameRequest(w http.ResponseWriter, r *http.Request) bool {
	for k, v := range frameHeaders {
		w.Header().Set(k, v)
	}

	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)

		return false
	case http.MethodGet, http.MethodHead:
		return true
	default:
		answerText(w, http.StatusMethodNotAllowed, "Read only.")

		return false
	}
}

// serveFrameFile sends a file of a revision by the request's path, a page with its policy naming folder, the
// address its files load from under this host; false when it answered with an error.
func (s *Server) serveFrameFile(w http.ResponseWriter, r *http.Request, artifact string, revision int,
	kind artifactpack.Type, folder string,
) bool {
	requestedPath := r.PathValue("path")
	if requestedPath != "" && artifactpack.CheckPath(requestedPath) != nil {
		answerText(w, http.StatusBadRequest, "Not a file of this page.")

		return false
	}

	f, err := s.svc.ReadArtifactFile(r.Context(), artifact, revision, requestedPath)
	if errors.Is(err, orchestration.ErrNotFound) {
		answerText(w, http.StatusNotFound, "Not found.")

		return false
	}

	if err != nil {
		answerText(w, http.StatusInternalServerError, "The file could not be read.")

		return false
	}

	if kind == artifactpack.TypeHTML && artifactpack.PageDownload(f.Path) {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
			"filename": path.Base(f.Path),
		}))
	} else if isPage(f.MediaType) {
		if !plainHost.MatchString(r.Host) {
			answerText(w, http.StatusBadRequest, "Not a host a page can name.")

			return false
		}

		w.Header().Set("Content-Security-Policy", pagePolicy(r.Host+folder))
	}

	w.Header().Set("Content-Type", f.MediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Data)))
	w.WriteHeader(http.StatusOK)

	if r.Method == http.MethodGet {
		_, _ = w.Write(f.Data)
	}

	return true
}

// answerText answers a files route's request with a line of text.
func answerText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}

// isPage says whether a file is opened as a page: HTML, the type artifactpack gives .html and .htm.
func isPage(mediaType string) bool {
	t, _, _ := mime.ParseMediaType(mediaType)

	return t == "text/html"
}
