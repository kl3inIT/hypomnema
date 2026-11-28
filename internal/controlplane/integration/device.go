package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// An Integration whose Connection is the owner's own messaging account, linked as a device of his (KindDevice,
// issue #615, step 1 of #612): WhatsApp first. The owner scans a QR code with his phone, as on WhatsApp Web; the
// control plane holds the session for as long as it runs and opens it again when it starts, as MemoryOS's bridge
// holds its bots (memoryos-integration-whatsapp/bridge/bots.go). The session's keys stay in the messaging
// service's own store (messaging/whatsapp); the Connection's vault secret names the device, as MemoryOS's
// channel_devices names the device of each bot.
//
// The QR code changes about every 20 seconds, so a linking is followed: Begin answers the first code, Code the
// current one, as MemoryOS's pairing answer has a code and when it expires (ChannelPairingResponse), and Complete
// waits until the phone scanned one, as the Lark device sign-in waits (larkuser.go).

// firstCodeWait bounds how long Begin waits for the service's first code.
const firstCodeWait = 30 * time.Second

// deviceLink is one linking in progress, followed in the background.
type deviceLink struct {
	label  string
	ctx    context.Context //nolint:containedctx // The linking's own lifetime, which a disconnect ends.
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	code    string
	image   string
	expires time.Time
	// ask is the question the linking waits on instead of a code (issue #1184); moved is closed when the step
	// changes, for an answer that waits for the step after it.
	ask   *messaging.Ask
	moved chan struct{}
	// conn and err are the linking's end, set before done is closed.
	conn Connection
	err  error
}

// show makes step the linking's current one; l.mu is held.
func (l *deviceLink) show(step messaging.LinkStep) {
	l.code, l.image, l.expires, l.ask = step.Code, step.Image, step.Expires, step.Ask
	close(l.moved)
	l.moved = make(chan struct{})
}

// signIn is the linking as its page shows it now.
func (l *deviceLink) signIn(id string, left time.Duration) SignIn {
	l.mu.Lock()
	defer l.mu.Unlock()

	in := SignIn{ID: id, ExpiresIn: max(left, 0), Code: l.code, CodeImage: l.image, CodeExpires: l.expires}
	if l.ask != nil {
		in.Ask = &Asked{Field: l.ask.Field, Hint: l.ask.Hint, Again: l.ask.Again}
	}

	return in
}

// WithDevices sets the messaging service of an Integration of kind device: whatsapp's.
func (s *Service) WithDevices(integration string, svc messaging.Service) *Service {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.devices[integration] = svc

	return s
}

func deviceKey(integration, label string) string { return integration + ":" + label }

// beginLink starts linking an account under a label and answers its first code. A label that has an account is
// linked again: the new device replaces the old one once the phone scanned a code.
func (s *Service) beginLink(ctx context.Context, d Definition, label string) (SignIn, error) {
	return s.startLink(ctx, d, label, func(svc messaging.Service, linkCtx context.Context) (<-chan messaging.LinkStep,
		error,
	) {
		return svc.Link(linkCtx)
	})
}

// LinkByPhone starts linking an account of a service that also links by phone number (Telegram, issue #1184), when
// its QR code cannot be scanned: its first step asks for the number, which Answer gives.
func (s *Service) LinkByPhone(ctx context.Context, integration, label string) (SignIn, error) {
	d, err := s.definition(integration)
	if err != nil {
		return SignIn{}, err
	}

	return s.startLink(ctx, d, label, func(svc messaging.Service, linkCtx context.Context) (<-chan messaging.LinkStep,
		error,
	) {
		byPhone, ok := svc.(messaging.PhoneLinker)
		if !ok {
			return nil, fmt.Errorf("%w: %s links by its QR code only", ErrInvalid, d.Name)
		}

		return byPhone.LinkByPhone(linkCtx)
	})
}

