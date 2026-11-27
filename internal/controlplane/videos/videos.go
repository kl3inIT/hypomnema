// Package videos makes the takes agents film into videos the owner approves
// (issue #818, rows 1, 2, 5, 6 and 10): a video belongs to one take, and
// each make or change request adds an immutable revision, made by one
// runner on the control plane, one revision at a time: voiced through the
// speech service, composed (package compose), captioned from its own voice
// (package captions), and put in place whole under videos/<video>/<rev>/.
// The owner approves one revision or asks for changes; an agent asks for a
// video of a take it filmed and reads its state.
package videos

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/speech"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// The states of a revision (issue #818, row 1).
const (
	Queued           = "queued"
	Voicing          = "voicing"
	Composing        = "composing"
	Captioning       = "captioning"
	Review           = "review"
	Approved         = "approved"
	ChangesRequested = "changes_requested"
	Failed           = "failed"
)

// Making says whether a revision is queued or being made.
func Making(state string) bool {
	return state == Queued || state == Voicing || state == Composing || state == Captioning
}

// Limits (issue #818, row 2).
const (
	// MaxQueued is how many revisions may wait in the queue.
	MaxQueued = 10
	// MakeTimeout bounds the making of one revision.
	MakeTimeout = 20 * time.Minute
	// minFreeBytes is the free disk a revision needs to start, as a take's upload keeps (takes/upload.go).
	minFreeBytes = 400_000_000
	// MaxLine bounds a scene's line, as video_say does (devicetool.MaxLine).
	MaxLine = devicetool.MaxLine
	// MaxNote bounds the owner's note.
	MaxNote = 4000
	// maxVoice bounds a voice's name.
	maxVoice = 100
	// MaxRequestID bounds a request id.
	MaxRequestID = 255
	// Owner is who asks when the owner does.
	Owner = "owner"
	// Language is the language every video is spoken and captioned in (row 5).
	Language = "vi"

	videoFile = "video.mp4"
	thumbFile = "thumbnail.jpg"
	workDir   = ".work"
	voiceDir  = ".voice"
)

// Errors.
var (
	ErrNotFound = errors.New("no such video")
	ErrConflict = errors.New("the video is not in a state for this")
	ErrInvalid  = errors.New("invalid video request")
	// ErrUnavailable reports what cannot be done now, such as before ffmpeg is installed.
	ErrUnavailable = errors.New("not available now")
	// ErrIdempotency reports a request id used before for another request.
	ErrIdempotency = errors.New("the request id was used for another request")
)

// Video is a video of a take, with its revisions, the oldest first.
type Video struct {
	ID         string     `json:"id"`
	Take       string     `json:"take"`
	Title      string     `json:"title"`
	MadeBy     string     `json:"made_by"`
	MadeByName string     `json:"made_by_name"`
	CreatedAt  time.Time  `json:"created_at"`
	Revisions  []Revision `json:"revisions"`
}

// Latest is the video's latest revision.
func (v Video) Latest() Revision { return v.Revisions[len(v.Revisions)-1] }

// Approved is the approved revision, if one is.
func (v Video) Approved() (Revision, bool) {
	for _, r := range v.Revisions {
		if r.State == Approved {
			return r, true
		}
	}

	return Revision{}, false
}

