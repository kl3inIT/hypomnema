package zalo

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/messaging"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The bridge is played by this test binary (TestFakeBridge), as os/exec's own tests run a helper process: it speaks
// bridges/zalo/src/protocol.mjs as bridge.mjs does, with what zca-js would give, so that the control plane's side is
// tested without Node or Zalo. FAKE_ZALO picks the script it plays.
const fakeEnv = "FAKE_ZALO"

// fakeMediaEnv is where the fake bridge's files are served, as Zalo's CDN serves them.
const fakeMediaEnv = "FAKE_ZALO_MEDIA"

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
		// Who is in a group, as bridge.mjs's members gives it; a group Zalo does not give is an error.
		case "members":
			var p struct {
				Params struct {
					Group string `json:"group"`
				} `json:"params"`
			}
			_ = json.Unmarshal(in.Bytes(), &p)

			if p.Params.Group != "g1" {
				write(map[string]any{"id": req.ID, "error": "Zalo gives no group " + p.Params.Group})

				continue
			}

			write(map[string]any{"id": req.ID, "result": map[string]any{"name": "Báo giá", "ids": []string{"200", "300"}}})
		// A group's past in two pages, the second its start; the first has a photo (zca-js getCloudMessage).
		case "groupHistory":
			var p struct {
				Params struct {
					LastMsgID string `json:"lastMsgId"`
				} `json:"params"`
			}
			_ = json.Unmarshal(in.Bytes(), &p)

			if p.Params.LastMsgID == "" {
				write(map[string]any{"id": req.ID, "result": map[string]any{"lastMsgId": "h1", "isOld": true, "more": true,
					"messages": []any{map[string]any{
						"id": "h1", "chat": "g1", "group": true, "from": "200", "fromName": "Lan", "at": 1_690_000_000_000,
						"text": "Ảnh hiện trường", "media": map[string]any{
							"type": "image", "url": os.Getenv(fakeMediaEnv) + "/p.jpg", "fileName": "", "size": 0,
						},
					}},
				}})

				continue
			}

			write(map[string]any{"id": req.ID, "result": map[string]any{"lastMsgId": "h2", "more": false,
				"messages": []any{map[string]any{
					"id": "h2", "chat": "g1", "group": true, "from": "200", "fromName": "Lan", "at": 1_680_000_000_000,
					"text": "Chào cả nhóm",
				}},
			}})
		// The direct chats' queue: one page, then nothing more.
		case "oldMessages":
			var p struct {
				Params struct {
					LastID string `json:"lastId"`
				} `json:"params"`
			}
			_ = json.Unmarshal(in.Bytes(), &p)

			if p.Params.LastID == "" {
				write(map[string]any{"id": req.ID, "result": map[string]any{"next": "50", "messages": []any{
					map[string]any{"id": "d1", "actionId": "50", "chat": "u9", "from": "100", "fromMe": true,
						"at": 1_695_000_000_000, "text": "Mai họp nhé"},
				}}})

				continue
			}

			write(map[string]any{"id": req.ID, "result": map[string]any{"next": "", "messages": []any{}}})
		// A send answers an id that says what it was given: a quote, mentions, and a file there at the time.
		case "send":
			var p struct {
				Params struct {
					Path     string          `json:"path"`
					Quote    json.RawMessage `json:"quote"`
					Group    bool            `json:"group"`
					Mentions []struct {
						Pos int    `json:"pos"`
						UID string `json:"uid"`
						Len int    `json:"len"`
					} `json:"mentions"`
				} `json:"params"`
			}
			_ = json.Unmarshal(in.Bytes(), &p)

			id := "s"
			if p.Params.Group {
				id += "-group"
			}

			if len(p.Params.Quote) > 0 {
				id += "-quote"
			}

			for _, m := range p.Params.Mentions {
				id += fmt.Sprintf("-tag:%d:%s:%d", m.Pos, m.UID, m.Len)
			}

			result := map[string]any{"textId": id}

			if _, err := os.Stat(p.Params.Path); p.Params.Path != "" && err == nil {
				result["fileId"] = "f-there"
			}

			// The text fails after its file went (bridge.mjs, send).
			if script == "textfails" {
				result["textId"], result["error"] = "", "rate limited"
			}

			write(map[string]any{"id": req.ID, "result": result})
		// The phone confirms and uploads its backup (bridge.mjs, onSync).
		case "phoneSync":
			var p struct {
				Params struct {
					Dir string `json:"dir"`
				} `json:"params"`
			}
			_ = json.Unmarshal(in.Bytes(), &p)

			path, err := fakeBackup(p.Params.Dir)
			if err != nil {
				write(map[string]any{"id": req.ID, "error": err.Error()})

				continue
			}

			write(map[string]any{"id": req.ID, "result": map[string]any{}})
			emit("phone_sync", map[string]any{"stage": "confirmed"})
			emit("phone_sync", map[string]any{"stage": "ready", "path": path,
				"key": base64.StdEncoding.EncodeToString([]byte(backupKeyForTests))})
		case "phoneIds":
			var p struct {
				Params struct {
					Fids []string `json:"fids"`
					Gids []string `json:"gids"`
				} `json:"params"`
			}
			_ = json.Unmarshal(in.Bytes(), &p)

			fids, gids := []string{}, []string{}
			for _, f := range p.Params.Fids {
				fids = append(fids, fakePhoneIDs["u"+f])
			}

			for _, g := range p.Params.Gids {
				gids = append(gids, fakePhoneIDs["g"+g])
			}

			write(map[string]any{"id": req.ID, "result": map[string]any{"fids": fids, "gids": gids}})
		case "users":
			write(map[string]any{"id": req.ID, "result": []any{map[string]any{"id": "u9", "name": "Minh"}}})
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

	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg bytes"))
	}))
	t.Cleanup(cdn.Close)
	t.Setenv(fakeMediaEnv, cdn.URL)

	// The fake CDN listens on this machine, which the real client refuses (TestAFileLinkToThisMachineIsRefused).
	svc.http = cdn.Client()
	svc.gap = 0

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

	// The listener starts before the groups are named: the name shows once they are.
	eventually(t, "the message kept under its group's name", func() bool {
		found, err = acc.Messages(t.Context(), messaging.Query{Search: "giúp em"})

		return err == nil && len(found) == 1 && found[0].ChatName != ""
	})
	assert.Equal(t, "g1", found[0].Chat)
	assert.Equal(t, "Báo giá", found[0].ChatName)
	assert.Equal(t, "Lan", found[0].SenderName)
	assert.Equal(t, time.UnixMilli(1_700_000_000_000).UTC(), found[0].At)

	chats, err := acc.Chats(t.Context(), 0)
	require.NoError(t, err)
	require.NotEmpty(t, chats)
	assert.Equal(t, "group", chats[0].Kind)
}

