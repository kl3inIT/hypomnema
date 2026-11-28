package clientapi

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Bots (issue #169). Every token reads them and changes a memory: pi's
// launcher reads its Bot's instructions when it starts, and pi writes its memory.
// A pi's own token (issue #260) reads and changes its own Bot only, so one
// Bot never reads another's instructions or memory. The owner makes, changes and
// removes a Bot, and the main Bot from its chat (setupchanges.go). A Bot is named by its id, which never changes;
// its name is only shown (issue #466).

// ListBots returns every Bot, with the removed ones by name; a pi's token is shown its own, the main Bot's
// every one.
func (s *Server) ListBots(ctx context.Context, _ openapi.ListBotsRequestObject,
) (openapi.ListBotsResponseObject, error) {
	bots, err := s.svc.ListBots(ctx)
	if err != nil {
		return nil, err
	}

	tok := principal(ctx)

	removed, err := s.svc.ListRemovedBots(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListBots200JSONResponse{
		Data: make([]openapi.Bot, 0, len(bots)), Removed: make([]openapi.RemovedBot, 0, len(removed)),
	}
	for _, b := range bots {
		if botReadable(tok, b.ID) == nil {
			out.Data = append(out.Data, toBot(b))
		}
	}

	// The removed Bots by name (issue #1139), to the same tokens that read every Bot.
	for _, b := range removed {
		if botReadable(tok, b.ID) == nil {
			out.Removed = append(out.Removed, openapi.RemovedBot{Id: b.ID, Name: b.Name})
		}
	}

	return out, nil
}

// GetBot returns a Bot.
func (s *Server) GetBot(ctx context.Context, req openapi.GetBotRequestObject) (openapi.GetBotResponseObject, error) {
	if err := botReadable(principal(ctx), req.BotId); err != nil {
		return nil, err
	}

	b, err := s.svc.GetBot(ctx, req.BotId)
	if err != nil {
		return nil, err
	}

	out := toBot(b)
	out.ToolAccess, err = s.toolAccess(ctx, &b, "", b.ToolSwitches())
	if err != nil {
		return nil, err
	}

	return openapi.GetBot200JSONResponse(out), nil
}

// CreateBot makes a Bot with a new id and the name given.
func (s *Server) CreateBot(ctx context.Context, req openapi.CreateBotRequestObject,
) (openapi.CreateBotResponseObject, error) {
	chat, err := setupChat(principal(ctx))
	if err != nil {
		return nil, err
	}

	settings := openapi.BotSettings{}
	if req.Body.Settings != nil {
		settings = *req.Body.Settings
	}

	bot := withSettings(orchestration.Bot{Name: req.Body.Name}, settings)

	var b orchestration.Bot
	if chat == "" {
		b, err = s.svc.PutBot(ctx, bot)
	} else {
		b, err = s.svc.PutBotFromChat(ctx, chat, bot)
	}

	if err != nil {
		return nil, err
	}

	return openapi.CreateBot201JSONResponse(toBot(b)), nil
}

// PutBot changes a Bot's settings; what is left out is kept.
func (s *Server) PutBot(ctx context.Context, req openapi.PutBotRequestObject) (openapi.PutBotResponseObject, error) {
	chat, err := setupChat(principal(ctx))
	if err != nil {
		return nil, err
	}

	had, err := s.svc.GetBot(ctx, req.BotId)
	if err != nil {
		return nil, err
	}

	bot := withSettings(had, *req.Body)

	var b orchestration.Bot
	if chat == "" {
		b, err = s.svc.PutBot(ctx, bot)
	} else {
		b, err = s.svc.PutBotFromChat(ctx, chat, bot)
	}

	if err != nil {
		return nil, err
	}

	return openapi.PutBot200JSONResponse(toBot(b)), nil
}

// withSettings is a Bot with the settings given: its model and instructions as given, and its skills, lists and
// tool display as given or, left out, as it had them, so that a client that does not know them does not take them
// away. persona, the instructions' name until 2026-11-28, is read for one release when instructions is left out
// (issue #1336).
func withSettings(had orchestration.Bot, in openapi.BotSettings) orchestration.Bot {
	bot := had
	bot.Provider, bot.Model, bot.Effort = val(in.Provider), val(in.Model), val(in.Effort)

	bot.Instructions = val(in.Persona)
	if in.Instructions != nil {
		bot.Instructions = *in.Instructions
	}

	if in.Fallbacks != nil {
		bot.Fallbacks = *in.Fallbacks
	}

	if in.Skills != nil {
		bot.Skills = *in.Skills
	}

	if in.SkillsOff != nil {
		bot.SkillsOff = *in.SkillsOff
	}

	if in.Tools != nil {
		bot.Tools = *in.Tools
	}

	if in.Preset != nil {
		bot.Preset = string(*in.Preset)
	}

	if in.ToolsOff != nil {
		bot.ToolsOff = *in.ToolsOff
	}

	if l := in.Connections; l != nil {
		bot.Connections = fromAllowList(*l)
	}

	if l := in.Workers; l != nil {
		bot.Workers = fromAllowList(*l)
	}

	if sc := in.Scopes; sc != nil {
		bot.Scopes = fromScopes(*sc)
	}

	if d := in.ToolDisplay; d != nil {
		bot.ToolDisplay = orchestration.ToolDisplay(*d)
	}

	if n := in.GuaranteedDispatches; n != nil {
		bot.GuaranteedDispatches = *n
	}

	if o := in.OwnerAccounts; o != nil {
		bot.OwnerAccounts = string(*o)
	}

	return bot
}

// RenameBot gives a Bot another name, moves it into a department or out of one (issue #471), or chooses its main
// chat (issue #923). Only the owner moves it and chooses the main chat: the order, the departments and the chat a
// click on a Bot opens are how his sidebar lists the Bots, not a Bot's setting the main Bot changes, as OpenMausBot
// refuses mainThreadChoice to any client but the owner's (server/routes/bot-patch.test.ts).
func (s *Server) RenameBot(ctx context.Context, req openapi.RenameBotRequestObject,
) (openapi.RenameBotResponseObject, error) {
	chat, err := setupChat(principal(ctx))
	if err != nil {
		return nil, err
	}

	if req.Body.Department != nil && chat != "" {
		return nil, fmt.Errorf("%w: only the owner moves a bot into a department", errForbidden)
	}

	if req.Body.MainChat != nil && chat != "" {
		return nil, fmt.Errorf("%w: only the owner chooses a bot's main chat", errForbidden)
	}

	b, err := s.svc.GetBot(ctx, req.BotId)
	if err != nil {
		return nil, err
	}

	switch {
	case req.Body.Name == nil:
	case chat == "":
		b, err = s.svc.RenameBot(ctx, req.BotId, *req.Body.Name)
	default:
		b, err = s.svc.RenameBotFromChat(ctx, chat, req.BotId, *req.Body.Name)
	}

	if err != nil {
		return nil, err
	}

	if req.Body.Department != nil {
		if b, err = s.svc.SetBotDepartment(ctx, req.BotId, *req.Body.Department); err != nil {
			return nil, err
		}
	}

	if req.Body.MainChat != nil {
		if b, err = s.svc.SetMainChat(ctx, req.BotId, *req.Body.MainChat); err != nil {
			return nil, err
		}
	}

	return openapi.RenameBot200JSONResponse(toBot(b)), nil
}

// ReorderBots moves the Bots named in the one order every client lists them in; only the owner (issue #470).
func (s *Server) ReorderBots(ctx context.Context, req openapi.ReorderBotsRequestObject,
) (openapi.ReorderBotsResponseObject, error) {
	order, err := s.svc.ReorderBots(ctx, req.Body.BotIds)
	if err != nil {
		return nil, err
	}

	return openapi.ReorderBots200JSONResponse{BotIds: order}, nil
}

// DeleteBot removes a Bot nothing depends on; with force, the owner removes one with its chats and its memory.
func (s *Server) DeleteBot(ctx context.Context, req openapi.DeleteBotRequestObject,
) (openapi.DeleteBotResponseObject, error) {
	chat, err := setupChat(principal(ctx))
	if err != nil {
		return nil, err
	}

	force := req.Params.Force != nil && *req.Params.Force

	switch {
	case force && chat != "":
		return nil, fmt.Errorf("%w: only the owner removes a bot with its chats", errForbidden)
	case force:
		err = s.forceDeleteBot(ctx, req.BotId)
	case chat == "":
		err = s.svc.DeleteBot(ctx, req.BotId)
	default:
		err = s.svc.DeleteBotFromChat(ctx, chat, req.BotId)
	}

	if err != nil {
		return nil, err
	}

	return openapi.DeleteBot204Response{}, nil
}

// forceDeleteBot removes a Bot with its chats, each as DeleteChat removes one
// (issue #503): refused while pi works in one of them, and after the commit
// each chat's pi is stopped and its files go. A file that stays is logged; the
// Bot stays deleted.
func (s *Server) forceDeleteBot(ctx context.Context, id string) error {
	if s.coordinator != nil {
		chats, err := s.svc.ChatIDsOfBot(ctx, id)
		if err != nil {
			return err
		}

		for _, chat := range chats {
			if s.coordinator.Working(chat) {
				return fmt.Errorf("%w: the coordinator is working in chat %s of bot %s; stop it first",
					orchestration.ErrConflict, chat, id)
			}
		}
	}

	chats, err := s.svc.ForceDeleteBot(ctx, id)
	if err != nil {
		return err
	}

	// The Bot is gone whatever follows, even if the owner's request ends meanwhile.
	ctx = context.WithoutCancel(ctx)

	for _, chat := range chats {
		if s.coordinator != nil {
			s.coordinator.Stop(chat.ID)
		}

		for _, err := range s.removeChatFiles(ctx, chat) {
			s.logger.ErrorContext(ctx, "removing a deleted chat's files failed",
				slog.String("event", "clientapi.chat_files_left"), slog.String("chat_id", chat.ID),
				slog.String("error", err.Error()))
		}
	}

	return nil
}

// GetBotMemory returns a Bot's memory in the shape of a profile's (issue #1336).
func (s *Server) GetBotMemory(ctx context.Context, req openapi.GetBotMemoryRequestObject,
) (openapi.GetBotMemoryResponseObject, error) {
	if err := botReadable(principal(ctx), req.BotId); err != nil {
		return nil, err
	}

	b, err := s.svc.GetBot(ctx, req.BotId)
	if err != nil {
		return nil, err
	}

	return openapi.GetBotMemory200JSONResponse(botMemory(b)), nil
}

// ChangeBotMemory applies one change to a Bot's memory.
func (s *Server) ChangeBotMemory(ctx context.Context, req openapi.ChangeBotMemoryRequestObject,
) (openapi.ChangeBotMemoryResponseObject, error) {
	if err := botAllowed(principal(ctx), req.BotId); err != nil {
		return nil, err
	}

	b, err := s.svc.ChangeBotMemory(ctx, req.BotId, orchestration.MemoryOp{
		Kind: string(req.Body.Kind), Text: val(req.Body.Text), Match: val(req.Body.Match),
	})
	if err != nil {
		return nil, err
	}

	return openapi.ChangeBotMemory200JSONResponse(botMemory(b)), nil
}

// SetBotMemory replaces a Bot's memory with the owner's text.
func (s *Server) SetBotMemory(ctx context.Context, req openapi.SetBotMemoryRequestObject,
) (openapi.SetBotMemoryResponseObject, error) {
	b, err := s.svc.SetBotMemory(ctx, req.BotId, req.Body.Memory)
	if err != nil {
		return nil, err
	}

	return openapi.SetBotMemory200JSONResponse(botMemory(b)), nil
}

// botMemory is a Bot's memory as a Memory, the shape a profile's has (issue #1336). A Bot keeps no time its memory
// changed apart from its own.
func botMemory(b orchestration.Bot) openapi.Memory {
	return openapi.Memory{Bot: &b.ID, Memory: b.Memory, MemoryLimit: orchestration.MaxMemory}
}

// ListBotVersions returns a Bot's versions, newest first (issue #1336), to the tokens that read the Bot.
func (s *Server) ListBotVersions(ctx context.Context, req openapi.ListBotVersionsRequestObject,
) (openapi.ListBotVersionsResponseObject, error) {
	if err := botReadable(principal(ctx), req.BotId); err != nil {
		return nil, err
	}

	versions, err := s.svc.ListBotVersions(ctx, req.BotId)
	if err != nil {
		return nil, err
	}

	out := openapi.ListBotVersions200JSONResponse{Data: make([]openapi.BotVersion, 0, len(versions))}
	for _, v := range versions {
		out.Data = append(out.Data, toBotVersion(v))
	}

	return out, nil
}

// RestoreBotVersion saves one of a Bot's versions again: the owner, or the main Bot from its chat, as PutBot.
func (s *Server) RestoreBotVersion(ctx context.Context, req openapi.RestoreBotVersionRequestObject,
) (openapi.RestoreBotVersionResponseObject, error) {
	chat, err := setupChat(principal(ctx))
	if err != nil {
		return nil, err
	}

	var b orchestration.Bot
	if chat == "" {
		b, err = s.svc.RestoreBotVersion(ctx, req.BotId, req.VersionId)
	} else {
		b, err = s.svc.RestoreBotVersionFromChat(ctx, chat, req.BotId, req.VersionId)
	}

	if err != nil {
		return nil, err
	}

	return openapi.RestoreBotVersion200JSONResponse(toBot(b)), nil
}

func toBotVersion(v orchestration.BotVersion) openapi.BotVersion {
	b := v.Bot

	return openapi.BotVersion{
		Id: v.ID, Version: v.Version, Source: openapi.BotVersionSource(v.Source),
		SavedBy: ptr(openapi.BotVersionSavedBy(v.SavedBy)), ChatId: ptr(v.ChatID), RestoredFrom: ptr(v.RestoredFrom),
		CreatedAt: v.CreatedAt,
		Settings: openapi.BotSettings{
			Provider: ptr(b.Provider), Model: ptr(b.Model), Effort: ptr(b.Effort), Instructions: &b.Instructions,
			Fallbacks: new(append([]string{}, b.Fallbacks...)), Skills: new(append([]string{}, b.Skills...)),
			SkillsOff: new(append([]string{}, b.SkillsOff...)), Preset: new(openapi.ToolPreset(b.Preset)),
			Tools: new(append([]string{}, b.Tools...)), ToolsOff: new(append([]string{}, b.ToolsOff...)),
			Connections: new(toAllowList(b.Connections)), Workers: new(toAllowList(b.Workers)),
			Scopes: new(toScopes(b.Scopes)), ToolDisplay: new(openapi.ToolDisplay(b.ToolDisplay)),
			GuaranteedDispatches: new(b.Guarantee()),
			OwnerAccounts:        new(openapi.OwnerAccounts(cmp.Or(b.OwnerAccounts, orchestration.OwnerAccountsUse))),
		},
	}
}

func toBot(b orchestration.Bot) openapi.Bot {
	return openapi.Bot{
		Id: b.ID, Name: b.Name, Main: b.Main(), Provider: ptr(b.Provider), Model: ptr(b.Model), Effort: ptr(b.Effort),
		Fallbacks: append([]string{}, b.Fallbacks...),
		// persona is the instructions under their name until 2026-11-28, given for one release (issue #1336).
		Instructions: b.Instructions, Persona: &b.Instructions, Memory: b.Memory, MemoryLimit: orchestration.MaxMemory,
		Skills: append([]string{}, b.Skills...), SkillsOff: append([]string{}, b.SkillsOff...),
		Preset: openapi.ToolPreset(b.Preset), Tools: append([]string{}, b.Tools...),
		ToolsOff:    append([]string{}, b.ToolsOff...),
		Connections: toAllowList(b.Connections), Workers: toAllowList(b.Workers), Scopes: toScopes(b.Scopes),
		ToolDisplay: openapi.ToolDisplay(b.ToolDisplay), GuaranteedDispatches: b.Guarantee(),
		OwnerAccounts: new(openapi.OwnerAccounts(cmp.Or(b.OwnerAccounts, orchestration.OwnerAccountsUse))),
		Position:      b.Position, Department: opt(b.Department), MainChat: opt(b.MainChat),
		CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt,
	}
}

func toScopes(scopes map[string]orchestration.ConnectionScope) map[string]openapi.ConnectionScope {
	out := make(map[string]openapi.ConnectionScope, len(scopes))
	for use, sc := range scopes {
		out[use] = openapi.ConnectionScope{Dashboards: append([]int64{}, sc.Dashboards...)}
	}

	return out
}

// fromScopes reads the scopes as given; none is nil, so that a Bot without
// any stores none.
func fromScopes(in map[string]openapi.ConnectionScope) map[string]orchestration.ConnectionScope {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string]orchestration.ConnectionScope, len(in))
	for use, sc := range in {
		out[use] = orchestration.ConnectionScope{Dashboards: append([]int64{}, sc.Dashboards...)}
	}

	return out
}

func toAllowList(l orchestration.AllowList) openapi.AllowList {
	return openapi.AllowList{Limited: l.Limited, Names: append([]string{}, l.Names...)}
}

// fromAllowList reads a list as given: its names only while it is limited, each once, in the order given.
func fromAllowList(l openapi.AllowList) orchestration.AllowList {
	if !l.Limited {
		return orchestration.AllowList{}
	}

	return orchestration.AllowList{Limited: true, Names: append([]string{}, l.Names...)}
}
