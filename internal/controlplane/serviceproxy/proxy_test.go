package serviceproxy_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

const realToken = "ya29.the-real-token" //nolint:gosec // A made-up token, which is what this test hands out.

// sent is one request the service received.
type sent struct {
	Method, URL, Authorization string
	Body                       []byte
}

// service stands for Google: it records what reaches it and answers 200.
type service struct {
	mu  sync.Mutex
	got []sent
}

func (s *service) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)

	s.mu.Lock()
	s.got = append(s.got, sent{r.Method, r.URL.String(), r.Header.Get("Authorization"), body})
	s.mu.Unlock()

	return &http.Response{
		StatusCode: http.StatusOK, Proto: "HTTP/2.0", ProtoMajor: 2, ContentLength: -1,
		Header:  http.Header{"Content-Type": {"application/json"}},
		Body:    io.NopCloser(strings.NewReader(`{"id":"from-the-service"}`)),
		Request: r,
	}, nil
}

func (s *service) received() []sent {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]sent(nil), s.got...)
}

type world struct {
	t       *testing.T
	svc     *orchestration.Service
	proxy   *serviceproxy.Proxy
	srv     *httptest.Server
	service *service
	logs    *bytes.Buffer
	pi      string
	chat    string
}

// newWorld is a control plane with the owner's Google account "work", which
// the coordinator may use in chats, and a chat pi works in. A held request
// waits for hold.
func newWorld(t *testing.T, hold time.Duration) *world {
	t.Helper()

	ctx := t.Context()

	db, err := sqlite.Open(ctx, sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(ctx, db))

	w := &world{
		t: t, svc: orchestration.New(db, platform.SystemClock{}, platform.NewID), service: &service{},
		logs: &bytes.Buffer{},
	}
	logger := slog.New(slog.NewTextHandler(w.logs, nil))

	for _, label := range []string{"work", "personal"} {
		_, err = w.svc.SaveServiceConnection(ctx, orchestration.Connection{
			Integration: "google", Label: label, Account: label + "@example.com", Secret: "conn-google-" + label,
		})
		require.NoError(t, err)
	}

	_, err = w.svc.SetConnectionChats(ctx, "google", "work", true)
	require.NoError(t, err)

	_, w.pi, err = w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "Reply to the invoice")
	require.NoError(t, err)

	w.chat = chat.ID

	agents := agentapi.New(w.svc, func(context.Context, string, string) (agentapi.Token, error) {
		return agentapi.Token{AccessToken: realToken}, nil
	}, logger)

	w.proxy, err = serviceproxy.New(serviceproxy.Config{
		Agents: agents, Store: w.svc, Logger: logger, Upstream: w.service, Hold: hold,
		Token: func(context.Context, string, string) (string, error) { return realToken, nil },
	})
	require.NoError(t, err)

	mux := http.NewServeMux()
	agents.Register(mux)
	w.proxy.Register(mux)

	w.srv = httptest.NewServer(mux)
	t.Cleanup(w.srv.Close)

	return w
}

// answer is what a tool got back.
type answer struct {
	Status int
	Body   string
}

// message is the proxy's own words in an answer it wrote itself.
func (a answer) message() string {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	_ = json.Unmarshal([]byte(a.Body), &body)

	return body.Error.Message
}

// tool is one connection of an agent's tool to a host, as the launcher's
// bridge opens it: a websocket to the tunnel, and TLS inside it that trusts
// the proxy's certificate.
type tool struct {
	t      *testing.T
	conn   *tls.Conn
	reader *bufio.Reader
}

// open opens a tool's connection; refused is the status when the control
// plane would not.
func (w *world) open(key, label, host, chat string) (tl *tool, refused int) {
	w.t.Helper()

	query := url.Values{
		agenttool.QueryIntegration: {"google"}, agenttool.QueryLabel: {label}, agenttool.QueryHost: {host + ":443"},
	}
	if chat != "" {
		query.Set(agenttool.QueryChat, chat)
	}

	header := http.Header{}
	if key != "" {
		header.Set("Authorization", "Bearer "+key)
	}

	//nolint:bodyclose // The library closes the handshake's body.
	ws, resp, err := websocket.Dial(w.t.Context(), "ws"+strings.TrimPrefix(w.srv.URL, "http")+agenttool.PathTunnel+
		"?"+query.Encode(), &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		require.NotNil(w.t, resp, "the tunnel answered nothing: %v", err)

		return nil, resp.StatusCode
	}

	ws.SetReadLimit(-1)

	resp, err = http.Get(w.srv.URL + agenttool.PathCA) //nolint:noctx // A test's own server.
	require.NoError(w.t, err)

	pem, err := io.ReadAll(resp.Body)
	require.NoError(w.t, err)
	require.NoError(w.t, resp.Body.Close())

	roots := x509.NewCertPool()
	require.True(w.t, roots.AppendCertsFromPEM(pem))

	conn := tls.Client(websocket.NetConn(context.WithoutCancel(w.t.Context()), ws, websocket.MessageBinary),
		&tls.Config{ServerName: host, RootCAs: roots, MinVersion: tls.VersionTLS12})
	require.NoError(w.t, conn.HandshakeContext(w.t.Context()))
	w.t.Cleanup(func() { _ = conn.Close() })

	return &tool{t: w.t, conn: conn, reader: bufio.NewReader(conn)}, 0
}

