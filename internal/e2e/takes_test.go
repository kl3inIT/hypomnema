package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/nodeapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/controlplane/takes"
	"github.com/kl3inIT/hypomnema/internal/node/daemon"
	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite/sqlitetest"
	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// filmingPhone is a node's phone that films as take.go does, for the end to
// end: a take's segment is a file of its folder, and its take.json is written
// when it stops.
type filmingPhone struct {
	*fakePhone

	dir   string
	mu    sync.Mutex
	takes map[string]*devicetool.Take
	ready chan struct{}
	ended []string
	drops []string
}

func newFilmingPhone(t *testing.T) *filmingPhone {
	t.Helper()

	p := &filmingPhone{
		fakePhone: newFakePhone(), dir: t.TempDir(), takes: map[string]*devicetool.Take{},
		ready: make(chan struct{}, 1),
	}
	p.state = nodeproto.DeviceState{State: nodeproto.DeviceRunning}

	return p
}

// segment is the bytes a take's one segment holds: large enough to take more than one write.
var segment = bytes.Repeat([]byte("frame "), 200_000)

func (p *filmingPhone) Call(ctx context.Context, agent, tool string, raw json.RawMessage) nodeproto.DeviceAnswer {
	if !devicetool.IsVideo(tool) {
		return p.fakePhone.Call(ctx, agent, tool, raw)
	}

	c, err := devicetool.ReadNode(tool, raw)
	if err != nil {
		return nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{{Type: "text", Text: err.Error()}}, IsError: true}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	t := p.takes[c.Take]

	switch {
	case tool == devicetool.VideoStart && t == nil:
		t = &devicetool.Take{ID: c.Take, Title: c.Title, Agent: agent, State: devicetool.TakeRecording,
			Steps: []devicetool.TakeStep{}, Pauses: []devicetool.TakePause{}}
		p.takes[c.Take] = t
		_ = os.MkdirAll(filepath.Join(p.dir, c.Take), 0o700)
	case t == nil:
		return nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{{Type: "text", Text: "no take"}}, IsError: true}
	case tool == devicetool.VideoSay:
		t.Steps = append(t.Steps, devicetool.TakeStep{AtMs: 1200, Text: c.Text})
	case tool == devicetool.VideoStop || tool == devicetool.VideoEnd:
		p.finishLocked(t, map[bool]string{true: devicetool.EndedStop, false: c.EndedBy}[tool == devicetool.VideoStop])
	}

	out := *t

	return nodeproto.DeviceAnswer{Content: []nodeproto.DeviceContent{{Type: "text", Text: devicetool.Describe(out)}},
		Take: &out}
}

// finishLocked ends a take as its writer does: its segment written, then take.json.
func (p *filmingPhone) finishLocked(t *devicetool.Take, endedBy string) {
	if !t.Running() {
		return
	}

	sum := sha256.Sum256(segment)
	_ = os.WriteFile(filepath.Join(p.dir, t.ID, "seg-001.mp4"), segment, 0o600)

	t.State, t.EndedBy, t.DurationMs = devicetool.TakeUploading, endedBy, 4000
	t.Segments = []devicetool.TakeSegment{{File: "seg-001.mp4", LengthMs: 4000, Width: 720, Height: 1280,
		Bytes: int64(len(segment)), SHA256: hex.EncodeToString(sum[:])}}

	raw, err := json.Marshal(t)
	if err != nil {
		return
	}

	_ = os.WriteFile(filepath.Join(p.dir, t.ID, "take.json"), raw, 0o600)
	p.ended = append(p.ended, t.ID+" "+endedBy)

	select {
	case p.ready <- struct{}{}:
	default:
	}
}

func (p *filmingPhone) HeldTakes() []nodeproto.TakeHeld {
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []nodeproto.TakeHeld
	for _, t := range p.takes {
		out = append(out, nodeproto.TakeHeld{ID: t.ID, State: t.State})
	}

	return out
}

func (p *filmingPhone) Uploads() []nodeproto.TakeUpload {
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []nodeproto.TakeUpload

	for _, t := range p.takes {
		if t.State == devicetool.TakeUploading {
			out = append(out, nodeproto.TakeUpload{
				ID: t.ID, Dir: filepath.Join(p.dir, t.ID), Files: []string{"seg-001.mp4", "take.json"},
			})
		}
	}

	return out
}

