package clientapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

// studioTakes stands for the takes: a list a test changes between pages.
type studioTakes struct {
	mu   sync.Mutex
	list []devicetool.Take
}

func (s *studioTakes) List() []devicetool.Take {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]devicetool.Take(nil), s.list...)
}

func (s *studioTakes) add(t devicetool.Take) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.list = append(s.list, t)
}

func (s *studioTakes) Get(id string) (devicetool.Take, error) {
	for _, t := range s.List() {
		if t.ID == id {
			return t, nil
		}
	}

	return devicetool.Take{}, fmt.Errorf("no take %s", id)
}

func (*studioTakes) File(string, string) (string, error)  { return "", fmt.Errorf("no file") }
func (*studioTakes) Delete(context.Context, string) error { return nil }
func (*studioTakes) Thumbnail(context.Context, string) (string, error) {
	return "", videos.ErrUnavailable
}

// studioVideos stands for the videos: only List is read by the Studio.
type studioVideos struct {
	Videos
	list []videos.Video
}

func (v *studioVideos) List(context.Context) ([]videos.Video, error) { return v.list, nil }

var studioStart = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

func studioTake(n int, state, title, agent string) devicetool.Take {
	return devicetool.Take{
		ID: fmt.Sprintf("tk_%016x", n), Title: title, Agent: "d-" + agent, AgentName: agent,
		StartedAt: studioStart.Add(time.Duration(n) * time.Minute), State: state, DurationMs: int64(n) * 1000,
	}
}

func studioWorld(t *testing.T) (*world, *studioTakes, *studioVideos, *openapi.ClientWithResponses) {
	t.Helper()

	w := newWorld(t)
	tk := &studioTakes{}
	vs := &studioVideos{}
	w.srv.WithTakes(tk).WithVideos(vs)
	owner, _ := w.token("owner", bearertoken.Owner)

	return w, tk, vs, owner
}

func studioList(t *testing.T, c *openapi.ClientWithResponses, p openapi.ListStudioItemsParams,
) openapi.StudioItemList {
	t.Helper()

	resp, err := c.ListStudioItemsWithResponse(t.Context(), &p)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))

	return *resp.JSON200
}

func ids(items []openapi.StudioItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Id)
	}

	return out
}

// Scenario: the owner pages through the Studio two items at a time while
// agents film: a take made between two pages is not read again, nor does it
// push an item read before onto the next page; every item is read once, in
// the order (created_at desc, id, kind), and two items made in the same
// millisecond keep their order by id.
func TestTheStudioPagesStablyWhileItemsAreAdded(t *testing.T) {
	t.Parallel()

	_, tk, _, owner := studioWorld(t)

	for n := 1; n <= 5; n++ {
		tk.add(studioTake(n, devicetool.TakeAvailable, fmt.Sprintf("take %d", n), "bot"))
	}

	// Two takes in one millisecond: the id orders them.
	same := studioTake(3, devicetool.TakeAvailable, "same time", "bot")
	same.ID = "tk_00000000000000a3"
	tk.add(same)

	first := studioList(t, owner, openapi.ListStudioItemsParams{Limit: ptr(2)})
	assert.Equal(t, []string{"tk_0000000000000005", "tk_0000000000000004"}, ids(first.Data))
	require.NotNil(t, first.NextCursor)

	// A newer take arrives between the pages.
	tk.add(studioTake(9, devicetool.TakeRecording, "filmed now", "bot"))

	second := studioList(t, owner, openapi.ListStudioItemsParams{Limit: ptr(2), Cursor: first.NextCursor})
	assert.Equal(t, []string{"tk_0000000000000003", "tk_00000000000000a3"}, ids(second.Data))
	require.NotNil(t, second.NextCursor)

	third := studioList(t, owner, openapi.ListStudioItemsParams{Limit: ptr(2), Cursor: second.NextCursor})
	assert.Equal(t, []string{"tk_0000000000000002", "tk_0000000000000001"}, ids(third.Data))
	assert.Nil(t, third.NextCursor, "the last page says no more follow")

	// A fresh first page has the new take on top.
	again := studioList(t, owner, openapi.ListStudioItemsParams{Limit: ptr(2)})
	assert.Equal(t, "tk_0000000000000009", again.Data[0].Id)

	resp, err := owner.ListStudioItemsWithResponse(t.Context(),
		&openapi.ListStudioItemsParams{Cursor: ptr("not-a-cursor")})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode(), "a cursor this API did not give")
}

