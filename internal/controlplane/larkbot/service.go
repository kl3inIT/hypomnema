package larkbot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
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

// Settings are what the owner sets of a Channel. A field left empty, or nil,
// keeps what the Channel had.
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
	Mode    orchestration.LarkChannelMode
	Enabled *bool
}

// Status is a Channel as the owner sees it.
type Status struct {
	Channel orchestration.LarkChannel
	People  []orchestration.LarkChannelPerson
	State   State
}

// SettingsStore is what the settings need of the store.
type SettingsStore interface {
	Store
	SetLarkChannel(ctx context.Context, c orchestration.LarkChannel) (orchestration.LarkChannel, error)
	RemoveLarkChannel(ctx context.Context, name string) error
	AddLarkChannelPerson(ctx context.Context, channel string, p orchestration.LarkChannelPerson,
	) (orchestration.LarkChannelPerson, error)
	LarkChannelPeople(ctx context.Context, channel string) ([]orchestration.LarkChannelPerson, error)
	RemoveLarkChannelPerson(ctx context.Context, channel, openID string) error
	GetBot(ctx context.Context, name string) (orchestration.Bot, error)
}

// Service is the owner's side of the Channels: what each is and who may talk
// through it.
type Service struct {
	store    SettingsStore
	vault    *vault.Service
	channels *Channels
	connect  func(appID, secret string) Lark
}

// NewService returns the settings of the Channels. connect makes Lark for an
// app's id and secret (Connect); channels runs them, and its state is shown.
func NewService(store SettingsStore, secrets *vault.Service, channels *Channels,
	connect func(appID, secret string) Lark,
) *Service {
	return &Service{store: store, vault: secrets, channels: channels, connect: connect}
}

// Open makes a Channel's connection to Lark from what the owner set up. It
// is the Channels' own way to an app's secret.
func (s *Service) Open(ctx context.Context, channel orchestration.LarkChannel) (Lark, error) {
	opened, err := s.vault.Open(ctx, channel.Secret)
	if err != nil {
		return nil, fmt.Errorf("opening the secret of Lark channel %s: %w", channel.Name, err)
	}

	return s.connect(channel.AppID, opened.Payload[vault.KeyValue]), nil
}

// List returns every Channel with its people and whether it is connected,
// oldest first.
func (s *Service) List(ctx context.Context) ([]Status, error) {
	channels, err := s.store.LarkChannels(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]Status, 0, len(channels))

	for _, ch := range channels {
		st, err := s.status(ctx, ch)
		if err != nil {
			return nil, err
		}

		out = append(out, st)
	}

	return out, nil
}

// Status returns a Channel, its people and whether it is connected;
// ErrNotFound when no Channel has that name.
func (s *Service) Status(ctx context.Context, name string) (Status, error) {
	ch, err := s.store.LarkChannel(ctx, name)
	if err != nil {
		return Status{}, err
	}

	return s.status(ctx, ch)
}

func (s *Service) status(ctx context.Context, ch orchestration.LarkChannel) (Status, error) {
	people, err := s.store.LarkChannelPeople(ctx, ch.Name)
	if err != nil {
		return Status{}, err
	}

	return Status{Channel: ch, People: people, State: s.channels.State(ch.Name)}, nil
}

