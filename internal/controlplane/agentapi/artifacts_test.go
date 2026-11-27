package agentapi_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// workerOf runs one Task of a Run made from the chat named (the owner's own
// Run when it is empty) on a node of its own, given the Connections named,
// and returns its Dispatch's key.
func (w *world) workerOf(node, chatID string, uses ...string) string {
	w.t.Helper()

	return w.workerUnder("", node, chatID, uses...)
}

// workerUnder is workerOf under a profile with more in its YAML: its tool switches (issue #536).
func (w *world) workerUnder(more, node, chatID string, uses ...string) string {
	w.t.Helper()

	ctx := w.t.Context()

	_, err := w.svc.Profiles().ApplyProvider(ctx, "", "name: gateway\nkind: model_gateway\nmodels: [claude-opus-5-5]\n")
	require.NoError(w.t, err)
	_, _, err = w.svc.Profiles().ApplyProfile(ctx, "",
		"name: implementer\ndescription: d\nruntime: claude\nmodel: {provider: gateway, name: claude-opus-5-5}\n"+more)
	require.NoError(w.t, err)

	n, _, err := w.svc.AddNode(ctx, node, 1)
	require.NoError(w.t, err)
	require.NoError(w.t, w.svc.NodeHello(ctx, n.ID, orchestration.Hello{WorktreeRoot: "/srv/wt",
		Runtimes: []orchestration.RuntimeInfo{{Runtime: "claude", Path: "/usr/local/bin/claude", Version: "1.0.0"}}}))
	require.NoError(w.t, w.svc.NodeHeartbeat(ctx, n.ID))

	run, err := w.svc.CreateRun(ctx, orchestration.NewRun{Objective: "a sales dashboard", CreatedBy: "owner",
		ChatID: chatID, Uses: uses})
	require.NoError(w.t, err)

	_, err = w.svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{
		{Ref: "a", Profile: "implementer", Spec: "draw the dashboard"},
	})
	require.NoError(w.t, err)

	sch, err := w.svc.Schedule(ctx)
	require.NoError(w.t, err)
	require.Len(w.t, sch.Created, 1)

	key, err := w.svc.AddAgentSession(ctx, sch.Created[0], uses)
	require.NoError(w.t, err)

	return key
}

// shownID is the id a show_artifact result names.
func shownID(t *testing.T, text string) string {
	t.Helper()

	m := regexp.MustCompile(`artifact (art_\w+)`).FindStringSubmatch(text)
	require.Len(t, m, 2, text)

	return m[1]
}

// arguments is a show_artifact call's arguments as a tools server sends them.
func arguments(t *testing.T, call agenttool.ArtifactCall) string {
	t.Helper()

	raw, err := json.Marshal(call)
	require.NoError(t, err)

	return string(raw)
}

