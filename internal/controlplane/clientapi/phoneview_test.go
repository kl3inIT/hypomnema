package clientapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// sharedPhone stands for the phone: a view gives the page `updates` and
// keeps what the page did; with `gone` set, no view opens.
type sharedPhone struct {
	held    bool
	gone    bool
	updates chan phone.Update
	did     chan phone.Action
	closed  chan bool
}

func newSharedPhone() *sharedPhone {
	return &sharedPhone{
		updates: make(chan phone.Update, 4), did: make(chan phone.Action, 4), closed: make(chan bool, 4),
	}
}

func (p *sharedPhone) Controlled() bool { return p.held }

func (p *sharedPhone) Watch(context.Context) (PhoneView, error) {
	if p.gone {
		return nil, fmt.Errorf("%w: no node of this control plane holds a phone", phone.ErrUnavailable)
	}

	return p, nil
}

func (p *sharedPhone) Next(ctx context.Context) (phone.Update, error) {
	select {
	case up := <-p.updates:
		return up, nil
	case <-ctx.Done():
		return phone.Update{}, ctx.Err()
	}
}

func (p *sharedPhone) Do(_ context.Context, a phone.Action) error {
	p.did <- a

	return nil
}

func (p *sharedPhone) Close(_ context.Context, clean bool) { p.closed <- clean }

func (w *world) phoneTicket(c *openapi.ClientWithResponses) string {
	w.t.Helper()

	resp, err := c.OpenDeviceViewWithResponse(w.t.Context())
	require.NoError(w.t, err)
	require.NotNil(w.t, resp.JSON201, "a ticket: %s", resp.Body)

	return "ws" + strings.TrimPrefix(w.url, "http") + BasePath + "/device/views/" + resp.JSON201.Ticket
}

// Scenario: the owner watches the phone. The ticket opens the WebSocket once;
// the page is told who controls, the phone's state and the screen's size as
// JSON and gets a frame as a binary message; what it sends reaches the view;
// and when the owner closes the page the view is closed as the owner's doing.
// Only the owner is given a ticket.
func TestTheOwnerWatchesThePhoneThroughATicket(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)
	pi, _ := w.token("pi", bearertoken.Coordinator)

	none, err := owner.OpenDeviceViewWithResponse(t.Context())
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, none.StatusCode())

	p := newSharedPhone()
	w.srv.WithPhone(p)

	refused, err := pi.OpenDeviceViewWithResponse(t.Context())
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, refused.StatusCode(), "the phone is the owner's to look at")

	target := w.phoneTicket(owner)

	conn, _, err := websocket.Dial(t.Context(), target, nil) //nolint:bodyclose // A WebSocket: no body is left open.
	require.NoError(t, err)

	p.updates <- phone.Update{Controller: phone.ControllerAgent}
	p.updates <- phone.Update{Said: &nodeproto.DeviceViewMessage{Type: "state", State: phone.StateWaking}}
	p.updates <- phone.Update{Said: &nodeproto.DeviceViewMessage{Type: "screen", Width: 720, Height: 1280}}
	p.updates <- phone.Update{Frame: []byte("SEMU a frame")}

	for _, want := range []string{
		`{"controller":"agent","type":"control"}`, `{"state":"waking","type":"state"}`,
		`{"height":1280,"type":"screen","width":720}`,
	} {
		kind, data, err := conn.Read(t.Context())
		require.NoError(t, err)
		assert.Equal(t, websocket.MessageText, kind)
		assert.JSONEq(t, want, string(data))
	}

	kind, frame, err := conn.Read(t.Context())
	require.NoError(t, err)
	assert.Equal(t, websocket.MessageBinary, kind)
	assert.Equal(t, "SEMU a frame", string(frame))

	touch := `{"type":"touch","action":"down","x":0.5,"y":0.25}`
	require.NoError(t, conn.Write(t.Context(), websocket.MessageText, []byte(touch)))
	assert.Equal(t, phone.Action{Type: "touch", Action: "down", X: 0.5, Y: 0.25}, <-p.did)

	_, _, err = websocket.Dial(t.Context(), target, nil) //nolint:bodyclose // The dial fails: no body is left open.
	require.Error(t, err, "a ticket opens its WebSocket once")

	require.NoError(t, conn.Close(websocket.StatusNormalClosure, ""))
	assert.True(t, <-p.closed)
}

// The phone's status says when the owner holds it; a phone that cannot be
// shown closes the view with the code the page does not try again on.
func TestThePhoneSaysItIsHeldAndAPhoneNotThereIsSaid(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("owner", bearertoken.Owner)

	p := newSharedPhone()
	p.held, p.gone = true, true
	w.srv.WithDevices(noDevices{}).WithPhone(p)

	resp, err := owner.GetDeviceWithResponse(t.Context())
	require.NoError(t, err)
	require.NotNil(t, resp.JSON200, "%s", resp.Body)
	assert.True(t, resp.JSON200.Held)

	conn, _, err := websocket.Dial(t.Context(), w.phoneTicket(owner), nil) //nolint:bodyclose // A WebSocket.
	require.NoError(t, err)

	_, _, err = conn.Read(t.Context())
	assert.Equal(t, websocket.StatusCode(4404), websocket.CloseStatus(err))
}

// noDevices is a control plane whose nodes said nothing of a phone.
type noDevices struct{}

func (noDevices) DeviceState(string) (nodeproto.DeviceState, bool) {
	return nodeproto.DeviceState{}, false
}
func (noDevices) Online(string) bool                        { return false }
func (noDevices) StartDevice(context.Context, string) error { return nil }
func (noDevices) StopDevice(context.Context, string) error  { return nil }
