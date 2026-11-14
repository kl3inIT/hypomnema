package orchestration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Setup changes (issue #169, part 3): the Bot coordinator alone makes,
// changes and removes Bots and worker profiles besides the owner, from its
// chat, with the operations the owner uses. A change applies at once and
// leaves a receipt in the chat with Undo, after OpenMausBot
// (docs/approval-levels.md L111-114: a team setup change applies at once and
// leaves a one-line receipt with Undo; server/chief-of-staff.ts L21: the
// Chief adds a bot without asking and says so). An undo is refused once what
// it would undo was changed or used since (server/team-setup-requests.ts
// L393-425). Who may make a change is the client API's to decide, from the
// caller's token; this package checks that the chat talks with the Bot
// coordinator and records the change.

// Kinds of what a setup change is about, and what was done.
const (
	changeBot     = "bot"
	changeProfile = "profile"

	actionMade    = "made"
	actionChanged = "changed"
	actionRemoved = "removed"
)

// SetupChange is a change of a Bot or a profile the Bot coordinator made from
// a chat.
type SetupChange struct {
	ID     string
	ChatID string
	// Kind is bot or profile; Action made, changed or removed.
	Kind   string
	Name   string
	Action string
	// CanUndo says an undo would be taken now; Problem says why not.
	CanUndo   bool
	Problem   string
	CreatedAt time.Time
	UndoneAt  *time.Time
}

// botSettings is what a Bot was before a change: everything a change sets.
// Its memory is not: a change keeps it, and a Bot with a memory is not
// removed.
type botSettings struct {
	Provider    string    `json:"provider,omitempty"`
	Model       string    `json:"model,omitempty"`
	Effort      string    `json:"effort,omitempty"`
	Persona     string    `json:"persona,omitempty"`
	Skills      []string  `json:"skills,omitempty"`
	SkillsOff   []string  `json:"skills_off,omitempty"`
	Connections AllowList `json:"connections"`
	Workers     AllowList `json:"workers"`
	// ToolDisplay is empty for ToolsOn, so a Bot's revision from before it was a setting stays the same.
	ToolDisplay ToolDisplay `json:"tool_display,omitempty"`
}

func settingsOfBot(b Bot) botSettings {
	display := b.ToolDisplay
	if display == ToolsOn {
		display = ""
	}

	return botSettings{
		Provider: b.Provider, Model: b.Model, Effort: b.Effort, Persona: b.Persona, Skills: b.Skills,
		SkillsOff: b.SkillsOff, Connections: b.Connections, Workers: b.Workers, ToolDisplay: display,
	}
}

func (b botSettings) bot(name string) Bot {
	return Bot{
		Name: name, Provider: b.Provider, Model: b.Model, Effort: b.Effort, Persona: b.Persona, Skills: b.Skills,
		SkillsOff: b.SkillsOff, Connections: b.Connections, Workers: b.Workers, ToolDisplay: b.ToolDisplay,
	}
}

// botRevision names a Bot's settings, as OpenMausBot's teamSetupRevision: a
// change of any of them since makes an undo stale; its memory does not.
func botRevision(b Bot) string {
	raw, err := json.Marshal(settingsOfBot(b))
	if err != nil {
		panic(fmt.Sprintf("encoding a bot's settings: %v", err))
	}

	sum := sha256.Sum256(raw)

	return hex.EncodeToString(sum[:])
}

// changeFromChat checks the chat a setup change is made from: it exists and
// talks with the Bot coordinator. No chat is the owner's change.
func changeFromChat(ctx context.Context, q *store.Queries, chat string) error {
	if chat == "" {
		return nil
	}

	row, err := q.GetChat(ctx, chat)
	if err != nil {
		return notFound(err, "chat", chat)
	}

	if row.Bot != DefaultBot {
		return fmt.Errorf("%w: only the bot %s makes and changes bots and profiles, and chat %s talks with %s",
			ErrForbidden, DefaultBot, chat, row.Bot)
	}

	return nil
}

func (s *Service) recordChange(ctx context.Context, q *store.Queries, now int64, chat, kind, name, action string,
	before any, after string,
) error {
	var stored sql.NullString

	if before != nil {
		raw, err := json.Marshal(before)
		if err != nil {
			return fmt.Errorf("recording what undoes the change: %w", err)
		}

		stored = sql.NullString{String: string(raw), Valid: true}
	}

	return q.InsertSetupChange(ctx, store.InsertSetupChangeParams{
		ID: s.newID("chg"), ChatID: chat, Kind: kind, Name: name, Action: action, Before: stored, After: after,
		Now: now,
	})
}

// PutBotFromChat is PutBot by the Bot coordinator from its chat.
func (s *Service) PutBotFromChat(ctx context.Context, chat string, b Bot) (Bot, error) {
	if chat == "" {
		return Bot{}, fmt.Errorf("%w: a bot's change is made from a chat", ErrInvalid)
	}

	return s.putBot(ctx, chat, b)
}

// DeleteBotFromChat is DeleteBot by the Bot coordinator from its chat.
func (s *Service) DeleteBotFromChat(ctx context.Context, chat, name string) error {
	if chat == "" {
		return fmt.Errorf("%w: a bot's change is made from a chat", ErrInvalid)
	}

	return s.deleteBot(ctx, chat, name)
}

// ApplyProfileFromChat is ApplyProfile by the Bot coordinator from its chat.
func (s *Service) ApplyProfileFromChat(ctx context.Context, chat, name, source string) (Profile, bool, error) {
	if chat == "" {
		return Profile{}, false, fmt.Errorf("%w: a bot's change is made from a chat", ErrInvalid)
	}

	return s.applyProfile(ctx, chat, name, source)
}

// ListSetupChanges returns the changes made from a chat, the oldest first,
// each saying whether it can be undone now.
func (s *Service) ListSetupChanges(ctx context.Context, chat string) ([]SetupChange, error) {
	q := s.read()

	rows, err := q.ListSetupChangesOfChat(ctx, chat)
	if err != nil {
		return nil, fmt.Errorf("listing the setup changes of chat %s: %w", chat, err)
	}

	out := make([]SetupChange, 0, len(rows))

	for _, r := range rows {
		c, err := setupChange(ctx, q, r)
		if err != nil {
			return nil, err
		}

		out = append(out, c)
	}

	return out, nil
}

// GetSetupChange returns one change.
func (s *Service) GetSetupChange(ctx context.Context, id string) (SetupChange, error) {
	q := s.read()

	row, err := q.GetSetupChange(ctx, id)
	if err != nil {
		return SetupChange{}, notFound(err, "setup change", id)
	}

	return setupChange(ctx, q, row)
}

func setupChange(ctx context.Context, q *store.Queries, r store.SetupChange) (SetupChange, error) {
	c := SetupChange{
		ID: r.ID, ChatID: r.ChatID, Kind: r.Kind, Name: r.Name, Action: r.Action, CreatedAt: millis(r.CreatedAt),
		UndoneAt: optionalTime(r.UndoneAt),
	}

	err := undoProblem(ctx, q, r)

	switch {
	case err == nil:
		c.CanUndo = true
	case errors.Is(err, ErrConflict):
		c.Problem = strings.TrimPrefix(err.Error(), ErrConflict.Error()+": ")
	default:
		return SetupChange{}, err
	}

	return c, nil
}

// undoStale is OpenMausBot's UNDO_STALE, in this repository's words.
const undoStale = "was changed or used since; change it by hand"

// undoProblem says why a change cannot be undone now, as a conflict, or nil
// when it can.
func undoProblem(ctx context.Context, q *store.Queries, r store.SetupChange) error {
	if r.UndoneAt.Valid {
		return fmt.Errorf("%w: this change was undone", ErrConflict)
	}

	last, err := q.LastSetupChangeOf(ctx, store.LastSetupChangeOfParams{Kind: r.Kind, Name: r.Name})
	if err != nil {
		return fmt.Errorf("reading the last change of %s %s: %w", r.Kind, r.Name, err)
	}

	if last.ID != r.ID {
		return fmt.Errorf("%w: %s %s %s", ErrConflict, r.Kind, r.Name, undoStale)
	}

	if r.Kind == changeBot {
		return botUndoProblem(ctx, q, r)
	}

	return profileUndoProblem(ctx, q, r)
}

func botUndoProblem(ctx context.Context, q *store.Queries, r store.SetupChange) error {
	row, err := q.GetBot(ctx, r.Name)

	switch {
	case errors.Is(err, sql.ErrNoRows) && r.Action == actionRemoved:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: bot %s was removed since", ErrConflict, r.Name)
	case err != nil:
		return notFound(err, "bot", r.Name)
	case r.Action == actionRemoved:
		return fmt.Errorf("%w: a bot named %s was made again since", ErrConflict, r.Name)
	case botRevision(botFromRow(row)) != r.After:
		return fmt.Errorf("%w: bot %s %s", ErrConflict, r.Name, undoStale)
	}

	// Used: it answered in a chat since, other than the one the change was made in, where the Bot coordinator
	// goes on in the same turn (OpenMausBot: a created bot whose thread a person wrote in).
	used, err := q.CountRepliesOfBotSince(ctx, store.CountRepliesOfBotSinceParams{
		Bot: r.Name, Since: r.CreatedAt, ChatID: r.ChatID,
	})
	if err != nil {
		return fmt.Errorf("reading the use of bot %s: %w", r.Name, err)
	}

	if used > 0 {
		return fmt.Errorf("%w: bot %s %s", ErrConflict, r.Name, undoStale)
	}

	return nil
}

func profileUndoProblem(ctx context.Context, q *store.Queries, r store.SetupChange) error {
	row, err := q.GetProfile(ctx, r.Name)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: profile %s was removed since", ErrConflict, r.Name)
	case err != nil:
		return notFound(err, "profile", r.Name)
	case row.CurrentVersionID != r.After:
		return fmt.Errorf("%w: profile %s %s", ErrConflict, r.Name, undoStale)
	}

	// Used: a Task was given to it since, or a worker ran the version the change made, also for a Task given
	// before the change or as a backup.
	tasks, err := q.CountTasksOfProfileSince(ctx, store.CountTasksOfProfileSinceParams{
		Profile: r.Name, Since: r.CreatedAt,
	})
	if err != nil {
		return fmt.Errorf("reading the use of profile %s: %w", r.Name, err)
	}

	ran, err := q.CountDispatchesOfProfileVersion(ctx, nullString(r.After))
	if err != nil {
		return fmt.Errorf("reading the use of profile %s: %w", r.Name, err)
	}

	if tasks > 0 || ran > 0 {
		return fmt.Errorf("%w: profile %s %s", ErrConflict, r.Name, undoStale)
	}

	if r.Action != actionMade {
		return nil
	}

	named, err := q.CountFallbacksNaming(ctx, r.Name)
	if err != nil {
		return fmt.Errorf("reading the backups naming profile %s: %w", r.Name, err)
	}

	if named > 0 {
		return fmt.Errorf("%w: %d profiles name %s as a backup; change them first", ErrConflict, named, r.Name)
	}

	return nil
}