// Scenario (issue #669): after connecting, the account walks each group's past back to its start and the direct
// chats' queue to its end, names the direct chats it found, and downloads the files of what it kept. Asking a group
// for more then answers that its start is reached.
func TestAfterConnectingThePastIsWalkedAndItsFilesKept(t *testing.T) {
	svc := fakeService(t, "plain")
	ctx := t.Context()
	require.NoError(t, svc.mirror.saveSession(ctx, "100", "Owner", []byte(`{"imei":"i1"}`)))

	acc, err := svc.Open(ctx, "100")
	require.NoError(t, err)
	t.Cleanup(acc.Close)

	eventually(t, "the group walked to its start", func() bool {
		w, err := svc.mirror.walkOf(ctx, "100", "g1")

		return err == nil && w.done
	})

	group, err := acc.Messages(ctx, messaging.Query{Chat: "g1"})
	require.NoError(t, err)
	require.Len(t, group, 3, "the live message and the two pages of the past")
	assert.Equal(t, "Gửi báo giá giúp em", group[0].Text)
	assert.Equal(t, "Chào cả nhóm", group[2].Text)

	eventually(t, "the direct chat named", func() bool {
		chats, err := acc.Chats(ctx, 0)
		for _, c := range chats {
			if c.ID == "u9" {
				return err == nil && c.Name == "Minh" && c.Kind == "dm"
			}
		}

		return false
	})

	eventually(t, "the photo downloaded", func() bool {
		found, err := acc.Messages(ctx, messaging.Query{Search: "hiện trường"})

		return err == nil && len(found) == 1 && found[0].MediaKept
	})

	photo, err := acc.Media(ctx, "g1", "h1")
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", photo.MimeType)

	data, err := os.ReadFile(photo.Path)
	require.NoError(t, err)
	assert.Equal(t, "jpeg bytes", string(data))

	more, err := acc.Backfill(ctx, "g1", 50)
	require.NoError(t, err)
	assert.Equal(t, messaging.Backfilled{Added: 0, Complete: true}, more)

	require.NoError(t, acc.Logout(ctx))
	_, err = os.Stat(photo.Path)
	assert.ErrorIs(t, err, os.ErrNotExist, "disconnecting removes the files")
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

	_, err := svc.mirror.keep(ctx, kept{account: "100", chat: "g1", chatKind: "group", id: "m1", at: at,
		text: "hello"})
	require.NoError(t, err)
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
	_, err := svc.mirror.keep(ctx, kept{account: "100", chat: "u2", chatKind: "dm", id: "m1", at: time.Now()})
	require.NoError(t, err)

	acc := svc.newAccount(func() {}, whoAmI{UID: "100"}, nil)
	require.NoError(t, acc.Logout(ctx))

	_, _, ok, err := svc.mirror.session(ctx, "100")
	require.NoError(t, err)
	assert.False(t, ok)

	chats, err := svc.mirror.chats(ctx, "100", 0)
	require.NoError(t, err)
	assert.Empty(t, chats)
}

