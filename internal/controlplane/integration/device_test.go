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
	// answers are what the owner typed for its questions, field=value.
	answers chan string
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

// telephone is a messaging service that also links by phone number (Telegram, issue #1184): its linking by phone
// asks for the number first.
type telephone struct{ *phone }

func (p telephone) LinkByPhone(context.Context) (<-chan messaging.LinkStep, error) {
	steps := make(chan messaging.LinkStep, 4)
	steps <- p.question(messaging.AskPhone, "", false)

	p.mu.Lock()
	p.linking = steps
	p.mu.Unlock()

	return steps, nil
}

// question is a step that asks the owner to type field; his answers come to the test on p.answers.
func (p *phone) question(field, hint string, again bool) messaging.LinkStep {
	return messaging.LinkStep{Ask: &messaging.Ask{
		Field: field, Hint: hint, Again: again,
		Answer: func(ctx context.Context, value string) error {
			select {
			case p.answers <- field + "=" + value:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}}
}

// ask sends the linking a question.
func (p *phone) ask(field, hint string, again bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.linking <- p.question(field, hint, again)
}

// answerWorld is deviceWorld whose service links by phone too and hears the owner's answers.
func answerWorld(t *testing.T) (*world, *phone) {
	t.Helper()

	w := newWorld(t)
	p := &phone{devices: map[string]*device{}, answers: make(chan string, 4)}
	w.svc = integration.New(w.orch, w.vault, w.clock, platform.NewID, integration.Telegram()).
		WithDevices(integration.TelegramID, telephone{p})

	return w, p
}

// answer gives the linking the owner's answer while the service plays the next step after it.
func answer(t *testing.T, w *world, p *phone, id, value string, next func()) (integration.SignIn, string) {
	t.Helper()

	type got struct {
		in  integration.SignIn
		err error
	}

	done := make(chan got, 1)

	go func() {
		in, err := w.svc.Answer(t.Context(), integration.TelegramID, id, value)
		done <- got{in, err}
	}()

	var heard string

	select {
	case heard = <-p.answers:
	case <-time.After(5 * time.Second):
		t.Fatal("the service never heard the answer")
	}

	next()

	r := <-done
	require.NoError(t, r.err)

	return r.in, heard
}

// Scenario: issue #1184, the owner links his Telegram by QR and his account has a two-step password. The QR code
// shows first; once his phone accepted it, the link asks for the password instead of a code, and the page answers
// it; then the Connection is his.
func TestALinkByQRAsksForThePasswordOfTheAccount(t *testing.T) {
	t.Parallel()

	w, p := answerWorld(t)
	ctx := t.Context()

	started, err := w.svc.Begin(ctx, integration.TelegramID, "main", nil)
	require.NoError(t, err)
	assert.Equal(t, "code-1", started.Code)
	assert.Nil(t, started.Ask, "a QR code asks nothing yet")

	done := make(chan error, 1)

	go func() {
		_, err := w.svc.Complete(ctx, integration.TelegramID, started.ID, "")
		done <- err
	}()

	p.ask(messaging.AskPassword, "my cat", false)
	require.Eventually(t, func() bool {
		got, err := w.svc.Code(ctx, integration.TelegramID, started.ID)

		return err == nil && got.Ask != nil && got.Ask.Field == messaging.AskPassword && got.Code == ""
	}, 5*time.Second, 10*time.Millisecond, "the QR code gives way to the password")

	got, err := w.svc.Code(ctx, integration.TelegramID, started.ID)
	require.NoError(t, err)
	assert.Equal(t, "my cat", got.Ask.Hint)

	_, heard := answer(t, w, p, started.ID, " hunter2", func() { p.scan("user:42", "+84912345678") })
	assert.Equal(t, "password= hunter2", heard, "a password is given as typed")
	require.NoError(t, <-done)

	all, err := w.svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, all[0].Connections, 1)
	assert.Equal(t, "+84912345678", all[0].Connections[0].Account)
}

// Scenario: issue #1184, the QR code cannot be scanned, so the owner links by his phone number: the link asks for
// the number, then for the code Telegram sent; a wrong code asks for it again, and the right one links the account.
// The answers keep their spaces trimmed, a password's excepted.
func TestALinkByPhoneAsksTheNumberThenTheCodeAgainWhenWrong(t *testing.T) {
	t.Parallel()

	w, p := answerWorld(t)
	ctx := t.Context()

	started, err := w.svc.LinkByPhone(ctx, integration.TelegramID, "main")
	require.NoError(t, err)
	assert.Empty(t, started.Code)
	require.NotNil(t, started.Ask)
	assert.Equal(t, messaging.AskPhone, started.Ask.Field)

	done := make(chan error, 1)

	go func() {
		_, err := w.svc.Complete(ctx, integration.TelegramID, started.ID, "")
		done <- err
	}()

	next, heard := answer(t, w, p, started.ID, " +84912345678 ",
		func() { p.ask(messaging.AskCode, "Telegram app", false) })
	assert.Equal(t, "phone=+84912345678", heard)
	require.NotNil(t, next.Ask, "the answer brings the next step")
	assert.Equal(t, messaging.AskCode, next.Ask.Field)
	assert.Equal(t, "Telegram app", next.Ask.Hint)

	next, _ = answer(t, w, p, started.ID, "11111", func() { p.ask(messaging.AskCode, "Telegram app", true) })
	require.NotNil(t, next.Ask)
	assert.True(t, next.Ask.Again, "a wrong code is asked again")

	answer(t, w, p, started.ID, "22222", func() { p.scan("user:42", "+84912345678") })
	require.NoError(t, <-done)
}

// A link that asks nothing now refuses an answer, a blank answer is refused before the service hears it, and a
// service that has no phone login refuses one.
func TestAnAnswerNobodyAskedForIsRefused(t *testing.T) {
	t.Parallel()

	w, _ := answerWorld(t)
	ctx := t.Context()

	started, err := w.svc.Begin(ctx, integration.TelegramID, "main", nil)
	require.NoError(t, err)

	_, err = w.svc.Answer(ctx, integration.TelegramID, started.ID, "12345")
	require.ErrorIs(t, err, integration.ErrInvalid, "a QR code waits for the phone, not for an answer")

	byPhone, err := w.svc.LinkByPhone(ctx, integration.TelegramID, "work")
	require.NoError(t, err)

	_, err = w.svc.Answer(ctx, integration.TelegramID, byPhone.ID, "  ")
	require.ErrorIs(t, err, integration.ErrInvalid)

	wa, _ := deviceWorld(t)
	_, err = wa.svc.LinkByPhone(ctx, integration.WhatsAppID, "main")
	require.ErrorIs(t, err, integration.ErrInvalid, "WhatsApp links by its QR code only")
}