// Set sets a Channel up or changes it. The first time it needs the app, its
// secret and the owner; after that each is kept unless given. The owner's
// email is looked up at Lark with the app, which also shows that the app's
// id and secret work. An app that is another Channel's and a Bot that does
// not exist are refused before the secret is kept.
func (s *Service) Set(ctx context.Context, name string, set Settings) (Status, error) {
	ch, err := s.store.LarkChannel(ctx, name)
	fresh := errors.Is(err, orchestration.ErrNotFound)

	if err != nil && !fresh {
		return Status{}, err
	}

	set.AppID, set.AppSecret = strings.TrimSpace(set.AppID), strings.TrimSpace(set.AppSecret)
	set.Owner, set.Bot = strings.TrimSpace(set.Owner), strings.TrimSpace(set.Bot)

	switch {
	case !orchestration.ValidLarkChannelName(name):
		return Status{}, fmt.Errorf("%w: a channel's name is lowercase letters, digits and dashes, starting with "+
			"a letter, at most %d characters", orchestration.ErrInvalid, orchestration.MaxLarkChannelName)
	case (set.AppID == "") != (set.AppSecret == ""):
		return Status{}, fmt.Errorf("%w: the app's id and secret are given together", orchestration.ErrInvalid)
	case len(set.AppID) > maxField || len(set.AppSecret) > maxField || len(set.Owner) > maxField ||
		strings.ContainsAny(set.AppID+set.AppSecret+set.Owner, " \t\r\n"):
		return Status{}, fmt.Errorf("%w: the app's id, its secret and the owner are each one line without spaces",
			orchestration.ErrInvalid)
	case fresh && (set.AppID == "" || set.Owner == ""):
		return Status{}, fmt.Errorf("%w: a new Lark channel needs its app's id and secret, and the owner's email "+
			"at Lark", orchestration.ErrInvalid)
	case set.Mode != "" && !orchestration.ValidLarkChannelMode(set.Mode):
		return Status{}, fmt.Errorf("%w: the mode is owner, allowlist or everyone", orchestration.ErrInvalid)
	}

	if fresh {
		ch = orchestration.LarkChannel{
			Name: name, Bot: orchestration.DefaultBot, Mode: orchestration.LarkChannelOwner, Secret: secretPrefix + name,
		}
	}

	if set.Bot != "" {
		ch.Bot = set.Bot
	}

	if err := s.refused(ctx, ch, set.AppID); err != nil {
		return Status{}, err
	}

	secret := set.AppSecret
	if set.AppID != "" {
		ch.AppID = set.AppID
	} else if set.Owner != "" && !strings.HasPrefix(set.Owner, "ou_") {
		opened, err := s.vault.Open(ctx, ch.Secret)
		if err != nil {
			return Status{}, vaultError(err)
		}

		secret = opened.Payload[vault.KeyValue]
	}

	if set.Owner != "" {
		id, err := s.openID(ctx, s.connect(ch.AppID, secret), set.Owner)
		if err != nil {
			return Status{}, err
		}

		ch.OwnerOpenID = id
	}

	if set.Mode != "" {
		ch.Mode = set.Mode
	}

	if set.Enabled != nil {
		ch.Enabled = *set.Enabled
	}

	if set.AppSecret != "" {
		if err := s.keep(ctx, ch, set.AppSecret); err != nil {
			return Status{}, err
		}
	}

	if _, err := s.store.SetLarkChannel(ctx, ch); err != nil {
		return Status{}, err
	}

	// A Channel that was set up or switched on connects now, not at the next look.
	s.channels.Look()

	return s.Status(ctx, name)
}

// refused says why a Channel cannot be what the owner asks: its Bot does not
// exist, or its app is another Channel's. The store refuses both as well;
// they are told here before the app's secret is kept.
func (s *Service) refused(ctx context.Context, ch orchestration.LarkChannel, appID string) error {
	if _, err := s.store.GetBot(ctx, ch.Bot); err != nil {
		if errors.Is(err, orchestration.ErrNotFound) {
			return fmt.Errorf("%w: no bot is named %q", orchestration.ErrInvalid, ch.Bot)
		}

		return err
	}

	if appID == "" {
		return nil
	}

	others, err := s.store.LarkChannels(ctx)
	if err != nil {
		return err
	}

	for _, other := range others {
		if other.AppID == appID && other.Name != ch.Name {
			return fmt.Errorf("%w: the Lark app %s is the app of channel %s; one app is one channel",
				orchestration.ErrConflict, appID, other.Name)
		}
	}

	return nil
}

