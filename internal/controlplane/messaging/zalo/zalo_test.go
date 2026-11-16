package zalo

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
)

// The bridge is played by this test binary (TestFakeBridge), as os/exec's own tests run a helper process: it speaks
// bridges/zalo/src/protocol.mjs as bridge.mjs does, with what zca-js would give, so that the control plane's side is
// tested without Node or Zalo. FAKE_ZALO picks the script it plays.
const fakeEnv = "FAKE_ZALO"

func TestFakeBridge(t *testing.T) {
	script := os.Getenv(fakeEnv)
	if script == "" {
		t.Skip("the fake bridge runs only as a helper process")
	}

	out := json.NewEncoder(os.Stdout)
	write := func(v any) {
		if err := out.Encode(v); err != nil {
			panic(err)
		}
	}
	emit := func(name string, data any) { write(map[string]any{"event": name, "data": data}) }
	in := bufio.NewScanner(os.Stdin)

	for in.Scan() {
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil {
			continue
		}

		switch req.Method {
		case "loginQR":
			emit("qr", map[string]any{"image": "iVBORw0KGgo", "code": "c1"})
			emit("scanned", map[string]any{"name": "Owner"})
			write(map[string]any{"id": req.ID, "result": map[string]any{
				"uid": "100", "name": "Owner", "credentials": map[string]any{"imei": "i1", "cookie": []any{}, "userAgent": "ua"},
			}})
		case "login":
			if script == "rejected" {
				write(map[string]any{"id": req.ID, "error": "Đăng nhập thất bại", "code": "rejected"})

				continue
			}

			write(map[string]any{"id": req.ID, "result": map[string]any{"uid": "100", "name": "Owner"}})
		case "groups":
			write(map[string]any{"id": req.ID, "result": []any{map[string]any{"id": "g1", "name": "Báo giá"}}})
		case "listen":
			write(map[string]any{"id": req.ID, "result": map[string]any{}})
			emit("message", map[string]any{
				"id": "m1", "chat": "g1", "group": true, "from": "200", "fromName": "Lan", "at": 1_700_000_000_000,
				"text": "Gửi báo giá giúp em",
			})

			if script == "replaced" {
				emit("closed", map[string]any{"code": 3000, "reason": "duplicate", "replaced": true})
			}
		}
	}

	os.Exit(0)
}

func fakeService(t *testing.T, script string) *Service {
	t.Helper()

	svc, err := open(t.Context(), filepath.Join(t.TempDir(), "zalo.db"),
		slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	t.Setenv(fakeEnv, script)

	svc.start = func(ctx context.Context) (*bridge, error) {
		return startBridge(ctx, os.Args[0], "-test.run=^TestFakeBridge$", svc.log)
	}

	return svc
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited for %s", what)
		}

		time.Sleep(20 * time.Millisecond)
	}
}

// link runs a linking to its end and answers its steps.
func link(t *testing.T, svc *Service) []messaging.LinkStep {
	t.Helper()

	steps, err := svc.Link(t.Context())
	require.NoError(t, err)

	var got []messaging.LinkStep
	for step := range steps {
		got = append(got, step)
	}

	return got
}

// A link shows Zalo's QR picture, then gives the account the phone confirmed, its session kept and its group named;
// a message that arrives is kept and found by a search, under its group's name.
func TestALinkShowsTheQRThenKeepsTheSessionAndEveryMessage(t *testing.T) {
	svc := fakeService(t, "plain")

	steps := link(t, svc)
	require.Len(t, steps, 2)
	assert.Equal(t, "data:image/png;base64,iVBORw0KGgo", steps[0].Image)
	assert.Empty(t, steps[0].Code)
	assert.False(t, steps[0].Expires.IsZero())

	acc := steps[1].Account
	require.NotNil(t, acc, "the last step: %v", steps[1].Err)
	t.Cleanup(acc.Close)
	assert.Equal(t, "100", acc.ID())
	assert.Equal(t, "Owner", acc.Name())
	assert.Equal(t, messaging.StatusConnected, acc.State().Status)

	_, _, kept, err := svc.mirror.session(t.Context(), "100")
	require.NoError(t, err)
	assert.True(t, kept, "the session is kept to open again")

	var found []messaging.Message

	eventually(t, "the message kept", func() bool {
		found, err = acc.Messages(t.Context(), messaging.Query{Search: "báo giá"})

		return err == nil && len(found) == 1
	})
	assert.Equal(t, "g1", found[0].Chat)
	assert.Equal(t, "Báo giá", found[0].ChatName)
	assert.Equal(t, "Lan", found[0].SenderName)
	assert.Equal(t, time.UnixMilli(1_700_000_000_000).UTC(), found[0].At)

	chats, err := acc.Chats(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, chats, 1)
	assert.Equal(t, "group", chats[0].Kind)
}