func (p *filmingPhone) UploadsReady() <-chan struct{} { return p.ready }

func (p *filmingPhone) StopTake(id, endedBy, _ string) (devicetool.Take, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	t := p.takes[id]
	if t == nil {
		return devicetool.Take{}, errors.New("no take")
	}

	p.finishLocked(t, endedBy)

	return *t, nil
}

func (p *filmingPhone) DropTake(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.takes, id)
	p.drops = append(p.drops, id)
	_ = os.RemoveAll(filepath.Join(p.dir, id))
}

func (p *filmingPhone) dropped() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.drops...)
}

// filmRig is a control plane that keeps takes, with a node that films.
type filmRig struct {
	t      *testing.T
	srv    *httptest.Server
	svc    *orchestration.Service
	store  *takes.Store
	held   *filmingPhone
	node   orchestration.Node
	worker string
	owner  string
	agent  string
	// nodeToken is the node's own token.
	nodeToken string
}

func newFilmRig(t *testing.T, held *filmingPhone, store *takes.Store) *filmRig {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: sqlitetest.Migrated(t, orchestration.Migrate)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	logger := slog.New(slog.DiscardHandler)
	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	mux := http.NewServeMux()
	nodes := nodeapi.New(svc, logger)
	shared := phone.New(phone.Config{Nodes: svc, Link: nodes, Logger: logger, Takes: store})
	nodes.WithTakes(store, shared.NodeTakes).Register(mux)
	agentapi.New(svc, nil, logger).WithDevice(shared).Register(mux)
	require.NoError(t, clientapi.New(svc, logger, stop).WithTakes(ownerTakes{store}).Register(mux))

	// The shared server's short read timeout, which a take's upload outlives with its own deadline.
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.ReadTimeout = 200 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	n, token, err := svc.AddNode(t.Context(), "phone", 1)
	require.NoError(t, err)

	state, err := statedb.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), time.Now)
	require.NoError(t, err)

	d := daemon.New(daemon.Config{
		URL: srv.URL, Token: token, WorktreeRoot: "/srv/wt", Version: "test", Device: nodeproto.DeviceAndroid,
		KVM: func() bool { return true }, Phone: held, Takes: held,
	}, state, newFakeRunner(), logger)

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan error, 1)

	go func() { done <- d.Run(ctx) }()

	t.Cleanup(func() {
		cancel()

		if err := <-done; err != nil {
			t.Logf("the node ended with: %v", err)
		}

		_ = state.Close()
	})

	require.Eventually(t, func() bool {
		_, err := nodes.Files(t.Context(), n.ID, "/srv/wt", workspacefs.Request{Op: workspacefs.OpList})

		return !errors.Is(err, orchestration.ErrUnavailable)
	}, wait, 20*time.Millisecond)

	worker, err := svc.AddAgentSession(t.Context(), "dsp_1", nil)
	require.NoError(t, err)

	_, owner, err := svc.AddToken(t.Context(), "owner", bearertoken.Owner)
	require.NoError(t, err)

	_, coordinator, err := svc.AddToken(t.Context(), "coordinator", bearertoken.Coordinator)
	require.NoError(t, err)

	return &filmRig{t: t, srv: srv, svc: svc, store: store, held: held, node: n, worker: worker, owner: owner,
		agent: coordinator, nodeToken: token}
}

// ownerTakes are the store as the owner deletes takes.
type ownerTakes struct{ *takes.Store }

func (o ownerTakes) Delete(_ context.Context, id string) error { return o.Store.Delete(id) }

// tool is a worker's call of a phone tool: the text it was answered.
func (r *filmRig) tool(name, args string) (string, bool) {
	r.t.Helper()

	body, err := json.Marshal(agenttool.BrowserCall{Tool: name, Arguments: json.RawMessage(args)})
	require.NoError(r.t, err)

	req, err := http.NewRequestWithContext(r.t.Context(), http.MethodPost, r.srv.URL+agenttool.PathDeviceCall,
		bytes.NewReader(body))
	require.NoError(r.t, err)
	req.Header.Set("Authorization", "Bearer "+r.worker)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)

	defer resp.Body.Close() //nolint:errcheck // a test's response body

	var answer nodeproto.DeviceAnswer
	require.NoError(r.t, json.NewDecoder(resp.Body).Decode(&answer))
	require.Nil(r.t, answer.Take, "the agent is given no take itself, and never a file")

	var text []string
	for _, c := range answer.Content {
		text = append(text, c.Text)
	}

	return strings.Join(text, " "), answer.IsError
}

