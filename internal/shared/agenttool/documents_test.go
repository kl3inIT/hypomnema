package agenttool

import (
	"io/fs"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uv is pinned for each Linux the coordinator runs on, so the installer
// accepts it: over HTTPS, with a SHA-256, the command at a path inside its
// release file. It is for no Integration, so no agent is given a script for
// it.
func TestUVIsPinnedForTheCoordinatorsSystems(t *testing.T) {
	t.Parallel()

	uv := UV()

	for _, system := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}} {
		tool, ok := uv.For(system[0], system[1])
		require.True(t, ok, system)
		require.NoError(t, ToolInstaller{}.valid(tool), system)
		assert.Contains(t, tool.URL, "/astral-sh/uv/releases/download/"+uv.Version+"/", system)
	}

	_, ok := uv.For("windows", "amd64")
	assert.False(t, ok)

	_, ok = Named("uv")
	assert.False(t, ok, "uv is no Integration's tool")
}

// The lock pins the packages the issue names at their versions, the Hermes
// pdf skill's own list, and every file it installs by its hash.
func TestTheDocumentsLockPinsTheSkillsPackages(t *testing.T) {
	t.Parallel()

	lock, err := fs.ReadFile(documentsProject, "documents/uv.lock")
	require.NoError(t, err)

	for name, version := range map[string]string{
		"python-docx": "1.2.0", "openpyxl": "3.1.5", "python-pptx": "1.0.2", "reportlab": "5.1.0",
		"pypdf": "6.21.4", "pdfplumber": "0.11.10", "pypdfium2": "5.14.0",
	} {
		assert.Regexp(t, `(?m)^name = "`+regexp.QuoteMeta(name)+`"\nversion = "`+regexp.QuoteMeta(version)+`"$`,
			string(lock), name)
	}

	files := regexp.MustCompile(`url = "https://files\.pythonhosted\.org/[^"]+"`).FindAllString(string(lock), -1)
	hashed := regexp.MustCompile(`url = "https://files\.pythonhosted\.org/[^"]+", hash = "sha256:[0-9a-f]{64}"`).
		FindAllString(string(lock), -1)

	require.NotEmpty(t, files)
	assert.Len(t, hashed, len(files), "every file is pinned by its hash")
}
