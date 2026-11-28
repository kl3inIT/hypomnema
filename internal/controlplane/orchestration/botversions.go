package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// Bot versions (issue #1336): each save of a Bot's settings keeps a version with what it set, who saved it and when,
// as a profile's apply keeps one (issue #171) and Paperclip keeps an agent's config revisions with who made each
// (packages/db/src/schema/agent_config_revisions.ts: createdByUserId, createdByAgentId, source,
// rolledBackFromRevisionId). A restore saves a version's settings again, as Paperclip's rollback does
// (server/src/services/agents.ts, rollbackConfigRevision). The Bot's name and memory are not part of a version, as a
// profile's memory is not part of its versions.

// How a Bot version was saved.
const (
	BotVersionSave    = "save"
	BotVersionRestore = "restore"
	BotVersionUndo    = "undo"
	// BotVersionBefore is what a Bot had when its first save after versions began (inferred: Paperclip keeps the
	// config before each change in beforeConfig; one version here holds it once).
	BotVersionBefore = "before_versions"
)

// Who saved a Bot version.
const (
	SavedByOwner   = "owner"
	SavedByMainBot = "main_bot"
)

// BotVersion is one save of a Bot's settings.
type BotVersion struct {
	ID      string
	Version int
	// Bot is the Bot with the settings the version kept; its name and memory are not part of them.
	Bot    Bot
	Source string
	// SavedBy is SavedByOwner or SavedByMainBot, with ChatID the chat the main Bot saved it from; empty for
	// BotVersionBefore.
	SavedBy      string
	ChatID       string
	RestoredFrom string
	CreatedAt    time.Time
}

// botSave says how a save of a Bot's settings is kept as a version.
type botSave struct {
	source       string
	restoredFrom string
}

// keepBotVersion keeps a version of a Bot's settings as just saved. A Bot with no version yet, saved before
// versions began, first keeps what it had (had), so a restore can bring it back.
func (s *Service) keepBotVersion(ctx context.Context, q *store.Queries, now int64, chat string, had *Bot, b Bot,
	save botSave,
) error {
	if had != nil {
		n, err := q.CountBotVersions(ctx, b.ID)
		if err != nil {
			return fmt.Errorf("reading the versions of bot %s: %w", b.ID, err)
		}

		if n == 0 {
			if err := s.insertBotVersion(ctx, q, now, "", *had, botSave{source: BotVersionBefore}); err != nil {
				return err
			}
		}
	}

	return s.insertBotVersion(ctx, q, now, chat, b, save)
}

func (s *Service) insertBotVersion(ctx context.Context, q *store.Queries, now int64, chat string, b Bot,
	save botSave,
) error {
	settings := settingsOfBot(b)
	settings.Name = ""

	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("encoding the settings of bot %s: %w", b.ID, err)
	}

	savedBy := sql.NullString{}

	switch {
	case save.source == BotVersionBefore:
	case chat == "":
		savedBy = nullString(SavedByOwner)
	default:
		savedBy = nullString(SavedByMainBot)
	}

	if err := q.InsertBotVersion(ctx, store.InsertBotVersionParams{
		ID: s.newID("bov"), BotID: b.ID, Settings: string(raw), Source: save.source, SavedBy: savedBy,
		ChatID: nullString(chat), RestoredFrom: nullString(save.restoredFrom), Now: now,
	}); err != nil {
		return fmt.Errorf("keeping a version of bot %s: %w", b.ID, err)
	}

	return nil
}

// ListBotVersions returns a Bot's versions, newest first.
func (s *Service) ListBotVersions(ctx context.Context, id string) ([]BotVersion, error) {
	q := s.read()

	if _, err := q.GetBot(ctx, id); err != nil {
		return nil, notFound(err, "bot", id)
	}

	rows, err := q.ListBotVersions(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("listing the versions of bot %s: %w", id, err)
	}

	out := make([]BotVersion, 0, len(rows))

	for _, r := range rows {
		v, err := botVersionFromRow(r)
		if err != nil {
			return nil, err
		}

		out = append(out, v)
	}

	return out, nil
}

func botVersionFromRow(r store.BotVersion) (BotVersion, error) {
	var settings botSettings
	if err := json.Unmarshal([]byte(r.Settings), &settings); err != nil {
		return BotVersion{}, fmt.Errorf("reading version %s of bot %s: %w", r.ID, r.BotID, err)
	}

	b := settings.bot(r.BotID)
	b.Name = ""

	if b.ToolDisplay == "" {
		b.ToolDisplay = ToolsOn
	}

	return BotVersion{
		ID: r.ID, Version: int(r.Version), Bot: b, Source: r.Source, SavedBy: r.SavedBy.String, ChatID: r.ChatID.String,
		RestoredFrom: r.RestoredFrom.String, CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
	}, nil
}

// RestoreBotVersion saves one of a Bot's versions again, as PutBot saves settings, keeping a new version restored
// from it. Its name and memory stay. What the version names that is gone or no longer served since is refused, as
// PutBot refuses it, and changes nothing.
func (s *Service) RestoreBotVersion(ctx context.Context, id, versionID string) (Bot, error) {
	return s.restoreBotVersion(ctx, "", id, versionID)
}

// RestoreBotVersionFromChat is RestoreBotVersion by the main Bot from its chat, recorded with what undoes it. What
// the Bot may do with the owner's accounts stays as it is: only the owner changes it (issue #1023).
func (s *Service) RestoreBotVersionFromChat(ctx context.Context, chat, id, versionID string) (Bot, error) {
	if chat == "" {
		return Bot{}, fmt.Errorf("%w: a bot's change is made from a chat", ErrInvalid)
	}

	return s.restoreBotVersion(ctx, chat, id, versionID)
}

func (s *Service) restoreBotVersion(ctx context.Context, chat, id, versionID string) (Bot, error) {
	row, err := s.read().GetBotVersion(ctx, store.GetBotVersionParams{ID: versionID, BotID: id})
	if err != nil {
		return Bot{}, notFound(err, "bot version", versionID)
	}

	v, err := botVersionFromRow(row)
	if err != nil {
		return Bot{}, err
	}

	b := v.Bot
	b.ID = id

	if chat != "" {
		current, err := s.GetBot(ctx, id)
		if err != nil {
			return Bot{}, err
		}

		b.OwnerAccounts = current.OwnerAccounts
	}

	return s.saveBot(ctx, chat, b, botSave{source: BotVersionRestore, restoredFrom: versionID})
}
