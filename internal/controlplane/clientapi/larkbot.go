package clientapi

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/larkbot"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The Lark bot (issue #136). Only the owner reads or changes it: the answer
// names the people who may talk to it. The app's secret is handed over and
// never comes back.

// LarkBot is what the API asks of the bot's settings.
type LarkBot interface {
	Status(ctx context.Context) (larkbot.Status, error)
	Set(ctx context.Context, set larkbot.Settings) (larkbot.Status, error)
	Remove(ctx context.Context) error
	AddPerson(ctx context.Context, who string) (orchestration.LarkBotPerson, error)
	RemovePerson(ctx context.Context, who string) error
}

// WithLarkBot sets the bot's settings.
func (s *Server) WithLarkBot(b LarkBot) *Server {
	s.larkBot = b

	return s
}

// errNoLarkBot answers when the control plane wired no bot.
var errNoLarkBot = fmt.Errorf("%w: this control plane runs no Lark bot", orchestration.ErrUnavailable)

func toLarkBotPerson(p orchestration.LarkBotPerson) openapi.LarkBotPerson {
	return openapi.LarkBotPerson{OpenId: p.OpenID, Email: p.Email, Name: p.Name, CreatedAt: p.CreatedAt}
}

func toLarkBot(st larkbot.Status) openapi.LarkBot {
	out := openapi.LarkBot{
		AppId: st.Bot.AppID, Mode: openapi.LarkBotMode(st.Bot.Mode), OwnerOpenId: st.Bot.OwnerOpenID,
		Enabled: st.Bot.Enabled, Connected: st.State.Connected, UpdatedAt: st.Bot.UpdatedAt,
		People: make([]openapi.LarkBotPerson, 0, len(st.People)),
	}

	if st.State.Connected && st.State.AppID != st.Bot.AppID {
		out.ConnectedAppId = &st.State.AppID
	}

	if st.State.LastError != "" {
		out.LastError = &st.State.LastError
	}

	for _, p := range st.People {
		out.People = append(out.People, toLarkBotPerson(p))
	}

	return out
}

// GetLarkBot returns the bot.
func (s *Server) GetLarkBot(ctx context.Context, _ openapi.GetLarkBotRequestObject,
) (openapi.GetLarkBotResponseObject, error) {
	if s.larkBot == nil {
		return nil, errNoLarkBot
	}

	st, err := s.larkBot.Status(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.GetLarkBot200JSONResponse(toLarkBot(st)), nil
}

// PutLarkBot sets the bot up or changes it. The secret is not logged and is
// not in the answer.
func (s *Server) PutLarkBot(ctx context.Context, req openapi.PutLarkBotRequestObject,
) (openapi.PutLarkBotResponseObject, error) {
	if s.larkBot == nil {
		return nil, errNoLarkBot
	}

	set := larkbot.Settings{
		AppID: val(req.Body.AppId), AppSecret: val(req.Body.AppSecret), Owner: val(req.Body.Owner),
		Enabled: req.Body.Enabled,
	}
	if req.Body.Mode != nil {
		set.Mode = orchestration.LarkBotMode(*req.Body.Mode)
	}

	st, err := s.larkBot.Set(ctx, set)
	if err != nil {
		return nil, err
	}

	return openapi.PutLarkBot200JSONResponse(toLarkBot(st)), nil
}

// DeleteLarkBot forgets the bot.
func (s *Server) DeleteLarkBot(ctx context.Context, _ openapi.DeleteLarkBotRequestObject,
) (openapi.DeleteLarkBotResponseObject, error) {
	if s.larkBot == nil {
		return nil, errNoLarkBot
	}

	if err := s.larkBot.Remove(ctx); err != nil {
		return nil, err
	}

	return openapi.DeleteLarkBot204Response{}, nil
}

// AddLarkBotPerson adds someone who may talk to the bot.
func (s *Server) AddLarkBotPerson(ctx context.Context, req openapi.AddLarkBotPersonRequestObject,
) (openapi.AddLarkBotPersonResponseObject, error) {
	if s.larkBot == nil {
		return nil, errNoLarkBot
	}

	p, err := s.larkBot.AddPerson(ctx, req.Body.Who)
	if err != nil {
		return nil, err
	}

	return openapi.AddLarkBotPerson200JSONResponse(toLarkBotPerson(p)), nil
}

// RemoveLarkBotPerson takes someone off the list.
func (s *Server) RemoveLarkBotPerson(ctx context.Context, req openapi.RemoveLarkBotPersonRequestObject,
) (openapi.RemoveLarkBotPersonResponseObject, error) {
	if s.larkBot == nil {
		return nil, errNoLarkBot
	}

	if err := s.larkBot.RemovePerson(ctx, req.Person); err != nil {
		return nil, err
	}

	return openapi.RemoveLarkBotPerson204Response{}, nil
}
