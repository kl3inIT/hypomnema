// Package whatsapp is the owner's WhatsApp account linked as a device of his (issue #615, step 1 of #612), on
// whatsmeow in the control plane's own process: Go needs no bridge, which MemoryOS runs only because the JVM has no
// WhatsApp Web library (MemoryOS ADR 0031). Its sessions are whatsmeow's own tables, in a SQLite file of their own
// beside the control plane's database, which keeps a single connection (internal/shared/sqlite); the file is private
// as every database is, since a copy of it can take over the number (ADR 0031).
//
// The client's settings and the session's states are MemoryOS's (memoryos-integration-whatsapp/bridge/bots.go:
// open, pair, handle). No message is kept yet: step 2 of #612 does.
package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// deviceName is what the phone's Linked devices list shows, as MemoryOS's shows "MemoryOS" (runbook, Linking).
const deviceName = "Hypomnema"

// maxConns lets whatsmeow's event handlers and a request share the file; WAL and the busy timeout of
// internal/shared/sqlite serialize their writes.
const maxConns = 4

// ErrNoSession is opening an account whose session the file does not hold.
var ErrNoSession = errors.New("whatsapp: no session of that device is kept")

// Service links and opens WhatsApp accounts.
type Service struct {
	closeDB   func() error
	container *sqlstore.Container
	log       *slog.Logger
	// connect connects a client; whatsmeow's own outside tests.
	connect func(context.Context, *whatsmeow.Client) error
}

// Open opens the sessions' database at path, making it when it does not exist, and brings whatsmeow's tables to
// its version.
func Open(ctx context.Context, path string, logger *slog.Logger) (*Service, error) {
	db, err := sqlite.Open(ctx, sqlite.Config{Path: path})
	if err != nil {
		return nil, fmt.Errorf("whatsapp: %w", err)
	}

	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)

	// whatsmeow names SQLite "sqlite3" whatever the driver; the probe of #615 ran it on modernc.org/sqlite.
	container := sqlstore.NewWithDB(db, "sqlite3", waLogger{logger.With(slog.String("module", "whatsmeow/store"))})
	if err := container.Upgrade(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("whatsapp: preparing the sessions' tables: %w", err), db.Close())
	}

	store.SetOSInfo(deviceName, [3]uint32{1, 0, 0})

	return &Service{
		closeDB: db.Close, container: container, log: logger,
		connect: func(ctx context.Context, c *whatsmeow.Client) error { return c.ConnectContext(ctx) },
	}, nil
}

// Close closes the sessions' database; close the accounts first.
func (s *Service) Close() error { return s.closeDB() }

// Link starts linking a new device: a QR code at a time until the phone scans one, as MemoryOS's bridge pairs
// (bots.go, open and pair).
func (s *Service) Link(ctx context.Context) (<-chan messaging.LinkStep, error) {
	device := s.container.NewDevice()
	acc := s.newAccount(device)

	codes, err := acc.client.GetQRChannel(ctx)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: starting the link: %w", err)
	}

	// The session outlives the request that linked it: only the QR codes end with ctx.
	if err := s.connect(context.WithoutCancel(ctx), acc.client); err != nil {
		acc.client.Disconnect()

		return nil, fmt.Errorf("whatsapp: reaching WhatsApp: %w", err)
	}

	steps := make(chan messaging.LinkStep, 1)

	go func() {
		defer close(steps)

		for item := range codes {
			step, last := s.linkStep(acc, item)
			steps <- step

			if last {
				return
			}
		}

		acc.client.Disconnect()
		steps <- messaging.LinkStep{Err: errors.New("whatsapp: the link ended before a code was scanned")}
	}()

	return steps, nil
}

// linkStep turns one item of whatsmeow's QR channel into a step, and says whether it is the last.
func (s *Service) linkStep(acc *account, item whatsmeow.QRChannelItem) (messaging.LinkStep, bool) {
	switch {
	case item.Event == whatsmeow.QRChannelEventCode:
		return messaging.LinkStep{Code: item.Code, Expires: time.Now().Add(item.Timeout)}, false
	case item == whatsmeow.QRChannelSuccess:
		// The device's JID is set once the phone paired it (whatsmeow's handlePairSuccess).
		if acc.client.Store.ID == nil {
			acc.client.Disconnect()

			return messaging.LinkStep{Err: errors.New("whatsapp: the phone paired a device that has no id")}, true
		}

		acc.setState(messaging.State{Status: messaging.StatusConnected})

		return messaging.LinkStep{Account: acc}, true
	}

	acc.client.Disconnect()

	switch item {
	case whatsmeow.QRChannelTimeout:
		return messaging.LinkStep{Err: errors.New("whatsapp: no code was scanned in time; link again")}, true
	case whatsmeow.QRChannelClientOutdated:
		return messaging.LinkStep{Err: errors.New("whatsapp: WhatsApp refuses this client's version; " +
			"whatsmeow must be updated")}, true
	case whatsmeow.QRChannelScannedWithoutMultidevice:
		return messaging.LinkStep{Err: errors.New("whatsapp: the phone scanned without multi-device; " +
			"update WhatsApp on the phone")}, true
	}

	if item.Error != nil {
		return messaging.LinkStep{Err: fmt.Errorf("whatsapp: the link failed: %w", item.Error)}, true
	}

	return messaging.LinkStep{Err: fmt.Errorf("whatsapp: the link ended: %s", item.Event)}, true
}

