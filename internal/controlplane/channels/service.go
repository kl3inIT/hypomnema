package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/channelstore"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// The owner's side of the Channels: what each is and who may talk through
// it. What every kind shares is here: the name, the Bot, the mode, the
// people, the groups, keeping the account's secret and removing a Channel.
// How an account is set up and how a person is named at the service are the
// kind's own (channels/lark's Service), as MemoryOS's ChannelAdapter checks
// an app's credentials (verify) and the runtime keeps the rest.

// Settings are what the owner sets of a Channel of any kind. nil, or an
// empty mode, keeps what the Channel had.
type Settings struct {
	Mode    channelstore.ChannelMode
	Enabled *bool
	// WebLink says a direct chat's reply ends with a link to the web client;
	// a new Channel has it only when it is the first (the owner's own).
	WebLink *bool
}

// Status is a Channel as the owner sees it.
type Status struct {
	Channel channelstore.Channel
	People  []channelstore.ChannelPerson
	// Groups are the groups the Channel's account is in.
	Groups []channelstore.ChannelGroup
	State  State
}

// SettingsStore is what the settings need of the store.
type SettingsStore interface {
	Store
	SetChannel(ctx context.Context, c channelstore.Channel) (channelstore.Channel, error)
	RemoveChannel(ctx context.Context, name string) error
	AddChannelPerson(ctx context.Context, channel string, p channelstore.ChannelPerson,
	) (channelstore.ChannelPerson, error)
	ChannelPeople(ctx context.Context, channel string) ([]channelstore.ChannelPerson, error)
	RemoveChannelPerson(ctx context.Context, channel, personID string) error
	ChannelGroups(ctx context.Context, channel string) ([]channelstore.ChannelGroup, error)
	SetChannelGroup(ctx context.Context, channel, chatID string, enabled bool,
	) (channelstore.ChannelGroup, error)
	GetBot(ctx context.Context, name string) (orchestration.Bot, error)
}

// Service is the owner's side of the Channels.
type Service struct {
	store    SettingsStore
	vault    *vault.Service
	channels *Channels
}

// NewService returns the settings of the Channels; channels runs them, and
// its state is shown.
func NewService(store SettingsStore, secrets *vault.Service, channels *Channels) *Service {
	return &Service{store: store, vault: secrets, channels: channels}
}

// List returns every Channel with its people and whether it is connected,
// oldest first.
func (s *Service) List(ctx context.Context) ([]Status, error) {
	channels, err := s.store.ListChannels(ctx)
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
	ch, err := s.store.Channel(ctx, name)
	if err != nil {
		return Status{}, err
	}

	return s.status(ctx, ch)
}

func (s *Service) status(ctx context.Context, ch channelstore.Channel) (Status, error) {
	people, err := s.store.ChannelPeople(ctx, ch.Name)
	if err != nil {
		return Status{}, err
	}

	groups, err := s.store.ChannelGroups(ctx, ch.Name)
	if err != nil {
		return Status{}, err
	}

	return Status{Channel: ch, People: people, Groups: groups, State: s.channels.State(ch.Name)}, nil
}

// SetGroup switches a group of a Channel on or off: the Channel's account
// answers only in a group the owner switched on (OpenClaw's Feishu
// groupPolicy allowlist).
func (s *Service) SetGroup(ctx context.Context, channel, chatID string, enabled bool,
) (channelstore.ChannelGroup, error) {
	if _, err := s.store.Channel(ctx, channel); err != nil {
		return channelstore.ChannelGroup{}, err
	}

	return s.store.SetChannelGroup(ctx, channel, strings.TrimSpace(chatID), enabled)
}

// Load returns the Channel of a name, and whether there is none yet.
func (s *Service) Load(ctx context.Context, name string) (channelstore.Channel, bool, error) {
	ch, err := s.store.Channel(ctx, name)
	if errors.Is(err, orchestration.ErrNotFound) {
		return channelstore.Channel{}, true, nil
	}

	return ch, false, err
}

// Refused says why a Channel cannot be what the owner asks: its Bot does not
// exist, or its account is another Channel's of its kind. The store refuses
// both as well; they are told here before the account's secret is kept.
func (s *Service) Refused(ctx context.Context, ch channelstore.Channel, appID string) error {
	if _, err := s.store.GetBot(ctx, ch.Bot); err != nil {
		if errors.Is(err, orchestration.ErrNotFound) {
			return fmt.Errorf("%w: no bot is named %q", orchestration.ErrInvalid, ch.Bot)
		}

		return err
	}

	if appID == "" {
		return nil
	}

	caps, err := s.channels.Capabilities(ch.Kind)
	if err != nil {
		return err
	}

	others, err := s.store.ListChannels(ctx)
	if err != nil {
		return err
	}

	for _, other := range others {
		if other.Kind == ch.Kind && other.AppID == appID && other.Name != ch.Name {
			return fmt.Errorf("%w: the %s app %s is the app of channel %s; one app is one channel",
				orchestration.ErrConflict, caps.Platform, appID, other.Name)
		}
	}

	return nil
}

