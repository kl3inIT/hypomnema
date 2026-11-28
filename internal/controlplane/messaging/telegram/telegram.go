// Package telegram is the owner's personal Telegram account (issue #1184), as a user client of Telegram's MTProto API,
// never a bot: a bot sees only the chats it was added to and none of his direct chats (Hermes and OpenClaw's Telegram
// docs). It runs github.com/gotd/td (MIT) in the control plane's own process, as messaging/whatsapp runs whatsmeow and
// as mautrix/telegram runs gotd (pkg/connector/client.go:183-266): the client with gotd's flood-wait middleware and
// rate limit as gotd's examples/userbot sets them (main.go:153-172), the updates manager with its gap recovery, and
// the session, the update state and the access hashes in telegram.db beside whatsapp.db (store.go, session.go), a
// private file since a copy of it is the account.
//
// The owner's api_id and api_hash, which a user client must give Telegram (core.telegram.org/api/obtaining_api_id),
// are read from the vault, the secrets APIIDSecret and APIHashSecret; until they are there no account connects
// (ErrNoAPIID). Linking an account, the Bots' tools and the Inbox's tab come next (issue #1184's second part).
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/log"
	"github.com/gotd/log/logslog"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"golang.org/x/time/rate"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// The vault's secrets of the owner's Telegram application, made at my.telegram.org: one generic value each.
const (
	APIIDSecret   = "telegram-api-id"
	APIHashSecret = "telegram-api-hash"
)

// ErrNoAPIID is a Telegram account that cannot connect because the owner's application is not in the vault.
var ErrNoAPIID = errors.New("telegram: the api_id is missing: make the application at my.telegram.org and keep " +
	"its api_id and api_hash in the vault as the secrets " + APIIDSecret + " and " + APIHashSecret)

// deviceName is what the owner's list of devices in Telegram shows, as whatsapp's "Hypomnema".
const deviceName = "Hypomnema"

// maxConns lets the updates manager, a fetch and a request share the file; WAL and the busy timeout of
// internal/shared/sqlite serialize their writes, as whatsapp.db's.
const maxConns = 4

// Secrets opens the vault's secrets; *vault.Service is it.
type Secrets interface {
	Open(ctx context.Context, name string) (vault.Opened, error)
}

// Service opens the owner's Telegram accounts.
type Service struct {
	closeDB func() error
	store   *store
	states  stateStorage
	secrets Secrets
	log     *slog.Logger
}

// Open opens telegram.db at path, making it and its tables when they do not exist.
func Open(ctx context.Context, path string, secrets Secrets, logger *slog.Logger) (*Service, error) {
	db, err := sqlite.Open(ctx, sqlite.Config{Path: path})
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}

	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)

	st, err := openStore(ctx, db)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}

	return &Service{closeDB: db.Close, store: st, states: stateStorage{db: db}, secrets: secrets, log: logger}, nil
}

// Close closes telegram.db; close the accounts first.
func (s *Service) Close() error { return s.closeDB() }

// Ready says whether an account can connect: ErrNoAPIID until the owner's application is in the vault.
func (s *Service) Ready(ctx context.Context) error {
	_, _, err := s.credentials(ctx)

	return err
}

// credentials are the owner's api_id and api_hash, from the vault.
func (s *Service) credentials(ctx context.Context) (int, string, error) {
	id, err := s.secret(ctx, APIIDSecret)
	if err != nil {
		return 0, "", err
	}

	hash, err := s.secret(ctx, APIHashSecret)
	if err != nil {
		return 0, "", err
	}

	appID, err := strconv.Atoi(id)
	if err != nil || appID <= 0 {
		return 0, "", fmt.Errorf("telegram: the vault's %s is not an api_id, a number", APIIDSecret)
	}

	return appID, hash, nil
}

func (s *Service) secret(ctx context.Context, name string) (string, error) {
	opened, err := s.secrets.Open(ctx, name)
	if errors.Is(err, vault.ErrNotFound) || errors.Is(err, vault.ErrNotConfigured) {
		return "", ErrNoAPIID
	}

	if err != nil {
		return "", fmt.Errorf("telegram: opening the vault's %s: %w", name, err)
	}

	value := strings.TrimSpace(opened.Payload[vault.KeyValue])
	if value == "" {
		return "", ErrNoAPIID
	}

	return value, nil
}

