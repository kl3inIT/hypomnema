package videos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/captions"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/internal/store"
)

// The runner (issue #818, row 2): one goroutine of the control plane makes
// the queued revisions one at a time, apart from any request. It takes a
// revision by a conditional update from queued to voicing, so that only one
// runner ever makes it; at its start, a revision found being made (the
// control plane stopped meanwhile) is failed as interrupted, for the owner
// to make again, as no paid speech is asked again on its own. A revision has
// 20 minutes; it does not start with less than 400 MB free. Its files are
// made in videos/<video>/<rev>/.work/ and renamed into place only when the
// whole MP4 is made, so a failure leaves no MP4. The references keep their
// jobs in memory and run one (aidemo src/mcp/jobs.ts:91, autodemo
// src/mcp.js:205); here they are rows, as the control plane restarts on
// self-update.

// idleCheck is how often the runner looks at the queue when nothing woke it.
const idleCheck = time.Minute

// Run makes the queued revisions until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.interrupt(ctx)

	for {
		for s.next(ctx) {
		}

		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-time.After(idleCheck):
		}
	}
}

// interrupt fails the revisions a control plane that stopped was making.
func (s *Service) interrupt(ctx context.Context) {
	var rows []store.InterruptRevisionsRow

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		var err error
		rows, err = q.InterruptRevisions(ctx, now)

		return err
	})
	if err != nil {
		s.cfg.Logger.ErrorContext(ctx, "failing the revisions a stop interrupted",
			slog.String("event", "videos.interrupt_failed"), slog.String("error", err.Error()))

		return
	}

	for _, r := range rows {
		_ = os.RemoveAll(s.revDir(r.VideoID, int(r.Rev)))

		s.cfg.Logger.WarnContext(ctx, "a revision being made was interrupted",
			slog.String("event", "videos.revision_interrupted"), slog.String("video", r.VideoID),
			slog.Int64("rev", r.Rev))
	}
}

// next makes the oldest queued revision; false when there is none, or ctx ended.
func (s *Service) next(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}

	q := store.New(s.db)

	r, err := q.NextQueued(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}

	if err != nil {
		s.cfg.Logger.ErrorContext(ctx, "reading the videos' queue", slog.String("event", "videos.queue_failed"),
			slog.String("error", err.Error()))

		return false
	}

	n, err := q.ClaimRevision(ctx, store.ClaimRevisionParams{
		VideoID: r.VideoID, Rev: r.Rev, Now: s.cfg.Clock.Now().UnixMilli(),
	})
	if err != nil || n == 0 {
		return err == nil
	}

	s.make(ctx, r)

	return true
}

// stageError is a failure at a stage.
type stageError struct {
	stage string
	err   error
}

func (e *stageError) Error() string { return e.stage + ": " + e.err.Error() }

func (e *stageError) Unwrap() error { return e.err }

func at(stage string, err error) error { return &stageError{stage: stage, err: err} }

// make makes one claimed revision and records how it ended.
func (s *Service) make(parent context.Context, r store.VideoRevision) {
	ctx, cancel := context.WithTimeout(parent, s.cfg.Timeout)
	defer cancel()

	rev := revisionOf(r)
	log := s.cfg.Logger.With(slog.String("video", r.VideoID), slog.Int("rev", rev.Rev))
	dir := s.revDir(r.VideoID, rev.Rev)
	started := time.Now()

	defer s.dropProgress(r.VideoID, rev.Rev)

	warnings, length, err := s.build(ctx, AudioSource{Video: r.VideoID, Rev: rev.Rev, By: r.RequestedBy}, rev, dir)
	if err == nil {
		err = s.finish(ctx, r.VideoID, rev.Rev, length, warnings)
	}

	if err == nil {
		log.InfoContext(ctx, "a revision was made", slog.String("event", "videos.revision_made"),
			slog.Duration("took", time.Since(started)))

		return
	}

	_ = os.RemoveAll(dir)

	if parent.Err() != nil {
		// The control plane is stopping: the revision stays where it was, and is failed as interrupted at the next
		// start.
		return
	}

	stage, cause := Voicing, err.Error()

	var se *stageError
	if errors.As(err, &se) {
		stage, cause = se.stage, se.err.Error()
	}

	if errors.Is(err, context.DeadlineExceeded) {
		cause = fmt.Sprintf("it took longer than %s", s.cfg.Timeout)
	}

	log.WarnContext(ctx, "a revision failed", slog.String("event", "videos.revision_failed"),
		slog.String("stage", stage), slog.String("cause", cause))

	ctx = context.WithoutCancel(parent)
	if werr := s.write(ctx, func(q *store.Queries, now int64) error {
		_, err := q.FailRevision(ctx, store.FailRevisionParams{
			VideoID: r.VideoID, Rev: int64(rev.Rev), Stage: stage, Cause: cause, Now: now,
		})

		return err
	}); werr != nil {
		log.ErrorContext(ctx, "recording a revision's failure", slog.String("event", "videos.fail_failed"),
			slog.String("error", werr.Error()))
	}
}

