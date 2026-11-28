package listening_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/listening"
	"github.com/kl3inIT/hypomnema/internal/controlplane/websearch"
)

func TestWebWindowAndDroppedItems(t *testing.T) {
	t.Parallel()

	// Same candidate dates from a provider with an exact filter and one with a query hint.
	for _, tc := range []struct {
		name, basis, bodyPart string
		undatedKept           bool
	}{
		{"firecrawl", "server_bounds", `"tbs":"cdr:1,cd_min:10/28/2026,cd_max:11/27/2026"`, true},
		{"parallel", "query_hint", "after:2026-10-28", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			requests := make(chan string, 1)
			fixtures := `[{"title":"VETC current","url":"https://example.org/current","publish_date":"2026-11-20"},
				{"title":"VETC old","url":"https://example.org/old","publish_date":"2022-11-20"},
				{"title":"VETC undated","url":"https://example.org/undated"}]`
			_, url := startSite(t, func(w http.ResponseWriter, r *http.Request) {
				var body json.RawMessage
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- string(body)
				w.Header().Set("Content-Type", "application/json")
				if tc.name == "parallel" {
					_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"results":%s}}}`, fixtures)
					return
				}
				// Firecrawl's current web response has no publication field.
				_, _ = fmt.Fprint(w, `{"success":true,"data":{"web":[
					{"title":"VETC undated","url":"https://example.org/undated"}]}}`)
			})
			web := websearch.New(websearch.Config{FirecrawlURL: url, ParallelMCPURL: url})
			c := newClock()
			s := listening.New(listening.Config{Now: c.Now, Sleep: noSleep})
			a, err := s.Search(t.Context(), plan([]string{listening.SourceWeb}, "VETC"),
				listening.Caller{Web: func(ctx context.Context, q string, limit int) (websearch.Answer, error) {
					return web.Search(ctx, q, limit, tc.name)
				}})
			require.NoError(t, err)
			select {
			case req := <-requests:
				assert.Contains(t, req, tc.bodyPart)
			default:
				t.Error("the provider did not receive a request")
			}

			cov, err := json.Marshal(a.Coverage[0])
			require.NoError(t, err)
			assert.Contains(t, string(cov), `"window_basis":"`+tc.basis+`"`)
			if tc.undatedKept {
				require.Len(t, a.Items, 1)
				assert.True(t, strings.HasSuffix(a.Items[0].URL, "/undated"))
			} else {
				require.Len(t, a.Items, 1)
				assert.True(t, strings.HasSuffix(a.Items[0].URL, "/current"))
				assert.Contains(t, string(cov), `"missing_date":1`)
				assert.Contains(t, string(cov), `"outside_window":1`)
			}

			out, err := json.Marshal(a)
			require.NoError(t, err)
			assert.Contains(t, string(out), `"warnings":["`)
		})
	}
}