// startLink starts a linking with start and answers its first step: a code to show, or a question.
func (s *Service) startLink(ctx context.Context, d Definition, label string,
	start func(svc messaging.Service, linkCtx context.Context) (<-chan messaging.LinkStep, error),
) (SignIn, error) {
	if !d.IsDevice() {
		return SignIn{}, fmt.Errorf("%w: %s is not linked as a device", ErrInvalid, d.Name)
	}

	if !orchestration.ValidConnectionLabel(label) {
		return SignIn{}, fmt.Errorf("%w: a label is lower-case letters, digits and dashes, such as work", ErrInvalid)
	}

	s.mu.Lock()
	svc, ok := s.devices[d.ID]
	s.mu.Unlock()

	if !ok {
		return SignIn{}, fmt.Errorf("%w: this control plane does not link %s accounts", orchestration.ErrUnavailable,
			d.Name)
	}

	if r, ok := svc.(messaging.Readier); ok {
		if err := r.Ready(ctx); err != nil {
			return SignIn{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
		}
	}

	// The linking outlives this request, up to the time any sign-in has.
	linkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), signInTimeout)

	steps, err := start(svc, linkCtx)
	if errors.Is(err, ErrInvalid) {
		cancel()

		return SignIn{}, err
	}

	if err != nil {
		cancel()

		return SignIn{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	var first messaging.LinkStep

	select {
	case first = <-steps:
	case <-ctx.Done():
		cancel()

		return SignIn{}, ctx.Err()
	case <-time.After(firstCodeWait):
		cancel()

		return SignIn{}, fmt.Errorf("%w: %s sent no code in %s", ErrSignInFailed, d.Name, firstCodeWait)
	}

	if first.Code == "" && first.Image == "" && first.Ask == nil {
		cancel()

		if first.Err == nil {
			first.Err = errors.New("the link ended at once")
		}

		return SignIn{}, fmt.Errorf("%w: %w", ErrSignInFailed, first.Err)
	}

	link := &deviceLink{
		label: label, ctx: linkCtx, cancel: cancel, done: make(chan struct{}), moved: make(chan struct{}),
	}
	link.show(first)

	go s.followLink(linkCtx, d, label, link, steps)

	id := s.newID("signin")
	pending := &signIn{def: d, label: label, started: s.clock.Now(), link: link}

	s.mu.Lock()
	s.dropExpired(pending.started)
	s.signIns[id] = pending
	s.mu.Unlock()

	return link.signIn(id, signInTimeout), nil
}

// dropExpired forgets the sign-ins older than any can be; s.mu is held.
func (s *Service) dropExpired(now time.Time) {
	for old, p := range s.signIns {
		if now.Sub(p.started) > signInTimeout {
			if p.link != nil {
				p.link.cancel()
			}

			delete(s.signIns, old)
		}
	}
}

// followLink keeps a linking's current code and records its account once the phone scanned one.
func (s *Service) followLink(ctx context.Context, d Definition, label string, link *deviceLink,
	steps <-chan messaging.LinkStep,
) {
	defer link.cancel()

	var (
		conn Connection
		err  = errors.New("the link ended")
	)

steps:
	for step := range steps {
		switch {
		case step.Code != "" || step.Image != "" || step.Ask != nil:
			link.mu.Lock()
			link.show(step)
			link.mu.Unlock()

			continue
		case step.Account != nil:
			conn, err = s.saveDevice(context.WithoutCancel(ctx), d, label, link, step.Account)
		case step.Err != nil:
			err = step.Err
		}

		break steps
	}

	link.mu.Lock()
	link.show(messaging.LinkStep{})
	link.conn = conn

	if err != nil {
		link.err = fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	link.mu.Unlock()
	close(link.done)
}

// saveDevice records a linked account as the label's Connection, its device named in the vault, and holds its
// session. A device the label had before is logged out: the phone keeps only the new one.
func (s *Service) saveDevice(ctx context.Context, d Definition, label string, link *deviceLink,
	acc messaging.Account,
) (Connection, error) {
	// One change of a label's device at a time, a disconnect included, so the vault, the Connection and the
	// session held name the same device.
	s.deviceMu.Lock()
	defer s.deviceMu.Unlock()

	// A linking the owner gave up, or a disconnect of its label ended, keeps nothing.
	if link.ctx.Err() != nil {
		_ = acc.Logout(ctx)

		return Connection{}, errors.New("the link was given up before the phone scanned the code")
	}

	name := connectionSecretName(d.ID, label)
	payload := vault.Payload{vault.KeyValue: acc.ID()}

	_, err := s.vault.Create(ctx, vault.NewSecret{
		Name: name, Kind: vault.KindGeneric, Payload: payload,
		Metadata: map[string]string{
			"integration": d.ID, "connection": label, "account": acc.Name(),
			"what": "the device this account is linked as; its keys are in the messaging service's own store",
		},
	})
	if errors.Is(err, vault.ErrConflict) {
		_, err = s.vault.Rotate(ctx, name, payload)
	}

	if err != nil {
		_ = acc.Logout(ctx)

		return Connection{}, vaultError(err)
	}

	saved, err := s.store.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: d.ID, Label: label, Account: acc.Name(), Secret: name,
	})
	if err != nil {
		_ = acc.Logout(ctx)

		return Connection{}, err
	}

	key := deviceKey(d.ID, label)

	s.mu.Lock()
	old := s.held[key]
	s.held[key] = acc
	s.mu.Unlock()

	if old != nil && old.ID() != acc.ID() {
		_ = old.Logout(ctx)
	} else if old != nil && old != acc {
		// The same account linked again (Telegram's user id stays): its new session replaces the old one's.
		old.Close()
	}

	return s.view(ctx, d, saved), nil
}

