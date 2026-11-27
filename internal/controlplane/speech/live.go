package speech

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Soniox's realtime transcription for a meeting recorded live (issue #820), as MemoryOS streams one track
// (memoryos-core/src/main/java/io/memoryos/voice/SonioxLiveTranscription.java, after anarlog's listener):
//
//   - one WebSocket to wss://stt-rt.soniox.com/transcribe-websocket per track, its key sent with the connection as a
//     bearer token (Soniox's websocket-api#authentication; the start request's api_key is deprecated), then the start
//     request: PCM16 24 kHz mono, endpoint detection, diarization when asked, the language hint and the Dictionary's
//     words as context.terms;
//   - the audio as binary frames, a {"type":"keepalive"} after five seconds without audio, and the end as an empty
//     text frame (Soniox's websocket-api#ending-the-stream; MemoryOS's empty binary frame is an empty chunk now);
//   - final tokens grouped into a segment that ends at a speaker change or an <end> endpoint; the non-final ones and
//     the segment being built are the preview;
//   - when the provider's stream fails, a new one after 2/5/10/20/30 seconds, the last five seconds of audio sent
//     replayed, then the audio not yet sent (kept meanwhile up to LiveMaxBehind), and the new stream's times shifted
//     by the audio before them, so the meeting's clock goes on;
//   - no line twice: a final token of a replay that ends at or before the end of the last segment given is dropped
//     (the issue's review, finding 7, which MemoryOS lacks).
//
// Audio and text are never logged.

// SonioxRealtimeModel is the realtime model the meetings stream to: MemoryOS's (SonioxVoiceAdapter, stt-rt-v5), the
// realtime sibling of SonioxSTTModel.
const SonioxRealtimeModel = "stt-rt-v5"

// The realtime stream's format and timings (SonioxLiveTranscription's).
const (
	// LiveSampleRate is the PCM16 mono sample rate the stream takes; LiveBytesPerSecond its bytes a second.
	LiveSampleRate     = 24_000
	LiveBytesPerSecond = LiveSampleRate * 2

	liveReplay         = 5 * time.Second
	liveConnectTimeout = 15 * time.Second
	liveKeepaliveAfter = 5 * time.Second
	liveFinishTimeout  = 15 * time.Second
	liveWriteTimeout   = 10 * time.Second
	// LiveMaxBehind bounds the audio waiting to be sent to the provider: a stream that falls further behind ends
	// (issue #820, review finding 9).
	LiveMaxBehind = 30 * time.Second
	// liveMaxMessage bounds one message of the provider.
	liveMaxMessage = 4 << 20
)

// liveBackoff is the wait before each new stream after a failure (MemoryOS's BACKOFF).
var liveBackoff = []time.Duration{
	2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second,
}

// ErrBehind says the provider took the audio slower than it came, past LiveMaxBehind.
var ErrBehind = errors.New("the speech provider fell behind the audio")

// LiveRequest is how a track is to be heard.
type LiveRequest struct {
	// Language is an ISO-639-1 hint; empty: the provider finds it.
	Language string
	// Speakers asks who spoke when.
	Speakers bool
	Terms    []string
	// Offset is the meeting's time of the first sample sent.
	Offset time.Duration
	// After is where the last line kept of this track ends, or negative for none: a final token that ends at or
	// before it is not given again.
	After time.Duration
}

// LiveListener is told what the stream hears. Its calls come one at a time, in order.
type LiveListener struct {
	// Preview is the speech not yet final, which replaces the one before; empty when there is none.
	Preview func(speaker, text string)
	// Segment is a stretch one voice said, final.
	Segment func(Segment)
	// Failed: the provider failed after every new stream was tried, or fell behind.
	Failed func(err error)
}

// Live is one track's realtime stream.
type Live struct {
	svc      *Service
	req      LiveRequest
	listener LiveListener
	backoff  []time.Duration

	provider, model string

	mu         sync.Mutex
	conn       *websocket.Conn
	generation int
	attempt    int
	// queue is the audio given and not yet sent to any stream, kept across a new stream up to LiveMaxBehind;
	// queued its bytes.
	queue  [][]byte
	queued int
	// replay is the retained audio a new stream is sent again before the queue; replayBytes its bytes.
	replay      [][]byte
	replayBytes int
	// retained is the last liveReplay of audio sent, replayed into a new stream.
	retained      [][]byte
	retainedBytes int
	sent          int64
	base          time.Duration
	lastSend      time.Time
	current       *segmentBuilder
	lastEnd       time.Duration
	finishing     bool
	ended         bool
	// reconnecting: a new stream is awaited after a failure; retry is the wait before it is asked for, if any.
	reconnecting bool
	retry        *time.Timer
	closed       bool
	finished     chan struct{}
	finishedOnce sync.Once
	wake         chan struct{}
	stop         chan struct{}
	stopOnce     sync.Once

	events      []func()
	eventWake   chan struct{}
	eventsDone  chan struct{}
	eventsEnded bool
}