// UndoSetupChange undoes a change of the Bot coordinator: a Bot or profile it
// made is removed, one it changed is what it was before, a Bot it removed is
// back. A change whose thing was changed or used since is not undone.
func (s *Service) UndoSetupChange(ctx context.Context, id string) (SetupChange, error) {
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		r, err := q.GetSetupChange(ctx, id)
		if err != nil {
			return notFound(err, "setup change", id)
		}

		if err := undoProblem(ctx, q, r); err != nil {
			return err
		}

		if r.Kind == changeBot {
			err = s.undoBot(ctx, q, now, r)
		} else {
			err = s.undoProfile(ctx, q, now, r)
		}

		if err != nil {
			return err
		}

		return q.MarkSetupChangeUndone(ctx, store.MarkSetupChangeUndoneParams{
			ID: id, Now: sql.NullInt64{Int64: now, Valid: true},
		})
	})
	if err != nil {
		return SetupChange{}, fmt.Errorf("undoing setup change %s: %w", id, err)
	}

	return s.GetSetupChange(ctx, id)
}

func (s *Service) undoBot(ctx context.Context, q *store.Queries, now int64, r store.SetupChange) error {
	if r.Action == actionMade {
		row, err := q.GetBot(ctx, r.Name)
		if err != nil {
			return notFound(err, "bot", r.Name)
		}

		if err := botRemovable(ctx, q, row); err != nil {
			return err
		}

		return q.DeleteBot(ctx, r.Name)
	}

	var before botSettings
	if err := json.Unmarshal([]byte(r.Before.String), &before); err != nil {
		return fmt.Errorf("reading what bot %s was before: %w", r.Name, err)
	}

	b := before.bot(r.Name)

	// What it was may no longer be served or exist: say so rather than put back a Bot that cannot run.
	provider, err := checkModel(ctx, q, botRuntime, b.Provider, b.Model, b.Effort)
	if err != nil {
		return err
	}

	b.Provider = provider

	if err := checkSkills(ctx, q, b.Skills, b.SkillsOff); err != nil {
		return err
	}

	return upsertBot(ctx, q, now, b)
}

