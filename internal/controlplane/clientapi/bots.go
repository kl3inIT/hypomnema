package clientapi

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Bots (issue #169). Every token reads them and changes a memory: pi's
// launcher reads its Bot's persona when it starts, and pi writes its memory.
// A pi's own token (issue #260) reads and changes its own Bot only, so one
// Bot never reads another's persona or memory. The owner makes, changes and
// removes a Bot, and the main Bot from its chat (setupchanges.go). A Bot is named by its id, which never changes;
// its name is only shown (issue #466).

// ListBots returns every Bot; a pi's token is shown its own, the main Bot's
// every one.
func (s *Server) ListBots(ctx context.Context, _ openapi.ListBotsRequestObject,
) (openapi.ListBotsResponseObject, error) {
	bots, err := s.svc.ListBots(ctx)
	if err != nil {
		return nil, err
	}

	tok := principal(ctx)

	out := openapi.ListBots200JSONResponse{Data: make([]openapi.Bot, 0, len(bots))}
	for _, b := range bots {
		if botReadable(tok, b.ID) == nil {
			out.Data = append(out.Data, toBot(b))
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

	return openapi.GetBot200JSONResponse(toBot(b)), nil
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

// withSettings is a Bot with the settings given: its model and persona as given, and its skills, lists and tool
// display as given or, left out, as it had them, so that a client that does not know them does not take them away.
func withSettings(had orchestration.Bot, in openapi.BotSettings) orchestration.Bot {
	bot := had
	bot.Provider, bot.Model, bot.Effort, bot.Persona = val(in.Provider), val(in.Model), val(in.Effort), val(in.Persona)

	if in.Skills != nil {
		bot.Skills = *in.Skills
	}

	if in.SkillsOff != nil {
		bot.SkillsOff = *in.SkillsOff
	}

	if l := in.Connections; l != nil {
		bot.Connections = fromAllowList(*l)
	}

	if l := in.Workers; l != nil {
		bot.Workers = fromAllowList(*l)
	}

	if d := in.ToolDisplay; d != nil {
		bot.ToolDisplay = orchestration.ToolDisplay(*d)
	}

	return bot
}

// RenameBot gives a Bot another name.
func (s *Server) RenameBot(ctx context.Context, req openapi.RenameBotRequestObject,
) (openapi.RenameBotResponseObject, error) {
	chat, err := setupChat(principal(ctx))
	if err != nil {
		return nil, err
	}

	var b orchestration.Bot
	if chat == "" {
		b, err = s.svc.RenameBot(ctx, req.BotId, req.Body.Name)
	} else {
		b, err = s.svc.RenameBotFromChat(ctx, chat, req.BotId, req.Body.Name)
	}

	if err != nil {
		return nil, err
	}

	return openapi.RenameBot200JSONResponse(toBot(b)), nil
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

	return openapi.ChangeBotMemory200JSONResponse(toBot(b)), nil
}

// SetBotMemory replaces a Bot's memory with the owner's text.
func (s *Server) SetBotMemory(ctx context.Context, req openapi.SetBotMemoryRequestObject,
) (openapi.SetBotMemoryResponseObject, error) {
	b, err := s.svc.SetBotMemory(ctx, req.BotId, req.Body.Memory)
	if err != nil {
		return nil, err
	}

	return openapi.SetBotMemory200JSONResponse(toBot(b)), nil
}

func toBot(b orchestration.Bot) openapi.Bot {
	return openapi.Bot{
		Id: b.ID, Name: b.Name, Main: b.Main(), Provider: ptr(b.Provider), Model: ptr(b.Model), Effort: ptr(b.Effort),
		Persona: b.Persona, Memory: b.Memory, MemoryLimit: orchestration.MaxMemory,
		Skills: append([]string{}, b.Skills...), SkillsOff: append([]string{}, b.SkillsOff...),
		Connections: toAllowList(b.Connections), Workers: toAllowList(b.Workers),
		ToolDisplay: openapi.ToolDisplay(b.ToolDisplay), CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt,
	}
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
