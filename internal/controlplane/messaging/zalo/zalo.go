// Package zalo is the owner's personal Zalo linked by QR (issue #657, step 1 of #613), through zca-js in a bridge
// the control plane runs with the machine's Node (bridge.go), one per account: zca-js is the library OpenClaw's
// zalouser, Hermes's Zalo sidecar and zlog use, and Go has none kept up with Zalo (docs/research,
// 2026-11-16-messaging-accounts-as-the-owner.md). It is a messaging.Service as WhatsApp's is; its session and every
// chat are kept in zalo.db (store.go). The past of chats, files, the Bot's tools and sending are steps 2 and 3.
package zalo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	zalobridge "github.com/kl3inIT/hypomnema/bridges/zalo"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// DefaultNode is the machine's Node the coordinator's setup installs (deploy/coordinator/setup-coordinator.sh).
const DefaultNode = "/opt/hypomnema-node/bin/node"

const (
	// qrLife is how long a Zalo QR code is valid (zca-js src/apis/loginQR.ts: 100 s).
	qrLife = 100 * time.Second
	// loginWait bounds a login with a kept session.
	loginWait = time.Minute
	// restartAfter is the first wait before a bridge that ended is started again, doubled up to restartMax.
	restartAfter = 5 * time.Second
	restartMax   = 5 * time.Minute
	// keepTimeout bounds keeping one message or naming the groups.
	keepTimeout = 30 * time.Second
)

// ErrUnavailable is a control plane that cannot run the bridge: built without it, or without Node.
var ErrUnavailable = errors.New("zalo: this control plane cannot run the Zalo bridge")

// Service links and opens Zalo accounts.
type Service struct {
	closeDB func() error
	mirror  *mirror
	log     *slog.Logger
	// start runs a bridge; the machine's Node on the bundle outside tests.
	start func(context.Context) (*bridge, error)
}

// Open opens zalo.db at path and writes the bridge beside it, to run with node (DefaultNode when empty).
func Open(ctx context.Context, path, node string, logger *slog.Logger) (*Service, error) {
	bundle, ok := zalobridge.Bundle()
	if !ok {
		return nil, fmt.Errorf("%w: it was built without the bridge (make zalo-bridge-build)", ErrUnavailable)
	}

	if node == "" {
		node = DefaultNode
	}

	if _, err := os.Stat(node); err != nil {
		return nil, fmt.Errorf("%w: no Node at %s: %w", ErrUnavailable, node, err)
	}

	script := filepath.Join(filepath.Dir(path), "zalo-bridge.cjs")
	if err := os.WriteFile(script, bundle, 0o600); err != nil {
		return nil, fmt.Errorf("zalo: writing the bridge: %w", err)
	}

	svc, err := open(ctx, path, logger)
	if err != nil {
		return nil, err
	}

	svc.start = func(ctx context.Context) (*bridge, error) { return startBridge(ctx, node, script, logger) }

	return svc, nil
}

func open(ctx context.Context, path string, logger *slog.Logger) (*Service, error) {
	db, err := sqlite.Open(ctx, sqlite.Config{Path: path})
	if err != nil {
		return nil, fmt.Errorf("zalo: %w", err)
	}

	kept, err := openMirror(ctx, db)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return &Service{closeDB: db.Close, mirror: kept, log: logger}, nil
}

// Close closes zalo.db; close the accounts first.
func (s *Service) Close() error { return s.closeDB() }

// whoAmI is the account a login opened (bridge.mjs, whoAmI).
type whoAmI struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
}

// Link starts linking an account: Zalo's QR image until the phone confirms, then the account, as WhatsApp's
// linking gives its codes (whatsapp.Service.Link).
func (s *Service) Link(ctx context.Context) (<-chan messaging.LinkStep, error) {
	// The bridge outlives the linking: the account it logs in stops it.
	b, err := s.start(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}

	steps := make(chan messaging.LinkStep, 1)

	go s.follow(ctx, b, steps)

	return steps, nil
}