// pi opens a connection of pi's tool in its chat.
func (w *world) piTool(host string) *tool {
	w.t.Helper()

	tl, refused := w.open(w.pi, "work", host, w.chat)
	require.Zero(w.t, refused)

	return tl
}

// do sends one request with the placeholder a tool holds.
func (tl *tool) do(method, path, contentType string, body []byte) answer {
	tl.t.Helper()

	req, err := http.NewRequestWithContext(tl.t.Context(), method, "https://service"+path, bytes.NewReader(body))
	require.NoError(tl.t, err)

	req.Header.Set("Authorization", "Bearer "+agenttool.Placeholder)

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	require.NoError(tl.t, req.Write(tl.conn))

	resp, err := http.ReadResponse(tl.reader, req)
	require.NoError(tl.t, err)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(tl.t, err)
	require.NoError(tl.t, resp.Body.Close())

	return answer{Status: resp.StatusCode, Body: string(raw)}
}

// mail is a Gmail send as gws makes it: metadata, then the message. Every
// call has its own boundary, Date and Message-Id, as two runs of one command
// have.
func mail(to, subject, body string, n int) (contentType string, raw []byte) {
	boundary := fmt.Sprintf("boundary-%d", n)
	message := fmt.Sprintf("From: owner@example.com\r\nTo: %s\r\nSubject: %s\r\n"+
		"Date: Tue, 11 Nov 2026 09:00:%02d +0700\r\n"+
		"Message-Id: <%d@mail.example>\r\nMime-Version: 1.0\r\nContent-Type: text/plain; charset=\"utf-8\"\r\n\r\n%s\r\n",
		to, subject, n, n, body)

	return "multipart/related; boundary=" + boundary, []byte("--" + boundary +
		"\r\nContent-Type: application/json\r\n\r\n{}\r\n--" + boundary +
		"\r\nContent-Type: message/rfc822\r\n\r\n" + message + "\r\n--" + boundary + "--\r\n")
}

const sendPath = "/upload/gmail/v1/users/me/messages/send?uploadType=multipart"

func (w *world) approvals(status orchestration.ApprovalStatus) []orchestration.Approval {
	w.t.Helper()

	all, err := w.svc.ListApprovals(w.t.Context(), orchestration.ApprovalFilter{Status: status})
	require.NoError(w.t, err)

	return all
}

// pending waits for the one request that waits for the owner.
func (w *world) pending() orchestration.Approval {
	w.t.Helper()

	var found []orchestration.Approval

	require.Eventually(w.t, func() bool {
		found = w.approvals(orchestration.ApprovalPending)

		return len(found) == 1
	}, 5*time.Second, 10*time.Millisecond, "one request should wait for the owner")

	return found[0]
}

