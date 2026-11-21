package videos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/compose"
	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/internal/store"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// The owner's music library (issue #910, rows 1, 6a-6e and 15-16). The owner uploads tracks; none is bundled and a
// video has no music until a look names one. A track is streamed to a temporary file of the music folder, as a take's
// upload is (takes/upload.go), checked by ffprobe and decoded whole by compose.Measure, and only then renamed to
// <id>.<ext> and given its row; its file never changes. A revision's look names a track as a snapshot (its id, name and
// length), so deleting a track leaves the revisions made with it as they are; a track named by a revision being made
// is not deleted. At start, before the API and the runner, the folder is swept: what a crash left half-written, and a
// file with no row, are removed.

const (
	// MaxMusicBytes bounds a track's upload.
	MaxMusicBytes = 64 << 20
	// MusicQuotaBytes bounds the library's files together (owner, 2026-11-21).
	MusicQuotaBytes = 5_000_000_000
	// MaxMusicName bounds a track's name, as a video's title is bounded.
	MaxMusicName = 200
	// minMusicMs and maxMusicMs bound a track's decoded length.
	minMusicMs = 10_000
	maxMusicMs = 20 * 60_000
	// musicDecodeLimitSec is how much of a track the upload decodes at most: past maxMusicMs, so a longer track is
	// told from one of 20 minutes.
	musicDecodeLimitSec = 1201
	// musicCheckTimeout bounds the probe and the decode of an upload.
	musicCheckTimeout = 60 * time.Second
	// musicNone is the track of a look with no music.
	musicNone = "none"
	// musicFolder is the music folder's name in the videos' folder.
	musicFolder = "music"
	// partSuffix ends the name of a track being received.
	partSuffix = ".part"
)

var (
	// ErrDiskLow reports a library past its quota, or a disk too full for a track.
	ErrDiskLow = errors.New("too little room for the track")
	// ErrMusicBusy reports an upload while another is received.
	ErrMusicBusy = errors.New("another track is being uploaded")
)

// MusicDir is the music folder of the control plane whose database is at database: videos/music beside it, in the
// videos' folder (controlplane.go keeps videos/ beside the database).
func MusicDir(database string) string {
	return filepath.Join(filepath.Dir(database), "videos", musicFolder)
}

// MusicLook is a revision's music, as stored: the track, with its name and length when it was chosen, and the gain
// that moves it from its level under the voice. A track taken from the default or the source that was deleted is
// stored as none, with the track it fell back from (row 16).
type MusicLook struct {
	Track        string         `json:"track"`
	Name         string         `json:"name,omitempty"`
	LengthMs     int64          `json:"length_ms,omitempty"`
	GainDB       float64        `json:"gain_db"`
	FellBackFrom *MusicSnapshot `json:"fell_back_from,omitempty"`
}

// MusicSnapshot is a track as a look named it.
type MusicSnapshot struct {
	Track    string `json:"track"`
	Name     string `json:"name"`
	LengthMs int64  `json:"length_ms"`
}

// MusicInput is the music section as sent: its track is required, its gain is 0 when left out (row 15).
type MusicInput struct {
	Track  *string  `json:"track,omitempty"`
	GainDB *float64 `json:"gain_db,omitempty"`
}

// musicOf is the sent music section: nil for none. Its name and length are filled from the library when it is queued.
func musicOf(in *MusicInput) (*MusicLook, error) {
	switch {
	case in.Track == nil:
		return nil, fmt.Errorf("%w: a music section needs its track: a track's id, or none", ErrInvalid)
	case *in.Track != musicNone && !platform.IsID("mus", *in.Track):
		return nil, fmt.Errorf("%w: a music track is a track's id or none, not %q", ErrInvalid, *in.Track)
	}

	gain := 0.0
	if in.GainDB != nil {
		gain = *in.GainDB
	}

	if math.IsNaN(gain) || gain < compose.MinMusicGainDB || gain > compose.MaxMusicGainDB {
		return nil, fmt.Errorf("%w: the music's gain is from %g to %g dB", ErrInvalid, compose.MinMusicGainDB,
			compose.MaxMusicGainDB)
	}

	if *in.Track == musicNone {
		return nil, nil //nolint:nilnil // No music is no section, and no error.
	}

	return &MusicLook{Track: *in.Track, GainDB: gain}, nil
}