// Open opens the account of that id, the owner's Telegram user id, and connects it in the background. An account
// whose session is no longer kept opens logged out, and one that cannot connect for want of the owner's application
// opens disconnected, saying so; what was kept of either is still read.
func (s *Service) Open(ctx context.Context, id string) (*Account, error) {
	uid, err := strconv.ParseInt(id, 10, 64)
	if err != nil || uid <= 0 {
		return nil, fmt.Errorf("telegram: %q is not an account's user id", id)
	}

	a := s.newAccount(uid)

	_, name, err := s.store.session(ctx, a.key)
	if errors.Is(err, errNoSession) {
		a.state = messaging.State{Status: messaging.StatusLoggedOut, Reason: "the session is no longer kept"}

		return a, nil
	}

	if err != nil {
		return nil, err
	}

	a.name = name

	appID, appHash, err := s.credentials(ctx)
	if err != nil {
		a.state = messaging.State{Status: messaging.StatusDisconnected, Reason: err.Error()}

		return a, nil //nolint:nilerr // an account that cannot connect still opens, to read what was kept
	}

	// The session outlives the request that opened it.
	a.start(context.WithoutCancel(ctx), appID, appHash)

	return a, nil
}

func (s *Service) newAccount(uid int64) *Account {
	return &Account{
		store: s.store, states: s.states, log: s.log, userID: uid, key: strconv.FormatInt(uid, 10),
		pause: waitPage, state: messaging.State{Status: messaging.StatusConnecting},
	}
}

// Account is one linked account's live session.
type Account struct {
	store  *store
	states stateStorage
	log    *slog.Logger
	// userID is the account's Telegram user id, and key the same as the mirror's account.
	userID int64
	key    string
	// pause waits between pages of a fetch (waitPage); fetchMu makes it one fetch at a time, as mautrix's
	// fetchMessagesSema (client.go:209).
	pause   func(ctx context.Context, page int) error
	fetchMu sync.Mutex
	// syncMu makes the listing of dialogs one at a time, as mautrix's syncChatsLock (chatsync.go:58-62).
	syncMu sync.Mutex

	// cancel and done end the connection that start began.
	cancel context.CancelFunc
	done   chan struct{}

	mu    sync.Mutex
	name  string
	state messaging.State
	// frozen is the state of an account Telegram froze, kept over its connections (state.go).
	frozen *messaging.State
	// client is the connection's API while it is connected; nil otherwise.
	client api
}

// ID is the account's Telegram user id.
func (a *Account) ID() string { return a.key }

// Name is the account as people know it: its number, +84…, else its name.
func (a *Account) Name() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.name
}

// State is the session's state now.
func (a *Account) State() messaging.State {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.state
}

func (a *Account) setState(st messaging.State) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.state = st
}

// connected marks the session open, or frozen when Telegram froze the account.
func (a *Account) connected() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.state = messaging.State{Status: messaging.StatusConnected}
	if a.frozen != nil {
		a.state = *a.frozen
	}
}

// froze keeps the state of an account Telegram froze.
func (a *Account) froze(st telegram.FreezeState) {
	frozen := frozenState(st)

	a.mu.Lock()
	defer a.mu.Unlock()

	a.frozen, a.state = &frozen, frozen
}

// dropped marks an open session disconnected; gotd connects it again by itself.
func (a *Account) dropped() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.state.Status == messaging.StatusConnected {
		a.state = messaging.State{Status: messaging.StatusDisconnected}
	}
}

// reconnected marks a dropped session open again once gotd connected it back; the first connection is open when
// the updates manager starts.
func (a *Account) reconnected() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.client == nil || a.state.Status != messaging.StatusDisconnected {
		return
	}

	a.state = messaging.State{Status: messaging.StatusConnected}
	if a.frozen != nil {
		a.state = *a.frozen
	}
}

func (a *Account) setClient(c api) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.client = c
}

// liveAPI is the connection's API, or why there is none.
func (a *Account) liveAPI() (api, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.client == nil {
		return nil, fmt.Errorf("telegram: the session is %s; it must be connected to ask Telegram", a.state.Status)
	}

	return a.client, nil
}

// Chats lists the account's chats kept, the latest first.
func (a *Account) Chats(ctx context.Context, limit int) ([]messaging.Chat, error) {
	return a.store.chats(ctx, a.key, limit)
}

// Messages lists kept messages, the latest first.
func (a *Account) Messages(ctx context.Context, q messaging.Query) ([]messaging.Message, error) {
	return a.store.messages(ctx, a.key, q)
}

// Close disconnects the session and keeps it, to be opened again.
func (a *Account) Close() {
	if a.cancel == nil {
		return
	}

	a.cancel()
	<-a.done
}

// Logout ends the session at Telegram (it leaves the owner's list of devices) and forgets it with what was kept of
// the account. A session Telegram already ended is only forgotten; any other failure keeps it, to try again, as
// whatsapp's Logout does.
func (a *Account) Logout(ctx context.Context) error {
	if client, err := a.liveAPI(); err == nil {
		if _, err := client.AuthLogOut(ctx); err != nil && !a.State().NeedsLink() && !terminal(err) {
			return fmt.Errorf("telegram: logging the session out: %w", err)
		}
	} else if st := a.State(); !st.NeedsLink() && st.Status != messaging.StatusReplaced {
		return err
	}

	a.Close()

	return a.store.forget(ctx, a.key, a.userID)
}