// keep puts the app's secret in the vault, replacing the one that was there.
func (s *Service) keep(ctx context.Context, ch orchestration.LarkChannel, secret string) error {
	payload := vault.Payload{vault.KeyValue: secret}

	_, err := s.vault.Create(ctx, vault.NewSecret{
		Name: ch.Secret, Kind: vault.KindGeneric, Payload: payload,
		Metadata: map[string]string{
			"channel": "lark", "name": ch.Name, "app": ch.AppID, "what": "the secret of a Lark channel's app",
		},
	})
	if errors.Is(err, vault.ErrConflict) {
		_, err = s.vault.Rotate(ctx, ch.Secret, payload)
	}

	return vaultError(err)
}

// openID is a person's open_id: what was given when it is one, else the
// member of the app's organization with that email.
func (s *Service) openID(ctx context.Context, lark Lark, who string) (string, error) {
	if strings.HasPrefix(who, "ou_") {
		return who, nil
	}

	if !strings.Contains(who, "@") {
		return "", fmt.Errorf("%w: name a person by their email at Lark, or by their open_id (ou_...)",
			orchestration.ErrInvalid)
	}

	id, err := lark.OpenIDOf(ctx, who)
	if err != nil {
		return "", fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	}

	return id, nil
}

// AddPerson adds someone who may talk through a Channel when its mode is the
// allowlist, by their email at Lark or their open_id in the Channel's app.
// Their name is read at Lark when the app may read it.
func (s *Service) AddPerson(ctx context.Context, channel, who string) (orchestration.LarkChannelPerson, error) {
	ch, err := s.store.LarkChannel(ctx, channel)
	if err != nil {
		return orchestration.LarkChannelPerson{}, err
	}

	lark, err := s.Open(ctx, ch)
	if err != nil {
		return orchestration.LarkChannelPerson{}, err
	}

	who = strings.TrimSpace(who)

	id, err := s.openID(ctx, lark, who)
	if err != nil {
		return orchestration.LarkChannelPerson{}, err
	}

	person := orchestration.LarkChannelPerson{OpenID: id}
	if who != id {
		person.Email = who
	}

	// A name is for the owner's eyes and the Bot's label; an app that may not read it adds the person all the
	// same.
	person.Name, _ = lark.NameOf(ctx, id)

	return s.store.AddLarkChannelPerson(ctx, channel, person)
}

// RemovePerson takes someone off a Channel's list, by open_id or by the
// email they were added with.
func (s *Service) RemovePerson(ctx context.Context, channel, who string) error {
	who = strings.TrimSpace(who)

	if !strings.HasPrefix(who, "ou_") {
		people, err := s.store.LarkChannelPeople(ctx, channel)
		if err != nil {
			return err
		}

		for _, p := range people {
			if strings.EqualFold(p.Email, who) {
				return s.store.RemoveLarkChannelPerson(ctx, channel, p.OpenID)
			}
		}

		return fmt.Errorf("%w: nobody was added to Lark channel %s with the email %s", orchestration.ErrNotFound,
			channel, who)
	}

	return s.store.RemoveLarkChannelPerson(ctx, channel, who)
}

// Remove forgets a Channel and its app's secret. A connection that is open
// stays until the control plane starts again, and takes no message.
func (s *Service) Remove(ctx context.Context, name string) error {
	ch, err := s.store.LarkChannel(ctx, name)
	if err != nil {
		return err
	}

	if err := s.store.RemoveLarkChannel(ctx, name); err != nil {
		return err
	}

	if err := s.vault.Delete(ctx, ch.Secret); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return vaultError(err)
	}

	return nil
}

// vaultError turns the vault's errors into the ones the API answers with. The
// vault's errors never carry a value.
func vaultError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, vault.ErrNotConfigured):
		return fmt.Errorf("%w: the vault has no key, so the app's secret cannot be kept; set HYPOMNEMA_VAULT_KEYS "+
			"(see `hypomnema secrets keygen`)", orchestration.ErrUnavailable)
	case errors.Is(err, vault.ErrInvalid):
		return fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	default:
		return fmt.Errorf("the vault: %w", err)
	}
}