// Scenario: the Studio lists takes and videos together. A video's state,
// length and thumbnail are its latest revision's (its first frame from the
// latest made one), its Bot the agent that filmed its take. The kind, the
// state group and the search (title or Bot, in any case) are applied before
// the page is cut: a page of one still finds the one match among many.
func TestTheStudioFiltersAndSearchesBeforePaging(t *testing.T) {
	t.Parallel()

	_, tk, vs, owner := studioWorld(t)

	tk.add(studioTake(1, devicetool.TakeAvailable, "Đăng nhập", "Atlas"))
	tk.add(studioTake(2, devicetool.TakePaused, "Thanh toán", "Atlas"))
	tk.add(studioTake(3, devicetool.TakeFailed, "Checkout", "Nova"))

	for n := 4; n <= 8; n++ {
		tk.add(studioTake(n, devicetool.TakeUploading, fmt.Sprintf("other %d", n), "Nova"))
	}

	rev := func(n int, state string, length int64) videos.Revision {
		return videos.Revision{Rev: n, State: state, LengthMs: length}
	}
	vs.list = []videos.Video{
		{
			ID: "vid_review", Take: "tk_0000000000000001", Title: "Đăng nhập", MadeByName: "owner",
			CreatedAt: studioStart.Add(20 * time.Minute),
			Revisions: []videos.Revision{rev(1, videos.ChangesRequested, 9000), rev(2, videos.Review, 8000)},
		},
		{
			ID: "vid_making", Take: "tk_0000000000000001", Title: "Bản khác", MadeByName: "owner",
			CreatedAt: studioStart.Add(21 * time.Minute),
			Revisions: []videos.Revision{rev(1, videos.Approved, 7000), rev(2, videos.Composing, 0)},
		},
		{
			ID: "vid_new", Take: "tk_00000000000000ee", Title: "Gone take", MadeByName: "worker 7",
			CreatedAt: studioStart.Add(22 * time.Minute), Revisions: []videos.Revision{rev(1, videos.Queued, 0)},
		},
	}

	all := studioList(t, owner, openapi.ListStudioItemsParams{})
	require.Len(t, all.Data, 11)
	assert.Equal(t, []string{"vid_new", "vid_making", "vid_review"}, ids(all.Data[:3]))

	making := all.Data[1]
	assert.Equal(t, openapi.StudioItemKindVideo, making.Kind)
	assert.Equal(t, "composing", making.State)
	assert.Equal(t, "Atlas", making.Bot, "the agent that filmed its take")
	assert.Equal(t, ptr(2), making.Rev)
	assert.Equal(t, ptr("tk_0000000000000001"), making.Take)
	assert.Equal(t, ptr("/api/v1/videos/vid_making/revisions/1/thumbnail.jpg"), making.Thumbnail,
		"the latest made revision's first frame")
	assert.Equal(t, "worker 7", all.Data[0].Bot, "who asked, when its take is gone")
	assert.Nil(t, all.Data[0].Thumbnail, "nothing made yet")
	assert.Equal(t, int64(8000), all.Data[2].LengthMs)

	byID := map[string]openapi.StudioItem{}
	for _, it := range all.Data {
		byID[it.Id] = it
	}

	assert.Equal(t, ptr("/api/v1/takes/tk_0000000000000001/thumbnail.jpg"), byID["tk_0000000000000001"].Thumbnail)
	assert.Nil(t, byID["tk_0000000000000002"].Thumbnail, "a take being filmed has a placeholder")

	cases := []struct {
		name string
		p    openapi.ListStudioItemsParams
		want []string
	}{
		{"videos", openapi.ListStudioItemsParams{Kind: ptr(openapi.ListStudioItemsParamsKindVideo)},
			[]string{"vid_new", "vid_making", "vid_review"}},
		{"in progress", openapi.ListStudioItemsParams{State: ptr(openapi.ListStudioItemsParamsStateInProgress), Limit: ptr(3)},
			[]string{"vid_new", "vid_making", "tk_0000000000000008"}},
		{"awaiting review", openapi.ListStudioItemsParams{State: ptr(openapi.ListStudioItemsParamsStateReview)}, []string{"vid_review"}},
		{"approved", openapi.ListStudioItemsParams{State: ptr(openapi.ListStudioItemsParamsStateApproved)}, nil},
		{"failed", openapi.ListStudioItemsParams{State: ptr(openapi.ListStudioItemsParamsStateFailed)}, []string{"tk_0000000000000003"}},
		{"takes in progress", openapi.ListStudioItemsParams{Kind: ptr(openapi.ListStudioItemsParamsKindTake), State: ptr(openapi.ListStudioItemsParamsStateInProgress),
			Limit: ptr(1)}, []string{"tk_0000000000000008"}},
		{"title, any case", openapi.ListStudioItemsParams{Q: ptr("THANH"), Limit: ptr(1)},
			[]string{"tk_0000000000000002"}},
		{"bot", openapi.ListStudioItemsParams{Q: ptr("atlas"), Kind: ptr(openapi.ListStudioItemsParamsKindTake)},
			[]string{"tk_0000000000000002", "tk_0000000000000001"}},
		{"diacritics", openapi.ListStudioItemsParams{Q: ptr("đăng")}, []string{"vid_review", "tk_0000000000000001"}},
	}

	for _, c := range cases {
		got := studioList(t, owner, c.p)
		assert.Equal(t, c.want, nilIfEmpty(ids(got.Data)), c.name)
	}

	// The match past the first page of a search is still found: the search is made before the page is cut.
	one := studioList(t, owner, openapi.ListStudioItemsParams{Q: ptr("checkout"), Limit: ptr(1)})
	assert.Equal(t, []string{"tk_0000000000000003"}, ids(one.Data))
	assert.Nil(t, one.NextCursor)
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}

	return s
}
