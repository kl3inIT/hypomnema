package clientapi

import (
	"context"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/larkbot"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The Lark Channels (issues #136 and #169). Only the owner reads or changes
// them: an answer names the people who may talk through a Channel. An app's
// secret is handed over and never comes back.

// LarkChannels is what the API asks of the Channels' settings.
type LarkChannels interface {
	List(ctx context.Context) ([]larkbot.Status, error)
	Status(ctx context.Context, channel string) (larkbot.Status, error)
	Set(ctx context.Context, channel string, set larkbot.Settings) (larkbot.Status, error)
	Remove(ctx context.Context, channel string) error
	AddPerson(ctx context.Context, channel, who string) (orchestration.LarkChannelPerson, error)
	RemovePerson(ctx context.Context, channel, who string) error
	SetGroup(ctx context.Context, channel, chatID string, enabled bool) (orchestration.LarkChannelGroup, error)
}

// WithLarkChannels sets the Channels' settings.
func (s *Server) WithLarkChannels(c LarkChannels) *Server {
	s.larkChannels = c

	return s
}

// errNoLarkChannels answers when the control plane wired no Channels.
var errNoLarkChannels = fmt.Errorf("%w: this control plane runs no Lark channels", orchestration.ErrUnavailable)

func toLarkChannelPerson(p orchestration.LarkChannelPerson) openapi.LarkChannelPerson {
	return openapi.LarkChannelPerson{OpenId: p.OpenID, Email: p.Email, Name: p.Name, CreatedAt: p.CreatedAt}
}

func toLarkChannel(st larkbot.Status) openapi.LarkChannel {
	out := openapi.LarkChannel{
		Name: st.Channel.Name, Bot: st.Channel.Bot, AppId: st.Channel.AppID,
		Mode: openapi.LarkChannelMode(st.Channel.Mode), OwnerOpenId: st.Channel.OwnerOpenID,
		Enabled: st.Channel.Enabled, Connected: st.State.Connected,
		CreatedAt: st.Channel.CreatedAt, UpdatedAt: st.Channel.UpdatedAt,
		People: make([]openapi.LarkChannelPerson, 0, len(st.People)),
	}

	if st.State.Connected && st.State.AppID != st.Channel.AppID {
		out.ConnectedAppId = &st.State.AppID
	}

	if st.State.LastError != "" {
		out.LastError = &st.State.LastError
	}

	for _, p := range st.People {
		out.People = append(out.People, toLarkChannelPerson(p))
	}

	out.Groups = make([]openapi.LarkChannelGroup, 0, len(st.Groups))
	for _, g := range st.Groups {
		out.Groups = append(out.Groups, toLarkChannelGroup(g))
	}

	return out
}

func toLarkChannelGroup(g orchestration.LarkChannelGroup) openapi.LarkChannelGroup {
	return openapi.LarkChannelGroup{
		ChatId: g.ChatID, Name: g.Name, Enabled: g.Enabled, AskedAt: g.AskedAt, CreatedAt: g.CreatedAt,
	}
}

// SetLarkChannelGroup switches a group of a Channel on or off.
func (s *Server) SetLarkChannelGroup(ctx context.Context, req openapi.SetLarkChannelGroupRequestObject,
) (openapi.SetLarkChannelGroupResponseObject, error) {
	if s.larkChannels == nil {
		return nil, errNoLarkChannels
	}

	g, err := s.larkChannels.SetGroup(ctx, req.Channel, req.Group, req.Body.Enabled)
	if err != nil {
		return nil, err
	}

	return openapi.SetLarkChannelGroup200JSONResponse(toLarkChannelGroup(g)), nil
}

// ListLarkChannels returns the Channels.
func (s *Server) ListLarkChannels(ctx context.Context, _ openapi.ListLarkChannelsRequestObject,
) (openapi.ListLarkChannelsResponseObject, error) {
	if s.larkChannels == nil {
		return nil, errNoLarkChannels
	}

	all, err := s.larkChannels.List(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.LarkChannelList{Data: make([]openapi.LarkChannel, 0, len(all))}
	for _, st := range all {
		out.Data = append(out.Data, toLarkChannel(st))
	}

	return openapi.ListLarkChannels200JSONResponse(out), nil
}

// GetLarkChannel returns one Channel.
func (s *Server) GetLarkChannel(ctx context.Context, req openapi.GetLarkChannelRequestObject,
) (openapi.GetLarkChannelResponseObject, error) {
	if s.larkChannels == nil {
		return nil, errNoLarkChannels
	}

	st, err := s.larkChannels.Status(ctx, req.Channel)
	if err != nil {
		return nil, err
	}

	return openapi.GetLarkChannel200JSONResponse(toLarkChannel(st)), nil
}

// PutLarkChannel sets a Channel up or changes it. The secret is not logged
// and is not in the answer.
func (s *Server) PutLarkChannel(ctx context.Context, req openapi.PutLarkChannelRequestObject,
) (openapi.PutLarkChannelResponseObject, error) {
	if s.larkChannels == nil {
		return nil, errNoLarkChannels
	}

	set := larkbot.Settings{
		AppID: val(req.Body.AppId), AppSecret: val(req.Body.AppSecret), Owner: val(req.Body.Owner),
		Bot: val(req.Body.Bot), Enabled: req.Body.Enabled,
	}
	if req.Body.Mode != nil {
		set.Mode = orchestration.LarkChannelMode(*req.Body.Mode)
	}

	st, err := s.larkChannels.Set(ctx, req.Channel, set)
	if err != nil {
		return nil, err
	}

	return openapi.PutLarkChannel200JSONResponse(toLarkChannel(st)), nil
}

// DeleteLarkChannel forgets a Channel.
func (s *Server) DeleteLarkChannel(ctx context.Context, req openapi.DeleteLarkChannelRequestObject,
) (openapi.DeleteLarkChannelResponseObject, error) {
	if s.larkChannels == nil {
		return nil, errNoLarkChannels
	}

	if err := s.larkChannels.Remove(ctx, req.Channel); err != nil {
		return nil, err
	}

	return openapi.DeleteLarkChannel204Response{}, nil
}

// AddLarkChannelPerson adds someone who may talk through a Channel.
func (s *Server) AddLarkChannelPerson(ctx context.Context, req openapi.AddLarkChannelPersonRequestObject,
) (openapi.AddLarkChannelPersonResponseObject, error) {
	if s.larkChannels == nil {
		return nil, errNoLarkChannels
	}

	p, err := s.larkChannels.AddPerson(ctx, req.Channel, req.Body.Who)
	if err != nil {
		return nil, err
	}

	return openapi.AddLarkChannelPerson200JSONResponse(toLarkChannelPerson(p)), nil
}

// RemoveLarkChannelPerson takes someone off a Channel's list.
func (s *Server) RemoveLarkChannelPerson(ctx context.Context, req openapi.RemoveLarkChannelPersonRequestObject,
) (openapi.RemoveLarkChannelPersonResponseObject, error) {
	if s.larkChannels == nil {
		return nil, errNoLarkChannels
	}

	if err := s.larkChannels.RemovePerson(ctx, req.Channel, req.Person); err != nil {
		return nil, err
	}

	return openapi.RemoveLarkChannelPerson204Response{}, nil
}
