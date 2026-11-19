package clientapi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// The Studio (issue #818, row 12): one owner listing of the takes and the
// videos, as Cap's My Caps lists a user's recordings a page at a time
// (apps/web/app/(org)/dashboard/caps/caps-page-data.tsx; layout learned,
// nothing copied, Cap being AGPLv3). The order is stable, (created_at desc,
// id, kind), the filters and the search are applied before the page is cut,
// and a page goes on after the last item of the page before, as the Runs'
// cursor does (convert.go), so an item made between two pages is not read
// twice and none is skipped. The takes and videos are few (an owner's), and
// read whole at each page, as GET /takes and GET /videos read them.

const (
	studioLimit    = 50
	studioMaxLimit = 100
)

// The state groups the Studio filters by.
const (
	groupInProgress = "in_progress"
	groupReview     = "review"
	groupApproved   = "approved"
	groupFailed     = "failed"
)

// studioItem is a take or a video as the Studio lists it.
type studioItem struct {
	openapi.StudioItem
	// group is the state's group; empty for an available take, which is in none.
	group string
}

// takeGroup is a take's state group: filmed, paused, finishing or uploading is in progress; failed is failed.
func takeGroup(state string) string {
	switch state {
	case devicetool.TakeAvailable:
		return ""
	case devicetool.TakeFailed:
		return groupFailed
	default:
		return groupInProgress
	}
}

// revisionGroup is a revision's state group.
func revisionGroup(state string) string {
	switch {
	case videos.Making(state):
		return groupInProgress
	case state == videos.Review:
		return groupReview
	case state == videos.Approved:
		return groupApproved
	default:
		return groupFailed
	}
}

// madeState says whether a revision in a state has its files.
func madeState(state string) bool {
	return state == videos.Review || state == videos.Approved || state == videos.ChangesRequested
}

// studioItems are the takes and videos as the Studio lists them, in no order.
func studioItems(takes []devicetool.Take, vids []videos.Video) []studioItem {
	out := make([]studioItem, 0, len(takes)+len(vids))
	filmedBy := make(map[string]string, len(takes))

	for _, t := range takes {
		bot := cmp.Or(t.AgentName, t.Agent)
		filmedBy[t.ID] = bot

		item := studioItem{
			StudioItem: openapi.StudioItem{
				Kind: openapi.StudioItemKindTake, Id: t.ID, Title: t.Title, Bot: bot, CreatedAt: t.StartedAt.UTC(),
				LengthMs: t.DurationMs, State: t.State,
			},
			group: takeGroup(t.State),
		}

		if t.State == devicetool.TakeAvailable {
			item.Thumbnail = ptr("/api/v1/takes/" + url.PathEscape(t.ID) + "/thumbnail.jpg")
		}

		out = append(out, item)
	}

	for _, v := range vids {
		if len(v.Revisions) == 0 {
			continue
		}

		latest := v.Latest()
		item := studioItem{
			StudioItem: openapi.StudioItem{
				Kind: openapi.StudioItemKindVideo, Id: v.ID, Title: v.Title,
				Bot: cmp.Or(filmedBy[v.Take], v.MadeByName), CreatedAt: v.CreatedAt.UTC(), LengthMs: latest.LengthMs,
				State: latest.State, Take: ptr(v.Take), Rev: ptr(latest.Rev),
			},
			group: revisionGroup(latest.State),
		}

		// The first frame of its latest made revision.
		for _, r := range slices.Backward(v.Revisions) {
			if madeState(r.State) {
				item.Thumbnail = ptr(fmt.Sprintf("/api/v1/videos/%s/revisions/%d/thumbnail.jpg", url.PathEscape(v.ID),
					r.Rev))

				break
			}
		}

		out = append(out, item)
	}

	return out
}

// studioCursor is where a page of the Studio goes on from: the last item's
// creation time in Unix milliseconds, its id and its kind.
type studioCursor struct {
	at   int64
	id   string
	kind string
}

func (c studioCursor) String() string { return strconv.FormatInt(c.at, 10) + "." + c.id + "." + c.kind }

func cursorOf(item openapi.StudioItem) studioCursor {
	return studioCursor{at: item.CreatedAt.UnixMilli(), id: item.Id, kind: string(item.Kind)}
}

