package clientapi

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// ListDispatchItems returns one page of a worker's transcript (issue #43). With
// wait, an empty page after the cursor waits up to that many seconds for the
// next item, as ListChatMessages does: the web follows a running worker
// without a second stream.
func (s *Server) ListDispatchItems(ctx context.Context, req openapi.ListDispatchItemsRequestObject,
) (openapi.ListDispatchItemsResponseObject, error) {
	p := req.Params
	before := val(p.Before)

	var (
		wake     <-chan struct{}
		deadline <-chan time.Time
	)

	if wait := val(p.Wait); wait > 0 && before == 0 {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		// Subscribe before the first read, so an item committed in between still wakes the wait.
		wake = s.svc.Subscribe(ctx)
		timer := time.NewTimer(time.Duration(wait) * time.Second)

		defer timer.Stop()

		deadline = timer.C
	}

	for {
		page, err := s.svc.ListItems(ctx, req.DispatchId, val(p.After), before, val(p.Limit))
		if err != nil {
			return nil, err
		}

		if len(page.Items) > 0 || wake == nil {
			return itemList(page)
		}

		select {
		case <-wake:
		case <-deadline:
			return itemList(page)
		case <-s.stop:
			return itemList(page)
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for items: %w", ctx.Err())
		}
	}
}

func itemList(page orchestration.ItemPage) (openapi.ListDispatchItems200JSONResponse, error) {
	out := openapi.ListDispatchItems200JSONResponse{
		Data: make([]openapi.TranscriptItem, 0, len(page.Items)), HasMore: page.HasMore,
	}

	for _, it := range page.Items {
		item := openapi.TranscriptItem{
			Seq: it.Seq, Kind: openapi.TranscriptItemKind(it.Kind), Text: ptr(it.Text), CallId: ptr(it.CallID),
			Tool: ptr(it.Tool), Failed: ptr(it.Failed), CreatedAt: it.CreatedAt,
		}

		if len(it.Input) > 0 {
			var input map[string]any
			if err := json.Unmarshal(it.Input, &input); err != nil {
				return openapi.ListDispatchItems200JSONResponse{}, fmt.Errorf("decoding the input of item %d: %w", it.Seq, err)
			}

			item.Input = &input
		}

		out.Data = append(out.Data, item)
	}

	return out, nil
}
