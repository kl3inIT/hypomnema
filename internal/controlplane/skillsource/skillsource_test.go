package skillsource

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

const commit = "0123456789abcdef0123456789abcdef01234567"

func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, body := range files {
		h := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		require.NoError(t, tw.WriteHeader(h))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	return buf.Bytes()
}

// github serves one repository, anthropics/skills, at one commit for the
// branch main and for HEAD.
func github(t *testing.T) (*GitHub, *[]string) {
	t.Helper()

	body := tarball(t, map[string]string{
		"anthropics-skills-0123456/README.md":           "repo",
		"anthropics-skills-0123456/skills/pdf/SKILL.md": "---\nname: pdf\ndescription: Read PDFs.\n---\n",
	})

	var asked []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)

		switch r.URL.Path {
		case "/repos/anthropics/skills/commits/main", "/repos/anthropics/skills/commits/HEAD":
			assert.Equal(t, "application/vnd.github.sha", r.Header.Get("Accept"))
			_, _ = w.Write([]byte(commit))
		case "/anthropics/skills/tar.gz/" + commit:
			_, _ = w.Write(body)
		case "/repos/anthropics/skills/commits/limited":
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	return &GitHub{Client: srv.Client(), API: srv.URL, Codeload: srv.URL}, &asked
}

// Scenario: the owner adds a skill by owner/repo/path@ref. The ref is
// resolved to its commit first and the tarball of that commit is read, so the
// commit recorded is the one whose files are kept.
func TestASkillIsFetchedAtTheCommitItsRefNames(t *testing.T) {
	g, asked := github(t)

	src, err := Parse("anthropics/skills/skills/pdf@main")
	require.NoError(t, err)
	assert.Equal(t, "github:anthropics/skills/skills/pdf@main", src.String())

	sk, got, err := g.Fetch(t.Context(), src)
	require.NoError(t, err)
	assert.Equal(t, commit, got)
	assert.Equal(t, "pdf", sk.Name)
	assert.Equal(t, []string{"/repos/anthropics/skills/commits/main", "/anthropics/skills/tar.gz/" + commit}, *asked)

	head, err := Parse("github:anthropics/skills/skills/pdf")
	require.NoError(t, err)
	assert.Equal(t, "github:anthropics/skills/skills/pdf@HEAD", head.String(), "no ref is the default branch")

	_, _, err = g.Fetch(t.Context(), head)
	require.NoError(t, err)
}

// Scenario: a source that names no skill is refused as the owner's mistake;
// GitHub refusing for its rate limit is GitHub being unavailable.
func TestASourceThatIsNotASkillIsRefused(t *testing.T) {
	g, _ := github(t)

	for _, bad := range []string{"anthropics", "../x/y", "a/b@ref with space", "a/b/../c"} {
		_, err := Parse(bad)
		require.ErrorIs(t, err, ErrInvalid, bad)
	}

	for spec, want := range map[string]error{
		"anthropics/missing":                 ErrInvalid,
		"anthropics/skills/skills/none@main": skillpack.ErrInvalid,
		"anthropics/skills@limited":          ErrUnavailable,
	} {
		src, err := Parse(spec)
		require.NoError(t, err)

		_, _, err = g.Fetch(t.Context(), src)
		require.ErrorIs(t, err, want, spec)
	}
}