// Open opens a device linked before and connects it. One whatsmeow forgot, because WhatsApp logged it out,
// opens logged out (MemoryOS bots.go, device).
func (s *Service) Open(ctx context.Context, id string) (messaging.Account, error) {
	jid, err := types.ParseJID(id)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: device %q: %w", id, err)
	}

	device, err := s.container.GetDevice(ctx, jid)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: reading the session of %s: %w", id, err)
	}

	if device == nil {
		return &gone{id: id, name: phoneOf(jid)}, nil
	}

	acc := s.newAccount(device)
	if err := s.connect(context.WithoutCancel(ctx), acc.client); err != nil {
		// whatsmeow reconnects by itself only once it was connected; this one is tried again at the next start.
		acc.setState(messaging.State{Status: messaging.StatusDisconnected, Reason: err.Error()})
		s.log.WarnContext(ctx, "whatsapp: the session could not connect", slog.String("device", id),
			slog.String("error", err.Error()))
	}

	return acc, nil
}

func (s *Service) newAccount(device *store.Device) *account {
	client := whatsmeow.NewClient(device, waLogger{s.log.With(slog.String("module", "whatsmeow"))})
	client.EnableAutoReconnect = true

	acc := &account{client: client, state: messaging.State{Status: messaging.StatusConnecting}}
	client.AddEventHandler(acc.handle)

	return acc
}

// account is one device's live session.
type account struct {
	client *whatsmeow.Client

	mu    sync.Mutex
	state messaging.State
}

func (a *account) ID() string {
	if id := a.client.Store.ID; id != nil {
		return id.String()
	}

	return ""
}

func (a *account) Name() string {
	if id := a.client.Store.ID; id != nil {
		return phoneOf(*id)
	}

	return ""
}

func (a *account) State() messaging.State {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.state
}

func (a *account) setState(st messaging.State) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.state = st
}

// handle follows the session's state from whatsmeow's events, as MemoryOS's bridge does (bots.go, handle).
func (a *account) handle(evt any) {
	switch e := evt.(type) {
	case *events.Connected:
		a.setState(messaging.State{Status: messaging.StatusConnected})
	case *events.Disconnected:
		// A session logged out, banned or replaced is not merely disconnected.
		if a.State().Status == messaging.StatusConnected {
			a.setState(messaging.State{Status: messaging.StatusDisconnected})
		}
	case *events.LoggedOut:
		a.setState(messaging.State{Status: messaging.StatusLoggedOut, Reason: e.Reason.String()})
	case *events.StreamReplaced:
		a.setState(messaging.State{Status: messaging.StatusReplaced})
	case *events.TemporaryBan:
		st := messaging.State{Status: messaging.StatusBanned, Reason: e.Code.String()}
		if e.Expire > 0 {
			st.Until = time.Now().Add(e.Expire)
		}

		a.setState(st)
	case *events.ClientOutdated:
		a.setState(messaging.State{Status: messaging.StatusOutdated})
	}
}

// Logout ends the link on WhatsApp, which drops the device from the phone and makes whatsmeow forget it. A session
// that cannot reach WhatsApp is forgotten here; the phone drops it when WhatsApp ends it.
func (a *account) Logout(ctx context.Context) error {
	err := a.client.Logout(ctx)
	if err == nil {
		return nil
	}

	a.client.Disconnect()

	if a.client.Store.ID == nil {
		return nil
	}

	if delErr := a.client.Store.Delete(ctx); delErr != nil {
		return fmt.Errorf("whatsapp: forgetting the session after the logout failed (%w): %w", err, delErr)
	}

	return nil
}

func (a *account) Close() { a.client.Disconnect() }

// gone is a device whatsmeow no longer holds: it was logged out while the control plane was not running.
type gone struct{ id, name string }

func (g *gone) ID() string   { return g.id }
func (g *gone) Name() string { return g.name }
func (g *gone) State() messaging.State {
	return messaging.State{Status: messaging.StatusLoggedOut, Reason: "the session is no longer kept"}
}
func (g *gone) Logout(context.Context) error { return nil }
func (g *gone) Close()                       {}

// phoneOf is a number as people write it: +84912345678.
func phoneOf(jid types.JID) string { return "+" + jid.User }

// waLogger writes whatsmeow's log to the control plane's; its debug lines are left out.
type waLogger struct{ l *slog.Logger }

func (w waLogger) Errorf(msg string, args ...any) { w.l.Error(fmt.Sprintf(msg, args...)) }
func (w waLogger) Warnf(msg string, args ...any)  { w.l.Warn(fmt.Sprintf(msg, args...)) }
func (w waLogger) Infof(msg string, args ...any)  { w.l.Info(fmt.Sprintf(msg, args...)) }
func (w waLogger) Debugf(string, ...any)          {}

func (w waLogger) Sub(module string) waLog.Logger { //nolint:ireturn // whatsmeow's interface.
	return waLogger{w.l.With(slog.String("module", module))}
}