// Secret opens a Channel's secret from the vault.
func (s *Service) Secret(ctx context.Context, ch channelstore.Channel) (string, error) {
	opened, err := s.vault.Open(ctx, ch.Secret)
	if err != nil {
		return "", vaultError(err)
	}

	return opened.Payload[vault.KeyValue], nil
}

// Connect makes a Channel's connection with a secret, to ask its service
// before the Channel is kept.
func (s *Service) Connect(ctx context.Context, ch channelstore.Channel, secret string) (Conn, error) {
	return s.channels.Connect(ctx, ch, secret)
}

// Save applies what every kind sets to a Channel, keeps its account's
// secret when one is given, and keeps the Channel. A Channel that was set up
// or switched on connects now, not at the next look.
func (s *Service) Save(ctx context.Context, ch channelstore.Channel, fresh bool, set Settings, secret string,
) (Status, error) {
	if set.Mode != "" {
		ch.Mode = set.Mode
	}

	if set.Enabled != nil {
		ch.Enabled = *set.Enabled
	}

	if fresh {
		// The first Channel is the owner's own, who opens the web client; the next are for other people, who
		// do not (owner, 2026-11-13).
		others, err := s.store.ListChannels(ctx)
		if err != nil {
			return Status{}, err
		}

		ch.WebLink = len(others) == 0
	}

	if set.WebLink != nil {
		ch.WebLink = *set.WebLink
	}

	if secret != "" {
		if err := s.keep(ctx, ch, secret); err != nil {
			return Status{}, err
		}
	}

	if _, err := s.store.SetChannel(ctx, ch); err != nil {
		return Status{}, err
	}

	s.channels.Look()

	return s.Status(ctx, ch.Name)
}

// keep puts the account's secret in the vault, replacing the one that was
// there.
func (s *Service) keep(ctx context.Context, ch channelstore.Channel, secret string) error {
	caps, err := s.channels.Capabilities(ch.Kind)
	if err != nil {
		return err
	}

	payload := vault.Payload{vault.KeyValue: secret}

	_, err = s.vault.Create(ctx, vault.NewSecret{
		Name: ch.Secret, Kind: vault.KindGeneric, Payload: payload,
		Metadata: map[string]string{
			"channel": string(ch.Kind), "name": ch.Name, "app": ch.AppID,
			"what": "the secret of a " + caps.Platform + " channel's app",
		},
	})
	if errors.Is(err, vault.ErrConflict) {
		_, err = s.vault.Rotate(ctx, ch.Secret, payload)
	}

	return vaultError(err)
}

// Open returns a Channel and a connection to its service, which is not
// listened to.
func (s *Service) Open(ctx context.Context, name string) (channelstore.Channel, Conn, error) {
	ch, err := s.store.Channel(ctx, name)
	if err != nil {
		return channelstore.Channel{}, nil, err
	}

	conn, err := s.channels.Open(ctx, ch)
	if err != nil {
		return channelstore.Channel{}, nil, err
	}

	return ch, conn, nil
}

// AddPerson adds someone who may talk through a Channel when its mode is the
// allowlist, by their id at the service and the email they were found by,
// if any. Their name is read at the service when the account may read it.
func (s *Service) AddPerson(ctx context.Context, channel string, conn Conn, personID, email string,
) (channelstore.ChannelPerson, error) {
	person := channelstore.ChannelPerson{PersonID: personID, Email: email}

	// A name is for the owner's eyes and the Bot's label; an account that may not read it adds the person all
	// the same.
	person.Name, _ = conn.NameOf(ctx, personID)

	return s.store.AddChannelPerson(ctx, channel, person)
}

// RemovePerson takes someone off a Channel's list by their id at the
// service.
func (s *Service) RemovePerson(ctx context.Context, channel, personID string) error {
	return s.store.RemoveChannelPerson(ctx, channel, personID)
}

// RemovePersonByEmail takes someone off a Channel's list by the email they
// were added with.
func (s *Service) RemovePersonByEmail(ctx context.Context, channel, email string) error {
	people, err := s.store.ChannelPeople(ctx, channel)
	if err != nil {
		return err
	}

	for _, p := range people {
		if strings.EqualFold(p.Email, email) {
			return s.store.RemoveChannelPerson(ctx, channel, p.PersonID)
		}
	}

	platform := ""
	if ch, err := s.store.Channel(ctx, channel); err == nil {
		if caps, err := s.channels.Capabilities(ch.Kind); err == nil {
			platform = caps.Platform + " "
		}
	}

	return fmt.Errorf("%w: nobody was added to %schannel %s with the email %s", orchestration.ErrNotFound,
		platform, channel, email)
}

// Remove forgets a Channel and its account's secret. A connection that is
// open stays until the control plane starts again, and takes no message.
func (s *Service) Remove(ctx context.Context, name string) error {
	ch, err := s.store.Channel(ctx, name)
	if err != nil {
		return err
	}

	if err := s.store.RemoveChannel(ctx, name); err != nil {
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
