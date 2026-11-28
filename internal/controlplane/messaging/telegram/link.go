package telegram

// Linking the owner's account (issue #1184): by the QR code his phone accepts in Telegram › Settings › Devices › Link
// Desktop Device, with his two-step password when the account has one, as gotd's QR login does it
// (telegram/auth/qrlogin/qrlogin.go: Auth, Options.Password; examples/qrauth.go: the password again when wrong); or,
// when the code cannot be scanned, by his phone number and the code Telegram sends, step by step as mautrix/telegram's
// phone login asks them (pkg/connector/loginphone.go:91-214: the number, the code, the code again when wrong, the
// password). Each typed answer is a step of the linking that asks for it (messaging.Ask). The linking runs a client of
// its own over a session in memory; once Telegram authorized it, the session is kept under the account's user id and
// the account opened over it, as mautrix keeps a login's session in its metadata and connects it
// (login.go:260-298).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/gotd/td/crypto"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// linkTimeout bounds a linking, as mautrix's LoginTimeout (loginqr.go:47); the device link's own time is shorter.
const linkTimeout = 10 * time.Minute

// Link starts linking an account by QR code.
func (s *Service) Link(ctx context.Context) (<-chan messaging.LinkStep, error) {
	return s.link(ctx, false)
}

// LinkByPhone starts linking an account by its phone number and the code Telegram sends to it.
func (s *Service) LinkByPhone(ctx context.Context) (<-chan messaging.LinkStep, error) {
	return s.link(ctx, true)
}

func (s *Service) link(ctx context.Context, byPhone bool) (<-chan messaging.LinkStep, error) {
	appID, appHash, err := s.credentials(ctx)
	if err != nil {
		return nil, err
	}

	steps := make(chan messaging.LinkStep, 1)

	go func() {
		defer close(steps)

		ctx, cancel := context.WithTimeout(ctx, linkTimeout)
		defer cancel()

		acc, err := s.runLink(ctx, appID, appHash, byPhone, steps)
		if err != nil {
			steps <- messaging.LinkStep{Err: err}

			return
		}

		steps <- messaging.LinkStep{Account: acc}
	}()

	return steps, nil
}

// runLink runs the linking's own client until Telegram authorized it, and keeps the account it made.
func (s *Service) runLink(ctx context.Context, appID int, appHash string, byPhone bool,
	steps chan<- messaging.LinkStep,
) (*Account, error) {
	// The QR code's acceptance comes as updateLoginToken (gotd's examples/userbot, main.go:136); nothing else of the
	// updates is wanted, as mautrix's login client (login.go:177-187).
	dispatcher := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(dispatcher)
	memory := &session.StorageMemory{}

	client := telegram.NewClient(appID, appHash, telegram.Options{
		SessionStorage: memory,
		UpdateHandler:  dispatcher,
		NoUpdates:      true,
		Device:         telegram.DeviceConfig{DeviceModel: deviceName},
	})

	var authz *tg.AuthAuthorization

	err := client.Run(ctx, func(ctx context.Context) error {
		l := linker{
			api: client.API(), appID: appID, appHash: appHash, migrate: client.MigrateTo, loggedIn: loggedIn,
			steps: steps, answers: make(chan string),
		}

		var err error
		if byPhone {
			authz, err = l.phone(ctx)
		} else {
			authz, err = l.qr(ctx)
		}

		return err
	})
	if err != nil {
		return nil, fmt.Errorf("telegram: linking: %w", err)
	}

	data, err := memory.LoadSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("telegram: reading the new session: %w", err)
	}

	return s.linked(ctx, authz, data)
}