// An account opened again logs in with its kept session; one another Zalo Web took over is replaced and its bridge
// is not started again (zca-js README: one web listener an account).
func TestAnAccountOpenedAgainLogsInAndStaysOutWhenReplaced(t *testing.T) {
	svc := fakeService(t, "replaced")
	require.NoError(t, svc.mirror.saveSession(t.Context(), "100", "Owner", []byte(`{"imei":"i1"}`)))

	acc, err := svc.Open(t.Context(), "100")
	require.NoError(t, err)
	t.Cleanup(acc.Close)

	eventually(t, "the session replaced", func() bool { return acc.State().Status == messaging.StatusReplaced })
	assert.Equal(t, "duplicate", acc.State().Reason)
}

// A session Zalo refuses (its cookies no longer taken) needs a link, and is not tried again.
func TestASessionZaloRefusesNeedsALink(t *testing.T) {
	svc := fakeService(t, "rejected")
	require.NoError(t, svc.mirror.saveSession(t.Context(), "100", "Owner", []byte(`{"imei":"i1"}`)))

	acc, err := svc.Open(t.Context(), "100")
	require.NoError(t, err)
	t.Cleanup(acc.Close)

	eventually(t, "the session logged out", func() bool { return acc.State().NeedsLink() })
}

// A message kept before its group's name was known is found by the name once it is; paging by Before keeps a
// message sent earlier in the same second, as Zalo stamps milliseconds.
func TestTheMirrorFindsByALateNameAndPagesByMilliseconds(t *testing.T) {
	svc := fakeService(t, "plain")
	ctx := t.Context()
	at := time.UnixMilli(1_700_000_000_750)

	require.NoError(t, svc.mirror.keep(ctx, kept{account: "100", chat: "g1", chatKind: "group", id: "m1", at: at,
		text: "hello"}))
	require.NoError(t, svc.mirror.nameChat(ctx, "100", "g1", "group", "Kế toán"))

	found, err := svc.mirror.messages(ctx, "100", messaging.Query{Search: "kế toán"})
	require.NoError(t, err)
	require.Len(t, found, 1)

	found, err = svc.mirror.messages(ctx, "100", messaging.Query{Before: at.Add(150 * time.Millisecond)})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, at.UTC(), found[0].At)
}

func TestAnAccountWhoseSessionIsNotKeptOpensLoggedOut(t *testing.T) {
	svc := fakeService(t, "plain")

	acc, err := svc.Open(t.Context(), "100")
	require.NoError(t, err)
	assert.True(t, acc.State().NeedsLink())
}

// Logout forgets the session and everything kept, so the account cannot be opened with it again.
func TestLogoutForgetsTheSessionAndTheChats(t *testing.T) {
	svc := fakeService(t, "plain")
	ctx := t.Context()

	require.NoError(t, svc.mirror.saveSession(ctx, "100", "Owner", []byte(`{}`)))
	require.NoError(t, svc.mirror.keep(ctx, kept{account: "100", chat: "u2", chatKind: "dm", id: "m1",
		at: time.Now()}))

	acc := svc.newAccount(func() {}, whoAmI{UID: "100"}, nil)
	require.NoError(t, acc.Logout(ctx))

	_, _, ok, err := svc.mirror.session(ctx, "100")
	require.NoError(t, err)
	assert.False(t, ok)

	chats, err := svc.mirror.chats(ctx, "100", 0)
	require.NoError(t, err)
	assert.Empty(t, chats)
}