// Scenario: pi reads the owner's mail. Its tool holds only a placeholder;
// the service receives the account's real token, and the tool's answer is
// the service's. What a tool needs to run at all carries no token. A host
// that is not the Integration's, an account pi was not given and a caller
// without a credential get no tunnel. The log names the agent, the account,
// the action and the verdict, and never a token.
func TestAnAgentReadsThroughTheProxyWithoutHoldingTheToken(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)

	gmail := w.piTool("gmail.googleapis.com")

	got := gmail.do(http.MethodGet, "/gmail/v1/users/me/messages?q=is%3Aunread", "", nil)
	require.Equal(t, http.StatusOK, got.Status)
	assert.JSONEq(t, `{"id":"from-the-service"}`, got.Body)

	again := gmail.do(http.MethodGet, "/gmail/v1/users/me/messages/1a2b", "", nil)
	require.Equal(t, http.StatusOK, again.Status, "a connection serves one request after another")

	discovery := w.piTool("www.googleapis.com").do(http.MethodGet, "/discovery/v1/apis/calendar/v3/rest", "", nil)
	require.Equal(t, http.StatusOK, discovery.Status)

	received := w.service.received()
	require.Len(t, received, 3)
	assert.Equal(t, "https://gmail.googleapis.com/gmail/v1/users/me/messages?q=is%3Aunread", received[0].URL)
	assert.Equal(t, "Bearer "+realToken, received[0].Authorization)
	assert.Equal(t, "Bearer "+realToken, received[1].Authorization)
	assert.Empty(t, received[2].Authorization, "the description of an API is public: no account is in it")

	_, refused := w.open(w.pi, "work", "example.com", w.chat)
	assert.Equal(t, http.StatusForbidden, refused, "only an Integration's hosts are reached")

	_, refused = w.open(w.pi, "personal", "gmail.googleapis.com", w.chat)
	assert.Equal(t, http.StatusForbidden, refused, "the owner did not allow that account for chats")

	_, refused = w.open("", "work", "gmail.googleapis.com", "")
	assert.Equal(t, http.StatusUnauthorized, refused)

	assert.Empty(t, w.approvals(""), "reading asks nobody")

	logs := w.logs.String()
	assert.Contains(t, logs, "connection=google:work")
	assert.Contains(t, logs, "action=gmail.read")
	assert.Contains(t, logs, "verdict=allowed")
	assert.NotContains(t, logs, realToken)
}

// Scenario: pi sends a mail. The request is held and the owner sees what
// would be sent, decoded: who to, the subject, the text, and that pi asked in
// this chat. Nothing reaches the service until he approves; then the held
// request goes on with the real token. The approval was for that one
// request: the same mail sent again asks again, and a rejection is told to
// the tool and never reaches the service.
func TestASendWaitsForTheOwnersApproval(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)
	ctx := t.Context()
	gmail := w.piTool("gmail.googleapis.com")

	contentType, body := mail("minh@example.com", "Re: October invoice", "Received. We pay before the 15th.", 1)

	answered := make(chan answer, 1)
	go func() { answered <- gmail.do(http.MethodPost, sendPath, contentType, body) }()

	asked := w.pending()
	assert.Equal(t, "Send mail: minh@example.com", asked.Title)
	assert.Equal(t, "gmail.send", asked.Action)
	assert.Equal(t, "pi", asked.AskedBy)
	assert.Equal(t, w.chat, asked.ChatID)
	assert.Contains(t, asked.Payload, orchestration.Field{Label: "To", Value: "minh@example.com"})
	assert.Contains(t, asked.Payload, orchestration.Field{Label: "Subject", Value: "Re: October invoice"})
	assert.Contains(t, asked.Payload, orchestration.Field{Label: "Body", Value: "Received. We pay before the 15th."})
	assert.Empty(t, w.service.received(), "nothing is sent while the owner has not answered")

	_, err := w.proxy.Decide(ctx, asked.ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)

	got := <-answered
	require.Equal(t, http.StatusOK, got.Status)

	received := w.service.received()
	require.Len(t, received, 1)
	assert.Equal(t, "Bearer "+realToken, received[0].Authorization)
	assert.Equal(t, body, received[0].Body, "the request goes on as the tool made it")

	// The same mail again: the approval was used.
	contentType, body = mail("minh@example.com", "Re: October invoice", "Received. We pay before the 15th.", 2)

	go func() { answered <- gmail.do(http.MethodPost, sendPath, contentType, body) }()

	second := w.pending()
	assert.NotEqual(t, asked.ID, second.ID)

	_, err = w.proxy.Decide(ctx, second.ID, false, "")
	require.NoError(t, err)

	got = <-answered
	assert.Equal(t, http.StatusForbidden, got.Status)
	assert.Contains(t, got.message(), "Rejected by the owner")
	assert.Len(t, w.service.received(), 1, "a rejected request never reaches the service")

	_, err = w.proxy.Decide(ctx, second.ID, true, orchestration.ScopeOnce)
	require.ErrorIs(t, err, orchestration.ErrConflict, "the first answer stands")
}