// build makes the revision's files in place: its MP4 and thumbnail. It
// answers the warnings and the MP4's length.
func (s *Service) build(ctx context.Context, source AudioSource, rev Revision, dir string) ([]string, int64, error) {
	id := source.Video
	if free, err := s.cfg.FreeBytes(s.cfg.Dir); err == nil && free < minFreeBytes {
		return nil, 0, at(Voicing, fmt.Errorf("the control plane has %d MB of disk free; a video needs %d MB",
			free/1_000_000, minFreeBytes/1_000_000))
	}

	v, err := s.Get(ctx, id)
	if err != nil {
		return nil, 0, at(Voicing, err)
	}

	take, err := s.cfg.Takes.Get(v.Take)
	if err != nil {
		return nil, 0, at(Voicing, fmt.Errorf("reading take %s: %w", v.Take, err))
	}

	takeJSON, err := s.cfg.Takes.File(v.Take, "take.json")
	if err != nil {
		return nil, 0, at(Voicing, fmt.Errorf("take %s has no files: %w", v.Take, err))
	}

	scenes, err := compose.Scenes(take)
	if err != nil {
		return nil, 0, at(Voicing, err)
	}

	if len(scenes) != len(rev.Lines) {
		return nil, 0, at(Voicing, fmt.Errorf("the take has %d scenes and the revision %d lines", len(scenes),
			len(rev.Lines)))
	}

	work := filepath.Join(dir, workDir)
	_ = os.RemoveAll(dir)

	if err := os.MkdirAll(work, 0o700); err != nil {
		return nil, 0, at(Voicing, err)
	}

	defer func() { _ = os.RemoveAll(work) }()

	// Each stage counts its scenes from none, as aidemo's runSubStage resets its count (src/mcp/jobs.ts:201-205).
	total := len(scenes)
	stage := func(n int) { s.setProgress(id, rev.Rev, n, total) }
	stage(0)

	said, err := s.speak(ctx, source, rev.Lines, rev.Voice, stage)
	if err != nil {
		return nil, 0, at(Voicing, err)
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		return q.SetRevisionVoice(ctx, store.SetRevisionVoiceParams{
			VideoID: id, Rev: int64(rev.Rev), Provider: said.provider, Model: said.model, UsedVoice: said.voice, Now: now,
		})
	}); err != nil {
		return nil, 0, at(Voicing, err)
	}

	if err := s.advance(ctx, id, rev.Rev, Voicing, Composing); err != nil {
		return nil, 0, at(Voicing, err)
	}

	stage(0)

	tools, err := s.cfg.Tools(ctx)
	if err != nil {
		return nil, 0, at(Composing, fmt.Errorf("ffmpeg is not installed: %w", err))
	}

	in := compose.Input{
		Dir: filepath.Dir(takeJSON), Voice: said.clips, Aspect: compose.Aspect(rev.Aspect),
		Work: filepath.Join(work, "compose"), Out: filepath.Join(work, "composed.mp4"), Progress: stage,
	}
	if take.Screen != nil {
		in.Screen = compose.Size{Width: take.Screen.Width, Height: take.Screen.Height}
	}

	made, err := s.cfg.Maker.Compose(ctx, tools, in)
	if err != nil {
		return nil, 0, at(Composing, err)
	}

	final := in.Out

	if rev.Captions {
		if err := s.advance(ctx, id, rev.Rev, Composing, Captioning); err != nil {
			return nil, 0, at(Composing, err)
		}

		stage(0)

		ass, err := s.captions(ctx, source, said.clips, made, filepath.Join(work, "captions.ass"), stage)
		if err != nil {
			return nil, 0, at(Captioning, err)
		}

		if final, err = s.cfg.Maker.Burn(ctx, tools, final, ass, filepath.Join(work, "burn")); err != nil {
			return nil, 0, at(Captioning, err)
		}
	}

	last := Composing
	if rev.Captions {
		last = Captioning
	}

	thumb := filepath.Join(work, thumbFile)
	if err := s.cfg.Maker.Thumbnail(ctx, tools, final, thumb); err != nil {
		return nil, 0, at(last, fmt.Errorf("taking the first frame: %w", err))
	}

	// The thumbnail first, then the MP4: a revision's MP4 is there only once it is whole.
	if err := os.Rename(thumb, filepath.Join(dir, thumbFile)); err != nil {
		return nil, 0, at(last, err)
	}

	if err := os.Rename(final, filepath.Join(dir, videoFile)); err != nil {
		return nil, 0, at(last, err)
	}

	return made.Warnings, made.LengthMs, nil
}

