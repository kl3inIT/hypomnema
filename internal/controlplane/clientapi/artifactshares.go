package clientapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// View-only links (issue #745): the owner makes a link to an artifact, and anyone who has it opens the web's page
// /s/<token> without signing in and sees the artifact's newest revision, until the owner revokes it. The page reads
// GetShare, a public operation as CreateSession is, and loads the artifact in the same locked frame the app uses
// (issue #737): by src from this server's files route for the link, whose answer carries the page's policy. The
// files route checks the link and the newest revision on every request, as OpenMausBot's server/page-files.ts
// checks its permission again on every file, so a revoke takes effect at once; no frame token is minted for it.

// sharesPath is where what a link shows is read; its files are under <token>/files/<revision>/.
const sharesPath = BasePath + "/shares/"

// shareHeaders are on every answer under sharesPath: never indexed (LibreChat's X-Robots-Tag on every answer of its
// share routes, open-webui's robots meta), never a referrer that carries the token (OpenMausBot), never cached
// (LibreChat), never framed (Grafana denies framing by default). The web's page /s/<token> carries the same
// (webui.go).
var shareHeaders = map[string]string{
	"X-Robots-Tag":            "noindex, nofollow",
	"Referrer-Policy":         "no-referrer",
	"Cache-Control":           "private, no-store",
	"Content-Security-Policy": "frame-ancestors 'none'",
}

func withShareHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range shareHeaders {
			w.Header().Set(k, v)
		}

		next.ServeHTTP(w, r)
	})
}

// CreateArtifactShare makes a view-only link to an artifact.
func (s *Server) CreateArtifactShare(ctx context.Context, req openapi.CreateArtifactShareRequestObject,
) (openapi.CreateArtifactShareResponseObject, error) {
	sh, err := s.svc.CreateArtifactShare(ctx, req.ArtifactId, req.Body.Label)
	if err != nil {
		return nil, err
	}

	return openapi.CreateArtifactShare201JSONResponse(toArtifactShare(sh)), nil
}

// ListArtifactShares lists an artifact's links.
func (s *Server) ListArtifactShares(ctx context.Context, req openapi.ListArtifactSharesRequestObject,
) (openapi.ListArtifactSharesResponseObject, error) {
	list, err := s.svc.ListArtifactShares(ctx, req.ArtifactId)
	if err != nil {
		return nil, err
	}

	out := openapi.ListArtifactShares200JSONResponse{Data: make([]openapi.ArtifactShare, 0, len(list))}
	for _, sh := range list {
		out.Data = append(out.Data, toArtifactShare(sh))
	}

	return out, nil
}

// ListShares lists every link with what it shows (issue #765).
func (s *Server) ListShares(ctx context.Context, _ openapi.ListSharesRequestObject,
) (openapi.ListSharesResponseObject, error) {
	list, err := s.svc.ListSharedLinks(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListShares200JSONResponse{Data: make([]openapi.SharedLink, 0, len(list))}
	for _, l := range list {
		out.Data = append(out.Data, openapi.SharedLink{
			Id: l.ID, ArtifactId: l.ArtifactID, Label: l.Label, Token: l.Token, Path: "/s/" + l.Token,
			CreatedAt: l.CreatedAt, Views: l.Views, LastViewedAt: l.LastViewedAt,
			Title: l.Title, Type: openapi.ArtifactType(l.Type), Bot: l.Bot, BotName: l.BotName,
			ChatId: l.ChatID,
		})
	}

	return out, nil
}

// DeleteArtifactShare revokes a link.
func (s *Server) DeleteArtifactShare(ctx context.Context, req openapi.DeleteArtifactShareRequestObject,
) (openapi.DeleteArtifactShareResponseObject, error) {
	if err := s.svc.DeleteArtifactShare(ctx, req.ShareId); err != nil {
		return nil, err
	}

	return openapi.DeleteArtifactShare204Response{}, nil
}

// GetShare returns what a link shows. An address that tries unknown tokens is locked as one that guesses
// passwords is (LibreChat limits its share route per address).
func (s *Server) GetShare(ctx context.Context, req openapi.GetShareRequestObject,
) (openapi.GetShareResponseObject, error) {
	sa, err := s.sharedArtifact(ctx, callerOf(ctx).address, req.Token)
	if err != nil {
		return nil, err
	}

	return openapi.GetShare200JSONResponse{
		Title: sa.Title, Type: openapi.ArtifactType(sa.Type), Entry: sa.Entry, MediaType: sa.MediaType,
		Revision: sa.Revision, UpdatedAt: sa.UpdatedAt, Files: shareFilesPath(req.Token, sa.Revision),
	}, nil
}

// sharedArtifact is what a token shows, counting an unknown one against the address that sent it.
func (s *Server) sharedArtifact(ctx context.Context, address, token string) (orchestration.SharedArtifact, error) {
	now := s.now()
	if wait := s.shareGuesses.locked(address, now); wait > 0 {
		return orchestration.SharedArtifact{}, &lockedError{wait: wait}
	}

	sa, err := s.svc.SharedArtifact(ctx, token)
	if errors.Is(err, orchestration.ErrNotFound) {
		if wait := s.shareGuesses.failed(address, now); wait > 0 {
			return orchestration.SharedArtifact{}, &lockedError{wait: wait}
		}
	}

	return sa, err
}

// shareFiles serves the files of a link's artifact at its newest revision. A file of a revision that is no longer
// the newest answers 404, until the page reloads on the new one. Each opening of the page itself is counted as a
// view.
func (s *Server) shareFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", shareHeaders["X-Robots-Tag"])

	if !frameRequest(w, r) {
		return
	}

	token := r.PathValue("token")

	sa, err := s.sharedArtifact(r.Context(), newCaller(r).address, token)

	var locked *lockedError

	switch {
	case errors.As(err, &locked):
		w.Header().Set("Retry-After", retryAfter(locked.wait))
		answerText(w, http.StatusTooManyRequests, "Too many unknown links; try again later.")

		return
	case errors.Is(err, orchestration.ErrNotFound):
		answerText(w, http.StatusNotFound, "Not found.")

		return
	case errors.Is(err, orchestration.ErrUnavailable):
		// Old links' tokens are being cleared from the database's files (issue #996), as what a link shows answers.
		answerText(w, http.StatusServiceUnavailable, "Links are not served yet; try again shortly.")

		return
	case err != nil:
		answerText(w, http.StatusInternalServerError, "The link could not be read.")

		return
	}

	if rev, err := strconv.Atoi(r.PathValue("rev")); err != nil || rev != sa.Revision {
		answerText(w, http.StatusNotFound, "Not found.")

		return
	}

	path := r.PathValue("path")
	if !s.serveFrameFile(w, r, sa.ArtifactID, sa.Revision, sa.Type, shareFilesPath(token, sa.Revision)) ||
		r.Method != http.MethodGet || path != sa.Entry {
		return
	}

	s.svc.CountArtifactShareView(sa.ShareID)
}

// shareFilesPath is where a link's files load from at a revision, ending in "/".
func shareFilesPath(token string, revision int) string {
	return sharesPath + token + "/files/" + strconv.Itoa(revision) + "/"
}

func toArtifactShare(sh orchestration.ArtifactShare) openapi.ArtifactShare {
	return openapi.ArtifactShare{
		Id: sh.ID, ArtifactId: sh.ArtifactID, Label: sh.Label, Token: sh.Token, Path: "/s/" + sh.Token,
		CreatedAt: sh.CreatedAt, Views: sh.Views, LastViewedAt: sh.LastViewedAt,
	}
}
