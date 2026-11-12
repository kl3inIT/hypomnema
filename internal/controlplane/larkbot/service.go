package larkbot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// secretName is the vault secret that holds the bot app's secret. The bot
// shares nothing with the Lark Integration: either works without the other,
// and both may name the same Lark app (MemoryOS keeps them apart the same
// way).
const secretName = "lark-bot-app" //nolint:gosec // G101: the name of a vault secret, not a credential

// maxField bounds what the owner types.
const maxField = 1024

// Settings are what the owner sets of the bot. A field left empty, or nil,
// keeps what the bot had.
type Settings struct {
	// AppID and AppSecret are the Lark app's; they are given together.
	AppID     string
	AppSecret string
	// Owner is the owner at Lark: his email in the app's organization, or
	// his open_id.
	Owner   string
	Mode    orchestration.LarkBotMode
	Enabled *bool
}

// Status is the bot as the owner sees it.
type Status struct {
	Bot    orchestration.LarkBot
	People []orchestration.LarkBotPerson
	State  State
}

// SettingsStore is what the settings need of the store.
type SettingsStore interface {
	Store
	SetLarkBot(ctx context.Context, b orchestration.LarkBot) (orchestration.LarkBot, error)
	RemoveLarkBot(ctx context.Context) error
	AddLarkBotPerson(ctx context.Context, p orchestration.LarkBotPerson) (orchestration.LarkBotPerson, error)
	LarkBotPeople(ctx context.Context) ([]orchestration.LarkBotPerson, error)
	RemoveLarkBotPerson(ctx context.Context, openID string) error
}

// Service is the owner's side of the bot: what it is and who may talk to it.
type Service struct {
	store   SettingsStore
	vault   *vault.Service
	bot     *Bot
	connect func(appID, secret string) Lark
}

// NewService returns the settings of a bot. connect makes Lark for an app's
// id and secret (Connect); bot is the running bot, whose state is shown.
func NewService(store SettingsStore, secrets *vault.Service, bot *Bot, connect func(appID, secret string) Lark,
) *Service {
	return &Service{store: store, vault: secrets, bot: bot, connect: connect}
}

// Open makes the bot's connection to Lark from what the owner set up. It is
// the bot's own way to the app's secret.
func (s *Service) Open(ctx context.Context, bot orchestration.LarkBot) (Lark, error) {
	opened, err := s.vault.Open(ctx, bot.Secret)
	if err != nil {
		return nil, fmt.Errorf("opening the Lark bot's secret: %w", err)
	}

	return s.connect(bot.AppID, opened.Payload[vault.KeyValue]), nil
}

// Status returns the bot, its people and whether it is connected;
// ErrNotFound when no bot is set up.
func (s *Service) Status(ctx context.Context) (Status, error) {
	bot, err := s.store.LarkBot(ctx)
	if err != nil {
		return Status{}, err
	}

	people, err := s.store.LarkBotPeople(ctx)
	if err != nil {
		return Status{}, err
	}

	return Status{Bot: bot, People: people, State: s.bot.State()}, nil
}

// Set sets the bot up or changes it. The first time it needs the app, its
// secret and the owner; after that each is kept unless given. The owner's
// email is looked up at Lark with the app, which also shows that the app's
// id and secret work.
func (s *Service) Set(ctx context.Context, set Settings) (Status, error) {
	bot, err := s.store.LarkBot(ctx)
	fresh := errors.Is(err, orchestration.ErrNotFound)

	if err != nil && !fresh {
		return Status{}, err
	}

	set.AppID, set.AppSecret = strings.TrimSpace(set.AppID), strings.TrimSpace(set.AppSecret)
	set.Owner = strings.TrimSpace(set.Owner)

	switch {
	case (set.AppID == "") != (set.AppSecret == ""):
		return Status{}, fmt.Errorf("%w: the app's id and secret are given together", orchestration.ErrInvalid)
	case len(set.AppID) > maxField || len(set.AppSecret) > maxField || len(set.Owner) > maxField ||
		strings.ContainsAny(set.AppID+set.AppSecret+set.Owner, " \t\r\n"):
		return Status{}, fmt.Errorf("%w: the app's id, its secret and the owner are each one line without spaces",
			orchestration.ErrInvalid)
	case fresh && (set.AppID == "" || set.Owner == ""):
		return Status{}, fmt.Errorf("%w: a new Lark bot needs its app's id and secret, and the owner's email at Lark",
			orchestration.ErrInvalid)
	case set.Mode != "" && !orchestration.ValidLarkBotMode(set.Mode):
		return Status{}, fmt.Errorf("%w: the mode is owner, allowlist or everyone", orchestration.ErrInvalid)
	}

	if fresh {
		bot = orchestration.LarkBot{Mode: orchestration.LarkBotOwner, Secret: secretName}
	}

	secret := set.AppSecret
	if set.AppID != "" {
		bot.AppID = set.AppID
	} else if set.Owner != "" && !strings.HasPrefix(set.Owner, "ou_") {
		opened, err := s.vault.Open(ctx, bot.Secret)
		if err != nil {
			return Status{}, vaultError(err)
		}

		secret = opened.Payload[vault.KeyValue]
	}

	if set.Owner != "" {
		id, err := s.openID(ctx, s.connect(bot.AppID, secret), set.Owner)
		if err != nil {
			return Status{}, err
		}

		bot.OwnerOpenID = id
	}

	if set.Mode != "" {
		bot.Mode = set.Mode
	}

	if set.Enabled != nil {
		bot.Enabled = *set.Enabled
	}

	if set.AppSecret != "" {
		if err := s.keep(ctx, bot.AppID, set.AppSecret); err != nil {
			return Status{}, err
		}
	}

	if _, err := s.store.SetLarkBot(ctx, bot); err != nil {
		return Status{}, err
	}

	return s.Status(ctx)
}