// A message's link is its sender's to choose: one to this machine, or to anything but http(s), is not fetched, so a
// message cannot make the control plane read its own services (review of #671; the browser's egress policy).
func TestAFileLinkToThisMachineIsRefused(t *testing.T) {
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	t.Cleanup(inside.Close)

	svc := &Service{http: newHTTP()}

	_, _, err := svc.fetch(t.Context(), inside.URL, maxFileBytes)
	require.ErrorIs(t, err, errNotPublic)

	_, _, err = svc.fetch(t.Context(), "file:///etc/passwd", maxFileBytes)
	require.ErrorIs(t, err, errNotPublic)
}

// The files left to download are listed a batch at a time, older than the last one listed, so that every one is
// reached however many there are (review of #671).
func TestTheFilesLeftAreListedBatchAfterBatch(t *testing.T) {
	svc := fakeService(t, "plain")
	ctx := t.Context()

	for i, id := range []string{"a", "b", "c"} {
		_, err := svc.mirror.keep(ctx, kept{account: "100", chat: "g1", chatKind: "group", id: id,
			at: time.UnixMilli(int64(1000 * (i + 1))), mediaType: "image", mediaURL: "https://f/" + id})
		require.NoError(t, err)
	}

	first, err := svc.mirror.pendingMedia(ctx, "100", 0, 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, []string{"c", "b"}, []string{first[0].id, first[1].id})

	rest, err := svc.mirror.pendingMedia(ctx, "100", first[1].ts, 2)
	require.NoError(t, err)
	require.Len(t, rest, 1)
	assert.Equal(t, "a", rest[0].id)
}

// Scenario (issue #680): once connected, the account sends as the owner: a reply in a group quotes the kept
// message, and a file is written for the bridge to send and removed after. What is sent is kept as the owner's.
func TestTheAccountSendsAsTheOwnerAndKeepsIt(t *testing.T) {
	svc := fakeService(t, "plain")
	ctx := t.Context()
	require.NoError(t, svc.mirror.saveSession(ctx, "100", "Owner", []byte(`{"imei":"i1"}`)))

	acc, err := svc.Open(ctx, "100")
	require.NoError(t, err)
	t.Cleanup(acc.Close)

	eventually(t, "the live message kept", func() bool {
		found, err := acc.Messages(ctx, messaging.Query{Search: "giúp em"})

		return err == nil && len(found) == 1
	})

	sent, err := acc.Send(ctx, messaging.Outgoing{Chat: "g1", Text: "Đây ạ", ReplyTo: "m1",
		File: []byte("%PDF"), FileName: "bao-gia.pdf"})
	require.NoError(t, err)
	assert.Equal(t, "s-group-quote", sent.ID, "the text's id, in a group, with its quote")

	file, err := acc.Messages(ctx, messaging.Query{Chat: "g1", Search: "bao-gia.pdf"})
	require.NoError(t, err)
	require.Len(t, file, 1, "the file is a message of its own")
	assert.Equal(t, "f-there", file[0].ID, "kept under its own id, the file there when it was sent")
	assert.True(t, file[0].FromMe)

	text, err := acc.Messages(ctx, messaging.Query{Chat: "g1", Search: "Đây"})
	require.NoError(t, err)
	require.Len(t, text, 1)
	assert.Equal(t, "m1", text[0].QuotedID)
	assert.Empty(t, text[0].FileName)

	left, err := os.ReadDir(filepath.Join(filepath.Dir(svc.mediaDir), "zalo-outgoing"))
	require.NoError(t, err)
	assert.Empty(t, left, "the file sent is not kept on disk")

	_, err = acc.Send(ctx, messaging.Outgoing{Chat: "nobody", Text: "hi"})
	require.ErrorIs(t, err, orchestration.ErrNotFound, "a chat not kept is said so")
}