func (s *Service) undoProfile(ctx context.Context, q *store.Queries, now int64, r store.SetupChange) error {
	if r.Action == actionMade {
		if err := q.DeleteProfileVersions(ctx, r.Name); err != nil {
			return fmt.Errorf("removing the versions of profile %s: %w", r.Name, err)
		}

		return q.DeleteProfile(ctx, r.Name)
	}

	var beforeID string
	if err := json.Unmarshal([]byte(r.Before.String), &beforeID); err != nil {
		return fmt.Errorf("reading what profile %s was before: %w", r.Name, err)
	}

	// Versions never change and their numbers only rise: what it was before comes back as a new version.
	before, err := q.GetProfileVersion(ctx, beforeID)
	if err != nil {
		return notFound(err, "profile version", beforeID)
	}

	current, err := q.CurrentProfileVersion(ctx, r.Name)
	if err != nil {
		return notFound(err, "profile", r.Name)
	}

	if _, err := checkModel(ctx, q, before.Runtime, before.Provider.String, before.Model.String,
		before.Effort.String); err != nil {
		return fmt.Errorf("profile %s as it was: %w", r.Name, err)
	}

	// A backup it named may have gone since: the scheduler would not find it.
	for _, name := range splitNames(before.Fallbacks) {
		if _, err := q.GetProfile(ctx, name); err != nil {
			return fmt.Errorf("%w: profile %s as it was names the backup %s, which is gone; change it by hand",
				ErrConflict, r.Name, name)
		}
	}

	id := s.newID("pfv")

	if err := q.SetProfileVersion(ctx, store.SetProfileVersionParams{Name: r.Name, VersionID: id, Now: now}); err != nil {
		return fmt.Errorf("pointing profile %s at what it was: %w", r.Name, err)
	}

	return q.InsertProfileVersion(ctx, store.InsertProfileVersionParams{
		ID: id, Profile: r.Name, Version: current.Version + 1, Source: before.Source, Digest: before.Digest,
		Description: before.Description, Runtime: before.Runtime, Provider: before.Provider, Model: before.Model,
		Effort: before.Effort, Instruction: before.Instruction, Fallbacks: before.Fallbacks, Skills: before.Skills,
		SkillsOff: before.SkillsOff, Now: now,
	})
}