// completeLink waits until the phone scanned a code, and answers the Connection. A wait given up before the
// linking ended can be waited for again.
func (s *Service) completeLink(ctx context.Context, id string, pending *signIn, pasted string) (Connection, error) {
	if pasted != "" {
		return Connection{}, fmt.Errorf("%w: this link waits for the phone to scan the code, not for an address",
			ErrInvalid)
	}

	link := pending.link

	select {
	case <-link.done:
	case <-ctx.Done():
		return Connection{}, ctx.Err()
	}

	s.mu.Lock()
	delete(s.signIns, id)
	s.mu.Unlock()

	link.mu.Lock()
	defer link.mu.Unlock()

	return link.conn, link.err
}

// Code answers a linking's current code, for the page that shows it. A linking that ended answers no code: its
// Complete answers how it ended.
func (s *Service) Code(_ context.Context, integration, id string) (SignIn, error) {
	s.mu.Lock()
	pending, ok := s.signIns[id]
	s.mu.Unlock()

	if !ok || pending.def.ID != integration || pending.link == nil {
		return SignIn{}, fmt.Errorf("%w: no link %q is waiting; it was completed, is older than %d minutes, "+
			"or the control plane restarted. Start it again", ErrNotFound, id, int(signInTimeout.Minutes()))
	}

	return pending.link.signIn(id, signInTimeout-s.clock.Now().Sub(pending.started)), nil
}

// answerWait bounds how long Answer waits for the step after an answer.
const answerWait = 30 * time.Second

// Answer gives a linking the owner's answer to the question it waits on (issue #1184): his phone number, the code
// sent to it, or his two-step password, and answers the step after it, as mautrix-telegram's SubmitUserInput answers
// the next login step (pkg/connector/loginphone.go:100-108). The answer is never kept or logged; a password is given
// as typed, the others without the spaces around them.
func (s *Service) Answer(ctx context.Context, integration, id, value string) (SignIn, error) {
	s.mu.Lock()
	pending, ok := s.signIns[id]
	s.mu.Unlock()

	if !ok || pending.def.ID != integration || pending.link == nil {
		return SignIn{}, fmt.Errorf("%w: no link %q is waiting; it was completed, is older than %d minutes, "+
			"or the control plane restarted. Start it again", ErrNotFound, id, int(signInTimeout.Minutes()))
	}

	link := pending.link

	link.mu.Lock()
	ask, moved := link.ask, link.moved
	link.mu.Unlock()

	if ask == nil {
		return SignIn{}, fmt.Errorf("%w: this link asks nothing now; it waits for the phone", ErrInvalid)
	}

	if ask.Field != messaging.AskPassword {
		value = strings.TrimSpace(value)
	}

	if strings.TrimSpace(value) == "" {
		return SignIn{}, fmt.Errorf("%w: type the %s", ErrInvalid, askWords[ask.Field])
	}

	if err := ask.Answer(ctx, value); err != nil {
		return SignIn{}, fmt.Errorf("%w: %w", ErrSignInFailed, err)
	}

	select {
	case <-moved:
	case <-link.done:
	case <-ctx.Done():
		return SignIn{}, ctx.Err()
	case <-s.after(answerWait):
	}

	return pending.link.signIn(id, signInTimeout-s.clock.Now().Sub(pending.started)), nil
}

// askWords name what a linking asks for.
var askWords = map[string]string{
	messaging.AskPhone: "phone number", messaging.AskCode: "code", messaging.AskPassword: "password",
}