// Scenario: issue #1200. A text the owner sends in a group tags the members it names: each goes to zca-js where
// the text writes "@" and his name, counted in UTF-16 units as JavaScript counts a string, beside the quote of the
// message it answers. A direct chat tags nobody (zca-js tags only in a group), and a tag the text does not write is
// refused before anything is sent.
func TestATextTagsTheMembersItNamesInAGroup(t *testing.T) {
	svc := fakeService(t, "plain")
	ctx := t.Context()
	require.NoError(t, svc.mirror.saveSession(ctx, "100", "Owner", []byte(`{"imei":"i1"}`)))

	acc, err := svc.Open(ctx, "100")
	require.NoError(t, err)
	t.Cleanup(acc.Close)

	eventually(t, "the group and the direct chat kept", func() bool {
		group, err := acc.Messages(ctx, messaging.Query{Chat: "g1", ID: "m1"})
		direct, derr := acc.Messages(ctx, messaging.Query{Chat: "u9"})

		return err == nil && derr == nil && len(group) == 1 && len(direct) > 0
	})

	lan := messaging.Tag{ID: "200", At: 0, Len: len("@Lan Anh"), Name: "Lan Anh"}

	sent, err := acc.Send(ctx, messaging.Outgoing{Chat: "g1", Text: "@Lan Anh dạ vâng chị", ReplyTo: "m1",
		Tags: []messaging.Tag{lan}})
	require.NoError(t, err)
	assert.Equal(t, "s-group-quote-tag:0:200:8", sent.ID, "@Lan Anh at 0, 8 long, with the quote")

	text := "Dạ 😀 @Lan Anh"
	sent, err = acc.Send(ctx, messaging.Outgoing{Chat: "g1", Text: text,
		Tags: []messaging.Tag{{ID: "200", At: strings.Index(text, "@"), Len: len("@Lan Anh"), Name: "Lan Anh"}}})
	require.NoError(t, err)
	assert.Equal(t, "s-group-tag:6:200:8", sent.ID, "ạ is one UTF-16 unit and 😀 two, not their bytes")

	sent, err = acc.Send(ctx, messaging.Outgoing{Chat: "u9", Text: "@Lan Anh dạ", Tags: []messaging.Tag{lan}})
	require.NoError(t, err)
	assert.Equal(t, "s", sent.ID, "a direct chat tags nobody")

	_, err = acc.Send(ctx, messaging.Outgoing{Chat: "g1", Text: "Lan Anh dạ", Tags: []messaging.Tag{lan}})
	require.Error(t, err, "a tag where the text writes no @ is refused")
}

