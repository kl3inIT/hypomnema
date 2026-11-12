package skillpack

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const manifest = "---\nname: review-go\ndescription: Review a Go change for correctness.\n---\n\n# Review\n"

type entry struct {
	name string
	mode int64
	body string
	link bool
}

func zipOf(t *testing.T, entries ...entry) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(0o644)

		if e.mode != 0 {
			h.SetMode(0o755)
		}

		if e.link {
			h.SetMode(0o777 | 1<<27) // os.ModeSymlink
		}

		w, err := zw.CreateHeader(h)
		require.NoError(t, err)

		_, err = w.Write([]byte(e.body))
		require.NoError(t, err)
	}

	require.NoError(t, zw.Close())

	return buf.Bytes()
}

func tarGzOf(t *testing.T, entries ...entry) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.mode != 0 {
			h.Mode = e.mode
		}

		if e.link {
			h.Typeflag, h.Size, h.Linkname = tar.TypeSymlink, 0, "/etc/passwd"
		}

		require.NoError(t, tw.WriteHeader(h))

		if !e.link {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	return buf.Bytes()
}

// Scenario: the same folder, zipped or tarred, in another order or inside
// its own folder, is the same skill with the same digest; the digest is what
// tells a revision from the one before (Omnara, OpenClaw).
func TestTheSameFolderHasTheSameDigestHoweverItIsPacked(t *testing.T) {
	flat, err := FromArchive(zipOf(t,
		entry{name: "SKILL.md", body: manifest},
		entry{name: "scripts/check.sh", mode: 0o755, body: "go vet ./...\n"},
	))
	require.NoError(t, err)

	nested, err := FromArchive(tarGzOf(t,
		entry{name: "review-go/scripts/check.sh", mode: 0o755, body: "go vet ./...\n"},
		entry{name: "review-go/SKILL.md", body: manifest},
	))
	require.NoError(t, err)

	assert.Equal(t, "review-go", flat.Name)
	assert.Equal(t, "Review a Go change for correctness.", flat.Description)
	assert.Equal(t, flat.Digest, nested.Digest)
	assert.Equal(t, []string{"SKILL.md", "scripts/check.sh"}, paths(nested))
	assert.True(t, nested.Files[1].Exec)

	// The kept archive reads back as the same skill.
	kept, err := flat.Archive()
	require.NoError(t, err)

	again, err := FromArchive(kept)
	require.NoError(t, err)
	assert.Equal(t, flat.Digest, again.Digest)
	assert.Equal(t, flat.Files, again.Files)
}

// Scenario: a change to a script's content or its executable flag is a new
// digest, so the owner's edit is a new revision.
func TestAnyChangeToAFileChangesTheDigest(t *testing.T) {
	base, err := FromArchive(zipOf(t, entry{name: "SKILL.md", body: manifest}, entry{name: "run.sh", body: "a"}))
	require.NoError(t, err)

	content, err := FromArchive(zipOf(t, entry{name: "SKILL.md", body: manifest}, entry{name: "run.sh", body: "b"}))
	require.NoError(t, err)

	exec, err := FromArchive(zipOf(t,
		entry{name: "SKILL.md", body: manifest}, entry{name: "run.sh", mode: 0o755, body: "a"}))
	require.NoError(t, err)

	assert.NotEqual(t, base.Digest, content.Digest)
	assert.NotEqual(t, base.Digest, exec.Digest)
}

// Scenario: an archive that could write outside the skill's folder, or is
// not a skill, is refused with what is wrong.
func TestAnArchiveThatIsNotASafeSkillIsRefused(t *testing.T) {
	cases := map[string]struct {
		archive []byte
		want    string
	}{
		"a link":           {tarGzOf(t, entry{name: "SKILL.md", body: manifest}, entry{name: "x", link: true}), "links are refused"},
		"a link in a zip":  {zipOf(t, entry{name: "SKILL.md", body: manifest}, entry{name: "x", link: true}), "links are refused"},
		"climbing out":     {tarGzOf(t, entry{name: "SKILL.md", body: manifest}, entry{name: "../x", body: "x"}), "plain relative path"},
		"absolute":         {tarGzOf(t, entry{name: "SKILL.md", body: manifest}, entry{name: "/etc/x", body: "x"}), "plain relative path"},
		"no manifest":      {zipOf(t, entry{name: "README.md", body: "x"}), "no SKILL.md"},
		"two top folders":  {zipOf(t, entry{name: "a/SKILL.md", body: manifest}, entry{name: "b/x", body: "x"}), "no SKILL.md"},
		"no frontmatter":   {zipOf(t, entry{name: "SKILL.md", body: "# Review\n"}), "frontmatter"},
		"no description":   {zipOf(t, entry{name: "SKILL.md", body: "---\nname: x\n---\n"}), "no description"},
		"a bad name":       {zipOf(t, entry{name: "SKILL.md", body: "---\nname: Review_Go\ndescription: d\n---\n"}), "single hyphens"},
		"not an archive":   {[]byte("hello"), "neither a zip"},
		"over the archive": {make([]byte, MaxArchive+1), "over 25 MB"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := FromArchive(c.archive)
			require.ErrorIs(t, err, ErrInvalid)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

// Scenario: a zip made by macOS's Finder carries __MACOSX beside the folder;
// it is not a second folder.
func TestFinderJunkIsLeftOut(t *testing.T) {
	s, err := FromArchive(zipOf(t,
		entry{name: "review-go/SKILL.md", body: manifest},
		entry{name: "__MACOSX/review-go/._SKILL.md", body: "x"},
		entry{name: "review-go/.DS_Store", body: "x"},
	))
	require.NoError(t, err)
	assert.Equal(t, []string{"SKILL.md"}, paths(s))
}

// Scenario: a skill is one folder of a repository's tarball, as GitHub serves
// it under a folder named after the commit; the rest of the repository is
// not read into the skill.
func TestASkillIsOneFolderOfARepositoryTarball(t *testing.T) {
	tarball := tarGzOf(t,
		entry{name: "owner-repo-abc123/README.md", body: "repo"},
		entry{name: "owner-repo-abc123/skills/review-go/SKILL.md", body: manifest},
		entry{name: "owner-repo-abc123/skills/review-go/references/style.md", body: "style"},
		entry{name: "owner-repo-abc123/skills/other/SKILL.md", body: "x"},
		entry{name: "owner-repo-abc123/link", link: true},
	)

	s, err := FromTarball(bytes.NewReader(tarball), "skills/review-go")
	require.NoError(t, err)
	assert.Equal(t, "review-go", s.Name)
	assert.Equal(t, []string{"SKILL.md", "references/style.md"}, paths(s))

	_, err = FromTarball(bytes.NewReader(tarball), "skills/missing")
	require.ErrorIs(t, err, ErrInvalid)
	assert.Contains(t, err.Error(), `no folder "skills/missing"`)

	_, err = FromTarball(bytes.NewReader(tarball), "../x")
	require.ErrorIs(t, err, ErrInvalid)
}

func paths(s Skill) []string {
	out := make([]string, len(s.Files))
	for i, f := range s.Files {
		out[i] = f.Path
	}

	return out
}
