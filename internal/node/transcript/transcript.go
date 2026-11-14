// Package transcript sends the transcript of every worker on a node to the
// control plane (issues #43 and #157), as Omnigent's forwarder does
// (omnigent/harnesses/claude_native/forwarder.py at cf1021e): the shim's journal
// is the buffer, and a cursor kept beside it says how far the control plane has
// it. A batch is the items of the next whole lines; the cursor moves when the
// batch is acknowledged, so a batch lost with the connection is read and sent
// again with the same numbers. Sending is apart from the worker's reports: a
// control plane that is slow or away delays the transcript only.
//
// What a journal holds is the worker's own output, and each runtime's differs:
// the runner writes the name of the format beside the journal (SetFormat), and
// a Decoder of that format turns its lines into the items every runtime shares
// (nodeproto.Item). The forwarder follows every journal under run/ by looking
// again at each tick, so a runner does not tell it about a Dispatch.
package transcript

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
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/shim"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

const (
	// sendEvery is how often the journals are read for new lines (Coder
	// flushes its agent's logs each second, codersdk/agentsdk/logs.go).
	sendEvery = time.Second
	// resendAfter: a batch not acknowledged by then is sent again (inferred).
	resendAfter = 15 * time.Second
	// scanBytes bounds the journal read in one step.
	scanBytes = 4 << 20
	// lineBytes bounds one journal line held in memory; a longer one is passed
	// over with a notice, since its items would be cut to far less anyway.
	lineBytes = 4 << 20
	// sentFile holds the cursor: the journal offset and the last seq acknowledged.
	sentFile = "sent"
	// sentDone marks a transcript the control plane has whole, or refused.
	sentDone = "sent.done"
	// formatFile names the journal's format.
	formatFile = "format"
)

// Decoder turns the lines of one journal into items. The forwarder makes one
// for each read of a journal, starting at a line the cursor stands before, so
// it keeps nothing between reads.
type Decoder interface {
	// Line returns the items of the next whole line of the journal, without
	// their seq.
	Line(raw []byte) []nodeproto.Item
	// OpenSince is the number of the oldest line, counted from 0 among the
	// lines given to Line, whose items are not all returned yet because
	// something is still open (a message whose chunks go on, a call whose input
	// is not known); -1 when nothing is. The cursor does not pass that line
	// until a later line closes it or the worker has ended.
	OpenSince() int
	// Flush closes what is open and returns its items. It is called when the
	// worker has exited and the journal is read to its end.
	Flush() []nodeproto.Item
}

// SetFormat names the format of the journal under dir, a Dispatch's run
// directory. A runner does it as it prepares the directory.
func SetFormat(dir, format string) error {
	if err := os.WriteFile(filepath.Join(dir, formatFile), []byte(format), 0o600); err != nil {
		return fmt.Errorf("naming the format of the journal in %s: %w", dir, err)
	}

	return nil
}

// formatOf is the format the journal under dir names; "" when it names none.
func formatOf(dir string) string {
	raw, _ := os.ReadFile(filepath.Join(dir, formatFile)) //nolint:gosec // the runner's own directory

	return strings.TrimSpace(string(raw))
}

// Forwarder sends the transcripts of a node's workers.
type Forwarder struct {
	// Home is the node's home: run/<dispatch> holds each worker's journal.
	Home string
	// Send sends a batch to the control plane; it reports false when the batch
	// could not be sent now. Nil sends nothing.
	Send func(nodeproto.Items) bool
	// Decoders make the Decoder of each format.
	Decoders map[string]func() Decoder
	Logger   *slog.Logger

	mu          sync.Mutex
	transcripts map[string]*transcript
	// finished holds the directories known to be sent, so that they are not looked at again.
	finished map[string]bool
}

// transcript is one Dispatch's journal on its way to the control plane.
type transcript struct {
	id, dir string
	// offset and seq are the cursor: the journal up to offset is acknowledged, its last item was seq.
	offset, seq int64
	flight      *flight
	// unknown says the journal's format has no decoder, which was told once.
	unknown bool
}