// OpenDevices opens the session of every Connection of an Integration of kind device, as the control plane starts.
// A session that cannot be opened is left out and its error returned with the others'; its Connection shows that
// it needs a link.
func (s *Service) OpenDevices(ctx context.Context) error {
	var errs []error

	for _, d := range s.defs {
		s.mu.Lock()
		svc, ok := s.devices[d.ID]
		s.mu.Unlock()

		if !d.IsDevice() || !ok {
			continue
		}

		conns, err := s.store.ConnectionsOf(ctx, d.ID)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		for _, c := range conns {
			if err := s.openDevice(ctx, svc, d, c); err != nil {
				errs = append(errs, fmt.Errorf("%s:%s: %w", d.ID, c.Label, err))
			}
		}
	}

	return errors.Join(errs...)
}

func (s *Service) openDevice(ctx context.Context, svc messaging.Service, d Definition, c orchestration.Connection,
) error {
	opened, err := s.vault.Open(ctx, c.Secret)
	if err != nil {
		return vaultError(err)
	}

	acc, err := svc.Open(ctx, opened.Payload[vault.KeyValue])
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.held[deviceKey(d.ID, c.Label)] = acc
	s.mu.Unlock()

	return nil
}

// CloseDevices disconnects every session held, keeping them to be opened at the next start.
func (s *Service) CloseDevices() {
	s.mu.Lock()
	held := s.held
	s.held = map[string]messaging.Account{}
	s.mu.Unlock()

	for _, acc := range held {
		acc.Close()
	}
}

// DeviceState is the session's state of a Connection of kind device: a Bot's read of a messaging account that must
// be linked again keeps the owner's "log in again" item of it (issue #1023).
func (s *Service) DeviceState(integration, label string) messaging.State {
	return s.deviceState(integration, label)
}

// deviceState is the session's state of a Connection of kind device; logged out when none is held.
func (s *Service) deviceState(integration, label string) messaging.State {
	s.mu.Lock()
	acc := s.held[deviceKey(integration, label)]
	s.mu.Unlock()

	if acc == nil {
		return messaging.State{Status: messaging.StatusLoggedOut, Reason: "the session is not open"}
	}

	return acc.State()
}

// forgetDevice logs a Connection's device out, which drops it from the phone, and forgets the Connection.
// A linking of the same label still waiting is ended: it would bring the Connection back. A session that was not
// open is opened to log it out; one that cannot be logged out keeps its Connection, to try again.
func (s *Service) forgetDevice(ctx context.Context, d Definition, conn orchestration.Connection) (bool, error) {
	s.deviceMu.Lock()
	defer s.deviceMu.Unlock()

	key := deviceKey(d.ID, conn.Label)

	s.mu.Lock()
	for _, p := range s.signIns {
		if p.link != nil && p.def.ID == d.ID && p.link.label == conn.Label {
			p.link.cancel()
		}
	}

	acc, svc := s.held[key], s.devices[d.ID]
	s.mu.Unlock()

	if acc == nil {
		if svc == nil {
			return false, fmt.Errorf("%w: this control plane does not link %s accounts", orchestration.ErrUnavailable,
				d.Name)
		}

		opened, err := s.vault.Open(ctx, conn.Secret)
		if err != nil {
			return false, vaultError(err)
		}

		if acc, err = svc.Open(ctx, opened.Payload[vault.KeyValue]); err != nil {
			return false, fmt.Errorf("opening the session to log it out: %w", err)
		}

		// Held from now on, so that a logout that fails while it connects is tried again on the same session.
		s.mu.Lock()
		s.held[key] = acc
		s.mu.Unlock()
	}

	if err := acc.Logout(ctx); err != nil {
		return false, fmt.Errorf("logging the device out: %w", err)
	}

	s.mu.Lock()
	delete(s.held, key)
	s.mu.Unlock()

	if _, err := s.store.RemoveConnection(ctx, d.ID, conn.Label); err != nil {
		return true, err
	}

	if err := s.vault.Delete(ctx, conn.Secret); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return true, vaultError(err)
	}

	return true, nil
}

// heldAccount is the session held for a Connection of kind device.
func (s *Service) heldAccount(ctx context.Context, integration, label string) (messaging.Account, error) {
	d, err := s.definition(integration)
	if err != nil {
		return nil, err
	}

	if !d.IsDevice() {
		return nil, fmt.Errorf("%w: %s keeps no chats", ErrInvalid, d.Name)
	}

	if _, err := s.store.ConnectionOf(ctx, d.ID, label); err != nil {
		return nil, err
	}

	s.mu.Lock()
	acc := s.held[deviceKey(d.ID, label)]
	s.mu.Unlock()

	if acc == nil {
		return nil, fmt.Errorf("%w: the session of %s:%s is not open; link it again", orchestration.ErrUnavailable,
			d.ID, label)
	}

	return acc, nil
}