type loggedIn struct {
	who whoAmI
	err error
}

func (s *Service) follow(ctx context.Context, b *bridge, steps chan<- messaging.LinkStep) {
	defer close(steps)

	done := make(chan loggedIn, 1)

	go func() {
		var who whoAmI

		err := b.call(ctx, "loginQR", map[string]any{}, &who)
		done <- loggedIn{who: who, err: err}
	}()

	send := func(step messaging.LinkStep) bool {
		select {
		case steps <- step:
			return true
		case <-ctx.Done():
			return false
		}
	}

	var credentials json.RawMessage

	for {
		select {
		case e, ok := <-b.events:
			if !ok {
				send(messaging.LinkStep{Err: errBridgeEnded})

				return
			}

			switch e.Name {
			case "qr":
				var qr struct {
					Image string `json:"image"`
				}

				if json.Unmarshal(e.Data, &qr) == nil && qr.Image != "" &&
					!send(messaging.LinkStep{Image: "data:image/png;base64," + qr.Image, Expires: time.Now().Add(qrLife)}) {
					b.stop()

					return
				}
			case "credentials":
				credentials = e.Data
			}
		case r := <-done:
			if r.err != nil || len(credentials) == 0 {
				b.stop()

				if r.err == nil {
					r.err = errors.New("zalo: the phone confirmed, but Zalo gave no session")
				}

				send(messaging.LinkStep{Err: r.err})

				return
			}

			if err := s.mirror.saveSession(context.WithoutCancel(ctx), r.who.UID, r.who.Name, credentials); err != nil {
				b.stop()
				send(messaging.LinkStep{Err: err})

				return
			}

			// The session lives until the account is closed, whatever the linking's end.
			life, cancel := context.WithCancel(context.WithoutCancel(ctx))
			acc := s.newAccount(cancel, r.who, credentials)
			acc.attach(life, b)

			if !send(messaging.LinkStep{Account: acc}) {
				// Nobody takes the account: the session is kept, to be opened again.
				acc.Close()
			}

			return
		case <-ctx.Done():
			b.stop()

			return
		}
	}
}

// Open opens an account linked before, with its kept session. One whose session is not kept opens logged out.
func (s *Service) Open(ctx context.Context, id string) (messaging.Account, error) {
	credentials, name, ok, err := s.mirror.session(ctx, id)
	if err != nil {
		return nil, err
	}

	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	acc := s.newAccount(cancel, whoAmI{UID: id, Name: name}, credentials)
	if !ok {
		acc.setState(messaging.State{Status: messaging.StatusLoggedOut, Reason: "no session of it is kept"})

		return acc, nil
	}

	go acc.run(life)

	return acc, nil
}

// newAccount makes an account; cancel ends its session's context, as Close does.
func (s *Service) newAccount(cancel context.CancelFunc, who whoAmI, credentials json.RawMessage) *account {
	return &account{
		svc: s, uid: who.UID, name: who.Name, credentials: credentials, cancel: cancel,
		state: messaging.State{Status: messaging.StatusConnecting},
	}
}

// account is one Zalo account's live session: its bridge, started again after it ends until the account is closed.
type account struct {
	svc         *Service
	uid, name   string
	credentials json.RawMessage

	// cancel ends the session's context, which its bridge and its work run in.
	cancel context.CancelFunc

	mu    sync.Mutex
	state messaging.State
	b     *bridge
}

func (a *account) ID() string   { return a.uid }
func (a *account) Name() string { return a.name }

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

// attach takes over a bridge that is logged in, as the link leaves it, and follows it.
func (a *account) attach(ctx context.Context, b *bridge) {
	a.mu.Lock()
	a.b = b
	a.state = messaging.State{Status: messaging.StatusConnected}
	a.mu.Unlock()

	go func() {
		a.follow(ctx, b)
		a.run(ctx)
	}()
}