// Scenario: a Bot's pi and a worker of a Run made from its chat both find
// show_artifact on their one list (issue #536). The pi shows a page in its
// chat; the worker shows a folder its tools server read, in the same chat,
// as that chat's Bot, and its result tells it to name the artifact in its
// finish summary. Each revises the other's by its id, as everyone in one
// chat may (OpenMausBot's per-conversation pages); the same files again make
// no new revision. A worker whose Run has no chat is refused and told what
// to do instead; a path sent without its files, by a tools server of an
// earlier release, is refused in words the agent can act on; what is over a
// limit or not of this chat changes nothing. The coordinator token, which
// has no MCP, is neither offered nor allowed the tool.
func TestBotsAndWorkersShowArtifactsInTheirChat(t *testing.T) {
	t.Parallel()

	w, _, _ := gatewayWorld(t, false, map[string]string{})
	ctx := t.Context()

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)

	chat, err := w.svc.CreateChat(ctx, "", "")
	require.NoError(t, err)

	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, orchestration.DefaultBotID)
	require.NoError(t, err)

	worker := w.workerOf("node_a", chat.ID)
	ownersWorker := w.workerOf("node_b", "")

	for _, key := range []string{pi, worker} {
		tools := w.tools(key)
		require.Contains(t, toolNames(tools), "show_artifact")
	}

	assert.Contains(t, w.tools(worker)[1].Description, "in the chat your Run came from")

	page := `<!doctype html><title>Sales</title><h1>Sales</h1>`

	status, text, isError := w.use(pi, "show_artifact", arguments(t,
		agenttool.ArtifactCall{Title: "Sales this month", HTML: page}))
	require.Equal(t, http.StatusOK, status, text)
	require.False(t, isError, text)

	mine := shownID(t, text)
	assert.Equal(t, "Shown as artifact "+mine+" (revision 1). To change it, call show_artifact again with id "+
		mine+".", text)

	_, text, isError = w.use(worker, "show_artifact", arguments(t, agenttool.ArtifactCall{
		Title: "Sales by region", Path: "dashboard", Files: []agenttool.ArtifactFile{
			{Path: "index.html", Data: []byte(page)}, {Path: "data.json", Data: []byte(`[{"region":"North"}]`)},
		},
	}))
	require.False(t, isError, text)

	theirs := shownID(t, text)
	assert.Contains(t, text, "(revision 1)")
	assert.Contains(t, text, "Name artifact "+theirs+" in your finish summary.")

	shown, err := w.svc.GetArtifact(ctx, theirs)
	require.NoError(t, err)
	assert.Equal(t, chat.ID, shown.ChatID, "a worker's artifact is in its Run's chat")
	assert.Equal(t, orchestration.DefaultBotID, shown.Bot, "as the chat's Bot's")
	assert.Equal(t, "index.html", shown.Entry)

	_, text, isError = w.use(worker, "show_artifact", arguments(t,
		agenttool.ArtifactCall{ID: mine, HTML: page + "<p>North leads.</p>"}))
	require.False(t, isError, text)
	assert.Contains(t, text, "Shown as artifact "+mine+" (revision 2)", "a worker revises its chat's Bot's")

	_, text, isError = w.use(pi, "show_artifact", arguments(t, agenttool.ArtifactCall{ID: theirs, HTML: page}))
	require.False(t, isError, text)
	assert.Contains(t, text, "Shown as artifact "+theirs+" (revision 2)", "the Bot revises its worker's")

	_, text, isError = w.use(pi, "show_artifact", arguments(t, agenttool.ArtifactCall{ID: theirs, HTML: page}))
	assert.False(t, isError, text)
	assert.Equal(t, "Artifact "+theirs+" is still at revision 2: its files and title are unchanged, so nothing "+
		"new was shown.", text)

	_, text, isError = w.use(ownersWorker, "show_artifact", arguments(t,
		agenttool.ArtifactCall{Title: "Sales", HTML: page}))
	assert.True(t, isError)
	assert.Equal(t, "Your Run has no chat to show an artifact in: name the file's path in your finish summary "+
		"instead.", text)

	refused := map[string]agenttool.ArtifactCall{
		"a title":           {HTML: page},
		"not both":          {Title: "Sales", HTML: page, Path: "dashboard"},
		"256 KB":            {Title: "Sales", HTML: strings.Repeat("x", 257<<10)},
		"index.html":        {Title: "Sales", Files: []agenttool.ArtifactFile{{Path: "a/b.html", Data: []byte(page)}}},
		"no artifact art_x": {ID: "art_x", HTML: page},
	}
	for want, call := range refused {
		_, text, isError = w.use(worker, "show_artifact", arguments(t, call))
		assert.True(t, isError, want)
		assert.Contains(t, text, want)
	}

	list, err := w.svc.ListArtifacts(ctx, orchestration.ArtifactFilter{ChatID: chat.ID})
	require.NoError(t, err)
	assert.Len(t, list, 2, "a refused call shows nothing")

	assert.NotContains(t, toolNames(w.tools(coordinator)), "show_artifact")

	status, _, _ = w.use(coordinator, "show_artifact", arguments(t, agenttool.ArtifactCall{Title: "x", HTML: page}))
	assert.Equal(t, http.StatusNotFound, status)

	assert.Contains(t, w.logs.String(), "an agent showed an artifact")
	assert.NotContains(t, w.logs.String(), "North leads")
}