// A file that went before its text failed is said, with its id, so that it is not sent again (review of #683).
func TestATextThatFailsAfterItsFileSaysTheFileWent(t *testing.T) {
	svc := fakeService(t, "textfails")
	ctx := t.Context()
	require.NoError(t, svc.mirror.saveSession(ctx, "100", "Owner", []byte(`{"imei":"i1"}`)))

	acc, err := svc.Open(ctx, "100")
	require.NoError(t, err)
	t.Cleanup(acc.Close)

	eventually(t, "connected", func() bool {
		chats, err := acc.Chats(ctx, 0)

		return err == nil && len(chats) > 0
	})

	_, err = acc.Send(ctx, messaging.Outgoing{Chat: "g1", Text: "Đây ạ", File: []byte("%PDF"), FileName: "q.pdf"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the file was sent in g1 (message f-there), but not the text")
}

// The owner's send keeps a message before the listener brings it back with Zalo's own fields: those fill in what
// it lacked, and its file is downloaded (review of #683).
func TestAMessageKeptBeforeTheListenerIsFilledInByIt(t *testing.T) {
	svc := fakeService(t, "plain")
	ctx := t.Context()

	mine := kept{account: "100", chat: "g1", chatKind: "group", id: "f1", at: time.Now(), fromMe: true,
		mediaType: "document", fileName: "q.pdf"}
	added, err := svc.mirror.keep(ctx, mine)
	require.NoError(t, err)
	assert.True(t, added)

	mine.cliID, mine.mediaURL = "c9", "https://f/q.pdf"
	added, err = svc.mirror.keep(ctx, mine)
	require.NoError(t, err)
	assert.False(t, added, "kept once")

	row, err := svc.mirror.mediaOf(ctx, "100", "g1", "f1")
	require.NoError(t, err)
	assert.Equal(t, "https://f/q.pdf", row.url, "the listener's link filled in")
}

// An image over 20 MiB is not downloaded and says why, once; a document of the same size is, under its 50 MiB cap
// (zaloclaw's caps, issue #743).
func TestAFileIsCappedByItsType(t *testing.T) {
	svc := fakeService(t, "plain")
	ctx := t.Context()
	acc := svc.newAccount(func() {}, whoAmI{UID: "100"}, nil)

	size := int64(25 << 20)
	for _, m := range []incoming{
		{ID: "big-image", Chat: "u9", From: "u9", At: 1, Kind: "chat.photo",
			Media: &mediaOf{Type: "image", URL: os.Getenv(fakeMediaEnv) + "/i.jpg", Size: size}},
		{ID: "big-doc", Chat: "u9", From: "u9", At: 2, Kind: "share.file",
			Media: &mediaOf{Type: "document", URL: os.Getenv(fakeMediaEnv) + "/d.pdf", FileName: "d.pdf", Size: size}},
	} {
		_, err := acc.keepMessage(ctx, m)
		require.NoError(t, err)
	}

	_, err := acc.download(ctx, mediaJob{account: "100", chat: "u9", id: "big-image"})
	require.ErrorContains(t, err, "larger than 20 MiB")

	var why string
	require.NoError(t, svc.mirror.db.QueryRowContext(ctx,
		`SELECT media_error FROM zl_messages WHERE account = '100' AND msg_id = 'big-image'`).Scan(&why))
	assert.Equal(t, "larger than 20 MiB", why, "said once, not tried again")

	doc, err := acc.download(ctx, mediaJob{account: "100", chat: "u9", id: "big-doc"})
	require.NoError(t, err)
	assert.NotEmpty(t, doc.Path)
}

// Scenario (issue #761): Zalo's phone sends an image as JPEG XL, which a model cannot read. Its link is fetched as the
// JPG copy the CDN keeps, and an image kept as JPEG XL before is fetched again as JPG when it is next opened.
func TestAJPEGXLImageIsKeptAsItsJPGCopy(t *testing.T) {
	svc := fakeService(t, "plain")
	ctx := t.Context()

	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 'j', 'p', 'g'}
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/jpg/a/1.jpg" && r.URL.Query().Get("jxlstatus") == "":
			_, _ = w.Write(jpeg)
		case r.URL.Path == "/jxl/a/1.jxl":
			_, _ = w.Write([]byte{0xFF, 0x0A, 'j', 'x', 'l'})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cdn.Close)
	svc.http = cdn.Client()

	acc := svc.newAccount(func() {}, whoAmI{UID: "100"}, nil)
	for i, id := range []string{"new", "old"} {
		_, err := acc.keepMessage(ctx, incoming{ID: id, Chat: "u9", From: "u9", At: int64(i + 1), Kind: "chat.photo",
			Media: &mediaOf{Type: "image", URL: cdn.URL + "/jxl/a/1.jxl?jxlstatus=1"}})
		require.NoError(t, err)
	}

	got, err := acc.Media(ctx, "u9", "new")
	require.NoError(t, err)
	data, err := os.ReadFile(got.Path)
	require.NoError(t, err)
	assert.Equal(t, jpeg, data, "the JPG copy is kept")

	// An image kept as JPEG XL before this fix.
	old := filepath.Join(t.TempDir(), "old.jfif")
	require.NoError(t, os.WriteFile(old, []byte{0xFF, 0x0A, 'j', 'x', 'l'}, 0o600))
	require.NoError(t, svc.mirror.keptMedia(ctx, "100", "u9", "old", old, "", "image/jpeg"))

	got, err = acc.Media(ctx, "u9", "old")
	require.NoError(t, err)
	data, err = os.ReadFile(got.Path)
	require.NoError(t, err)
	assert.Equal(t, jpeg, data, "fetched again as JPG")
	assert.Equal(t, "image/jpeg", got.MimeType)
}

