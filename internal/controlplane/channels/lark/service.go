package lark

import (
	"context"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/channels"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
)

// secretPrefix starts the name of the vault secret that holds a Channel's
// app's secret: each Channel has its own. The Channel that was the one Lark
// bot keeps the name its secret had then (lark-bot-app), which its row
// records. A Channel shares nothing with the Lark Integration: either works
// without the other, and both may name the same Lark app (MemoryOS keeps
// them apart the same way).
const secretPrefix = "lark-channel-"

// maxField bounds what the owner types.
const maxField = 1024

// Settings are what the owner sets of a Lark Channel. A field left empty, or
// nil, keeps what the Channel had.
type Settings struct {
	// AppID and AppSecret are the Lark app's; they are given together.
	AppID     string
	AppSecret string
	// Owner is the owner at Lark: his email in the app's organization, or
	// his open_id.
	Owner string
	// Bot is the Bot the Channel talks to; the first Bot for a new Channel
	// that names none.
	Bot     string
	Mode    channelstore.ChannelMode
	Enabled *bool
	// WebLink says a direct chat's reply ends with a link to the web client;
	// a new Channel has it only when it is the first (the owner's own).
	WebLink *bool
}

// Service is the owner's side of the Lark Channels: what every kind has, and
// how a Lark app and a person at Lark are named (MemoryOS's
// LarkChannelAdapter verifies an app the same way, by asking Lark).
type Service struct {
	*channels.Service
}

// NewService returns the settings of the Lark Channels over the settings
// every kind shares.
func NewService(shared *channels.Service) *Service {
	return &Service{Service: shared}
}

// Set sets a Channel up or changes it. The first time it needs the app, its
// secret and the owner; after that each is kept unless given. The owner's
// email is looked up at Lark with the app, which also shows that the app's
// id and secret work. An app that is another Channel's and a Bot that does
// not exist are refused before the secret is kept.
func (s *Service) Set(ctx context.Context, name string, set Settings) (channels.Status, error) {
	ch, fresh, err := s.Load(ctx, name)
	if err != nil {
		return channels.Status{}, err
	}

	set.AppID, set.AppSecret = strings.TrimSpace(set.AppID), strings.TrimSpace(set.AppSecret)
	set.Owner, set.Bot = strings.TrimSpace(set.Owner), strings.TrimSpace(set.Bot)

	switch {
	case !channelstore.ValidChannelName(name):
		return channels.Status{}, fmt.Errorf("%w: a channel's name is lowercase letters, digits and dashes, "+
			"starting with a letter, at most %d characters", orchestration.ErrInvalid, channelstore.MaxChannelName)
	case (set.AppID == "") != (set.AppSecret == ""):
		return channels.Status{}, fmt.Errorf("%w: the app's id and secret are given together", orchestration.ErrInvalid)
	case len(set.AppID) > maxField || len(set.AppSecret) > maxField || len(set.Owner) > maxField ||
		strings.ContainsAny(set.AppID+set.AppSecret+set.Owner, " \t\r\n"):
		return channels.Status{}, fmt.Errorf("%w: the app's id, its secret and the owner are each one line "+
			"without spaces", orchestration.ErrInvalid)
	case fresh && (set.AppID == "" || set.Owner == ""):
		return channels.Status{}, fmt.Errorf("%w: a new Lark channel needs its app's id and secret, and the "+
			"owner's email at Lark", orchestration.ErrInvalid)
	case set.Mode != "" && !channelstore.ValidChannelMode(set.Mode):
		return channels.Status{}, fmt.Errorf("%w: the mode is owner, allowlist or everyone", orchestration.ErrInvalid)
	}

	if fresh {
		ch = channelstore.Channel{
			Name: name, Kind: channelstore.KindLark, Bot: orchestration.DefaultBotID, Mode: channelstore.ChannelOwner,
			Secret: secretPrefix + name,
		}
	}

	if set.Bot != "" {
		ch.Bot = set.Bot
	}

	if err := s.Refused(ctx, ch, set.AppID); err != nil {
		return channels.Status{}, err
	}

	secret := set.AppSecret
	if set.AppID != "" {
		ch.AppID = set.AppID
	} else if set.Owner != "" && !strings.HasPrefix(set.Owner, openIDPrefix) {
		if secret, err = s.Secret(ctx, ch); err != nil {
			return channels.Status{}, err
		}
	}

	if set.Owner != "" {
		conn, err := s.Connect(ctx, ch, secret)
		if err != nil {
			return channels.Status{}, err
		}

		if ch.OwnerID, err = openID(ctx, conn, set.Owner); err != nil {
			return channels.Status{}, err
		}
	}

	return s.Save(ctx, ch, fresh, channels.Settings{Mode: set.Mode, Enabled: set.Enabled, WebLink: set.WebLink},
		set.AppSecret)
}

// openID is a person's open_id: what was given when it is one, else the
// member of the app's organization with that email.
func openID(ctx context.Context, conn channels.Conn, who string) (string, error) {
	if strings.HasPrefix(who, openIDPrefix) {
		return who, nil
	}

	if !strings.Contains(who, "@") {
		return "", fmt.Errorf("%w: name a person by their email at Lark, or by their open_id (ou_...)",
			orchestration.ErrInvalid)
	}

	id, err := conn.PersonByEmail(ctx, who)
	if err != nil {
		return "", fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	}

	return id, nil
}

// AddPerson adds someone who may talk through a Channel when its mode is the
// allowlist, by their email at Lark or their open_id in the Channel's app.
// Their name is read at Lark when the app may read it.
func (s *Service) AddPerson(ctx context.Context, channel, who string) (channelstore.ChannelPerson, error) {
	_, conn, err := s.Open(ctx, channel)
	if err != nil {
		return channelstore.ChannelPerson{}, err
	}

	who = strings.TrimSpace(who)

	id, err := openID(ctx, conn, who)
	if err != nil {
		return channelstore.ChannelPerson{}, err
	}

	email := ""
	if who != id {
		email = who
	}

	return s.Service.AddPerson(ctx, channel, conn, id, email)
}

// RemovePerson takes someone off a Channel's list, by open_id or by the
// email they were added with.
func (s *Service) RemovePerson(ctx context.Context, channel, who string) error {
	who = strings.TrimSpace(who)

	if !strings.HasPrefix(who, openIDPrefix) {
		return s.RemovePersonByEmail(ctx, channel, who)
	}

	return s.Service.RemovePerson(ctx, channel, who)
}