// run keeps a bridge logged in with the kept session until the account is closed, as zlog's session manager
// reconnects its clients: a bridge that ends is started again after a wait that doubles. A session Zalo refuses
// is logged out; one another Zalo Web took over waits, as WhatsApp's replaced session does.
func (a *account) run(ctx context.Context) {
	wait := restartAfter

	for {
		if ctx.Err() != nil {
			return
		}

		if st := a.State().Status; st == messaging.StatusLoggedOut || st == messaging.StatusReplaced {
			return
		}

		b, err := a.svc.start(ctx)
		if err == nil {
			err = a.login(ctx, b)
		}

		if err == nil {
			wait = restartAfter

			a.follow(ctx, b)
		} else {
			a.svc.log.WarnContext(ctx, "zalo: the session could not be opened", slog.String("account", a.uid),
				slog.String("error", err.Error()))
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		wait = min(wait*2, restartMax)
	}
}

func (a *account) login(ctx context.Context, b *bridge) error {
	ctx, cancel := context.WithTimeout(ctx, loginWait)
	defer cancel()

	a.mu.Lock()
	credentials := a.credentials
	a.mu.Unlock()

	var who whoAmI
	if err := b.call(ctx, "login", map[string]any{"credentials": credentials}, &who); err != nil {
		b.stop()
		a.setState(messaging.State{Status: messaging.StatusDisconnected, Reason: err.Error()})

		return err
	}

	a.mu.Lock()
	a.b = b
	a.state = messaging.State{Status: messaging.StatusConnected}

	if who.Name != "" {
		a.name = who.Name
	}
	a.mu.Unlock()

	return nil
}

// follow reads a logged-in bridge's events until it ends.
func (a *account) follow(ctx context.Context, b *bridge) {
	go a.nameGroups(ctx, b)

	for e := range b.events {
		switch e.Name {
		case "message":
			a.keep(ctx, e.Data)
		case "connected":
			a.setState(messaging.State{Status: messaging.StatusConnected})
		case "credentials":
			a.renew(ctx, e.Data)
		case "closed":
			var c struct {
				Code     int    `json:"code"`
				Reason   string `json:"reason"`
				Replaced bool   `json:"replaced"`
			}

			_ = json.Unmarshal(e.Data, &c)

			if c.Replaced {
				// Another Zalo Web took the session: it is not taken back, as zca-js's README says only one runs.
				a.setState(messaging.State{Status: messaging.StatusReplaced, Reason: c.Reason})
				b.stop()
			} else {
				a.setState(messaging.State{Status: messaging.StatusDisconnected, Reason: c.Reason})
			}
		case "error":
			a.svc.log.WarnContext(ctx, "zalo: the bridge reported an error", slog.String("account", a.uid),
				slog.String("error", string(e.Data)))
		}
	}

	a.mu.Lock()
	if a.b == b {
		a.b = nil
	}

	if a.state.Status == messaging.StatusConnected {
		a.state = messaging.State{Status: messaging.StatusDisconnected, Reason: "the bridge ended"}
	}
	a.mu.Unlock()
}

// renew keeps the cookies Zalo renewed, so that the next start logs in with them.
func (a *account) renew(ctx context.Context, credentials json.RawMessage) {
	ctx, cancel := context.WithTimeout(ctx, keepTimeout)
	defer cancel()

	a.mu.Lock()
	a.credentials = credentials
	a.mu.Unlock()

	if err := a.svc.mirror.saveSession(ctx, a.uid, "", credentials); err != nil {
		a.svc.log.WarnContext(ctx, "zalo: renewed cookies could not be kept", slog.String("error", err.Error()))
	}
}

// group is a group as the bridge lists it.
type group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// nameGroups keeps every group's name, which a message does not carry.
func (a *account) nameGroups(ctx context.Context, b *bridge) {
	ctx, cancel := context.WithTimeout(ctx, loginWait)
	defer cancel()

	var groups []group
	if err := b.call(ctx, "groups", map[string]any{}, &groups); err != nil {
		a.svc.log.WarnContext(ctx, "zalo: the groups could not be listed", slog.String("error", err.Error()))

		return
	}

	for _, g := range groups {
		if err := a.svc.mirror.nameChat(ctx, a.uid, g.ID, "group", g.Name); err != nil {
			a.svc.log.WarnContext(ctx, "zalo: a group's name could not be kept", slog.String("error", err.Error()))
		}
	}
}

// incoming is a message as the bridge gives it (bridges/zalo/src/protocol.mjs, message).
type incoming struct {
	ID         string `json:"id"`
	CliID      string `json:"cliId"`
	Chat       string `json:"chat"`
	Group      bool   `json:"group"`
	From       string `json:"from"`
	FromName   string `json:"fromName"`
	FromMe     bool   `json:"fromMe"`
	At         int64  `json:"at"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	QuoteID    string `json:"quoteId"`
	Attachment *struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"attachment"`
}

func (a *account) keep(ctx context.Context, raw json.RawMessage) {
	var m incoming
	if err := json.Unmarshal(raw, &m); err != nil || m.ID == "" || m.Chat == "" {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, keepTimeout)
	defer cancel()

	k := kept{
		account: a.uid, chat: m.Chat, chatKind: "dm", id: m.ID, cliID: m.CliID, sender: m.From,
		senderName: m.FromName, at: time.UnixMilli(m.At), fromMe: m.FromMe, kind: m.Kind, text: m.Text,
		quotedID: m.QuoteID,
	}

	if m.Group {
		k.chatKind = "group"
		k.chatName = a.svc.mirror.chatName(ctx, a.uid, m.Chat)
	} else if !m.FromMe {
		// A direct chat is named after the person in it, as their messages carry their name.
		k.chatName = m.FromName
	}

	if m.Attachment != nil {
		k.attachmentURL, k.attachmentTitle = m.Attachment.URL, m.Attachment.Title
	}

	if err := a.svc.mirror.keep(ctx, k); err != nil {
		a.svc.log.WarnContext(ctx, "zalo: a message could not be kept", slog.String("error", err.Error()))
	}
}

// Close stops the bridge and keeps the session, to be opened again.
func (a *account) Close() {
	a.cancel()

	a.mu.Lock()
	b := a.b
	a.mu.Unlock()

	if b != nil {
		b.stop()
	}
}

// Logout forgets the session and everything kept of the account. Zalo has no call that drops a web session from
// the phone: the owner removes it there (Zalo, Settings, Account and security, Login devices).
func (a *account) Logout(ctx context.Context) error {
	a.Close()
	a.setState(messaging.State{Status: messaging.StatusLoggedOut})

	return a.svc.mirror.forget(ctx, a.uid)
}

func (a *account) Chats(ctx context.Context, limit int) ([]messaging.Chat, error) {
	return a.svc.mirror.chats(ctx, a.uid, limit)
}

func (a *account) Messages(ctx context.Context, q messaging.Query) ([]messaging.Message, error) {
	return a.svc.mirror.messages(ctx, a.uid, q)
}

// errLater is what steps 2 and 3 of #613 add.
func errLater(what string) error {
	return fmt.Errorf("%w: Zalo %s comes in a later step of issue #613", orchestration.ErrUnavailable, what)
}

func (a *account) Media(context.Context, string, string) (messaging.Media, error) {
	return messaging.Media{}, errLater("files")
}

func (a *account) Backfill(context.Context, string, int) (messaging.Backfilled, error) {
	return messaging.Backfilled{}, errLater("older messages")
}

func (a *account) Send(context.Context, messaging.Outgoing) (messaging.Message, error) {
	return messaging.Message{}, errLater("sending")
}