func parseStudioCursor(s string) (studioCursor, error) {
	parts := strings.Split(s, ".")

	at, err := strconv.ParseInt(parts[0], 10, 64)
	if len(parts) != 3 || err != nil || parts[1] == "" || parts[2] == "" {
		return studioCursor{}, fmt.Errorf("%w: cursor %q is not one this API gave", orchestration.ErrInvalid, s)
	}

	return studioCursor{at: at, id: parts[1], kind: parts[2]}, nil
}

// compareStudio orders the Studio: the newest first, then by id, then by kind.
func compareStudio(a, b studioCursor) int {
	return cmp.Or(cmp.Compare(b.at, a.at), cmp.Compare(a.id, b.id), cmp.Compare(a.kind, b.kind))
}

// studioFilter is what a page of the Studio is asked for.
type studioFilter struct {
	kind  string
	group string
	q     string
	after *studioCursor
	limit int
}

// studioPage is one page of the items: filtered and searched, put in order,
// then cut after the cursor. next is set when more items follow.
func studioPage(items []studioItem, f studioFilter) ([]openapi.StudioItem, *studioCursor) {
	q := strings.ToLower(strings.TrimSpace(f.q))
	kept := make([]studioItem, 0, len(items))

	for _, it := range items {
		switch {
		case f.kind != "" && string(it.Kind) != f.kind:
		case f.group != "" && it.group != f.group:
		case q != "" && !strings.Contains(strings.ToLower(it.Title), q) && !strings.Contains(strings.ToLower(it.Bot), q):
		case f.after != nil && compareStudio(cursorOf(it.StudioItem), *f.after) <= 0:
		default:
			kept = append(kept, it)
		}
	}

	slices.SortFunc(kept, func(a, b studioItem) int {
		return compareStudio(cursorOf(a.StudioItem), cursorOf(b.StudioItem))
	})

	limit := f.limit
	if limit <= 0 {
		limit = studioLimit
	}

	limit = min(limit, studioMaxLimit)

	var next *studioCursor

	if len(kept) > limit {
		kept = kept[:limit]
		c := cursorOf(kept[limit-1].StudioItem)
		next = &c
	}

	out := make([]openapi.StudioItem, 0, len(kept))
	for _, it := range kept {
		out = append(out, it.StudioItem)
	}

	return out, next
}

// ListStudioItems lists a page of the takes and videos.
func (s *Server) ListStudioItems(ctx context.Context, req openapi.ListStudioItemsRequestObject,
) (openapi.ListStudioItemsResponseObject, error) {
	if s.takes == nil {
		return nil, errNoTakes
	}

	f := studioFilter{q: val(req.Params.Q), limit: val(req.Params.Limit)}
	if req.Params.Kind != nil {
		f.kind = string(*req.Params.Kind)
	}

	if req.Params.State != nil {
		f.group = string(*req.Params.State)
	}

	if req.Params.Cursor != nil {
		c, err := parseStudioCursor(*req.Params.Cursor)
		if err != nil {
			return nil, err
		}

		f.after = &c
	}

	var vids []videos.Video

	if s.videos != nil {
		var err error
		if vids, err = s.videos.List(ctx); err != nil {
			return nil, videoError(err)
		}
	}

	data, next := studioPage(studioItems(s.takes.List(), vids), f)

	out := openapi.ListStudioItems200JSONResponse{Data: data}
	if next != nil {
		out.NextCursor = ptr(next.String())
	}

	return out, nil
}

// ReadTakeThumbnail serves an available take's first frame.
func (s *Server) ReadTakeThumbnail(ctx context.Context, req openapi.ReadTakeThumbnailRequestObject,
) (openapi.ReadTakeThumbnailResponseObject, error) {
	if _, err := s.takeOrNotFound(req.TakeId); err != nil {
		return nil, err
	}

	path, err := s.takes.Thumbnail(ctx, req.TakeId)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// The videos' ffmpeg is not installed yet.
		if errors.Is(err, videos.ErrUnavailable) {
			return nil, fmt.Errorf("%w: %w", orchestration.ErrUnavailable, err)
		}

		return nil, fmt.Errorf("%w: the first frame of take %s: %w", orchestration.ErrNotFound, req.TakeId, err)
	}

	return takeThumbnail{path: path}, nil
}

type takeThumbnail struct{ path string }

func (t takeThumbnail) VisitReadTakeThumbnailResponse(w http.ResponseWriter) error {
	return serveFile(w, t.path, "", "image/jpeg", "")
}
