package agentapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/agentapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

const hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB8TL1wZn5vEveB+nHTVCX490uF9kH8g6jcBT6BgFep8"

const sshPrivateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjE\n-----END OPENSSH PRIVATE KEY-----\n"

// withSSHKey serves the agent API again with the control plane's SSH key.
func (w *world) withSSHKey() {
	w.t.Helper()

	mux := http.NewServeMux()
	agentapi.New(w.svc, nil, slog.New(slog.NewTextHandler(w.logs, nil))).
		WithGitSSHKey(func(context.Context) (string, string, error) {
			return sshPrivateKey, "ssh-ed25519 AAAA hypomnema", nil
		}).Register(mux)

	w.srv.Close()
	w.srv = httptest.NewServer(mux)
	w.t.Cleanup(w.srv.Close)
}

// workerOnRepo runs one Task of an owner's Run on repo on a node of its own,
// and returns its Dispatch's key and id.
func (w *world) workerOnRepo(node, repo string) (string, string) {
	w.t.Helper()

	ctx := w.t.Context()

	_, err := w.svc.ApplyProvider(ctx, "", "name: gateway\nkind: model_gateway\nmodels: [claude-opus-5-5]\n")
	require.NoError(w.t, err)
	_, _, err = w.svc.ApplyProfile(ctx, "",
		"name: implementer\ndescription: d\nruntime: claude\nmodel: {provider: gateway, name: claude-opus-5-5}\n")
	require.NoError(w.t, err)

	n, _, err := w.svc.AddNode(ctx, node, 1)
	require.NoError(w.t, err)
	require.NoError(w.t, w.svc.NodeHello(ctx, n.ID, orchestration.Hello{WorktreeRoot: "/srv/wt",
		Runtimes: []orchestration.RuntimeInfo{{Runtime: "claude", Path: "/usr/local/bin/claude", Version: "1.0.0"}}}))
	require.NoError(w.t, w.svc.NodeHeartbeat(ctx, n.ID))

	run, err := w.svc.CreateRun(ctx, orchestration.NewRun{Objective: "a fix", CreatedBy: "owner", Repo: repo})
	require.NoError(w.t, err)

	_, err = w.svc.CreateTasks(ctx, run.ID, []orchestration.NewTask{{Ref: "a", Profile: "implementer", Spec: "fix"}})
	require.NoError(w.t, err)

	sch, err := w.svc.Schedule(ctx)
	require.NoError(w.t, err)
	require.Len(w.t, sch.Created, 1)

	key, err := w.svc.AddAgentSession(ctx, sch.Created[0], nil)
	require.NoError(w.t, err)

	return key, sch.Created[0]
}

func (w *world) sshKey(key string) (int, agenttool.GitSSHKey) {
	w.t.Helper()

	status, body := w.call(http.MethodGet, agenttool.PathGitSSHKey, key, nil)

	var got agenttool.GitSSHKey
	if status == http.StatusOK {
		require.NoError(w.t, json.Unmarshal(body, &got))
	}

	return status, got
}

// Scenario: the owner registered the company's platform repository with its
// SSH address (issue #625). A worker of a Run on it gets the control plane's
// SSH key for its git; a worker of a Run on another repository of the same
// host, registered without one, does not. Neither do the coordinator, a Bot's
// pi or its Routine's script, which clone nothing, nor the owner's token. Once
// the Dispatch is over its key gets nothing. Who got the key is logged, never
// the key.
func TestOnlyAWorkerWhoseRepositoryIsClonedOverSSHGetsTheSSHKey(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.withSSHKey()

	ctx := t.Context()

	_, err := w.svc.AddGitHost(ctx, "gitlab.company.example", githost.GitLab)
	require.NoError(t, err)
	_, err = w.svc.AddRepo(ctx, "platform", "https://gitlab.company.example/mobility/platform", "", "",
		"ssh://git@167.254.68.172:222/mobility/platform.git", hostKey)
	require.NoError(t, err)
	_, err = w.svc.AddRepo(ctx, "site", "https://gitlab.company.example/mobility/site", "", "", "", "")
	require.NoError(t, err)

	onPlatform, platformDispatch := w.workerOnRepo("box-1", "platform")
	onSite, _ := w.workerOnRepo("box-2", "site")

	status, key := w.sshKey(onPlatform)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, sshPrivateKey, key.PrivateKey)
	assert.Equal(t, "ssh-ed25519 AAAA hypomnema", key.PublicKey)
	assert.Equal(t, hostKey, key.HostKeys, "the host's keys the owner confirmed come with it")

	status, _ = w.sshKey(onSite)
	assert.Equal(t, http.StatusForbidden, status, "a repository registered without an SSH address")

	_, coordinator, err := w.svc.AddToken(ctx, "pi", bearertoken.Coordinator)
	require.NoError(t, err)
	_, owner, err := w.svc.AddToken(ctx, "me", bearertoken.Owner)
	require.NoError(t, err)

	bot, err := w.svc.PutBot(ctx, orchestration.Bot{Name: "vmap"})
	require.NoError(t, err)
	chat, err := w.svc.CreateChat(ctx, bot.ID, "")
	require.NoError(t, err)
	pi, err := w.svc.AddBotToken(ctx, "pi", chat.ID, bot.ID)
	require.NoError(t, err)

	routine, err := w.svc.Routines().PutRoutine(ctx, routinestore.RoutineSpec{
		Bot: bot.ID, Name: "sync", Kind: routinestore.RoutineScript, Script: "sync.sh", ScriptText: "#!/bin/sh\n",
		EveryMinutes: 60,
	}, routinestore.RoutineAuthor{})
	require.NoError(t, err)
	script, err := w.svc.AddRoutineToken(ctx, "pi", routine.ID)
	require.NoError(t, err)

	for name, key := range map[string]string{
		"the coordinator": coordinator, "a Bot's pi": pi, "a Routine's script": script, "the owner": owner,
	} {
		status, _ := w.sshKey(key)
		assert.Equal(t, http.StatusForbidden, status, name)
	}

	status, _ = w.sshKey("")
	assert.Equal(t, http.StatusUnauthorized, status)

	assert.Contains(t, w.logs.String(), "agentapi.git_ssh_key")
	assert.NotContains(t, w.logs.String(), "b3BlbnNzaC1rZXktdjE")

	require.NoError(t, w.svc.RevokeAgentSessions(ctx, platformDispatch))

	status, _ = w.sshKey(onPlatform)
	assert.Equal(t, http.StatusUnauthorized, status, "the Dispatch is over")
}
