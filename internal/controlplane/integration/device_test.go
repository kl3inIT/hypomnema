package integration_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/integration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// phone is the messaging service as the owner's phone drives it: each linking shows the codes the test sends,
// until the test scans one with a number.
type phone struct {
	mu      sync.Mutex
	linking chan messaging.LinkStep
	devices map[string]*device
	opened  []string
	// pictures makes its codes pictures, as Zalo gives them.
	pictures bool
}

type device struct {
	id, name string
	mu       sync.Mutex
	state    messaging.State
	out      bool
}

func (d *device) ID() string   { return d.id }
func (d *device) Name() string { return d.name }
func (d *device) State() messaging.State {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.state
}

func (d *device) Logout(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.out = true

	return nil
}
func (d *device) Close() {}

func (d *device) Chats(context.Context, int) ([]messaging.Chat, error) {
	return []messaging.Chat{{ID: "120363@g.us", Kind: "group", Name: "Team"}}, nil
}

func (d *device) Media(context.Context, string, string) (messaging.Media, error) {
	return messaging.Media{}, nil
}

func (d *device) Send(context.Context, messaging.Outgoing) (messaging.Message, error) {
	return messaging.Message{}, nil
}

func (d *device) Backfill(context.Context, string, int) (messaging.Backfilled, error) {
	return messaging.Backfilled{}, nil
}

func (d *device) Messages(_ context.Context, q messaging.Query) ([]messaging.Message, error) {
	return []messaging.Message{{ID: "m1", Chat: q.Chat, Text: "hello"}}, nil
}

func (d *device) loggedOut() bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.out
}

func (p *phone) Link(context.Context) (<-chan messaging.LinkStep, error) {
	steps := make(chan messaging.LinkStep, 4)
	steps <- p.step("code-1")

	p.mu.Lock()
	p.linking = steps
	p.mu.Unlock()

	return steps, nil
}

func (p *phone) Open(_ context.Context, id string) (messaging.Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.opened = append(p.opened, id)

	d, ok := p.devices[id]
	if !ok {
		return nil, errors.New("no such device")
	}

	return d, nil
}

// show sends the linking a new code.
func (p *phone) show(code string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.linking <- p.step(code)
}

// step is a code to show: what it says, or a picture of it for a service that gives pictures (Zalo).
func (p *phone) step(code string) messaging.LinkStep {
	if p.pictures {
		return messaging.LinkStep{Image: "data:image/png;base64," + code, Expires: time.Now().Add(100 * time.Second)}
	}

	return messaging.LinkStep{Code: code, Expires: time.Now().Add(20 * time.Second)}
}

// scan links a device for the number and ends the linking.
func (p *phone) scan(id, number string) *device {
	d := &device{id: id, name: number, state: messaging.State{Status: messaging.StatusConnected}}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.devices[id] = d
	p.linking <- messaging.LinkStep{Account: d}
	close(p.linking)

	return d
}

func deviceWorld(t *testing.T) (*world, *phone) {
	t.Helper()

	w := newWorld(t)
	p := &phone{devices: map[string]*device{}}
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, integration.WhatsApp()).
		WithDevices(integration.WhatsAppID, p)

	return w, p
}

// waitForCode reads the linking's code until it is want.
func waitForCode(t *testing.T, w *world, id, want string) {
	t.Helper()

	require.Eventually(t, func() bool {
		got, err := w.svc.Code(t.Context(), integration.WhatsAppID, id)

		return err == nil && got.Code == want
	}, 5*time.Second, 10*time.Millisecond)
}