type segmentBuilder struct {
	speaker    string
	start, end time.Duration
	text       strings.Builder
}

// Live opens a realtime stream for one track with the first Soniox key that answers.
func (s *Service) Live(ctx context.Context, req LiveRequest, listener LiveListener) (*Live, error) {
	l := &Live{
		svc: s, req: req, listener: listener, backoff: s.liveBackoff(), base: req.Offset, lastEnd: req.After,
		provider: ProviderSoniox, model: SonioxRealtimeModel,
		finished: make(chan struct{}), wake: make(chan struct{}, 1), stop: make(chan struct{}),
		eventWake: make(chan struct{}, 1), eventsDone: make(chan struct{}), lastSend: s.now(),
	}

	conn, err := l.dial(ctx)
	if err != nil {
		return nil, err
	}

	l.conn = conn

	// The stream lives until it is finished or closed, past the request that opened it.
	streaming := context.WithoutCancel(ctx)

	go l.dispatch()
	go l.write(streaming)
	go l.read(streaming, conn, 0)

	return l, nil
}

func (s *Service) liveBackoff() []time.Duration {
	if s.cfg.LiveBackoff != nil {
		return s.cfg.LiveBackoff
	}

	return liveBackoff
}

// Provider and Model say who hears the stream.
func (l *Live) Provider() string { return l.provider }

func (l *Live) Model() string { return l.model }

// Sent is how much audio the stream was given.
func (l *Live) Sent() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	return bytesToDuration(l.sent)
}

// dial asks the Soniox keys in turn for a stream and sends it the start request.
func (l *Live) dial(ctx context.Context) (*websocket.Conn, error) {
	var conn *websocket.Conn

	err := l.svc.each(ctx, KindSTT, ProviderSoniox, "", false, func(ctx context.Context, r route) error {
		c, err := l.svc.sonioxDial(ctx, r.key, l.req)
		conn = c

		return err
	}, func(string, string, []Failure) {})
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// sonioxDial opens one realtime stream with a key.
func (s *Service) sonioxDial(ctx context.Context, k Key, req LiveRequest) (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, liveConnectTimeout)
	defer cancel()

	//nolint:bodyclose // The library closes the handshake's body.
	conn, resp, err := websocket.Dial(ctx, s.cfg.SonioxRealtimeURL, &websocket.DialOptions{
		HTTPClient: s.client,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + k.Value}, "User-Agent": {userAgent}},
	})
	if err != nil {
		if resp != nil && resp.StatusCode >= http.StatusBadRequest {
			return nil, refusal(resp, nil, s.now())
		}

		return nil, fmt.Errorf("no stream: %w", err)
	}

	conn.SetReadLimit(liveMaxMessage)

	start := map[string]any{
		"model": SonioxRealtimeModel, "audio_format": "pcm_s16le", "sample_rate": LiveSampleRate, "num_channels": 1,
		"enable_endpoint_detection": true, "enable_speaker_diarization": req.Speakers,
	}

	if req.Language != "" {
		start["language_hints"] = []string{req.Language}
		start["language_hints_strict"] = true
	}

	if len(req.Terms) > 0 {
		start["context"] = map[string]any{"terms": req.Terms}
	}

	body, err := json.Marshal(start)
	if err == nil {
		err = conn.Write(ctx, websocket.MessageText, body)
	}

	if err != nil {
		_ = conn.CloseNow()

		return nil, fmt.Errorf("starting the stream: %w", err)
	}

	return conn, nil
}