// flight is the batch sent and not yet acknowledged.
type flight struct {
	// last is the batch's last seq, offset the journal offset after its lines.
	last, offset int64
	at           time.Time
}

func (f *Forwarder) dir(id string) string { return filepath.Join(f.Home, "run", id) }

// Forward sends the transcripts of this node's workers until ctx ends: those of
// running workers, and those an earlier daemon left unsent.
func (f *Forwarder) Forward(ctx context.Context) {
	tick := time.NewTicker(sendEvery)
	defer tick.Stop()

	for {
		f.Step(ctx)

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Step is one pass over every journal.
func (f *Forwarder) Step(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.scan()

	for _, s := range f.transcripts {
		f.step(ctx, s)
	}
}

// scan follows every journal under run/ that is not marked sent, and forgets
// the ones whose directory is gone (a Dispatch the node released).
func (f *Forwarder) scan() {
	if f.transcripts == nil {
		f.transcripts = map[string]*transcript{}
		f.finished = map[string]bool{}
	}

	entries, _ := os.ReadDir(filepath.Join(f.Home, "run"))
	present := map[string]bool{}

	for _, e := range entries {
		id := e.Name()
		present[id] = true

		if f.finished[id] || f.transcripts[id] != nil {
			continue
		}

		dir := f.dir(id)
		if _, err := os.Stat(filepath.Join(dir, sentDone)); err == nil {
			f.finished[id] = true

			continue
		}

		s := &transcript{id: id, dir: dir}
		if raw, err := os.ReadFile(filepath.Join(dir, sentFile)); err == nil { //nolint:gosec // the runner's own directory
			_, _ = fmt.Sscanf(string(raw), "%d %d", &s.offset, &s.seq)
		}

		f.transcripts[id] = s
	}

	for id := range f.transcripts {
		if !present[id] {
			delete(f.transcripts, id)
		}
	}

	for id := range f.finished {
		if !present[id] {
			delete(f.finished, id)
		}
	}
}

// step sends the transcript's next batch unless one is on its way. f.mu is held.
func (f *Forwarder) step(ctx context.Context, s *transcript) {
	if f.Send == nil || s.unknown || (s.flight != nil && time.Since(s.flight.at) < resendAfter) {
		return
	}

	format := formatOf(s.dir)

	// The runner names the format before the worker starts: a directory with
	// neither is one being prepared, read on a later tick.
	if format == "" {
		if _, err := os.Stat(filepath.Join(s.dir, shim.JournalFile)); err != nil {
			return
		}
	}

	newDecoder, ok := f.Decoders[format]
	if !ok {
		s.unknown = true

		f.Logger.WarnContext(ctx, "a worker's journal has a format this node cannot read",
			slog.String("event", "node.transcript_format_unknown"), slog.String("dispatch_id", s.id),
			slog.String("format", format))

		return
	}

	// Read the exit first: lines written before it are then all in the journal.
	_, exited, _ := shim.ReadExit(s.dir)

	items, next, err := readBatch(s.dir, s.offset, s.seq, newDecoder(), exited)
	if err != nil {
		f.Logger.WarnContext(ctx, "reading a worker's transcript",
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
			f.finish(s)
		}

		return
	}

	if f.Send(nodeproto.Items{DispatchID: s.id, Items: items}) {
		s.flight = &flight{last: items[len(items)-1].Seq, offset: next, at: time.Now()}
	}
}

// ItemsAcked moves a transcript's cursor past an acknowledged batch and sends the
// next one. A refused transcript is not sent again.
func (f *Forwarder) ItemsAcked(ack nodeproto.ItemsAck) {
	f.mu.Lock()
	defer f.mu.Unlock()

	s := f.transcripts[ack.DispatchID]
	if s == nil {
		return
	}

	ctx := context.Background()

	switch ack.Status {
	case nodeproto.AckRejected:
		f.Logger.WarnContext(ctx, "the control plane refused a worker's transcript",
			slog.String("event", "node.transcript_refused"), slog.String("dispatch_id", s.id),
			slog.String("error", ack.Error))
		f.finish(s)
	case nodeproto.AckCommitted:
		if s.flight == nil || ack.Seq < s.flight.last {
			return
		}

		s.offset, s.seq, s.flight = s.flight.offset, s.flight.last, nil
		s.save()
		f.step(ctx, s)
	}
}

// finish marks the transcript as sent and forgets it. f.mu is held.
func (f *Forwarder) finish(s *transcript) {
	_ = os.WriteFile(filepath.Join(s.dir, sentDone), nil, 0o600)

	delete(f.transcripts, s.id)
	f.finished[s.id] = true
}

func (s *transcript) save() {
	tmp := filepath.Join(s.dir, sentFile+".tmp")
	if os.WriteFile(tmp, fmt.Appendf(nil, "%d %d", s.offset, s.seq), 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(s.dir, sentFile))
	}
}

// readBatch reads the journal's whole lines after offset into one batch,
// numbering the items after seq. It returns the offset after the lines it took:
// not past a line the decoder still holds open, unless the worker exited, when
// what is open is closed.
func readBatch(
	dir string, offset, seq int64, dec Decoder, exited bool,
) ([]nodeproto.Item, int64, error) {
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
		// lineStarts[i] is where the i-th line the decoder was given begins, counted from offset.
		lineStarts []int
		reachedEnd bool
	)

	for len(batch) < nodeproto.MaxBatchItems && read < scanBytes {
		raw, n, err := readLine(br)
		if errors.Is(err, io.EOF) {
			// A partial line waits for the rest.
			reachedEnd = true

			break
		}

		if err != nil {
			return nil, offset, err
		}

		openBefore := dec.OpenSince()

		var items []nodeproto.Item

		if raw == nil {
			// A line too long to hold ends what was open before it, and says it was left out.
			items = append(dec.Flush(), nodeproto.Item{
				Kind: nodeproto.ItemNotice, Text: fmt.Sprintf("[a line of %d bytes of the worker's output was not kept]", n),
			})
		} else {
			lineStarts = append(lineStarts, read)
			items = dec.Line(raw)
		}

		bytes := 0

		for _, it := range items {
			enc, err := json.Marshal(it)
			if err != nil {
				return nil, offset, fmt.Errorf("encoding an item: %w", err)
			}

			bytes += len(enc) + 32
		}

		// A line's items go together; a line that does not fit starts the next batch, and is
		// read again with what was open before it.
		if len(batch) > 0 &&
			(len(batch)+len(items) > nodeproto.MaxBatchItems || size+bytes > nodeproto.MaxBatchBytes) {
			stop := read
			if openBefore >= 0 && openBefore < len(lineStarts) {
				stop = lineStarts[openBefore]
			}

			return batch, offset + int64(stop), nil
		}

		for _, it := range items {
			seq++
			it.Seq = seq
			batch = append(batch, it)
		}

		size += bytes
		read += n
	}

	if exited && reachedEnd {
		for _, it := range dec.Flush() {
			seq++
			it.Seq = seq
			batch = append(batch, it)
		}

		return batch, offset + int64(read), nil
	}

	if open := dec.OpenSince(); open >= 0 && open < len(lineStarts) {
		return batch, offset + int64(lineStarts[open]), nil
	}

	return batch, offset + int64(read), nil
}

// readLine reads the next whole line and its length. A line longer than
// lineBytes is read past without being held: it comes back nil with its
// length. io.EOF means the last line is not whole yet.
func readLine(br *bufio.Reader) ([]byte, int, error) {
	var (
		line []byte
		n    int
	)

	for {
		part, err := br.ReadSlice('\n')
		n += len(part)

		if n <= lineBytes {
			line = append(line, part...)
		} else {
			line = nil
		}

		switch {
		case err == nil:
			return line, n, nil
		case errors.Is(err, bufio.ErrBufferFull):
		default:
			return nil, n, err
		}
	}
}