// start connects the session in the background until Close, again after each failure that may pass.
func (a *Account) start(ctx context.Context, appID int, appHash string) {
	ctx, cancel := context.WithCancel(ctx)
	a.cancel, a.done = cancel, make(chan struct{})

	go func() {
		defer close(a.done)

		a.run(ctx, appID, appHash)
	}()
}

func (a *Account) run(ctx context.Context, appID int, appHash string) {
	// gotd's own reconnection backoff (telegram/options.go:313-318), without an end.
	wait := backoff.NewExponentialBackOff()
	wait.MaxElapsedTime = 0

	for {
		err := a.connect(ctx, appID, appHash)
		if ctx.Err() != nil {
			return
		}

		a.setClient(nil)
		a.setState(stateOf(err))

		if terminal(err) {
			a.log.WarnContext(ctx, "telegram: the session ended", slog.String("account", a.key),
				slog.String("state", string(a.State().Status)), slog.String("error", errText(err)))

			return
		}

		a.log.WarnContext(ctx, "telegram: the session stopped; connecting again", slog.String("account", a.key),
			slog.String("error", errText(err)))

		select {
		case <-time.After(wait.NextBackOff()):
		case <-ctx.Done():
			return
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// connect runs one connection until it ends: gotd's client with the flood-wait middleware and the rate limit of
// examples/userbot, and the updates manager over telegram.db. What the manager cannot recover is fetched in the
// background, one fetch at a time, until the connection ends.
func (a *Account) connect(ctx context.Context, appID int, appHash string) error {
	logger := logslog.New(a.log.With(slog.String("module", "gotd"), slog.String("account", a.key)))
	jobs := &background{}
	// The manager's callbacks have no context: they run on the connection's (updatesConfig).
	manager := updates.New(a.updatesConfig(jobs, logger)) //nolint:contextcheck // As above.

	// gotd's waiter retries a request Telegram answers FLOOD_WAIT, waiting as long as it says up to a minute, five
	// times at most (gotd/contrib middleware/floodwait/waiter.go:20-24); a longer wait fails the request.
	waiter := floodwait.NewWaiter().WithCallback(func(ctx context.Context, wait floodwait.FloodWait) {
		a.log.WarnContext(ctx, "telegram: flood wait", slog.String("account", a.key), slog.String("request", wait.Name),
			slog.Duration("wait", wait.Duration))
	})

	client := telegram.NewClient(appID, appHash, telegram.Options{
		Logger:         logger,
		SessionStorage: sessionStorage{store: a.store, account: a.key},
		UpdateHandler:  manager,
		Middlewares: []telegram.Middleware{
			waiter,
			ratelimit.New(rate.Every(100*time.Millisecond), 5),
		},
		Device: telegram.DeviceConfig{DeviceModel: deviceName},
		OnAuthLost: func(_ context.Context, err error) error {
			a.setState(stateOf(err))

			return err
		},
		OnFrozen: func(_ context.Context, st telegram.FreezeState) { a.froze(st) },
		OnConnectionState: func(st telegram.ConnectionState) {
			switch st {
			case telegram.ConnectionStateDisconnected:
				a.dropped()
			case telegram.ConnectionStateReady:
				a.reconnected()
			case telegram.ConnectionStateConnecting:
			}
		},
	})

	return waiter.Run(ctx, func(ctx context.Context) error {
		return client.Run(ctx, func(ctx context.Context) error {
			self, err := client.Self(ctx)
			if err != nil {
				return err
			}

			a.named(ctx, self)

			raw := client.API()
			jobs.start(ctx, conn{api: raw, tracker: manager})
			defer jobs.stop()

			a.setClient(raw)
			defer a.setClient(nil)

			// gotd checks the freeze state on a refused request only; it is read once at the start, as its doc says to
			// (telegram/options.go:253-261).
			if st, err := client.FreezeState(ctx); err == nil && st.Frozen() {
				a.froze(st)
			}

			return manager.Run(ctx, raw, self.ID, updates.AuthOptions{
				// The listing runs on the connection's context, as every background fetch.
				OnStart: func(context.Context) { a.onStart(jobs) }, //nolint:contextcheck // As above.
			})
		})
	})
}

// updatesConfig is the updates manager's: the handler over the mirror, the state and access hashes in telegram.db, and
// what the manager reports it could not recover. gotd gives these callbacks no context: what they read or write now
// runs on the connection's (background.context), and each fetch in the background (background.spawn).
func (a *Account) updatesConfig(jobs *background, logger log.Logger) updates.Config {
	return updates.Config{
		Handler:      a.handler(),
		Storage:      a.states,
		AccessHasher: a.states,
		Logger:       logger,
		// The last message known before the gap is read now, before the channel's next updates are kept (gotd
		// telegram/updates/config.go:55-65: what lies between it and gap.Messages is to be fetched).
		OnChannelGap: func(gap updates.ChannelGap) {
			newest, err := a.newestOf(jobs.context(), channelID(gap.ChannelID))
			a.spawned(jobs.context(), "reading a channel's last message before its gap", err)

			jobs.spawn(func(ctx context.Context, c conn) {
				a.spawned(ctx, "recovering a channel's gap", a.recoverGap(ctx, c.api, newest, gap))
			})
		},
		// A channel the account lost (CHANNEL_PRIVATE on getChannelDifference, config.go:66-70) leaves the list.
		OnChannelInaccessible: func(id int64) {
			if ctx := jobs.context(); ctx != nil {
				a.spawned(ctx, "taking a lost channel off the list", a.store.unlist(ctx, a.key, channelID(id)))
			}
		},
		// A gap of the account's own updates the manager could not recover (differenceTooLong), or a state it did not
		// find (the first start after a link, or a state lost), lists every dialog again and catches each up.
		OnTooLong:             func() { a.relist(jobs) },
		OnLoadUserStateFailed: func() { a.relist(jobs) },
	}
}

// relist starts a new listing of the dialogs that catches each chat up, and runs it in the background.
func (a *Account) relist(jobs *background) {
	ctx := jobs.context()
	if ctx == nil {
		return
	}

	if err := a.store.restartDialogs(ctx, a.key, true); err != nil {
		a.spawned(ctx, "restarting the listing of dialogs", err)

		return
	}

	jobs.spawn(func(ctx context.Context, c conn) { a.spawned(ctx, "listing the dialogs", a.syncDialogs(ctx, c)) })
}

// onStart marks the session open once the updates manager runs, and goes on with a listing of the dialogs a former
// connection left unfinished, as mautrix resumes its chat sync on each connection (pkg/connector/client.go:603-612).
func (a *Account) onStart(jobs *background) {
	a.connected()
	jobs.spawn(func(ctx context.Context, c conn) { a.spawned(ctx, "listing the dialogs", a.syncDialogs(ctx, c)) })
}

// newestOf is the id of a chat's newest message kept, zero when none is or ctx is nil.
func (a *Account) newestOf(ctx context.Context, chat string) (int, error) {
	if ctx == nil {
		return 0, nil
	}

	_, newest, err := a.store.edges(ctx, a.key, chat)

	return newest, err
}

// named keeps the account's name as Telegram gives it: its number, else its name.
func (a *Account) named(ctx context.Context, self *tg.User) {
	name := userName(self)
	if self.Phone != "" {
		name = "+" + self.Phone
	}

	a.mu.Lock()
	a.name = name
	a.mu.Unlock()

	if err := a.store.nameSession(ctx, a.key, name); err != nil {
		a.log.WarnContext(ctx, "telegram: keeping the account's name", slog.String("error", err.Error()))
	}
}

// conn is one connection to Telegram, as the background fetches use it: its API, and its updates manager to track
// channels with (*updates.Manager).
type conn struct {
	api     api
	tracker channelTracker
}

// channelTracker has the updates manager track a channel; *updates.Manager is it.
type channelTracker interface {
	TrackChannel(ctx context.Context, channelID, accessHash int64, pts int) error
}

// background is the fetches the updates manager asked for during one connection.
type background struct {
	mu     sync.Mutex
	ctx    context.Context //nolint:containedctx // The connection's, which every fetch ends with.
	cancel context.CancelFunc
	conn   conn
	wg     sync.WaitGroup
}

func (b *background) start(ctx context.Context, c conn) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ctx, b.cancel = context.WithCancel(ctx)
	b.conn = c
}

// context is the connection's context, nil before it started or after it ended.
func (b *background) context() context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.ctx == nil || b.ctx.Err() != nil {
		return nil
	}

	return b.ctx
}

// spawn runs f while the connection lasts; asked before it started or after it ended, it does nothing.
func (b *background) spawn(f func(ctx context.Context, c conn)) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.ctx == nil || b.ctx.Err() != nil {
		return
	}

	ctx, c := b.ctx, b.conn

	b.wg.Go(func() { f(ctx, c) })
}

// stop ends the fetches and waits for them.
func (b *background) stop() {
	b.mu.Lock()
	if b.cancel != nil {
		b.cancel()
	}
	b.mu.Unlock()

	b.wg.Wait()
}