// api is a call of the client API: its status, headers and body.
func (r *filmRig) api(token, method, path string, header http.Header) (int, http.Header, []byte) {
	r.t.Helper()

	req, err := http.NewRequestWithContext(r.t.Context(), method, r.srv.URL+clientapi.BasePath+path, nil)
	require.NoError(r.t, err)
	req.Header = header.Clone()

	if req.Header == nil {
		req.Header = http.Header{}
	}

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)

	defer resp.Body.Close() //nolint:errcheck // a test's response body

	body, err := io.ReadAll(resp.Body)
	require.NoError(r.t, err)

	return resp.StatusCode, resp.Header, body
}

// Scenario: a worker films the phone (issue #817). Its video_start reaches the
// node, which films; video_say and video_stop go the same way; the node
// uploads the segment and take.json on its own route, past the server's read
// timeout, and drops its copy; the take is then available to the owner, who
// plays a range of its segment, downloads it as a zip and deletes it. An
// agent's token reads none of it.
func TestAWorkerFilmsATakeTheOwnerWatches(t *testing.T) {
	t.Parallel()

	store, err := takes.Open(filepath.Join(t.TempDir(), "takes"))
	require.NoError(t, err)

	r := newFilmRig(t, newFilmingPhone(t), store)

	text, failed := r.tool(devicetool.VideoStart, `{"title":"Sign in to the shop","request_id":"r1"}`)
	require.False(t, failed, text)
	assert.Contains(t, text, "recording")

	list := store.List()
	require.Len(t, list, 1)
	id := list[0].ID
	assert.Equal(t, r.node.ID, list[0].Node)

	text, failed = r.tool(devicetool.VideoSay, `{"take":"`+id+`","text":"Tap Sign in","tap":"360,640"}`)
	require.False(t, failed, text)

	text, failed = r.tool(devicetool.VideoStop, `{"take":"`+id+`"}`)
	require.False(t, failed, text)

	require.Eventually(t, func() bool {
		take, err := store.Get(id)

		return err == nil && take.State == devicetool.TakeAvailable
	}, 10*time.Second, 20*time.Millisecond)

	assert.Eventually(t, func() bool { return len(r.held.dropped()) == 1 }, wait, 10*time.Millisecond,
		"the node drops its copy once take.json was taken")

	text, failed = r.tool(devicetool.VideoStatus, `{"take":"`+id+`"}`)
	require.False(t, failed)
	assert.Contains(t, text, "available")

	status, _, body := r.api(r.owner, http.MethodGet, "/takes/"+id, nil)
	require.Equal(t, http.StatusOK, status, string(body))

	var take struct {
		State string `json:"state"`
		Steps []struct {
			Text string `json:"text"`
		} `json:"steps"`
	}
	require.NoError(t, json.Unmarshal(body, &take))
	assert.Equal(t, "available", take.State)
	require.Len(t, take.Steps, 1)

	status, header, body := r.api(r.owner, http.MethodGet, "/takes/"+id+"/segments/seg-001.mp4",
		http.Header{"Range": {"bytes=6-11"}})
	require.Equal(t, http.StatusPartialContent, status, string(body))
	assert.Equal(t, "frame ", string(body))
	assert.Equal(t, "video/mp4", header.Get("Content-Type"))
	assert.Equal(t, "bytes", header.Get("Accept-Ranges"))

	status, _, body = r.api(r.owner, http.MethodGet, "/takes/"+id+"/archive", nil)
	require.Equal(t, http.StatusOK, status, string(body))

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)

	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}

	assert.Equal(t, []string{"seg-001.mp4", "take.json"}, names)

	for _, path := range []string{"/takes", "/takes/" + id, "/takes/" + id + "/segments/seg-001.mp4",
		"/takes/" + id + "/archive"} {
		status, _, _ = r.api(r.agent, http.MethodGet, path, nil)
		assert.Equal(t, http.StatusForbidden, status, "an agent's token reads no take: %s", path)
	}

	status, _, _ = r.api(r.owner, http.MethodGet, "/takes/"+id+"/segments/..%2Ftake.json", nil)
	assert.NotEqual(t, http.StatusOK, status, "a path out of the take is refused")

	status, _, _ = r.api(r.owner, http.MethodDelete, "/takes/"+id, nil)
	require.Equal(t, http.StatusNoContent, status)

	status, _, _ = r.api(r.owner, http.MethodGet, "/takes/"+id, nil)
	assert.Equal(t, http.StatusNotFound, status)
}