// keep puts the app's secret in the vault, replacing the one that was there.
func (s *Service) keep(ctx context.Context, appID, secret string) error {
	payload := vault.Payload{vault.KeyValue: secret}

	_, err := s.vault.Create(ctx, vault.NewSecret{
		Name: secretName, Kind: vault.KindGeneric, Payload: payload,
		Metadata: map[string]string{"channel": "lark", "app": appID, "what": "the secret of the Lark bot's app"},
	})
	if errors.Is(err, vault.ErrConflict) {
		_, err = s.vault.Rotate(ctx, secretName, payload)
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

// AddPerson adds someone who may talk to the bot when its mode is the
// allowlist, by their email at Lark or their open_id. Their name is read at
// Lark when the app may read it.
func (s *Service) AddPerson(ctx context.Context, who string) (orchestration.LarkBotPerson, error) {
	bot, err := s.store.LarkBot(ctx)
	if err != nil {
		return orchestration.LarkBotPerson{}, err
	}

	lark, err := s.Open(ctx, bot)
	if err != nil {
		return orchestration.LarkBotPerson{}, err
	}

	who = strings.TrimSpace(who)

	id, err := s.openID(ctx, lark, who)
	if err != nil {
		return orchestration.LarkBotPerson{}, err
	}

	person := orchestration.LarkBotPerson{OpenID: id}
	if who != id {
		person.Email = who
	}

	// A name is for the owner's eyes and the coordinator's label; an app that may not read it adds the person
	// all the same.
	person.Name, _ = lark.NameOf(ctx, id)

	return s.store.AddLarkBotPerson(ctx, person)
}

// RemovePerson takes someone off the list, by open_id or by the email they
// were added with.
func (s *Service) RemovePerson(ctx context.Context, who string) error {
	who = strings.TrimSpace(who)

	if !strings.HasPrefix(who, "ou_") {
		people, err := s.store.LarkBotPeople(ctx)
		if err != nil {
			return err
		}

		for _, p := range people {
			if strings.EqualFold(p.Email, who) {
				return s.store.RemoveLarkBotPerson(ctx, p.OpenID)
			}
		}

		return fmt.Errorf("%w: nobody was added with the email %s", orchestration.ErrNotFound, who)
	}

	return s.store.RemoveLarkBotPerson(ctx, who)
}

// Remove forgets the bot and its app's secret. A connection that is open
// stays until the control plane starts again, and takes no message.
func (s *Service) Remove(ctx context.Context) error {
	if _, err := s.store.LarkBot(ctx); err != nil {
		return err
	}

	if err := s.store.RemoveLarkBot(ctx); err != nil {
		return err
	}

	if err := s.vault.Delete(ctx, secretName); err != nil && !errors.Is(err, vault.ErrNotFound) {
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
		return fmt.Errorf("%w: the vault has no key, so the bot's secret cannot be kept; set HYPOMNEMA_VAULT_KEYS "+
			"(see `hypomnema secrets keygen`)", orchestration.ErrUnavailable)
	case errors.Is(err, vault.ErrInvalid):
		return fmt.Errorf("%w: %w", orchestration.ErrInvalid, err)
	default:
		return fmt.Errorf("the vault: %w", err)
	}
}