// Scenario: the owner is away. The hold ends and pi's tool is told the mail
// waits for approval and was not sent; the request stays open. Hours later
// the owner approves it: pi is woken in its chat, runs the same command
// again, and the mail goes without a second question, though its bytes
// differ from the first run's. It goes once: the same again asks again, and
// so does a mail with other words.
func TestAnApprovalGivenLaterLetsTheSameRequestPassOnce(t *testing.T) {
	t.Parallel()

	w := newWorld(t, 150*time.Millisecond)
	ctx := t.Context()
	gmail := w.piTool("gmail.googleapis.com")

	contentType, body := mail("minh@example.com", "Re: October invoice", "Received.", 1)

	got := gmail.do(http.MethodPost, sendPath, contentType, body)
	require.Equal(t, http.StatusForbidden, got.Status)

	asked := w.pending()
	assert.Contains(t, got.message(), "Waiting for the owner's approval ("+asked.ID+")")
	assert.Contains(t, got.message(), "NOT run")
	assert.Empty(t, w.service.received())

	_, err := w.proxy.Decide(ctx, asked.ID, true, orchestration.ScopeOnce)
	require.NoError(t, err)

	woken, err := w.svc.Undelivered(ctx, w.chat)
	require.NoError(t, err)
	require.Len(t, woken, 1, "pi is woken in the chat it asked in")
	assert.Contains(t, woken[0].Text, "approved")
	assert.Contains(t, woken[0].Text, asked.ID)

	contentType, body = mail("minh@example.com", "Re: October invoice", "Received.", 2)

	got = gmail.do(http.MethodPost, sendPath, contentType, body)
	require.Equal(t, http.StatusOK, got.Status, "the same mail passes: it is what the owner approved")
	require.Len(t, w.service.received(), 1)
	assert.Empty(t, w.approvals(orchestration.ApprovalPending), "and nobody is asked a second time")

	contentType, body = mail("minh@example.com", "Re: October invoice", "Received.", 3)

	got = gmail.do(http.MethodPost, sendPath, contentType, body)
	assert.Equal(t, http.StatusForbidden, got.Status, "an approval for one request runs once")

	contentType, body = mail("minh@example.com", "Re: October invoice", "Received. Also, send me your bank details.", 4)

	got = gmail.do(http.MethodPost, sendPath, contentType, body)
	assert.Equal(t, http.StatusForbidden, got.Status, "other words are another request")
	assert.Len(t, w.approvals(orchestration.ApprovalPending), 2)
	assert.Len(t, w.service.received(), 1)
}

// Scenario: the owner approves sending for the rest of the chat. Other mails
// from that chat then go without a question; another chat's still ask.
func TestApprovingForTheSessionStopsAskingInThatChatOnly(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)
	ctx := t.Context()
	gmail := w.piTool("gmail.googleapis.com")

	contentType, body := mail("minh@example.com", "One", "First.", 1)

	answered := make(chan answer, 1)
	go func() { answered <- gmail.do(http.MethodPost, sendPath, contentType, body) }()

	_, err := w.proxy.Decide(ctx, w.pending().ID, true, orchestration.ScopeSession)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, (<-answered).Status)

	contentType, body = mail("lan@example.com", "Two", "Second.", 2)
	require.Equal(t, http.StatusOK, gmail.do(http.MethodPost, sendPath, contentType, body).Status)
	assert.Empty(t, w.approvals(orchestration.ApprovalPending))

	other, err := w.svc.CreateChat(ctx, "Another chat")
	require.NoError(t, err)

	elsewhere, refused := w.open(w.pi, "work", "gmail.googleapis.com", other.ID)
	require.Zero(t, refused)

	go func() { answered <- elsewhere.do(http.MethodPost, sendPath, contentType, body) }()

	asked := w.pending()
	assert.Equal(t, other.ID, asked.ChatID)

	_, err = w.proxy.Decide(ctx, asked.ID, false, "")
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, (<-answered).Status)
	assert.Len(t, w.service.received(), 2)
}