// Append gives the stream audio, PCM16 24 kHz mono. It is refused once the stream ends, and with ErrBehind when the
// provider has not taken the last LiveMaxBehind of audio.
func (l *Live) Append(pcm []byte) error {
	if len(pcm)%2 != 0 {
		return fmt.Errorf("%w: PCM16 audio is whole samples", ErrInvalid)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed || l.finishing {
		return errors.New("the stream takes no more audio")
	}

	if len(pcm) == 0 {
		// Nothing to send; an empty frame would be taken for the end.
		return nil
	}

	// The audio not yet sent waits, while a new stream is awaited too, up to the bound (the issue's row #11).
	if int64(l.queued+len(pcm)) > durationToBytes(LiveMaxBehind) {
		return ErrBehind
	}

	frame := append([]byte(nil), pcm...)
	l.sent += int64(len(frame))
	l.queue = append(l.queue, frame)
	l.queued += len(frame)
	l.signal(l.wake)

	return nil
}

// Finish ends the stream: the audio given is sent, then the end, and the last segments are given before it returns,
// or after liveFinishTimeout, or when ctx ends. While a new stream is awaited after a failure, it waits for it and
// sends it the audio not yet sent (the review of #827, finding 1); a wait before asking for it is cut short.
func (l *Live) Finish(ctx context.Context) {
	l.mu.Lock()

	if l.closed || l.finishing {
		l.mu.Unlock()
		l.waitEvents(ctx)

		return
	}

	l.finishing = true

	switch {
	case l.conn != nil:
	case l.reconnecting:
		if l.retry != nil && l.retry.Stop() {
			l.retry = nil
			gen := l.generation

			// The new stream lives past this call, as the first did (Live).
			go l.reconnect(context.WithoutCancel(ctx), gen)
		}
	default:
		// The provider gave up: nothing can hear the audio left.
		l.flush()
		l.closeFinished()
	}

	l.signal(l.wake)
	l.mu.Unlock()

	timer := time.NewTimer(liveFinishTimeout)
	defer timer.Stop()

	select {
	case <-l.finished:
	case <-timer.C:
	case <-ctx.Done():
	}

	l.mu.Lock()
	l.flush()
	l.mu.Unlock()

	l.Close()
	l.waitEvents(ctx)
}

// Close ends the stream at once: the words already final are given, as MemoryOS's close flushes
// (SonioxLiveTranscription.close); what was not final is not.
func (l *Live) Close() {
	l.mu.Lock()

	if l.closed {
		l.mu.Unlock()

		return
	}

	l.closed = true
	conn := l.conn
	l.conn = nil
	l.reconnecting = false

	if l.retry != nil {
		l.retry.Stop()
		l.retry = nil
	}

	l.queue, l.queued = nil, 0
	l.replay, l.replayBytes = nil, 0
	l.retained, l.retainedBytes = nil, 0
	l.generation++
	l.flush()
	l.endEvents()
	l.mu.Unlock()

	l.stopOnce.Do(func() { close(l.stop) })
	l.closeFinished()

	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "done")
	}
}

// Done is closed once the stream has ended and every call to its listener was made: the last segments are kept.
func (l *Live) Done() <-chan struct{} { return l.eventsDone }

func (l *Live) waitEvents(ctx context.Context) {
	select {
	case <-l.eventsDone:
	case <-ctx.Done():
	}
}

func (l *Live) closeFinished() { l.finishedOnce.Do(func() { close(l.finished) }) }

func (l *Live) signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// write sends the queued audio, a keepalive while none flows, and the end once finishing; it is the one writer of
// every stream, so frames keep their order.
func (l *Live) write(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for {
		select {
		case <-l.stop:
			return
		case <-l.wake:
		case <-tick.C:
		}

		for {
			l.mu.Lock()

			conn, gen := l.conn, l.generation
			if conn == nil {
				l.mu.Unlock()

				break
			}

			var (
				frame   []byte
				typ     = websocket.MessageBinary
				message = ""
			)

			switch {
			case len(l.replay) > 0:
				frame = l.replay[0]
				l.replay = l.replay[1:]
				l.replayBytes -= len(frame)
				l.retain(frame)
				l.lastSend = l.svc.now()
			case len(l.queue) > 0:
				frame = l.queue[0]
				l.queue = l.queue[1:]
				l.queued -= len(frame)
				l.retain(frame)
				l.lastSend = l.svc.now()
			case l.finishing && !l.ended:
				// The end is an empty text frame (Soniox's websocket-api#ending-the-stream).
				l.ended = true
				typ, frame, message = websocket.MessageText, []byte{}, "end"
			case !l.finishing && l.svc.now().Sub(l.lastSend) >= liveKeepaliveAfter:
				l.lastSend = l.svc.now()
				typ, frame, message = websocket.MessageText, []byte(`{"type":"keepalive"}`), "keepalive"
			default:
				l.mu.Unlock()
			}

			if frame == nil {
				break
			}

			l.mu.Unlock()

			wctx, cancel := context.WithTimeout(ctx, liveWriteTimeout)
			err := conn.Write(wctx, typ, frame)

			cancel()

			if err != nil {
				l.failed(ctx, gen, cmpOr(message, "sending audio")+" failed")
			}
		}
	}
}