// Scenario: the control plane restarts while a take is filmed, and the owner
// deleted another take its node still holds. At the node's next hello the
// running take, which no lease holds any more, is ended "restart", and the
// deleted one is dropped; another node's token cannot upload a take.
func TestAHelloSettlesTheTakesANodeHolds(t *testing.T) {
	t.Parallel()

	store, err := takes.Open(filepath.Join(t.TempDir(), "takes"))
	require.NoError(t, err)

	held := newFilmingPhone(t)

	// What the earlier control plane recorded, and what the node held when it came back.
	const running, deleted = "tk_00000000000000aa", "tk_00000000000000bb"

	held.takes[running] = &devicetool.Take{ID: running, State: devicetool.TakeRecording}
	held.takes[deleted] = &devicetool.Take{ID: deleted, State: devicetool.TakeUploading}
	require.NoError(t, os.MkdirAll(filepath.Join(held.dir, running), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(held.dir, deleted), 0o700))

	for _, id := range []string{running, deleted} {
		require.NoError(t, store.Create(devicetool.Take{ID: id, Title: "x", Agent: "d-1", Node: "nod_unknown",
			State: devicetool.TakeRecording}))
	}

	require.NoError(t, store.Delete(deleted))

	r := newFilmRig(t, held, store)

	require.Eventually(t, func() bool {
		held.mu.Lock()
		defer held.mu.Unlock()

		return len(held.ended) == 1
	}, wait, 10*time.Millisecond)

	held.mu.Lock()
	assert.Equal(t, []string{running + " restart"}, held.ended)
	held.mu.Unlock()

	assert.Contains(t, held.dropped(), deleted, "the deleted take is dropped at hello")

	// The running take was bound to another node by the earlier control plane: this node's upload is refused.
	take, err := store.Get(running)
	require.NoError(t, err)
	assert.NotEqual(t, devicetool.TakeAvailable, take.State)
	assert.NotContains(t, held.dropped(), running, "a refused upload keeps the node's copy")

	// A take bound to this node is uploaded by it, slowly, past the server's read timeout; another node's
	// token is refused.
	const slow = "tk_00000000000000cc"
	require.NoError(t, store.Create(devicetool.Take{ID: slow, Title: "x", Agent: "d-1", Node: r.node.ID,
		State: devicetool.TakeUploading}))

	assert.Equal(t, http.StatusOK, r.put(r.nodeToken, slow, "seg-001.mp4", 400*time.Millisecond))

	_, other, err := r.svc.AddNode(t.Context(), "other", 1)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, r.put(other, slow, "seg-002.mp4", 0))
}

// trickle gives its chunks a pause apart.
type trickle struct {
	chunks [][]byte
	pause  time.Duration
}

func (r *trickle) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}

	time.Sleep(r.pause)

	n := copy(p, r.chunks[0])
	r.chunks = r.chunks[1:]

	return n, nil
}

// put uploads a small file of a take as a node does, its three parts a pause apart: the status answered.
func (r *filmRig) put(token, take, name string, pause time.Duration) int {
	r.t.Helper()

	data := []byte("abcdefghi")
	sum := sha256.Sum256(data)

	req, err := http.NewRequestWithContext(r.t.Context(), http.MethodPut,
		r.srv.URL+nodeproto.PathTakes+take+"/files/"+name,
		&trickle{chunks: [][]byte{data[:3], data[3:6], data[6:]}, pause: pause})
	require.NoError(r.t, err)

	req.ContentLength = int64(len(data))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(nodeproto.TakeSumHeader, hex.EncodeToString(sum[:]))

	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)

	defer resp.Body.Close() //nolint:errcheck // a test's response body

	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode
}