// Scenario: the owner links his WhatsApp (issue #615). Starting the link answers a QR code with no address; the
// page reads the code again as the phone's code changes; completing waits until the phone scanned one, and then
// the Connection is his number with its session connected. Linking the label again replaces the device and logs
// the old one out. When the control plane starts again, the device is opened by the id the vault names; a
// session the phone logged out needs a link; disconnecting logs the device out and forgets the Connection.
func TestTheOwnerLinksHisWhatsAppByQR(t *testing.T) {
	t.Parallel()

	w, p := deviceWorld(t)
	ctx := t.Context()

	started, err := w.svc.Begin(ctx, integration.WhatsAppID, "main", nil)
	require.NoError(t, err)
	assert.Empty(t, started.URL)
	assert.Equal(t, "code-1", started.Code)
	assert.False(t, started.CodeExpires.IsZero())

	p.show("code-2")
	waitForCode(t, w, started.ID, "code-2")

	done := make(chan integration.Connection, 1)

	go func() {
		conn, err := w.svc.Complete(ctx, integration.WhatsAppID, started.ID, "")
		assert.NoError(t, err)
		done <- conn
	}()

	first := p.scan("84912345678:11@s.whatsapp.net", "+84912345678")
	conn := <-done
	assert.Equal(t, "+84912345678", conn.Account)
	require.NotNil(t, conn.Session)
	assert.Equal(t, messaging.StatusConnected, conn.Session.Status)
	assert.False(t, conn.SignInRequired)

	_, err = w.svc.Code(ctx, integration.WhatsAppID, started.ID)
	require.ErrorIs(t, err, integration.ErrNotFound, "a completed link has no code")

	again, err := w.svc.Begin(ctx, integration.WhatsAppID, "main", nil)
	require.NoError(t, err)
	p.scan("84912345678:12@s.whatsapp.net", "+84912345678")
	_, err = w.svc.Complete(ctx, integration.WhatsAppID, again.ID, "")
	require.NoError(t, err)
	assert.True(t, first.loggedOut(), "the phone keeps only the new device")

	// The control plane starts again: the device is opened by the id the vault names.
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, integration.WhatsApp()).
		WithDevices(integration.WhatsAppID, p)
	require.NoError(t, w.svc.OpenDevices(ctx))
	assert.Equal(t, []string{"84912345678:12@s.whatsapp.net"}, p.opened)

	second := p.devices["84912345678:12@s.whatsapp.net"]
	second.mu.Lock()
	second.state = messaging.State{Status: messaging.StatusLoggedOut, Reason: "401"}
	second.mu.Unlock()

	all, err := w.svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, integration.KindDevice, all[0].CredentialKind())
	require.Len(t, all[0].Connections, 1)
	assert.True(t, all[0].Connections[0].SignInRequired, "a session the phone logged out needs a link")

	loggedOut, err := w.svc.Disconnect(ctx, integration.WhatsAppID, "main")
	require.NoError(t, err)
	assert.True(t, loggedOut)
	assert.True(t, second.loggedOut())

	secrets, err := w.vault.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, secrets, "nothing of the device is kept")
}

// Zalo gives its QR code as a picture, with nothing to draw it from (issue #657): starting the link answers the
// picture, and the page reads the next one as it changes.
func TestALinkWhoseServiceGivesPicturesShowsThem(t *testing.T) {
	t.Parallel()

	w, p := deviceWorld(t)
	p.pictures = true
	ctx := t.Context()

	started, err := w.svc.Begin(ctx, integration.WhatsAppID, "main", nil)
	require.NoError(t, err)
	assert.Empty(t, started.Code)
	assert.Equal(t, "data:image/png;base64,code-1", started.CodeImage)
	assert.False(t, started.CodeExpires.IsZero())

	p.show("code-2")
	require.Eventually(t, func() bool {
		got, err := w.svc.Code(ctx, integration.WhatsAppID, started.ID)

		return err == nil && got.CodeImage == "data:image/png;base64,code-2"
	}, 5*time.Second, 10*time.Millisecond)
}

// A linking that ended without a scan answers how it ended, and keeps nothing.
func TestALinkNoPhoneScannedKeepsNothing(t *testing.T) {
	t.Parallel()

	w, p := deviceWorld(t)
	ctx := t.Context()

	started, err := w.svc.Begin(ctx, integration.WhatsAppID, "main", nil)
	require.NoError(t, err)

	p.mu.Lock()
	p.linking <- messaging.LinkStep{Err: errors.New("no code was scanned in time")}
	close(p.linking)
	p.mu.Unlock()

	_, err = w.svc.Complete(ctx, integration.WhatsAppID, started.ID, "")
	require.ErrorIs(t, err, integration.ErrSignInFailed)
	assert.Contains(t, err.Error(), "no code was scanned in time")

	all, err := w.svc.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, all[0].Connections)
}

// A link of a label still waiting when the label is disconnected is ended: the phone scanning it afterwards
// brings nothing back, and the device it linked is logged out (review of #622).
func TestDisconnectingALabelEndsItsWaitingLink(t *testing.T) {
	t.Parallel()

	w, p := deviceWorld(t)
	ctx := t.Context()

	first, err := w.svc.Begin(ctx, integration.WhatsAppID, "main", nil)
	require.NoError(t, err)
	p.scan("84912345678:11@s.whatsapp.net", "+84912345678")
	_, err = w.svc.Complete(ctx, integration.WhatsAppID, first.ID, "")
	require.NoError(t, err)

	waiting, err := w.svc.Begin(ctx, integration.WhatsAppID, "main", nil)
	require.NoError(t, err)

	_, err = w.svc.Disconnect(ctx, integration.WhatsAppID, "main")
	require.NoError(t, err)

	late := p.scan("84912345678:12@s.whatsapp.net", "+84912345678")
	_, err = w.svc.Complete(ctx, integration.WhatsAppID, waiting.ID, "")
	require.ErrorIs(t, err, integration.ErrSignInFailed)
	assert.True(t, late.loggedOut(), "the device linked after the disconnect does not stay on the phone")

	all, err := w.svc.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, all[0].Connections)
}