// read reads one stream's messages until it closes.
func (l *Live) read(ctx context.Context, conn *websocket.Conn, gen int) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			l.mu.Lock()
			finishing := l.finishing && gen == l.generation
			l.mu.Unlock()

			if finishing {
				l.mu.Lock()
				l.flush()
				l.mu.Unlock()
				l.closeFinished()

				return
			}

			l.failed(ctx, gen, "the stream closed")

			return
		}

		if !l.message(ctx, gen, data) {
			return
		}
	}
}

type liveToken struct {
	Text    string `json:"text"`
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	IsFinal bool   `json:"is_final"`
	Speaker any    `json:"speaker"`
}

// message reads one message of the provider; false when the stream is done with.
func (l *Live) message(ctx context.Context, gen int, data []byte) bool {
	var m struct {
		Tokens    []liveToken `json:"tokens"`
		Finished  bool        `json:"finished"`
		ErrorCode any         `json:"error_code"`
		ErrorType string      `json:"error_type"`
	}

	if err := json.Unmarshal(data, &m); err != nil {
		l.failed(ctx, gen, "an unreadable message")

		return false
	}

	if m.ErrorCode != nil {
		// The code and type say what to fix; the provider's own words may carry the account's details.
		l.failed(ctx, gen, fmt.Sprintf("the provider's error %v %s", m.ErrorCode, m.ErrorType))

		return false
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed || gen != l.generation {
		return false
	}

	var (
		pending        strings.Builder
		pendingSpeaker string
	)

	for _, t := range m.Tokens {
		if t.Text == "<end>" {
			l.flush()

			continue
		}

		if t.Text == "" || t.Text == "<fin>" {
			continue
		}

		speaker := cmpOr(speakerOf(t.Speaker), "1")

		if !t.IsFinal {
			if pendingSpeaker == "" {
				pendingSpeaker = speaker
			}

			pending.WriteString(t.Text)

			continue
		}

		start := l.base + time.Duration(t.StartMs)*time.Millisecond
		end := l.base + time.Duration(t.EndMs)*time.Millisecond

		// A replay hears again the end of what was given: none of it is given twice.
		if end <= l.lastEnd {
			continue
		}

		if l.current != nil && l.current.speaker != speaker {
			l.flush()
		}

		if l.current == nil {
			l.current = &segmentBuilder{speaker: speaker, start: start, end: start}
		}

		l.current.text.WriteString(t.Text)
		l.current.end = max(l.current.end, end)
	}

	speaker := pendingSpeaker
	text := pending.String()

	if l.current != nil {
		speaker, text = l.current.speaker, l.current.text.String()+text
	}

	text = strings.TrimSpace(text)
	l.emit(func() {
		if l.listener.Preview != nil {
			l.listener.Preview(speaker, text)
		}
	})

	if m.Finished {
		l.flush()
		l.closeFinished()

		return false
	}

	return true
}

// failed ends a broken stream and opens a new one after a wait, unless the stream was finishing or every wait was
// spent.
func (l *Live) failed(ctx context.Context, gen int, reason string) {
	l.mu.Lock()

	if l.closed || gen != l.generation || l.conn == nil {
		l.mu.Unlock()

		return
	}

	broken := l.conn
	l.conn = nil
	l.generation++
	// What the new stream did not get to send again stays to be replayed; the queue waits for the next stream.
	for _, frame := range l.replay {
		l.retain(frame)
	}

	l.replay, l.replayBytes = nil, 0
	l.flush()
	l.emit(func() {
		if l.listener.Preview != nil {
			l.listener.Preview("", "")
		}
	})

	switch {
	case l.finishing:
		l.closeFinished()
	case l.attempt >= len(l.backoff):
		l.reconnecting = false
		l.svc.cfg.Logger.WarnContext(ctx, "a live transcription gave up", slog.String("event", "speech.live_abandoned"),
			slog.Int("attempts", l.attempt), slog.String("reason", reason))
		l.emit(func() {
			if l.listener.Failed != nil {
				l.listener.Failed(fmt.Errorf("%w: %s", ErrFailed, reason))
			}
		})
	default:
		wait := l.backoff[l.attempt]
		l.attempt++
		next := l.generation

		l.svc.cfg.Logger.WarnContext(ctx, "a live transcription failed; opening a new one",
			slog.String("event", "speech.live_reconnecting"), slog.Int("attempt", l.attempt),
			slog.String("reason", reason))
		l.reconnecting = true
		l.retry = time.AfterFunc(wait, func() { l.reconnect(ctx, next) })
	}

	l.mu.Unlock()

	_ = broken.Close(websocket.StatusNormalClosure, "reconnect")
}

// reconnect opens a new stream, replays the retained audio into it and shifts its times to the meeting's clock. A
// stream finishing meanwhile still gets it: the audio waiting is sent to it before the end.
func (l *Live) reconnect(base context.Context, gen int) {
	l.mu.Lock()
	stale := l.closed || gen != l.generation
	l.retry = nil
	l.mu.Unlock()

	if stale {
		return
	}

	ctx, cancel := context.WithTimeout(base, liveConnectTimeout)
	conn, err := l.dial(ctx)

	cancel()

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed || gen != l.generation {
		if conn != nil {
			_ = conn.CloseNow()
		}

		return
	}

	if err != nil {
		if l.finishing {
			// Finishing waits for no further try.
			l.reconnecting = false
			l.flush()
			l.closeFinished()

			return
		}

		if l.attempt >= len(l.backoff) {
			l.reconnecting = false

			l.emit(func() {
				if l.listener.Failed != nil {
					l.listener.Failed(err)
				}
			})

			return
		}

		wait := l.backoff[l.attempt]
		l.attempt++
		l.retry = time.AfterFunc(wait, func() { l.reconnect(base, gen) })

		return
	}

	// Anarlog's replay offset: the new stream starts at the replayed tail, which the audio not yet sent follows, not
	// at the recording's start.
	l.conn = conn
	l.reconnecting = false
	l.replay, l.replayBytes = l.retained, l.retainedBytes
	l.retained, l.retainedBytes = nil, 0
	l.base = l.req.Offset + bytesToDuration(l.sent-int64(l.queued+l.replayBytes))
	l.signal(l.wake)

	go l.read(base, conn, gen)
}

// retain keeps a frame sent among the last liveReplay of audio. The caller holds l.mu.
func (l *Live) retain(frame []byte) {
	l.retained = append(l.retained, frame)
	l.retainedBytes += len(frame)

	for len(l.retained) > 1 && l.retainedBytes-len(l.retained[0]) >= int(durationToBytes(liveReplay)) {
		l.retainedBytes -= len(l.retained[0])
		l.retained = l.retained[1:]
	}
}

// flush gives the segment being built, if any. The caller holds l.mu.
func (l *Live) flush() {
	seg := l.current
	l.current = nil

	if seg == nil {
		return
	}

	text := strings.TrimSpace(seg.text.String())
	if text == "" {
		return
	}

	l.lastEnd = max(l.lastEnd, seg.end)
	out := Segment{Start: seg.start, End: seg.end, Text: text, Speaker: seg.speaker}

	l.emit(func() {
		if l.listener.Segment != nil {
			l.listener.Segment(out)
		}
	})
}

// emit queues a call to the listener; the caller holds l.mu. The calls are made one at a time, in order, outside
// the lock, so a listener that stores a line or writes to a socket never holds the audio back.
func (l *Live) emit(call func()) {
	if l.eventsEnded {
		return
	}

	l.events = append(l.events, call)
	l.signal(l.eventWake)
}

// endEvents lets the dispatcher end once the calls queued are made. The caller holds l.mu.
func (l *Live) endEvents() {
	l.eventsEnded = true
	l.signal(l.eventWake)
}

func (l *Live) dispatch() {
	defer close(l.eventsDone)

	for range l.eventWake {
		for {
			l.mu.Lock()

			if len(l.events) == 0 {
				done := l.eventsEnded
				l.mu.Unlock()

				if done {
					return
				}

				break
			}

			call := l.events[0]
			l.events = l.events[1:]
			l.mu.Unlock()

			call()
		}
	}
}

func bytesToDuration(n int64) time.Duration {
	return time.Duration(n) * time.Second / LiveBytesPerSecond
}

func durationToBytes(d time.Duration) int64 {
	return int64(d / time.Millisecond * LiveBytesPerSecond / 1000)
}