// fitMusic checks a look's track against the library while a revision is queued, under s.mu, which a track's delete
// holds too (row 16): a track there gets its name and length; one deleted is refused when sent, and otherwise falls
// back to none, with the track it was.
func fitMusic(ctx context.Context, q *store.Queries, l Look, sent bool) (Look, error) {
	if l.Music == nil || l.Music.Track == musicNone {
		return l, nil
	}

	row, err := q.GetMusic(ctx, l.Music.Track)

	switch {
	case err == nil:
		m := *l.Music
		m.Name, m.LengthMs = row.Name, row.LengthMs
		l.Music = &m

		return l, nil
	case !errors.Is(err, sql.ErrNoRows):
		return Look{}, err
	case sent:
		return Look{}, fmt.Errorf("%w: the music track %s was deleted", ErrConflict, l.Music.Track)
	}

	l.Music = &MusicLook{
		Track: musicNone, FellBackFrom: &MusicSnapshot{Track: l.Music.Track, Name: l.Music.Name, LengthMs: l.Music.LengthMs},
	}

	return l, nil
}

// fitStoredMusic is a look copied as stored (a retry, changed lines) with its music checked as fitMusic does; the
// stored text is kept as it is unless the track fell back.
func fitStoredMusic(ctx context.Context, q *store.Queries, raw string) (string, error) {
	l, err := parseLook(raw)
	if err != nil || l.Music == nil || l.Music.Track == musicNone {
		return raw, nil //nolint:nilerr // A look that does not parse is the runner's to read as legacy.
	}

	fitted, err := fitMusic(ctx, q, l, false)
	if err != nil {
		return "", err
	}

	if fitted.Music.Track == l.Music.Track {
		return raw, nil
	}

	return fitted.encode(), nil
}

// musicFellBackWarning is the warning of a revision whose track was deleted before it was queued.
func musicFellBackWarning(m *MusicSnapshot) string {
	return fmt.Sprintf("the music %q was deleted, so the video has no music", m.Name)
}

// Track is a track of the library.
type Track struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	MediaType string    `json:"media_type"`
	LengthMs  int64     `json:"length_ms"`
	Bytes     int64     `json:"bytes"`
	CreatedAt time.Time `json:"created_at"`
	// Missing is a track whose file is not there, as after restoring an older backup.
	Missing bool `json:"missing,omitempty"`
}

// Library is the tracks, the newest first, with the bytes the music folder holds and the quota.
type Library struct {
	Tracks     []Track `json:"tracks"`
	UsedBytes  int64   `json:"used_bytes"`
	QuotaBytes int64   `json:"quota_bytes"`
}

// musicFile is a temporary file a track is received into.
type musicFile interface {
	io.Writer
	Sync() error
	Close() error
	Name() string
}

// musicFS is the file operations of the library, which tests replace (row 6e).
type musicFS struct {
	createTemp func(dir, pattern string) (musicFile, error)
	rename     func(from, to string) error
	syncDir    func(dir string) error
	remove     func(path string) error
}

func osMusicFS() musicFS {
	return musicFS{
		createTemp: func(dir, pattern string) (musicFile, error) { return os.CreateTemp(dir, pattern) },
		rename:     os.Rename,
		syncDir:    syncDir,
		remove:     os.Remove,
	}
}

// diskError is err of doing, an ErrDiskLow when the disk or the database was full: ENOSPC from a file, or SQLite's
// SQLITE_FULL.
func diskError(doing string, err error) error {
	if errors.Is(err, syscall.ENOSPC) || sqlite.IsFull(err) {
		return fmt.Errorf("%w: %s: %w", ErrDiskLow, doing, err)
	}

	return fmt.Errorf("%s: %w", doing, err)
}

// CheckMusicName checks a track's name, before its upload is read.
func CheckMusicName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxMusicName || !utf8.ValidString(name) {
		return "", fmt.Errorf("%w: a track's name is 1 to %d characters", ErrInvalid, MaxMusicName)
	}

	return name, nil
}

