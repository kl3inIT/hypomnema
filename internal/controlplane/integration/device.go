package integration

import (
	"context"
	"errors"
	"fmt"
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
	// conn and err are the linking's end, set before done is closed.
	conn Connection
	err  error
}

func (l *deviceLink) current() (code, image string, expires time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.code, l.image, l.expires
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

	// The linking outlives this request, up to the time any sign-in has.
	linkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), signInTimeout)

	steps, err := svc.Link(linkCtx)
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

	if first.Code == "" && first.Image == "" {
		cancel()

		if first.Err == nil {
			first.Err = errors.New("the link ended at once")
		}

		return SignIn{}, fmt.Errorf("%w: %w", ErrSignInFailed, first.Err)
	}

	link := &deviceLink{
		label: label, ctx: linkCtx, cancel: cancel, done: make(chan struct{}), code: first.Code,
		image: first.Image, expires: first.Expires,
	}
	go s.followLink(linkCtx, d, label, link, steps)

	id := s.newID("signin")
	pending := &signIn{def: d, label: label, started: s.clock.Now(), link: link}

	s.mu.Lock()
	s.dropExpired(pending.started)
	s.signIns[id] = pending
	s.mu.Unlock()

	return SignIn{
		ID: id, ExpiresIn: signInTimeout, Code: first.Code, CodeImage: first.Image, CodeExpires: first.Expires,
	}, nil
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
		case step.Code != "" || step.Image != "":
			link.mu.Lock()
			link.code, link.image, link.expires = step.Code, step.Image, step.Expires
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
	link.code, link.image, link.conn = "", "", conn

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

	code, image, expires := pending.link.current()
	left := signInTimeout - s.clock.Now().Sub(pending.started)

	return SignIn{ID: id, ExpiresIn: max(left, 0), Code: code, CodeImage: image, CodeExpires: expires}, nil
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

// Messages lists the messages kept of a Connection of kind device, the latest first (issue #631).
func (s *Service) Messages(ctx context.Context, integration, label string, q messaging.Query,
) ([]messaging.Message, error) {
	acc, err := s.heldAccount(ctx, integration, label)
	if err != nil {
		return nil, err
	}

	return acc.Messages(ctx, q)
}
