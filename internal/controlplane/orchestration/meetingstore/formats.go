package meetingstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// The owner's notes formats (issue #747): a name and its sections, each a title and a short instruction, as
// Hyprnote's templates are a title and sections of a title and a description, and Meetily's custom templates a name
// and sections of a title and an instruction. The built-in formats are the meetings package's.

// Format is a notes format: a list of sections fed to the prompt.
type Format struct {
	ID       string
	Name     string
	Sections []FormatSection
	// Builtin is a format the control plane ships, which the owner copies rather than changes.
	Builtin   bool
	UpdatedAt time.Time
}

// FormatSection is one section a format adds to the notes.
type FormatSection struct {
	Title string `json:"title"`
	// About says what the section holds, as Hyprnote's description and Meetily's instruction.
	About string `json:"about"`
}

// Limits of a format the owner makes.
const (
	MaxFormats       = 100
	MaxFormatName    = 100
	MaxSections      = 20
	MaxSectionTitle  = 100
	MaxSectionAbout  = 500
	formatIDPrefix   = "mtf"
	errBuiltinFormat = "a built-in format is not changed; copy it to make your own"
)

// Formats returns the owner's formats, the oldest first.
func (s *Store) Formats(ctx context.Context) ([]Format, error) {
	rows, err := s.read().ListMeetingFormats(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the notes formats: %w", err)
	}

	out := make([]Format, 0, len(rows))
	for _, r := range rows {
		out = append(out, formatOf(r))
	}

	return out, nil
}

// Format returns one of the owner's formats.
func (s *Store) Format(ctx context.Context, id string) (Format, error) {
	row, err := s.read().GetMeetingFormat(ctx, id)
	if err != nil {
		return Format{}, storeutil.NotFound(err, "notes format", id)
	}

	return formatOf(row), nil
}

// IsFormatID says whether an id is one the owner's formats are given.
func IsFormatID(id string) bool { return strings.HasPrefix(id, formatIDPrefix+"_") }

// CreateFormat keeps a format the owner made.
func (s *Store) CreateFormat(ctx context.Context, name string, sections []FormatSection) (Format, error) {
	name, body, err := checkFormat(name, sections)
	if err != nil {
		return Format{}, err
	}

	id := s.newID(formatIDPrefix)

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.CountMeetingFormats(ctx)
		if err != nil {
			return err
		}

		if n >= MaxFormats {
			return fmt.Errorf("%w: at most %d formats of your own", storeerr.ErrInvalid, MaxFormats)
		}

		return q.InsertMeetingFormat(ctx, store.InsertMeetingFormatParams{ID: id, Name: name, Sections: body, Now: now})
	}); err != nil {
		return Format{}, err
	}

	return s.Format(ctx, id)
}

// UpdateFormat changes a format the owner made.
func (s *Store) UpdateFormat(ctx context.Context, id, name string, sections []FormatSection) (Format, error) {
	if !IsFormatID(id) {
		return Format{}, fmt.Errorf("%w: %s", storeerr.ErrInvalid, errBuiltinFormat)
	}

	name, body, err := checkFormat(name, sections)
	if err != nil {
		return Format{}, err
	}

	if err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.UpdateMeetingFormat(ctx, store.UpdateMeetingFormatParams{
			Name: name, Sections: body, Now: now, ID: id,
		})
		if err != nil {
			return err
		}

		if n == 0 {
			return fmt.Errorf("%w: notes format %s", storeerr.ErrNotFound, id)
		}

		return nil
	}); err != nil {
		return Format{}, err
	}

	return s.Format(ctx, id)
}

// DeleteFormat removes a format the owner made. Notes written in it keep their sections; written again, they are
// in the default format.
func (s *Store) DeleteFormat(ctx context.Context, id string) error {
	if !IsFormatID(id) {
		return fmt.Errorf("%w: %s", storeerr.ErrInvalid, errBuiltinFormat)
	}

	return s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.DeleteMeetingFormat(ctx, id)
		if err != nil {
			return err
		}

		if n == 0 {
			return fmt.Errorf("%w: notes format %s", storeerr.ErrNotFound, id)
		}

		return nil
	})
}

// checkFormat trims a format's name and sections, refuses what is past the limits, and encodes the sections.
func checkFormat(name string, sections []FormatSection) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", fmt.Errorf("%w: a format has a name", storeerr.ErrInvalid)
	}

	if err := checkText(name, MaxFormatName, "a format's name"); err != nil {
		return "", "", err
	}

	clean := make([]FormatSection, 0, len(sections))

	for _, sec := range sections {
		sec.Title, sec.About = strings.TrimSpace(sec.Title), strings.TrimSpace(sec.About)
		if sec.Title == "" && sec.About == "" {
			continue
		}

		if sec.Title == "" {
			return "", "", fmt.Errorf("%w: every section has a title", storeerr.ErrInvalid)
		}

		if err := checkText(sec.Title, MaxSectionTitle, "a section's title"); err != nil {
			return "", "", err
		}

		if err := checkParagraph(sec.About, MaxSectionAbout, "a section's instruction"); err != nil {
			return "", "", err
		}

		clean = append(clean, sec)
	}

	switch {
	case len(clean) == 0:
		return "", "", fmt.Errorf("%w: a format has at least one section", storeerr.ErrInvalid)
	case len(clean) > MaxSections:
		return "", "", fmt.Errorf("%w: a format has at most %d sections", storeerr.ErrInvalid, MaxSections)
	}

	body, err := json.Marshal(clean)
	if err != nil {
		return "", "", fmt.Errorf("encoding the sections: %w", err)
	}

	return name, string(body), nil
}

func formatOf(r store.MeetingFormat) Format {
	var sections []FormatSection
	_ = json.Unmarshal([]byte(r.Sections), &sections)

	return Format{ID: r.ID, Name: r.Name, Sections: sections, UpdatedAt: storeutil.Millis(r.UpdatedAt)}
}