// Scenario: the owner's answers for an account's actions. Set to never,
// sending is refused at once and asks nobody. Set to ask, a draft, which runs
// on its own by default, waits. A request to the service that is none of the
// listed actions asks whatever was set.
func TestTheOwnersAnswerForAnActionDecidesWhatARequestDoes(t *testing.T) {
	t.Parallel()

	w := newWorld(t, 100*time.Millisecond)
	ctx := t.Context()
	gmail := w.piTool("gmail.googleapis.com")
	contentType, body := mail("minh@example.com", "One", "First.", 1)

	draft := "/upload/gmail/v1/users/me/drafts?uploadType=multipart"
	require.Equal(t, http.StatusOK, gmail.do(http.MethodPost, draft, contentType, body).Status,
		"a draft sends nothing and runs on its own")

	require.NoError(t, w.svc.SetActionPolicy(ctx, "google", "work", "gmail.send", orchestration.PolicyNever))
	require.NoError(t, w.svc.SetActionPolicy(ctx, "google", "work", "gmail.draft", orchestration.PolicyAsk))

	got := gmail.do(http.MethodPost, sendPath, contentType, body)
	assert.Equal(t, http.StatusForbidden, got.Status)
	assert.Contains(t, got.message(), "never")
	assert.Empty(t, w.approvals(""), "never asks nobody")

	got = gmail.do(http.MethodPost, draft, contentType, body)
	assert.Equal(t, http.StatusForbidden, got.Status)
	assert.Contains(t, got.message(), "Waiting for the owner's approval")

	got = gmail.do(http.MethodPost, "/gmail/v1/users/me/settings/forwardingAddresses", "application/json",
		[]byte(`{"forwardingEmail":"elsewhere@example.net"}`))
	assert.Equal(t, http.StatusForbidden, got.Status)

	pending := w.approvals(orchestration.ApprovalPending)
	require.Len(t, pending, 2)
	assert.Equal(t, "google.unlisted", pending[0].Action, "what the list does not name asks")
	assert.Contains(t, pending[0].Payload, orchestration.Field{Label: "forwardingEmail", Value: "elsewhere@example.net"})
	assert.Len(t, w.service.received(), 1, "only the first draft reached the service")
}

// raw sends a request exactly as written and reads the answer.
func (tl *tool) raw(request string) answer {
	tl.t.Helper()

	_, err := io.WriteString(tl.conn, request)
	require.NoError(tl.t, err)

	resp, err := http.ReadResponse(tl.reader, nil)
	require.NoError(tl.t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(tl.t, err)
	require.NoError(tl.t, resp.Body.Close())

	return answer{Status: resp.StatusCode, Body: string(body)}
}

// Scenario: a request written so that the proxy and the service could read
// it differently. Another address in the request line, a path that steps out
// of where it starts or hides a slash, and a header or a parameter that asks
// the service to treat a read as a delete are all refused, and none reaches
// the service. A Host header that names another machine changes nothing: the
// request goes to the tunnel's host.
func TestARequestThatCouldBeReadTwoWaysIsRefused(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)
	gmail := w.piTool("gmail.googleapis.com")
	head := " HTTP/1.1\r\nHost: gmail.googleapis.com\r\nAuthorization: Bearer " + agenttool.Placeholder + "\r\n"

	for name, request := range map[string]string{
		"another address":    "GET https://collector.example/take" + head + "\r\n",
		"a path that climbs": "GET /gmail/v1/users/me/messages/../../../../drive/v3/files" + head + "\r\n",
		"a hidden slash":     "GET /gmail/v1/users/me/messages%2F1a2b%2Ftrash" + head + "\r\n",
		"a method override":  "GET /gmail/v1/users/me/messages/1a2b" + head + "X-HTTP-Method-Override: DELETE\r\n\r\n",
		"a method parameter": "GET /gmail/v1/users/me/messages/1a2b?$httpMethod=DELETE" + head + "\r\n",
	} {
		got := gmail.raw(request)
		assert.Equal(t, http.StatusForbidden, got.Status, name)
		assert.Contains(t, got.message(), "Refused", name)
	}

	assert.Empty(t, w.service.received(), "none of them reaches the service")
	assert.Empty(t, w.approvals(""), "and none is put to the owner")

	got := gmail.raw("GET /gmail/v1/users/me/profile HTTP/1.1\r\nHost: collector.example\r\nAuthorization: Bearer " +
		agenttool.Placeholder + "\r\n\r\n")
	require.Equal(t, http.StatusOK, got.Status)

	received := w.service.received()
	require.Len(t, received, 1)
	assert.Equal(t, "https://gmail.googleapis.com/gmail/v1/users/me/profile", received[0].URL,
		"the token goes to the Integration's host, whatever the request named")
}

