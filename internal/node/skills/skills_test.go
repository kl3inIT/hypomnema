package skills

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

func packed(t *testing.T, name string) (nodeproto.Skill, []byte) {
	t.Helper()

	archive, err := skillpack.Skill{Files: []skillpack.File{
		{Path: "SKILL.md", Data: []byte("---\nname: " + name + "\ndescription: d.\n---\n")},
		{Path: "scripts/run.sh", Exec: true, Data: []byte("echo hi\n")},
	}}.Archive()
	require.NoError(t, err)

	sk, err := skillpack.FromArchive(archive)
	require.NoError(t, err)

	return nodeproto.Skill{Name: name, Revision: 1, Digest: sk.Digest}, archive
}

// controlPlane serves archives by digest to the node token "tok", counting
// what it is asked.
func controlPlane(t *testing.T, archives map[string][]byte) (*httptest.Server, *int) {
	t.Helper()

	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++

		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)

			return
		}

		b, ok := archives[r.URL.Path[len(nodeproto.PathSkills):]]
		if !ok {
			http.Error(w, "no offer of this node names that skill", http.StatusNotFound)

			return
		}

		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)

	return srv, &asked
}

// Scenario: the node fetches a skill its offer names once, keeps it under its
// digest and fetches nothing for the next offer that names it. An archive
// whose files are not the digest the offer names is refused, and so is a
// skill the control plane will not give.
func TestTheNodeFetchesEachRevisionOnce(t *testing.T) {
	sk, archive := packed(t, "review-go")
	liar, _ := packed(t, "pdf")

	srv, asked := controlPlane(t, map[string][]byte{sk.Digest: archive, liar.Digest: archive})
	c := Cache{Home: t.TempDir(), URL: srv.URL, Token: "tok", Client: srv.Client()}

	require.NoError(t, c.Ensure(t.Context(), []nodeproto.Skill{sk}))
	require.NoError(t, c.Ensure(t.Context(), []nodeproto.Skill{sk}))
	assert.Equal(t, 1, *asked, "the second offer finds it kept")

	b, err := os.ReadFile(filepath.Join(Dir(c.Home, sk.Digest), "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "name: review-go")

	err = c.Ensure(t.Context(), []nodeproto.Skill{liar})
	require.ErrorContains(t, err, "not "+liar.Digest)

	missing := nodeproto.Skill{Name: "x", Revision: 1, Digest: "ab" + liar.Digest[2:]}
	require.ErrorContains(t, c.Ensure(t.Context(), []nodeproto.Skill{missing}), "404")

	require.ErrorContains(t, c.Ensure(t.Context(), []nodeproto.Skill{{Name: "x", Digest: "../../etc"}}),
		"not a digest")

	entries, err := os.ReadDir(filepath.Join(c.Home, "skills"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "nothing is left of what was refused")
}

// Scenario: Codex's skills folder holds what the offer names and nothing a
// former Dispatch had, and Codex's own .system stays; Claude Code's plugin
// folder holds the skills under the manifest that names them hypomnema.
func TestSkillsArePlacedWhereEachCLIReadsThem(t *testing.T) {
	sk, archive := packed(t, "review-go")
	srv, _ := controlPlane(t, map[string][]byte{sk.Digest: archive})
	c := Cache{Home: t.TempDir(), URL: srv.URL, Token: "tok", Client: srv.Client()}
	require.NoError(t, c.Ensure(t.Context(), []nodeproto.Skill{sk}))

	codex := filepath.Join(t.TempDir(), "skills")
	require.NoError(t, os.MkdirAll(filepath.Join(codex, ".system", "openai-docs"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(codex, "taken-away"), 0o700))

	require.NoError(t, Place(c.Home, codex, []nodeproto.Skill{sk}))

	entries, err := os.ReadDir(codex)
	require.NoError(t, err)

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}

	assert.Equal(t, []string{".system", "review-go"}, names)

	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(codex, "review-go", "scripts", "run.sh"))
		require.NoError(t, err)
		assert.NotZero(t, info.Mode().Perm()&0o100, "a script stays executable")
	}

	plugin := t.TempDir()
	require.NoError(t, PlacePlugin(c.Home, plugin, []nodeproto.Skill{sk}))

	b, err := os.ReadFile(filepath.Join(plugin, ".claude-plugin", "plugin.json")) //nolint:gosec // the test's own folder
	require.NoError(t, err)

	var manifest map[string]string
	require.NoError(t, json.Unmarshal(b, &manifest))
	assert.Equal(t, PluginName, manifest["name"])
	assert.FileExists(t, filepath.Join(plugin, "skills", "review-go", "SKILL.md"))
}