// UploadMusic receives a track of size bytes from body into the library (row 6a, steps 4-9): one upload at a time;
// refused before the body is read when the library's quota or the free disk cannot hold it; streamed to a temporary
// file, synced, checked (row 6b) and only then renamed and given its row. Whatever fails leaves no file.
func (s *Service) UploadMusic(ctx context.Context, name string, size int64, body io.Reader) (Track, error) {
	name, err := CheckMusicName(name)
	if err != nil {
		return Track{}, err
	}

	if size <= 0 || size > MaxMusicBytes {
		return Track{}, fmt.Errorf("%w: a track is at most %d MB", ErrInvalid, MaxMusicBytes>>20)
	}

	select {
	case s.upload <- struct{}{}:
		defer func() { <-s.upload }()
	default:
		return Track{}, ErrMusicBusy
	}

	used, err := s.musicBytes()
	if err != nil {
		return Track{}, err
	}

	if used+size > MusicQuotaBytes {
		return Track{}, fmt.Errorf("%w: the library holds %d MB of its %d MB; this track is %d MB", ErrDiskLow,
			used/1_000_000, MusicQuotaBytes/1_000_000, size/1_000_000)
	}

	if err := os.MkdirAll(s.musicDir, 0o700); err != nil {
		return Track{}, diskError("making the music folder", err)
	}

	need := minFreeBytes + uint64(size)
	if free, err := s.cfg.FreeBytes(s.musicDir); err == nil && free < need {
		return Track{}, fmt.Errorf("%w: the control plane has %d MB of disk free", ErrDiskLow, free/1_000_000)
	}

	id := platform.NewID("mus")

	part, err := s.receiveMusic(id, size, body)
	if part != "" {
		defer func() { _ = s.fs.remove(part) }()
	}

	if err != nil {
		return Track{}, err
	}

	if free, err := s.cfg.FreeBytes(s.musicDir); err == nil && free < minFreeBytes {
		return Track{}, fmt.Errorf("%w: the control plane has %d MB of disk free after the track", ErrDiskLow,
			free/1_000_000)
	}

	mediaType, ext, length, err := s.checkTrack(ctx, part)
	if err != nil {
		return Track{}, err
	}

	file := id + "." + ext
	final := filepath.Join(s.musicDir, file)

	if err := s.fs.rename(part, final); err != nil {
		return Track{}, diskError("keeping the track", err)
	}

	kept := false

	defer func() {
		if !kept {
			_ = s.fs.remove(final)
		}
	}()

	if err := s.fs.syncDir(s.musicDir); err != nil {
		return Track{}, diskError("keeping the track", err)
	}

	now := s.cfg.Clock.Now()
	if err := store.New(s.db).InsertMusic(ctx, store.InsertMusicParams{
		ID: id, Name: name, File: file, MediaType: mediaType, LengthMs: length, Bytes: size, Now: now.UnixMilli(),
	}); err != nil {
		return Track{}, diskError("recording the track", err)
	}

	kept = true

	return Track{
		ID: id, Name: name, MediaType: mediaType, LengthMs: length, Bytes: size,
		CreatedAt: time.UnixMilli(now.UnixMilli()).UTC(),
	}, nil
}

// receiveMusic streams the body to a temporary file of the music folder, synced and closed; it answers the file's
// path, set as soon as the file is made, so the caller removes it.
func (s *Service) receiveMusic(id string, size int64, body io.Reader) (string, error) {
	tmp, err := s.fs.createTemp(s.musicDir, id+".*"+partSuffix)
	if err != nil {
		return "", diskError("receiving the track", err)
	}

	n, err := io.Copy(tmp, io.LimitReader(body, size+1))
	if err == nil {
		err = tmp.Sync()
	}

	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}

	switch {
	case err != nil:
		return tmp.Name(), diskError("receiving the track", err)
	case n != size:
		return tmp.Name(), fmt.Errorf("%w: the track is %d bytes, not the %d said", ErrInvalid, n, size)
	}

	return tmp.Name(), nil
}