func TestOnlyAZaloJPEGXLLinkIsTurnedToJPG(t *testing.T) {
	for in, want := range map[string]string{
		"https://photo-stal-7.zdn.vn/jxl/x/y/1.jxl":           "https://photo-stal-7.zdn.vn/jpg/x/y/1.jpg",
		"https://photo-stal-7.zdn.vn/jxl/x/1.JXL?jxlstatus=1": "https://photo-stal-7.zdn.vn/jpg/x/1.jpg",
		"https://photo-stal-7.zdn.vn/jpg/x/1.jpg":             "https://photo-stal-7.zdn.vn/jpg/x/1.jpg",
		"https://f.zdn.vn/jxl/x/1.jxl?e2esession=s":           "https://f.zdn.vn/jxl/x/1.jxl?e2esession=s",
		"https://f.zdn.vn/doc/jxl/notes.pdf":                  "https://f.zdn.vn/doc/jxl/notes.pdf",
	} {
		assert.Equal(t, want, jpgURL(in), in)
	}
}

// A kept message is found by its chat and id (issue #1004: the owner's message given as evidence), and only in its
// chat.
func TestTheMirrorFindsAMessageByItsID(t *testing.T) {
	svc := fakeService(t, "plain")
	ctx := t.Context()
	at := time.UnixMilli(1_700_000_000_750)

	for _, id := range []string{"m1", "m2"} {
		_, err := svc.mirror.keep(ctx, kept{account: "100", chat: "g1", chatKind: "group", id: id, at: at,
			text: "hello " + id, fromMe: id == "m2"})
		require.NoError(t, err)
	}

	found, err := svc.mirror.messages(ctx, "100", messaging.Query{Chat: "g1", ID: "m2"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "m2", found[0].ID)
	assert.True(t, found[0].FromMe)

	found, err = svc.mirror.messages(ctx, "100", messaging.Query{Chat: "g2", ID: "m2"})
	require.NoError(t, err)
	assert.Empty(t, found)
}

// Scenario: issue #1163. Who is in a chat is read to tell whether the owner answered someone in another chat: a
// group's members are asked of Zalo, with its name; a direct chat's other party is the chat's own id, with the name
// kept; a chat not kept is not found.
func TestAChatsMembersAreAskedOfZalo(t *testing.T) {
	svc := fakeService(t, "plain")
	ctx := t.Context()
	require.NoError(t, svc.mirror.saveSession(ctx, "100", "Owner", []byte(`{"imei":"i1"}`)))

	opened, err := svc.Open(ctx, "100")
	require.NoError(t, err)
	t.Cleanup(opened.Close)

	acc, ok := opened.(messaging.MemberLister)
	require.True(t, ok, "a Zalo account lists a chat's members")

	eventually(t, "the group and the direct chat kept", func() bool {
		return svc.mirror.chatKind(ctx, "100", "g1") == "group" && svc.mirror.chatName(ctx, "100", "u9") == "Minh"
	})

	group, err := acc.Members(ctx, "g1")
	require.NoError(t, err)
	assert.Equal(t, messaging.Members{Name: "Báo giá", IDs: []string{"200", "300"}}, group)

	direct, err := acc.Members(ctx, "u9")
	require.NoError(t, err)
	assert.Equal(t, messaging.Members{Name: "Minh", Direct: true, IDs: []string{"u9"}}, direct)

	_, err = acc.Members(ctx, "never-kept")
	require.ErrorIs(t, err, orchestration.ErrNotFound)
}