// Chats lists the chats kept of a Connection of kind device, the latest first (issue #631).
func (s *Service) Chats(ctx context.Context, integration, label string, limit int) ([]messaging.Chat, error) {
	acc, err := s.heldAccount(ctx, integration, label)
	if err != nil {
		return nil, err
	}

	return acc.Chats(ctx, limit)
}

// AccountID is the account's own id at the service, as the session held of a Connection of kind device knows it:
// the Zalo id, the WhatsApp number's JID (issue #1124). Its Connection records its name, which for Zalo is not it.
func (s *Service) AccountID(ctx context.Context, integration, label string) (string, error) {
	acc, err := s.heldAccount(ctx, integration, label)
	if err != nil {
		return "", err
	}

	return acc.ID(), nil
}

// Media is a kept message's image or file, downloaded first when it is not kept yet (issue #634).
func (s *Service) Media(ctx context.Context, integration, label, chat, id string) (messaging.Media, error) {
	acc, err := s.heldAccount(ctx, integration, label)
	if err != nil {
		return messaging.Media{}, err
	}

	return acc.Media(ctx, chat, id)
}

// Send sends a message or a file as the owner through a Connection of kind device (issue #645).
func (s *Service) Send(ctx context.Context, integration, label string, out messaging.Outgoing,
) (messaging.Message, error) {
	acc, err := s.heldAccount(ctx, integration, label)
	if err != nil {
		return messaging.Message{}, err
	}

	return acc.Send(ctx, out)
}

// Backfill asks for older messages of a chat of a Connection of kind device (issue #634).
func (s *Service) Backfill(ctx context.Context, integration, label, chat string, count int,
) (messaging.Backfilled, error) {
	acc, err := s.heldAccount(ctx, integration, label)
	if err != nil {
		return messaging.Backfilled{}, err
	}

	return acc.Backfill(ctx, chat, count)
}

// SyncFromPhone asks the owner's phone for the past of every chat of a Connection of kind device whose service
// brings it so, Zalo's (issue #697); its State's PhoneSync tells what follows.
func (s *Service) SyncFromPhone(ctx context.Context, integration, label string) error {
	acc, err := s.heldAccount(ctx, integration, label)
	if err != nil {
		return err
	}

	syncer, ok := acc.(messaging.PhoneSyncer)
	if !ok {
		return fmt.Errorf("%w: %s's past does not come from the phone in one sync", orchestration.ErrInvalid,
			integration)
	}

	return syncer.SyncFromPhone(ctx)
}

// Members reads who is in a chat of a Connection of kind device now (issue #1163), of a service that lists them.
func (s *Service) Members(ctx context.Context, integration, label, chat string) (messaging.Members, error) {
	acc, err := s.heldAccount(ctx, integration, label)
	if err != nil {
		return messaging.Members{}, err
	}

	lister, ok := acc.(messaging.MemberLister)
	if !ok {
		return messaging.Members{}, fmt.Errorf("%w: %s does not list a chat's members", orchestration.ErrInvalid,
			integration)
	}

	return lister.Members(ctx, chat)
}

// Messages lists the messages kept of a Connection of kind device, the latest first (issue #631).
func (s *Service) Messages(ctx context.Context, integration, label string, q messaging.Query,
) ([]messaging.Message, error) {
	acc, err := s.heldAccount(ctx, integration, label)
	if err != nil {
		return nil, err
	}

	return acc.Messages(ctx, q)
}

// unready says what a device's service lacks before it links an account (messaging.Readier), in its own words
// without the service's name before them.
func (s *Service) unready(ctx context.Context, d Definition) string {
	if !d.IsDevice() {
		return ""
	}

	s.mu.Lock()
	svc := s.devices[d.ID]
	s.mu.Unlock()

	r, ok := svc.(messaging.Readier)
	if !ok {
		return ""
	}

	err := r.Ready(ctx)
	if err == nil {
		return ""
	}

	return strings.TrimPrefix(err.Error(), strings.ToLower(d.Name)+": ")
}