// checkTrack types a received track by ffprobe and decodes it whole (rows 6b and 6c): exactly one audio stream, a
// cover picture aside, of a format the Studio plays; a strict decode; a decoded length of 10 seconds to 20 minutes;
// not silence. It answers the type, the file's extension and the decoded length.
func (s *Service) checkTrack(parent context.Context, file string) (string, string, int64, error) {
	tools, err := s.cfg.Tools(parent)
	if err != nil {
		return "", "", 0, fmt.Errorf("%w: ffmpeg is not installed: %w", ErrUnavailable, err)
	}

	ctx, cancel := context.WithTimeout(parent, musicCheckTimeout)
	defer cancel()

	probe, err := compose.ProbeFile(ctx, tools, file)
	if err != nil {
		if parent.Err() != nil {
			return "", "", 0, parent.Err()
		}

		return "", "", 0, fmt.Errorf("%w: the file is not a track ffmpeg reads: %s", ErrInvalid, firstLine(err))
	}

	mediaType, ext, err := musicType(probe)
	if err != nil {
		return "", "", 0, err
	}

	m, err := compose.Measure(ctx, tools, file, musicDecodeLimitSec)

	switch {
	case parent.Err() != nil:
		return "", "", 0, parent.Err()
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
		return "", "", 0, fmt.Errorf("%w: the track took longer than %s to decode", ErrInvalid, musicCheckTimeout)
	case err != nil:
		return "", "", 0, fmt.Errorf("%w: the track does not decode: %s", ErrInvalid, firstLine(err))
	case m.LengthMs > maxMusicMs:
		return "", "", 0, fmt.Errorf("%w: the track is longer than 20 minutes", ErrInvalid)
	case m.LengthMs < minMusicMs:
		return "", "", 0, fmt.Errorf("%w: the track is %.1f s; a track is at least 10 seconds", ErrInvalid,
			float64(m.LengthMs)/1000)
	case m.Silent():
		return "", "", 0, fmt.Errorf("%w: the track is silent", ErrInvalid)
	}

	return mediaType, ext, m.LengthMs, nil
}

// musicType is a probed track's type and extension: mp3; m4a or mp4 with AAC; wav with PCM; flac; ogg with Vorbis or
// Opus. Anything else, ALAC among them, is refused.
func musicType(p compose.Probe) (string, string, error) {
	var audio []compose.Stream

	for _, st := range p.Streams {
		switch {
		case st.Type == "audio":
			audio = append(audio, st)
		case st.Type == "video" && st.AttachedPicture:
		default:
			return "", "", fmt.Errorf("%w: a track holds one audio stream and nothing else; this holds a %s stream",
				ErrInvalid, st.Type)
		}
	}

	if len(audio) != 1 {
		return "", "", fmt.Errorf("%w: a track holds exactly one audio stream; this holds %d", ErrInvalid, len(audio))
	}

	codec := audio[0].Codec
	formats := strings.Split(p.Format, ",")
	is := func(format string) bool {
		for _, f := range formats {
			if f == format {
				return true
			}
		}

		return false
	}

	switch {
	case is("mp3") && codec == "mp3":
		return "audio/mpeg", "mp3", nil
	case (is("mp4") || is("m4a")) && codec == "aac":
		return "audio/mp4", "m4a", nil
	case is("wav") && strings.HasPrefix(codec, "pcm_"):
		return "audio/wav", "wav", nil
	case is("flac") && codec == "flac":
		return "audio/flac", "flac", nil
	case is("ogg") && (codec == "vorbis" || codec == "opus"):
		return "audio/ogg", "ogg", nil
	}

	return "", "", fmt.Errorf("%w: a track is mp3, m4a with AAC, wav, flac, or ogg with Vorbis or Opus; "+
		"this is %s with %s", ErrInvalid, p.Format, codec)
}

// musicBytes is what the music folder's tracks hold: its files, a file whose remove failed among them until the
// sweep at start removes it; what is being received is not counted.
func (s *Service) musicBytes() (int64, error) {
	entries, err := os.ReadDir(s.musicDir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}

	if err != nil {
		return 0, err
	}

	var n int64

	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasSuffix(e.Name(), partSuffix) {
			continue
		}

		info, err := e.Info()
		if err != nil {
			continue
		}

		n += info.Size()
	}

	return n, nil
}