// Scenario: the owner takes an account back from pi while a tool of pi's
// still has a connection open. The next request on that connection is
// refused: what an agent may use is asked for every request.
func TestAnAccountTakenBackStopsAConnectionThatIsOpen(t *testing.T) {
	t.Parallel()

	w := newWorld(t, time.Minute)
	gmail := w.piTool("gmail.googleapis.com")

	require.Equal(t, http.StatusOK, gmail.do(http.MethodGet, "/gmail/v1/users/me/profile", "", nil).Status)

	_, err := w.svc.SetConnectionChats(t.Context(), "google", "work", false)
	require.NoError(t, err)

	got := gmail.do(http.MethodGet, "/gmail/v1/users/me/messages", "", nil)
	assert.Equal(t, http.StatusForbidden, got.Status)
	assert.Contains(t, got.message(), "may no longer use google:work")
	assert.Len(t, w.service.received(), 1)
}

// Scenario: a mail whose headers hide where it goes: To written twice, and a
// header the owner would not think to look for. The owner is shown every
// recipient and every header the message carries, so that what he approves is
// what is sent.
func TestTheOwnerIsShownEveryRecipientAndHeaderOfAMail(t *testing.T) {
	t.Parallel()

	w := newWorld(t, 100*time.Millisecond)
	gmail := w.piTool("gmail.googleapis.com")

	message := "From: owner@example.com\r\nTo: minh@example.com\r\nTo: collector@example.net\r\n" +
		"Subject: Invoice\r\nX-Forward-Copy: archive@example.net\r\nDate: Tue, 11 Nov 2026 09:00:00 +0700\r\n\r\nPaid.\r\n"
	body := "--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b\r\nContent-Type: message/rfc822\r\n\r\n" + message +
		"\r\n--b--\r\n"

	got := gmail.do(http.MethodPost, sendPath, "multipart/related; boundary=b", []byte(body))
	require.Equal(t, http.StatusForbidden, got.Status)

	asked := w.pending()
	assert.Contains(t, asked.Payload, orchestration.Field{Label: "To", Value: "minh@example.com, collector@example.net"})
	assert.Contains(t, asked.Payload, orchestration.Field{Label: "X-Forward-Copy", Value: "archive@example.net"})
	assert.Equal(t, "Send mail: minh@example.com, collector@example.net", asked.Title)

	for _, f := range asked.Payload {
		assert.NotEqual(t, "Date", f.Label, "what is made anew for every send is not part of what is approved")
	}
}

// standIn makes this test binary stand in for gws: installed under its name,
// it makes one request to Gmail the way a tool does, through the proxy and
// with the certificate file its environment names, and prints the answer.
const standIn = "SERVICEPROXY_TEST_TOOL"

func TestMain(m *testing.M) {
	if os.Getenv(standIn) == "" {
		os.Exit(m.Run())
	}

	//nolint:noctx // a stand-in
	req, _ := http.NewRequest(http.MethodGet, "https://gmail.googleapis.com/gmail/v1/users/me/profile", nil)
	req.Header.Set("Authorization", "Bearer "+os.Getenv("GOOGLE_WORKSPACE_CLI_TOKEN"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("failed:", err)
		os.Exit(1)
	}

	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	fmt.Printf("%d %s sent=%s\n", resp.StatusCode, body, os.Getenv("GOOGLE_WORKSPACE_CLI_TOKEN"))
}

// Scenario: the whole way, with the real launcher. pi runs gws; the launcher
// gives it a placeholder, a proxy on loopback and the proxy's certificate,
// and carries its connection to the control plane. The tool's request is
// answered by the service, which was given the account's real token.
func TestAToolRunByTheLauncherReachesTheServiceThroughTheProxy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a tool is a file without an extension, which Windows does not run; nodes are Linux")
	}

	w := newWorld(t, time.Minute)

	def := agenttool.GoogleWorkspaceCLI()
	home := t.TempDir()
	target := agenttool.ToolPath(home, def.Name, def.Version)

	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))

	self, err := os.ReadFile(os.Args[0]) //nolint:gosec // this test's own binary
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(target, self, 0o700)) //nolint:gosec // a command, run by this test

	t.Setenv(standIn, "1")

	var out bytes.Buffer

	launcher := agenttool.Launcher{
		URL: w.srv.URL, Key: w.pi, ToolsHome: home, Chat: w.chat, Stdout: &out, Stderr: &out,
	}
	require.NoError(t, launcher.Run(t.Context(), "gws", nil))
	assert.Equal(t, "200 {\"id\":\"from-the-service\"} sent="+agenttool.Placeholder+"\n", out.String())

	received := w.service.received()
	require.Len(t, received, 1)
	assert.Equal(t, "https://gmail.googleapis.com/gmail/v1/users/me/profile", received[0].URL)
	assert.Equal(t, "Bearer "+realToken, received[0].Authorization)
}