// linked keeps a new session under the account's user id and opens the account over it. A session the account had
// is ended first: its connection would write the old session back (session.Storage), and Telegram keeps one device
// a session.
func (s *Service) linked(ctx context.Context, authz *tg.AuthAuthorization, data []byte) (*Account, error) {
	self, ok := authz.User.AsNotEmpty()
	if !ok {
		return nil, errors.New("telegram: the authorization names no account")
	}

	key := strconv.FormatInt(self.ID, 10)
	s.endLive(ctx, key)

	if err := s.store.keepSession(ctx, key, data); err != nil {
		return nil, err
	}

	name := userName(self)
	if self.Phone != "" {
		name = "+" + self.Phone
	}

	if err := s.store.nameSession(ctx, key, name); err != nil {
		return nil, err
	}

	// The account lives past the linking's own time.
	return s.Open(context.WithoutCancel(ctx), key)
}

// linker asks Telegram to authorize a new session, and the owner what it needs.
type linker struct {
	api      *tg.Client
	appID    int
	appHash  string
	migrate  func(ctx context.Context, dc int) error
	loggedIn qrlogin.LoggedIn
	steps    chan<- messaging.LinkStep
	answers  chan string
}

// ask shows a question and waits for the owner's answer.
func (l *linker) ask(ctx context.Context, field, hint string, again bool) (string, error) {
	answers := l.answers
	step := messaging.LinkStep{Ask: &messaging.Ask{
		Field: field, Hint: hint, Again: again,
		Answer: func(actx context.Context, value string) error {
			select {
			case answers <- value:
				return nil
			case <-actx.Done():
				return actx.Err()
			case <-ctx.Done():
				return errors.New("the link ended")
			}
		},
	}}

	select {
	case l.steps <- step:
	case <-ctx.Done():
		return "", ctx.Err()
	}

	select {
	case value := <-answers:
		return value, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// qr shows Telegram's login link as a QR code, a new one as each expires, until the phone accepted one; an account
// with a two-step password is asked for it (qrlogin's Options.Password), again while it is wrong (gotd's
// examples/qrauth.go, handle2FA).
func (l *linker) qr(ctx context.Context) (*tg.AuthAuthorization, error) {
	qr := qrlogin.NewQR(l.api, l.appID, l.appHash, qrlogin.Options{
		Migrate: l.migrate,
		Password: func(ctx context.Context) (string, error) {
			return l.askPassword(ctx, false)
		},
	})

	show := func(ctx context.Context, token qrlogin.Token) error {
		select {
		case l.steps <- messaging.LinkStep{Code: token.URL(), Expires: token.Expires()}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	authz, err := qr.Auth(ctx, l.loggedIn, show)
	if errors.Is(err, auth.ErrPasswordInvalid) {
		return l.password(ctx, true)
	}

	if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
		return l.password(ctx, false)
	}

	return authz, err
}

// phone asks the number, sends the code to it and asks the code, again while either is refused, then the password
// when the account has one, as mautrix's phone login (loginphone.go:144-214).
func (l *linker) phone(ctx context.Context) (*tg.AuthAuthorization, error) {
	client := auth.NewClient(l.api, crypto.DefaultRand(), l.appID, l.appHash)

	var (
		number, hash, hint string
		again              bool
	)

	for hash == "" {
		var err error

		if number, err = l.ask(ctx, messaging.AskPhone, "", again); err != nil {
			return nil, err
		}

		sent, err := client.SendCode(ctx, number, auth.SendCodeOptions{})
		if tg.IsPhoneNumberInvalid(err) {
			again = true

			continue
		}

		if err != nil {
			return nil, err
		}

		switch s := sent.(type) {
		case *tg.AuthSentCode:
			if _, ok := s.Type.(*tg.AuthSentCodeTypeSetUpEmailRequired); ok {
				return nil, errors.New("Telegram asks this account to set up a login email first; link it by its " +
					"QR code instead")
			}

			hash, hint = s.PhoneCodeHash, codeSentTo(s.Type)
		case *tg.AuthSentCodeSuccess:
			return authorized(s.Authorization)
		default:
			return nil, fmt.Errorf("telegram: unexpected answer %T to sending the code", sent)
		}
	}

	for again = false; ; again = true {
		code, err := l.ask(ctx, messaging.AskCode, hint, again)
		if err != nil {
			return nil, err
		}

		authz, err := client.SignIn(ctx, number, code, hash)

		var signUp *auth.SignUpRequired

		switch {
		case tg.IsPhoneCodeInvalid(err):
			continue
		case errors.Is(err, auth.ErrPasswordAuthNeeded):
			return l.password(ctx, false)
		case errors.As(err, &signUp):
			return nil, errors.New("this number has no Telegram account; Hypomnema does not make one")
		}

		return authz, err
	}
}

// authorized is an authorization that links the account; one that would sign a new account up is refused.
func authorized(a tg.AuthAuthorizationClass) (*tg.AuthAuthorization, error) {
	if authz, ok := a.(*tg.AuthAuthorization); ok {
		return authz, nil
	}

	return nil, errors.New("this number has no Telegram account; Hypomnema does not make one")
}

// password asks the account's two-step password and checks it, again while it is wrong.
func (l *linker) password(ctx context.Context, again bool) (*tg.AuthAuthorization, error) {
	client := auth.NewClient(l.api, crypto.DefaultRand(), l.appID, l.appHash)

	for {
		value, err := l.askPassword(ctx, again)
		if err != nil {
			return nil, err
		}

		authz, err := client.Password(ctx, value)
		if errors.Is(err, auth.ErrPasswordInvalid) {
			again = true

			continue
		}

		return authz, err
	}
}

// askPassword asks the two-step password with the hint the owner set for it, as Telegram's apps show it.
func (l *linker) askPassword(ctx context.Context, again bool) (string, error) {
	hint := ""
	if p, err := l.api.AccountGetPassword(ctx); err == nil {
		hint = p.Hint
	}

	return l.ask(ctx, messaging.AskPassword, hint, again)
}

// codeSentTo says where Telegram sent the code, as mautrix's phoneCodeStep (loginphone.go:101-128).
func codeSentTo(t tg.AuthSentCodeTypeClass) string {
	switch t := t.(type) {
	case *tg.AuthSentCodeTypeApp:
		return "the Telegram app"
	case *tg.AuthSentCodeTypeSMS, *tg.AuthSentCodeTypeSMSWord, *tg.AuthSentCodeTypeSMSPhrase:
		return "an SMS"
	case *tg.AuthSentCodeTypeCall:
		return "a phone call"
	case *tg.AuthSentCodeTypeEmailCode:
		return "your email " + t.EmailPattern
	case *tg.AuthSentCodeTypeFragmentSMS:
		return "Fragment, " + t.URL
	}

	return ""
}

// endLive ends the session an account opened before had, at Telegram and here: the owner linked it again.
func (s *Service) endLive(ctx context.Context, key string) {
	s.mu.Lock()
	old := s.live[key]
	delete(s.live, key)
	s.mu.Unlock()

	if old == nil {
		return
	}

	if client, err := old.liveAPI(); err == nil {
		if _, err := client.AuthLogOut(ctx); err != nil {
			s.log.WarnContext(ctx, "telegram: ending the session linked before", slog.String("error", err.Error()))
		}
	}

	old.Close()
}

// Device is the service as the Integrations link and open its accounts (messaging.Service).
func (s *Service) Device() messaging.Service { return device{s} }

type device struct{ s *Service }

var (
	_ messaging.Service     = device{}
	_ messaging.PhoneLinker = device{}
	_ messaging.Readier     = device{}
	_ messaging.Account     = (*Account)(nil)
)

func (d device) Ready(ctx context.Context) error { return d.s.Ready(ctx) }

func (d device) Link(ctx context.Context) (<-chan messaging.LinkStep, error) { return d.s.Link(ctx) }

func (d device) LinkByPhone(ctx context.Context) (<-chan messaging.LinkStep, error) {
	return d.s.LinkByPhone(ctx)
}

func (d device) Open(ctx context.Context, id string) (messaging.Account, error) {
	acc, err := d.s.Open(ctx, id)
	if err != nil {
		return nil, err
	}

	return acc, nil
}
