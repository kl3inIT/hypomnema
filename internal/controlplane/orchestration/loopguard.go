package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// A worker that loops is judged from its transcript, not from the time
// (issue #359): OpenMausBot reads each execution's evidence
// (docs/runtime-contract.md §2, §6), OpenHands' StuckDetector the repeated
// action and error (conversation/stuck_detector.py), Gemini CLI the repeated
// tool call (services/loopDetectionService.ts). All three stop the work and
// say why.

// loopWindow bounds the items read back to find a streak of identical errors.
const loopWindow = 200

// judgeProgress reads the items a running Dispatch's worker just sent: a
// tool that succeeded is progress, which moves the stall timer; the same
// tool with the same input failing with the same text StalledErrors times in
// a row stops the worker, and the Dispatch ends failed with the reason.
func judgeProgress(ctx context.Context, q *store.Queries, now int64, d store.Dispatch, fresh []Item) error {
	if DispatchStatus(d.Status) != DispatchRunning {
		return nil
	}

	succeeded, failed := false, false

	for _, it := range fresh {
		if it.Kind != ItemToolResult {
			continue
		}

		if it.Failed {
			failed = true
		} else {
			succeeded = true
		}
	}

	if succeeded {
		if _, err := q.MarkProgress(ctx, store.MarkProgressParams{Now: now, ID: d.ID, Epoch: d.Epoch}); err != nil {
			return fmt.Errorf("recording progress of %s: %w", d.ID, err)
		}
	}

	if !failed {
		return nil
	}

	rows, err := q.ListDispatchItemsBefore(ctx, store.ListDispatchItemsBeforeParams{
		DispatchID: d.ID, Before: math.MaxInt64, Lim: loopWindow,
	})
	if err != nil {
		return fmt.Errorf("reading the items of %s: %w", d.ID, err)
	}

	recent := make([]Item, 0, len(rows)) // newest first

	for _, r := range rows {
		it := Item{Seq: r.Seq, Kind: ItemKind(r.Kind)}
		if err := json.Unmarshal([]byte(r.Data), &it); err != nil {
			return fmt.Errorf("decoding item %d of %s: %w", r.Seq, d.ID, err)
		}

		recent = append(recent, it)
	}

	reason, loops := identicalErrors(recent)
	if !loops {
		return nil
	}

	n, err := q.SetStalledReason(ctx, store.SetStalledReasonParams{
		Now: now, Reason: nullString(reason), ID: d.ID, Epoch: d.Epoch,
	})
	if err != nil {
		return fmt.Errorf("recording why %s is stopped: %w", d.ID, err)
	}

	if n != 1 {
		return nil
	}

	return requestStop(ctx, q, now, d)
}

// identicalErrors looks at a transcript, newest item first, and says whether
// its last StalledErrors tool results failed for the same call, the same tool
// with the same input and the same text, with no other tool result between
// them; and if so the reason to give.
func identicalErrors(recent []Item) (string, bool) {
	calls := map[string]Item{}

	for _, it := range recent {
		if it.Kind == ItemToolCall {
			calls[it.CallID] = it
		}
	}

	var (
		first Item
		count int
	)

	for _, it := range recent {
		if it.Kind != ItemToolResult {
			continue
		}

		call, ok := calls[it.CallID]
		if !it.Failed || !ok {
			break
		}

		if count == 0 {
			first = it
		} else if !sameFailure(first, it, calls) {
			break
		}

		count++

		if count == StalledErrors {
			return fmt.Sprintf("Stopped after %d identical errors: %s", StalledErrors,
				oneLine(call.Tool+": "+oneLine(first.Text))), true
		}
	}

	return "", false
}

// sameFailure reports whether two failed results answer the same tool with
// the same input and say the same thing.
func sameFailure(a, b Item, calls map[string]Item) bool {
	ca, cb := calls[a.CallID], calls[b.CallID]

	return a.Text == b.Text && ca.Tool == cb.Tool && sameJSON(ca.Input, cb.Input)
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}

	return reflect.DeepEqual(x, y)
}
