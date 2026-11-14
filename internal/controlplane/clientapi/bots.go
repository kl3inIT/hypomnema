package clientapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Bots (issue #169). Every token reads them and changes a memory: pi's
// launcher reads its Bot's persona when it starts, and pi writes its memory.
// A pi's own token (issue #260) reads and changes its own Bot only, so one
// Bot never reads another's persona or memory. The owner makes, changes and
// removes a Bot, and the Bot coordinator from its chat (setupchanges.go).

// ListBots returns every Bot; a pi's token is shown its own, the Bot
// coordinator's every one.
func (s *Server) ListBots(ctx context.Context, _ openapi.ListBotsRequestObject,
) (openapi.ListBotsResponseObject, error) {
	bots, err := s.svc.ListBots(ctx)
	if err != nil {
		return nil, err
	}

	tok := principal(ctx)

	out := openapi.ListBots200JSONResponse{Data: make([]openapi.Bot, 0, len(bots))}
	for _, b := range bots {
		if botReadable(tok, b.Name) == nil {
			out.Data = append(out.Data, toBot(b))
		}
	}

	return out, nil
}

// GetBot returns a Bot.
func (s *Server) GetBot(ctx context.Context, req openapi.GetBotRequestObject) (openapi.GetBotResponseObject, error) {
	if err := botReadable(principal(ctx), req.Name); err != nil {
		return nil, err
	}

	b, err := s.svc.GetBot(ctx, req.Name)
	if err != nil {
		return nil, err
	}

	return openapi.GetBot200JSONResponse(toBot(b)), nil
}

// PutBot makes a Bot or changes its settings.
func (s *Server) PutBot(ctx context.Context, req openapi.PutBotRequestObject) (openapi.PutBotResponseObject, error) {
	chat, err := setupChat(principal(ctx))
	if err != nil {
		return nil, err
	}

	bot := orchestration.Bot{
		Name: req.Name, Provider: val(req.Body.Provider), Model: val(req.Body.Model), Effort: val(req.Body.Effort),
		Persona: val(req.Body.Persona),
	}

	// Skills, lists and the tool display left out are kept: a client that does not know them does not take them away.
	if req.Body.Skills == nil || req.Body.SkillsOff == nil || req.Body.Connections == nil || req.Body.Workers == nil ||
		req.Body.ToolDisplay == nil {
		had, err := s.svc.GetBot(ctx, req.Name)

		switch {
		case err == nil:
			bot.Skills, bot.SkillsOff = had.Skills, had.SkillsOff
			bot.Connections, bot.Workers = had.Connections, had.Workers
			bot.ToolDisplay = had.ToolDisplay
		case !errors.Is(err, orchestration.ErrNotFound):
			return nil, err
		}
	}

	if req.Body.Skills != nil {
		bot.Skills = *req.Body.Skills
	}

	if req.Body.SkillsOff != nil {
		bot.SkillsOff = *req.Body.SkillsOff
	}

	if l := req.Body.Connections; l != nil {
		bot.Connections = fromAllowList(*l)
	}

	if l := req.Body.Workers; l != nil {
		bot.Workers = fromAllowList(*l)
	}

	if d := req.Body.ToolDisplay; d != nil {
		bot.ToolDisplay = orchestration.ToolDisplay(*d)
	}

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
		err = s.svc.ForceDeleteBot(ctx, req.Name)
	case chat == "":
		err = s.svc.DeleteBot(ctx, req.Name)
	default:
		err = s.svc.DeleteBotFromChat(ctx, chat, req.Name)
	}

	if err != nil {
		return nil, err
	}

	return openapi.DeleteBot204Response{}, nil
}

// ChangeBotMemory applies one change to a Bot's memory.
func (s *Server) ChangeBotMemory(ctx context.Context, req openapi.ChangeBotMemoryRequestObject,
) (openapi.ChangeBotMemoryResponseObject, error) {
	if err := botAllowed(principal(ctx), req.Name); err != nil {
		return nil, err
	}

	b, err := s.svc.ChangeBotMemory(ctx, req.Name, orchestration.MemoryOp{
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
	b, err := s.svc.SetBotMemory(ctx, req.Name, req.Body.Memory)
	if err != nil {
		return nil, err
	}

	return openapi.SetBotMemory200JSONResponse(toBot(b)), nil
}

func toBot(b orchestration.Bot) openapi.Bot {
	return openapi.Bot{
		Name: b.Name, Provider: ptr(b.Provider), Model: ptr(b.Model), Effort: ptr(b.Effort),
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