// captions transcribes each scene's clip with its words' times, builds the
// cues on the video's time and writes them as an ASS file (row 6). done is
// told how many scenes are transcribed, after each.
func (s *Service) captions(ctx context.Context, source AudioSource, clips []string, made compose.Result, out string,
	done func(n int),
) (string, error) {
	if len(made.Scenes) != len(clips) {
		return "", fmt.Errorf("the video has %d scenes and %d clips", len(made.Scenes), len(clips))
	}

	scenes := make([]captions.Scene, 0, len(clips))

	for k, clip := range clips {
		words, err := s.clipWords(ctx, source, clip)
		if err != nil {
			return "", fmt.Errorf("scene %d: %w", k+1, err)
		}

		sc := made.Scenes[k]
		scenes = append(scenes, captions.Scene{
			StartMs: sc.OutStartMs, EndMs: sc.OutStartMs + sc.OutLengthMs,
			Words: words,
		})
		done(len(scenes))
	}

	cues, err := captions.Build(scenes, captions.Default)
	if err != nil {
		return "", err
	}

	var rings []compose.Ring

	for _, sc := range made.Scenes {
		if sc.Ring != nil {
			rings = append(rings, *sc.Ring)
		}
	}

	// Every revision keeps the captions it was made with until a revision stores its look (#908, part 3).
	ass, err := captions.Render(cues, captions.Look{Preset: captions.Legacy}, made.Size.Width, made.Size.Height, rings)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(out, []byte(ass), 0o600); err != nil {
		return "", err
	}

	return out, nil
}

// advance moves a revision from one stage to the next.
func (s *Service) advance(ctx context.Context, id string, rev int, from, to string) error {
	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.AdvanceRevision(ctx, store.AdvanceRevisionParams{
			VideoID: id, Rev: int64(rev), From: from, State: to, Now: now,
		})
		if err == nil && n == 0 {
			err = fmt.Errorf("%w: revision %d is no longer %s", ErrConflict, rev, from)
		}

		return err
	})
}

// finish puts a made revision in review.
func (s *Service) finish(ctx context.Context, id string, rev int, length int64, warnings []string) error {
	if warnings == nil {
		warnings = []string{}
	}

	raw, err := json.Marshal(warnings)
	if err != nil {
		return err
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.FinishRevision(ctx, store.FinishRevisionParams{
			VideoID: id, Rev: int64(rev), LengthMs: length, Warnings: string(raw), Now: now,
		})
		if err == nil && n == 0 {
			err = fmt.Errorf("%w: revision %d is no longer being made", ErrConflict, rev)
		}

		return err
	})
}
