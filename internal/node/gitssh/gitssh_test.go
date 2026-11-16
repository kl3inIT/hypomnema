package gitssh

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

const (
	privateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n"
	publicKey  = "ssh-ed25519 AAAAC3Nza hypomnema"
)

// agentAPI answers the key to the Dispatch key "hyp_s_worker" and refuses
// any other, as the control plane's agent API does.
func agentAPI(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != agenttool.PathGitSSHKey {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", "application/json")

		if r.Header.Get("Authorization") != "Bearer hyp_s_worker" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"this worker's repository is not registered with an SSH address"}`))

			return
		}

		//nolint:gosec // A fake key, as the agent API answers.
		_ = json.NewEncoder(w).Encode(agenttool.GitSSHKey{PrivateKey: privateKey, PublicKey: publicKey})
	}))
	t.Cleanup(srv.Close)

	return srv
}

// Scenario: git runs the helper as its ssh for a clone. The helper gets the
// key for this Dispatch, writes it to a file only the node's user reads, runs
// ssh with that key alone, a host remembered in the node's own known_hosts,
// and git's arguments after its own; once ssh is done the file is gone.
func TestTheKeyIsOnDiskOnlyWhileSSHRuns(t *testing.T) {
	t.Parallel()

	srv := agentAPI(t)
	knownHosts := filepath.Join(t.TempDir(), "ssh", "known_hosts")

	var keyFile string

	h := Helper{URL: srv.URL, Key: "hyp_s_worker", KnownHosts: knownHosts, Client: srv.Client(),
		SSH: func(_ context.Context, args []string) error {
			i := slices.Index(args, "-i")
			require.GreaterOrEqual(t, i, 0)

			keyFile = args[i+1]

			raw, err := os.ReadFile(keyFile) //nolint:gosec // the file the helper named
			require.NoError(t, err)
			assert.Equal(t, privateKey, string(raw))

			if runtime.GOOS != "windows" {
				info, err := os.Stat(keyFile)
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			}

			assert.Equal(t, []string{
				"-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=accept-new",
				"-o", "UserKnownHostsFile=" + knownHosts,
				"-p", "222", "git@167.254.68.172", "git-upload-pack 'mobility/platform.git'",
			}, args[i+2:])
			assert.DirExists(t, filepath.Dir(knownHosts))

			return nil
		}}

	require.NoError(t, h.Run(t.Context(), []string{"-p", "222", "git@167.254.68.172",
		"git-upload-pack 'mobility/platform.git'"}))
	require.NotEmpty(t, keyFile)
	assert.NoFileExists(t, keyFile)
}

// Scenario: a worker whose repository is not cloned over SSH, or a key the
// control plane does not know, gets no key: ssh is never run and git is
// told why.
func TestARefusedKeyRunsNoSSH(t *testing.T) {
	t.Parallel()

	srv := agentAPI(t)
	ran := false
	h := Helper{URL: srv.URL, Key: "hyp_s_other", KnownHosts: filepath.Join(t.TempDir(), "known_hosts"),
		Client: srv.Client(), SSH: func(context.Context, []string) error { ran = true; return nil }}

	err := h.Run(t.Context(), []string{"git@gitlab.example", "git-upload-pack 'a/b.git'"})
	require.ErrorContains(t, err, "not registered with an SSH address")
	assert.False(t, ran)

	h.Key = ""
	require.ErrorContains(t, h.Run(t.Context(), []string{"git@gitlab.example"}), agenttool.EnvKey)
}

// Scenario: the git host refuses the key. ssh's own code goes back to git,
// the file is gone all the same, and the node's log says which public key the
// host must have, as Coder's helper prints it.
func TestARefusedSignInSaysWhichKeyToAdd(t *testing.T) {
	t.Parallel()

	srv := agentAPI(t)

	var (
		stderr  bytes.Buffer
		keyFile string
	)

	h := Helper{URL: srv.URL, Key: "hyp_s_worker", KnownHosts: filepath.Join(t.TempDir(), "known_hosts"),
		Client: srv.Client(), Stderr: &stderr, SSH: func(ctx context.Context, args []string) error {
			keyFile = args[slices.Index(args, "-i")+1]

			return exitWith(ctx, 255)
		}}

	err := h.Run(t.Context(), []string{"git@gitlab.example", "git-upload-pack 'a/b.git'"})

	var exit *agenttool.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 255, exit.Code)
	assert.Contains(t, stderr.String(), publicKey)
	assert.NoFileExists(t, keyFile)
}

// Git runs the helper through the shell with each path quoted, and tells git
// it takes OpenSSH's options.
func TestGitIsToldToRunTheHelper(t *testing.T) {
	t.Parallel()

	env := Env("/home/n/.hypomnema-node/bin/hypomnema-node", "/home/n/it's/known_hosts", "https://cp", "hyp_s_x")
	assert.Equal(t, []string{
		`GIT_SSH_COMMAND='/home/n/.hypomnema-node/bin/hypomnema-node' gitssh --known-hosts '/home/n/it'\''s/known_hosts' --`,
		"GIT_SSH_VARIANT=ssh", agenttool.EnvURL + "=https://cp", agenttool.EnvKey + "=hyp_s_x",
	}, env)
}

// exitWith is a process that ended with code, as ssh's would.
func exitWith(ctx context.Context, code int) error {
	//nolint:gosec // This test binary itself, which exits at once in TestMain.
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = append(os.Environ(), exitEnv+"="+strconv.Itoa(code))

	return cmd.Run()
}

const exitEnv = "GITSSH_TEST_EXIT"

func TestMain(m *testing.M) {
	if code, err := strconv.Atoi(os.Getenv(exitEnv)); err == nil {
		os.Exit(code)
	}

	os.Exit(m.Run())
}
