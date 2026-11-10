package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Sending a worker's transcript to the control plane (issue #43), as Omnigent's
// forwarder (omnigent/harnesses/claude_native/forwarder.py at cf1021e): the
// journal is the buffer, and a cursor kept beside it says how far the control
// plane has it. A batch is the items of the next whole lines; the cursor moves
// when the batch is acknowledged, so a batch lost with the connection is read
// and sent again with the same numbers. Sending is apart from the worker's
// reports: a control plane that is slow or away delays the transcript only.

const (
	// sendEvery is how often the journals are read for new lines (Coder
	// flushes its agent's logs each second, codersdk/agentsdk/logs.go).
	sendEvery = time.Second
	// resendAfter: a batch not acknowledged by then is sent again (inferred).
	resendAfter = 15 * time.Second
	// scanBytes bounds the journal read in one step.
	scanBytes = 4 << 20
	// sentFile holds the cursor: the journal offset and the last seq acknowledged.
	sentFile = "sent"
	// sentDone marks a transcript the control plane has whole, or refused.
	sentDone = "sent.done"
)

// transcript is one Dispatch's journal on its way to the control plane.
type transcript struct {
	id, dir string
	// offset and seq are the cursor: the journal up to offset is acknowledged, its last item was seq.
	offset, seq int64
	flight      *flight
}

// flight is the batch sent and not yet acknowledged.
type flight struct {
	// last is the batch's last seq, offset the journal offset after its lines.
	last, offset int64
	at           time.Time
}

// Forward sends the transcripts of this node's workers until ctx ends: those of
// running workers, and those an earlier daemon left unsent.
func (rn *Runner) Forward(ctx context.Context) {
	rn.discover()

	tick := time.NewTicker(sendEvery)
	defer tick.Stop()

	for {
		rn.smu.Lock()
		for _, s := range rn.transcripts {
			rn.step(ctx, s)
		}
		rn.smu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// discover follows every journal under run/ that is not marked sent.
func (rn *Runner) discover() {
	entries, err := os.ReadDir(filepath.Join(rn.Home, "run"))
	if err != nil {
		return
	}

	for _, e := range entries {
		dir := rn.dir(e.Name())
		if _, err := os.Stat(filepath.Join(dir, shim.JournalFile)); err != nil {
			continue
		}

		rn.follow(e.Name(), dir)
	}
}

// follow starts sending a Dispatch's transcript from its cursor.
func (rn *Runner) follow(id, dir string) {
	if _, err := os.Stat(filepath.Join(dir, sentDone)); err == nil {
		return
	}

	rn.smu.Lock()
	defer rn.smu.Unlock()

	if rn.transcripts == nil {
		rn.transcripts = map[string]*transcript{}
	}

	if _, ok := rn.transcripts[id]; ok {
		return
	}

	s := &transcript{id: id, dir: dir}
	if raw, err := os.ReadFile(filepath.Join(dir, sentFile)); err == nil { //nolint:gosec // the runner's own directory
		_, _ = fmt.Sscanf(string(raw), "%d %d", &s.offset, &s.seq)
	}

	rn.transcripts[id] = s
}

// unfollow forgets a transcript whose directory is going away.
func (rn *Runner) unfollow(id string) {
	rn.smu.Lock()
	defer rn.smu.Unlock()

	delete(rn.transcripts, id)
}

// step sends the transcript's next batch unless one is on its way. rn.smu is held.
func (rn *Runner) step(ctx context.Context, s *transcript) {
	if rn.Send == nil || (s.flight != nil && time.Since(s.flight.at) < resendAfter) {
		return
	}

	// Read the exit first: lines written before it are then all in the journal.
	_, exited, _ := shim.ReadExit(s.dir)

	items, next, err := readBatch(s.dir, s.offset, s.seq)
	if err != nil {
		rn.Logger.WarnContext(ctx, "reading a worker's transcript",
			slog.String("event", "node.transcript_read_failed"), slog.String("dispatch_id", s.id),
			slog.String("error", err.Error()))

		return
	}

	if len(items) == 0 {
		switch {
		case next > s.offset:
			// Lines that hold no item.
			s.offset = next
			s.save()
		case exited:
			rn.finish(s)
		}

		return
	}

	if rn.Send(nodeproto.Items{DispatchID: s.id, Items: items}) {
		s.flight = &flight{last: items[len(items)-1].Seq, offset: next, at: time.Now()}
	}
}

// ItemsAcked moves a transcript's cursor past an acknowledged batch and sends the
// next one. A refused transcript is not sent again.
func (rn *Runner) ItemsAcked(ack nodeproto.ItemsAck) {
	rn.smu.Lock()
	defer rn.smu.Unlock()

	s := rn.transcripts[ack.DispatchID]
	if s == nil {
		return
	}

	ctx := context.Background()

	switch ack.Status {
	case nodeproto.AckRejected:
		rn.Logger.WarnContext(ctx, "the control plane refused a worker's transcript",
			slog.String("event", "node.transcript_refused"), slog.String("dispatch_id", s.id),
			slog.String("error", ack.Error))
		rn.finish(s)
	case nodeproto.AckCommitted:
		if s.flight == nil || ack.Seq < s.flight.last {
			return
		}

		s.offset, s.seq, s.flight = s.flight.offset, s.flight.last, nil
		s.save()
		rn.step(ctx, s)
	}
}

// finish marks the transcript as sent and forgets it. rn.smu is held.
func (rn *Runner) finish(s *transcript) {
	_ = os.WriteFile(filepath.Join(s.dir, sentDone), nil, 0o600)

	delete(rn.transcripts, s.id)
}

func (s *transcript) save() {
	tmp := filepath.Join(s.dir, sentFile+".tmp")
	if os.WriteFile(tmp, fmt.Appendf(nil, "%d %d", s.offset, s.seq), 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(s.dir, sentFile))
	}
}

// readBatch reads the journal's whole lines after offset into one batch,
// numbering the items after seq. It returns the offset after the lines it took.
func readBatch(dir string, offset, seq int64) ([]nodeproto.Item, int64, error) {
	f, err := os.Open(filepath.Join(dir, shim.JournalFile)) //nolint:gosec // the runner's own directory
	if errors.Is(err, os.ErrNotExist) {
		return nil, offset, nil
	}

	if err != nil {
		return nil, offset, err
	}
	defer f.Close() //nolint:errcheck // read only

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}

	br := bufio.NewReaderSize(f, 64<<10)

	var (
		batch      []nodeproto.Item
		size, read int
	)

	for len(batch) < nodeproto.MaxBatchItems && read < scanBytes {
		raw, err := br.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			// A partial line waits for the rest.
			break
		}

		if err != nil {
			return nil, offset, err
		}

		items := itemsOf(raw)
		bytes := 0

		for _, it := range items {
			enc, err := json.Marshal(it)
			if err != nil {
				return nil, offset, fmt.Errorf("encoding an item: %w", err)
			}

			bytes += len(enc) + 32
		}

		// A line's items go together; a line that does not fit starts the next batch.
		if len(batch) > 0 &&
			(len(batch)+len(items) > nodeproto.MaxBatchItems || size+bytes > nodeproto.MaxBatchBytes) {
			break
		}

		for _, it := range items {
			seq++
			it.Seq = seq
			batch = append(batch, it)
		}

		size += bytes
		read += len(raw)
	}

	return batch, offset + int64(read), nil
}