// Revision is one make of a video.
type Revision struct {
	Rev         int      `json:"rev"`
	Parent      int      `json:"parent,omitempty"`
	State       string   `json:"state"`
	Lines       []string `json:"lines"`
	Voice       string   `json:"voice,omitempty"`
	Captions    bool     `json:"captions"`
	Aspect      string   `json:"aspect"`
	Provider    string   `json:"provider,omitempty"`
	Model       string   `json:"model,omitempty"`
	UsedVoice   string   `json:"used_voice,omitempty"`
	LengthMs    int64    `json:"length_ms"`
	FailedStage string   `json:"failed_stage,omitempty"`
	FailedCause string   `json:"failed_cause,omitempty"`
	Note        string   `json:"note,omitempty"`
	Warnings    []string `json:"warnings"`
	// Look is how it is drawn: its captions, frame and background (issue #908).
	Look Look `json:"look"`
	// Progress is how far the stage it is at has come, while it is made.
	Progress  *Progress `json:"progress,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// pin is the voice it must be spoken with, when it was made from another revision (a look change, a retry).
	pin *voicePin
}

// voicePin is a provider and the exact voice it speaks with, even an empty one: its own default (issue #908, row
// 4). A pinned revision asks only that provider, and never reads the owner's audio settings.
type voicePin struct {
	provider string
	voice    string
}

// Progress is how many of a revision's scenes the stage it is at has done,
// as aidemo's job status counts them (src/mcp/jobs.ts:64-67, scenesTotal and
// scenesDone, reset at each sub-stage). It is kept in memory, as aidemo's
// job is: a revision being made when the control plane stops is failed as
// interrupted at its next start.
type Progress struct {
	ScenesDone  int `json:"scenes_done"`
	ScenesTotal int `json:"scenes_total"`
}

// Takes are the takes videos are made of (package takes).
type Takes interface {
	Get(id string) (devicetool.Take, error)
	// File is the path of a file of an available take: take.json, whose folder holds its segments.
	File(id, name string) (string, error)
}

type AudioSource struct {
	Video string
	Rev   int
	By    string
}

// Speech is the speech service with usage attributed to the revision that asks.
type Speech interface {
	Speak(ctx context.Context, source AudioSource, req speech.SpeakRequest) (speech.Speech, error)
	Transcribe(ctx context.Context, source AudioSource, req speech.TranscribeRequest) (speech.Transcript, error)
}

// Maker makes a revision's files with ffmpeg (package compose); tests give their own.
type Maker interface {
	Compose(ctx context.Context, tools ffmpeg.Tools, in compose.Input) (compose.Result, error)
	Burn(ctx context.Context, tools ffmpeg.Tools, video, captions, work string) (string, error)
	Thumbnail(ctx context.Context, tools ffmpeg.Tools, video, out string) error
}

// composeMaker is package compose.
type composeMaker struct{}

func (composeMaker) Compose(ctx context.Context, tools ffmpeg.Tools, in compose.Input) (compose.Result, error) {
	return compose.Compose(ctx, tools, in)
}

func (composeMaker) Burn(ctx context.Context, tools ffmpeg.Tools, video, captions, work string) (string, error) {
	return compose.Burn(ctx, tools, video, captions, work)
}

func (composeMaker) Thumbnail(ctx context.Context, tools ffmpeg.Tools, video, out string) error {
	return compose.Thumbnail(ctx, tools, video, out)
}

// Config is what the Service needs.
type Config struct {
	DB *sql.DB
	// Dir is the videos' folder, videos/ beside the database.
	Dir    string
	Takes  Takes
	Speech Speech
	// Settings are the owner's audio settings: the providers' order, switches and voices.
	Settings func(ctx context.Context) (speech.Settings, error)
	// Tools gives the installed ffmpeg (ffmpeg.Installer.Ensure).
	Tools func(ctx context.Context) (ffmpeg.Tools, error)
	// Ready gives the installed ffmpeg without waiting for an install under way (ffmpeg.Installer.Ready), for the
	// look sample; nil: Tools.
	Ready func() (ffmpeg.Tools, error)
	// Maker makes the files; nil: package compose.
	Maker Maker
	// FreeBytes says how much disk is free; nil: the file system's own.
	FreeBytes func(path string) (uint64, error)
	// MusicFS is the music library's file operations; its fields left nil are the os package's.
	MusicFS MusicFS
	// Timeout bounds one revision; zero: MakeTimeout.
	Timeout time.Duration
	// PreviewWait is how long a look sample waits for the one being made; zero: PreviewWait.
	PreviewWait time.Duration
	// PreviewTimeout bounds one look sample; zero: PreviewTimeout.
	PreviewTimeout time.Duration
	Clock          platform.Clock
	Logger         *slog.Logger
}

// Service keeps the videos and makes their revisions.
type Service struct {
	cfg  Config
	db   *sql.DB
	wake chan struct{}
	// mu orders the writes that read before they write: a make's queue cap and revision number, a delete's check.
	mu sync.Mutex
	// progress is the stage's progress of the revision being made, by revKey; pmu guards it.
	pmu      sync.Mutex
	progress map[string]Progress
	// preview is held while a look sample is made: one at a time.
	preview chan struct{}
	// upload is held while a track is received: one at a time (issue #910, row 6a).
	upload chan struct{}
	// musicDir is the music library's folder; fs its file operations, which tests replace.
	musicDir string
	fs       MusicFS
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Maker == nil {
		cfg.Maker = composeMaker{}
	}

	if cfg.FreeBytes == nil {
		cfg.FreeBytes = platform.FreeBytes
	}

	if cfg.Timeout <= 0 {
		cfg.Timeout = MakeTimeout
	}

	if cfg.PreviewWait <= 0 {
		cfg.PreviewWait = PreviewWait
	}

	if cfg.PreviewTimeout <= 0 {
		cfg.PreviewTimeout = PreviewTimeout
	}

	if cfg.Clock == nil {
		cfg.Clock = platform.SystemClock{}
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("making the videos' folder: %w", err)
	}

	s := &Service{
		cfg: cfg, db: cfg.DB, wake: make(chan struct{}, 1), progress: map[string]Progress{},
		preview: make(chan struct{}, 1), upload: make(chan struct{}, 1), musicDir: filepath.Join(cfg.Dir, musicFolder),
		fs: cfg.MusicFS.withDefaults(),
	}

	// The music folder is swept before the Service answers anything or makes anything (issue #910, row 6d).
	if err := s.sweepMusic(context.Background()); err != nil {
		return nil, err
	}

	return s, nil
}

var (
	videoIDs = regexp.MustCompile(`^vid_[0-9a-z]{1,40}$`)
	takeIDs  = regexp.MustCompile(`^tk_[0-9a-f]{16}$`)
)

// MakeRequest asks for a video of a take, or a new revision of it.
type MakeRequest struct {
	Take string
	// Title is the video's on its first make; empty: the take's.
	Title string
	// Aspect is 9:16 or 16:9; empty: 9:16.
	Aspect string
	// Captions; nil: on.
	Captions *bool
	// Voice is the voice asked for; empty: each provider's set one.
	Voice string
	// By is who asks: an agent's key, or Owner; ByName names it to people.
	By     string
	ByName string
	// RequestID makes the make safe to repeat: the same By and RequestID answer the same revision.
	RequestID string
	// Look is the sections of the look sent; nil: the saved default's (issue #908, row 2).
	Look *LookInput
}

// Make queues a revision of the take's video, making the video on the
// take's first make. Its lines are the latest revision's, or the take's
// scenes' on the first make. Its look is the sections sent, the others the
// saved default's; an owner's make that sends sections saves them as the
// default, in the same transaction (issue #908, rows 2 and 3).
func (s *Service) Make(ctx context.Context, req MakeRequest) (Video, Revision, error) {
	req.Title, req.Voice = strings.TrimSpace(req.Title), strings.TrimSpace(req.Voice)
	if req.Aspect == "" {
		req.Aspect = string(compose.Portrait)
	}

	captionsOn := req.Captions == nil || *req.Captions

	switch {
	case !takeIDs.MatchString(req.Take):
		return Video{}, Revision{}, fmt.Errorf("%w: take is a take's id, such as tk_0a1b2c3d4e5f6a7b", ErrInvalid)
	case req.Aspect != string(compose.Portrait) && req.Aspect != string(compose.Landscape):
		return Video{}, Revision{}, fmt.Errorf("%w: the aspect is 9:16 or 16:9", ErrInvalid)
	case utf8.RuneCountInString(req.Title) > devicetool.MaxTitle:
		return Video{}, Revision{}, fmt.Errorf("%w: a title is at most %d characters", ErrInvalid, devicetool.MaxTitle)
	case len(req.Voice) > maxVoice:
		return Video{}, Revision{}, fmt.Errorf("%w: a voice is at most %d characters", ErrInvalid, maxVoice)
	case len(req.RequestID) > MaxRequestID:
		return Video{}, Revision{}, fmt.Errorf("%w: a request id is at most %d bytes", ErrInvalid, MaxRequestID)
	case req.By == "":
		return Video{}, Revision{}, fmt.Errorf("%w: a make needs who asks", ErrInvalid)
	}

	if _, err := resolveLook(req.Look, DefaultLook()); err != nil {
		return Video{}, Revision{}, err
	}

	// A make that sends no look keeps the hash it had before the looks, so that its retry answers the same revision.
	parts := []string{"make", req.Take, req.Title, req.Aspect, strconv.FormatBool(captionsOn), req.Voice}
	if req.Look != nil {
		parts = append(parts, req.Look.canonical())
	}

	hash := requestHash(parts...)

	s.mu.Lock()
	defer s.mu.Unlock()

	if v, r, ok, err := s.byRequest(ctx, req.By, req.RequestID, hash); ok || err != nil {
		return v, r, err
	}

	take, err := s.cfg.Takes.Get(req.Take)
	if err != nil {
		return Video{}, Revision{}, fmt.Errorf("%w: take %s", ErrNotFound, req.Take)
	}

	if take.State != devicetool.TakeAvailable {
		return Video{}, Revision{}, fmt.Errorf("%w: take %s is %s, not available", ErrConflict, take.ID, take.State)
	}

	scenes, err := compose.Scenes(take)
	if err != nil {
		return Video{}, Revision{}, fmt.Errorf("%w: %w", ErrConflict, err)
	}

	var id string

	var lines []string

	parent := sql.NullInt64{}

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if n, err := q.CountQueued(ctx); err != nil {
			return err
		} else if n >= MaxQueued {
			return fmt.Errorf("%w: %d revisions are queued already; try again once one is made", ErrConflict, n)
		}

		v, err := q.GetVideoOfTake(ctx, take.ID)

		switch {
		case errors.Is(err, sql.ErrNoRows):
			id = platform.NewID("vid")
			title := cmp.Or(req.Title, take.Title)

			if err := q.InsertVideo(ctx, store.InsertVideoParams{
				ID: id, TakeID: take.ID, Title: title, MadeBy: req.By, MadeByName: req.ByName, Now: now,
			}); err != nil {
				return err
			}

			for _, sc := range scenes {
				lines = append(lines, sc.Text)
			}
		case err != nil:
			return err
		default:
			id = v.ID

			last, err := q.LastRev(ctx, id)
			if err != nil {
				return err
			}

			prev, err := q.GetRevision(ctx, store.GetRevisionParams{VideoID: id, Rev: last})
			if err != nil {
				return err
			}

			if err := json.Unmarshal([]byte(prev.Lines), &lines); err != nil || len(lines) != len(scenes) {
				lines = nil
				for _, sc := range scenes {
					lines = append(lines, sc.Text)
				}
			}

			parent = sql.NullInt64{Int64: last, Valid: true}
		}

		saved, err := s.savedLook(ctx, q)
		if err != nil {
			return err
		}

		look, err := resolveLook(req.Look, saved)
		if err != nil {
			return err
		}

		if look, err = fitTake(look, req.Look != nil && req.Look.Frame != nil, take, req.Aspect); err != nil {
			return err
		}

		if look, err = fitMusic(ctx, q, look, req.Look != nil && req.Look.Music != nil); err != nil {
			return err
		}

		if req.Look != nil && req.By == Owner {
			if err := q.SetVideoSettings(ctx, store.SetVideoSettingsParams{
				Look: mergeDefault(saved, req.Look, look).encode(), Now: now,
			}); err != nil {
				return err
			}
		}

		return s.insertRevision(ctx, q, now, id, newRevision{
			parent: parent, lines: lines, voice: req.Voice, captions: captionsOn, aspect: req.Aspect,
			look: look.encode(), by: req.By, requestID: req.RequestID, hash: hash,
		})
	})
	if err != nil {
		return Video{}, Revision{}, err
	}

	s.poke()

	v, err := s.Get(ctx, id)
	if err != nil {
		return Video{}, Revision{}, err
	}

	return v, v.Latest(), nil
}

// newRevision is what a revision is queued with.
type newRevision struct {
	parent   sql.NullInt64
	lines    []string
	voice    string
	captions bool
	aspect   string
	// look is the resolved look, as stored.
	look string
	pin  *voicePin

	by, requestID, hash string
}

// insertRevision queues a revision numbered after the video's last.
func (s *Service) insertRevision(ctx context.Context, q *store.Queries, now int64, id string, r newRevision) error {
	last, err := q.LastRev(ctx, id)
	if err != nil {
		return err
	}

	raw, err := json.Marshal(r.lines)
	if err != nil {
		return err
	}

	p := store.InsertRevisionParams{
		VideoID: id, Rev: last + 1, Parent: r.parent, Lines: string(raw), Voice: r.voice, Captions: flag(r.captions),
		Aspect: r.aspect, Look: r.look, RequestedBy: r.by, RequestID: r.requestID, RequestHash: r.hash, Now: now,
	}
	if r.pin != nil {
		p.PinProvider, p.PinVoice = r.pin.provider, r.pin.voice
	}

	return q.InsertRevision(ctx, p)
}

// savedLook is the saved default look.
func (s *Service) savedLook(ctx context.Context, q *store.Queries) (Look, error) {
	raw, err := q.GetVideoSettings(ctx)
	if err != nil {
		return Look{}, fmt.Errorf("reading the default look: %w", err)
	}

	return parseLook(raw)
}

// byRequest is the revision a request id made before: ok when there is one,
// ErrIdempotency when it was made by another request.
func (s *Service) byRequest(ctx context.Context, by, requestID, hash string) (Video, Revision, bool, error) {
	if requestID == "" {
		return Video{}, Revision{}, false, nil
	}

	r, err := store.New(s.db).RevisionByRequest(ctx, store.RevisionByRequestParams{
		RequestedBy: by, RequestID: requestID,
	})

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Video{}, Revision{}, false, nil
	case err != nil:
		return Video{}, Revision{}, false, err
	case r.RequestHash != hash:
		return Video{}, Revision{}, false, fmt.Errorf("%w: %q", ErrIdempotency, requestID)
	}

	v, err := s.Get(ctx, r.VideoID)
	if err != nil {
		return Video{}, Revision{}, false, err
	}

	for _, rev := range v.Revisions {
		if int64(rev.Rev) == r.Rev {
			return v, rev, true, nil
		}
	}

	return Video{}, Revision{}, false, fmt.Errorf("%w: %s", ErrNotFound, r.VideoID)
}

func requestHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(strconv.Itoa(len(p)) + ":" + p))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// Get reads a video with its revisions.
func (s *Service) Get(ctx context.Context, id string) (Video, error) {
	if !videoIDs.MatchString(id) {
		return Video{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}

	q := store.New(s.db)

	v, err := q.GetVideo(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Video{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}

	if err != nil {
		return Video{}, err
	}

	return s.withRevisions(ctx, q, v)
}

// OfTake is a take's video.
func (s *Service) OfTake(ctx context.Context, take string) (Video, error) {
	q := store.New(s.db)

	v, err := q.GetVideoOfTake(ctx, take)
	if errors.Is(err, sql.ErrNoRows) {
		return Video{}, fmt.Errorf("%w: of take %s", ErrNotFound, take)
	}

	if err != nil {
		return Video{}, err
	}

	return s.withRevisions(ctx, q, v)
}

// List lists the videos, the newest first.
func (s *Service) List(ctx context.Context) ([]Video, error) {
	q := store.New(s.db)

	rows, err := q.ListVideos(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]Video, 0, len(rows))

	for _, row := range rows {
		v, err := s.withRevisions(ctx, q, row)
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	return out, nil
}

func (s *Service) withRevisions(ctx context.Context, q *store.Queries, row store.Video) (Video, error) {
	revs, err := q.ListRevisions(ctx, row.ID)
	if err != nil {
		return Video{}, err
	}

	v := Video{
		ID: row.ID, Take: row.TakeID, Title: row.Title, MadeBy: row.MadeBy, MadeByName: row.MadeByName,
		CreatedAt: time.UnixMilli(row.CreatedAt).UTC(), Revisions: make([]Revision, 0, len(revs)),
	}

	for _, r := range revs {
		rev := revisionOf(r)
		if Making(rev.State) {
			rev.Progress = s.progressOf(row.ID, rev.Rev)
		}

		v.Revisions = append(v.Revisions, rev)
	}

	if len(v.Revisions) == 0 {
		return Video{}, fmt.Errorf("%w: %s has no revision", ErrNotFound, row.ID)
	}

	return v, nil
}

func revisionOf(r store.VideoRevision) Revision {
	out := Revision{
		Rev: int(r.Rev), Parent: int(r.Parent.Int64), State: r.State, Voice: r.Voice, Captions: r.Captions != 0,
		Aspect: r.Aspect, Provider: r.Provider, Model: r.Model, UsedVoice: r.UsedVoice, LengthMs: r.LengthMs,
		FailedStage: r.FailedStage, FailedCause: r.FailedCause, Note: r.Note,
		CreatedAt: time.UnixMilli(r.CreatedAt).UTC(), UpdatedAt: time.UnixMilli(r.UpdatedAt).UTC(),
	}

	_ = json.Unmarshal([]byte(r.Lines), &out.Lines)
	_ = json.Unmarshal([]byte(r.Warnings), &out.Warnings)

	// Every revision keeps its look since the looks' migration wrote one for those before (issue #937).
	out.Look, _ = parseLook(r.Look)

	if r.PinProvider != "" {
		out.pin = &voicePin{provider: r.PinProvider, voice: r.PinVoice}
	}

	if out.Lines == nil {
		out.Lines = []string{}
	}

	if out.Warnings == nil {
		out.Warnings = []string{}
	}

	return out
}

// Approve approves a revision in review; the one approved before goes back
// to review. An approved revision is approved already.
func (s *Service) Approve(ctx context.Context, id string, rev int) (Video, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return Video{}, err
	}

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		r, err := q.GetRevision(ctx, store.GetRevisionParams{VideoID: id, Rev: int64(rev)})

		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: revision %d of %s", ErrNotFound, rev, id)
		case err != nil:
			return err
		case r.State == Approved:
			return nil
		case r.State != Review:
			return fmt.Errorf("%w: revision %d is %s; only one in review is approved", ErrConflict, rev, r.State)
		}

		if err := q.UnapproveOthers(ctx, store.UnapproveOthersParams{VideoID: id, Rev: int64(rev), Now: now}); err != nil {
			return err
		}

		n, err := q.ApproveRevision(ctx, store.ApproveRevisionParams{VideoID: id, Rev: int64(rev), Now: now})
		if err == nil && n == 0 {
			err = fmt.Errorf("%w: revision %d is no longer in review", ErrConflict, rev)
		}

		return err
	})
	if err != nil {
		return Video{}, err
	}

	return s.Get(ctx, id)
}

// ChangeRequest is what the owner asks of a revision in review.
type ChangeRequest struct {
	// Lines are each scene's line, edited; nil: none changed.
	Lines []string
	Note  string
	// By and RequestID make the request safe to repeat, as a make's.
	By        string
	RequestID string
}

// RequestChanges marks a revision in review changes_requested with the
// owner's note, and queues a new revision with the lines when they changed.
func (s *Service) RequestChanges(ctx context.Context, id string, rev int, req ChangeRequest) (Video, error) {
	req.Note = strings.TrimSpace(req.Note)

	switch {
	case utf8.RuneCountInString(req.Note) > MaxNote:
		return Video{}, fmt.Errorf("%w: a note is at most %d characters", ErrInvalid, MaxNote)
	case len(req.RequestID) > MaxRequestID:
		return Video{}, fmt.Errorf("%w: a request id is at most %d bytes", ErrInvalid, MaxRequestID)
	case req.By == "":
		req.By = Owner
	}

	for i, l := range req.Lines {
		req.Lines[i] = strings.TrimSpace(l)
		if req.Lines[i] == "" || utf8.RuneCountInString(req.Lines[i]) > MaxLine {
			return Video{}, fmt.Errorf("%w: line %d is empty or longer than %d characters", ErrInvalid, i+1, MaxLine)
		}
	}

	raw, _ := json.Marshal(req.Lines) //nolint:errchkjson // Strings.
	hash := requestHash("changes", id, strconv.Itoa(rev), string(raw), req.Note)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, _, ok, err := s.byRequest(ctx, req.By, req.RequestID, hash); ok || err != nil {
		if err != nil {
			return Video{}, err
		}

		return s.Get(ctx, id)
	}

	if _, err := s.Get(ctx, id); err != nil {
		return Video{}, err
	}

	queued := false

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		r, err := q.GetRevision(ctx, store.GetRevisionParams{VideoID: id, Rev: int64(rev)})

		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: revision %d of %s", ErrNotFound, rev, id)
		case err != nil:
			return err
		case r.State == Approved:
			return fmt.Errorf("%w: revision %d is approved, and an approved revision is never changed", ErrConflict, rev)
		case r.State != Review && r.State != ChangesRequested:
			return fmt.Errorf("%w: revision %d is %s; changes are asked of one in review", ErrConflict, rev, r.State)
		}

		var lines []string
		if err := json.Unmarshal([]byte(r.Lines), &lines); err != nil {
			return err
		}

		if req.Lines != nil && len(req.Lines) != len(lines) {
			return fmt.Errorf("%w: the revision has %d lines, not %d", ErrInvalid, len(lines), len(req.Lines))
		}

		n, err := q.RequestChanges(ctx, store.RequestChangesParams{VideoID: id, Rev: int64(rev), Note: req.Note, Now: now})
		if err != nil {
			return err
		}

		if n == 0 {
			return fmt.Errorf("%w: revision %d is no longer in review", ErrConflict, rev)
		}

		if req.Lines == nil || slices.Equal(req.Lines, lines) {
			return nil
		}

		if n, err := q.CountQueued(ctx); err != nil {
			return err
		} else if n >= MaxQueued {
			return fmt.Errorf("%w: %d revisions are queued already; try again once one is made", ErrConflict, n)
		}

		queued = true

		// The new revision is drawn as the one it changes: its look is copied as stored (row 2), its music fallen
		// back to none if its track was deleted (issue #910, row 16).
		look, err := fitStoredMusic(ctx, q, r.Look)
		if err != nil {
			return err
		}

		return s.insertRevision(ctx, q, now, id, newRevision{
			parent: sql.NullInt64{Int64: int64(rev), Valid: true}, lines: req.Lines, voice: r.Voice,
			captions: r.Captions != 0, aspect: r.Aspect, look: look, by: req.By, requestID: req.RequestID, hash: hash,
		})
	})
	if err != nil {
		return Video{}, err
	}

	if queued {
		s.poke()
	}

	return s.Get(ctx, id)
}

// Delete deletes a video, its revisions and their files; refused while a
// revision of it is queued or being made.
func (s *Service) Delete(ctx context.Context, id string) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.CountUnfinished(ctx, id)
		if err != nil {
			return err
		}

		if n > 0 {
			return fmt.Errorf("%w: a revision of %s is being made; delete it once it is made", ErrConflict, id)
		}

		if err := q.DeleteRevisions(ctx, id); err != nil {
			return err
		}

		return q.DeleteVideo(ctx, id)
	})
	if err != nil {
		return err
	}

	return os.RemoveAll(filepath.Join(s.cfg.Dir, id))
}

// TakeInWork says whether a revision of the take's video is queued or being
// made: the take may not be deleted then (row 2).
func (s *Service) TakeInWork(ctx context.Context, take string) (bool, error) {
	ids, err := store.New(s.db).TakesInWork(ctx)
	if err != nil {
		return false, err
	}

	return slices.Contains(ids, take), nil
}

// File is the path of a made revision's MP4.
func (s *Service) File(ctx context.Context, id string, rev int) (string, error) {
	return s.made(ctx, id, rev, videoFile)
}

// Thumbnail is the path of a made revision's first frame.
func (s *Service) Thumbnail(ctx context.Context, id string, rev int) (string, error) {
	return s.made(ctx, id, rev, thumbFile)
}

// FirstFrame writes a picture of a video's first frame to out, with the
// installed ffmpeg: a take's thumbnail in the Studio (takes.Store.Thumbnail).
func (s *Service) FirstFrame(ctx context.Context, video, out string) error {
	tools, err := s.cfg.Tools(ctx)
	if err != nil {
		return fmt.Errorf("%w: ffmpeg is not installed: %w", ErrUnavailable, err)
	}

	return s.cfg.Maker.Thumbnail(ctx, tools, video, out)
}

func (s *Service) made(ctx context.Context, id string, rev int, name string) (string, error) {
	v, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}

	for _, r := range v.Revisions {
		if r.Rev != rev {
			continue
		}

		if r.State != Review && r.State != Approved && r.State != ChangesRequested {
			return "", fmt.Errorf("%w: revision %d is %s", ErrNotFound, rev, r.State)
		}

		path := s.revDir(id, rev, name)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("%w: %s of revision %d", ErrNotFound, name, rev)
		}

		return path, nil
	}

	return "", fmt.Errorf("%w: revision %d of %s", ErrNotFound, rev, id)
}

func (s *Service) revDir(id string, rev int, name ...string) string {
	return filepath.Join(append([]string{s.cfg.Dir, id, strconv.Itoa(rev)}, name...)...)
}

func revKey(id string, rev int) string { return id + "/" + strconv.Itoa(rev) }

// setProgress says how many of a revision's scenes its stage has done.
func (s *Service) setProgress(id string, rev, done, total int) {
	s.pmu.Lock()
	defer s.pmu.Unlock()

	s.progress[revKey(id, rev)] = Progress{ScenesDone: done, ScenesTotal: total}
}

// dropProgress forgets a revision's progress, once it is no longer made.
func (s *Service) dropProgress(id string, rev int) {
	s.pmu.Lock()
	defer s.pmu.Unlock()

	delete(s.progress, revKey(id, rev))
}

func (s *Service) progressOf(id string, rev int) *Progress {
	s.pmu.Lock()
	defer s.pmu.Unlock()

	p, ok := s.progress[revKey(id, rev)]
	if !ok {
		return nil
	}

	return &p
}

// write runs fn in one transaction.
func (s *Service) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}

	if err := fn(store.New(tx), s.cfg.Clock.Now().UnixMilli()); err != nil {
		return errors.Join(err, tx.Rollback())
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing: %w", err)
	}

	return nil
}

// poke wakes the runner.
func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func flag(b bool) int64 {
	if b {
		return 1
	}

	return 0
}