// Music is the library.
func (s *Service) Music(ctx context.Context) (Library, error) {
	rows, err := store.New(s.db).ListMusic(ctx)
	if err != nil {
		return Library{}, err
	}

	used, err := s.musicBytes()
	if err != nil {
		return Library{}, err
	}

	out := Library{Tracks: make([]Track, 0, len(rows)), UsedBytes: used, QuotaBytes: MusicQuotaBytes}

	for _, r := range rows {
		_, statErr := os.Stat(filepath.Join(s.musicDir, r.File))
		out.Tracks = append(out.Tracks, Track{
			ID: r.ID, Name: r.Name, MediaType: r.MediaType, LengthMs: r.LengthMs, Bytes: r.Bytes,
			CreatedAt: time.UnixMilli(r.CreatedAt).UTC(), Missing: statErr != nil,
		})
	}

	return out, nil
}

// MusicAudio is a track's file and the type it is served as.
func (s *Service) MusicAudio(ctx context.Context, id string) (string, string, error) {
	if !platform.IsID("mus", id) {
		return "", "", fmt.Errorf("%w: music track %s", ErrNotFound, id)
	}

	row, err := store.New(s.db).GetMusic(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: music track %s", ErrNotFound, id)
	}

	if err != nil {
		return "", "", err
	}

	path := filepath.Join(s.musicDir, row.File)
	if _, err := os.Stat(path); err != nil {
		return "", "", fmt.Errorf("%w: the file of music track %s is missing", ErrNotFound, id)
	}

	return path, row.MediaType, nil
}

// DeleteMusic deletes a track (row 6d): refused while a revision queued or being made names it; the default's music
// becomes none only when it names this track. Its file is removed after the commit; a remove that fails is counted in
// the quota until the sweep at the next start.
func (s *Service) DeleteMusic(ctx context.Context, id string) error {
	if !platform.IsID("mus", id) {
		return fmt.Errorf("%w: music track %s", ErrNotFound, id)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var file string

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetMusic(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: music track %s", ErrNotFound, id)
		}

		if err != nil {
			return err
		}

		n, err := q.CountMakingWithMusic(ctx, id)
		if err != nil {
			return err
		}

		if n > 0 {
			return fmt.Errorf("%w: a revision being made has music track %s; delete it once it is made", ErrConflict, id)
		}

		if _, err := q.DeleteMusic(ctx, id); err != nil {
			return err
		}

		saved, err := s.savedLook(ctx, q)
		if err != nil {
			return err
		}

		if saved.Music != nil && saved.Music.Track == id {
			saved.Music = nil
			if err := q.SetVideoSettings(ctx, store.SetVideoSettingsParams{Look: saved.encode(), Now: now}); err != nil {
				return err
			}
		}

		file = row.File

		return nil
	})
	if err != nil {
		return err
	}

	if err := s.fs.remove(filepath.Join(s.musicDir, file)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.cfg.Logger.WarnContext(ctx, "a deleted track's file was not removed; the next start removes it",
			slog.String("event", "videos.music_remove_failed"), slog.String("track", id),
			slog.String("error", err.Error()))
	}

	return nil
}

// sweepMusic removes from the music folder what a crash left half-written and the files with no row, and logs the
// rows with no file (row 6d). It runs at start, before the API and the runner.
func (s *Service) sweepMusic(ctx context.Context) error {
	entries, err := os.ReadDir(s.musicDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("reading the music folder: %w", err)
	}

	rows, err := store.New(s.db).ListMusic(ctx)
	if err != nil {
		return fmt.Errorf("reading the music library: %w", err)
	}

	files := make(map[string]bool, len(rows))
	for _, r := range rows {
		files[r.File] = true
	}

	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || files[name] {
			continue
		}

		if err := os.Remove(filepath.Join(s.musicDir, name)); err != nil {
			return fmt.Errorf("removing %s from the music folder: %w", name, err)
		}

		if !strings.HasSuffix(name, partSuffix) {
			s.cfg.Logger.InfoContext(ctx, "a music file with no track was removed",
				slog.String("event", "videos.music_swept"), slog.String("file", name))
		}
	}

	for _, r := range rows {
		if _, err := os.Stat(filepath.Join(s.musicDir, r.File)); err != nil {
			s.cfg.Logger.WarnContext(ctx, "a music track's file is missing", slog.String("event", "videos.music_missing"),
				slog.String("track", r.ID), slog.String("file", r.File))
		}
	}

	return nil
}

func firstLine(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")

	return line
}
