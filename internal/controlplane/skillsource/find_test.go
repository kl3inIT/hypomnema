package skillsource

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func manifest(name string) string {
	return "---\nname: " + name + "\ndescription: A skill.\n---\n"
}

// repo serves one repository, o/r, at one commit for HEAD, holding files.
func repo(t *testing.T, files map[string]string) *GitHub {
	t.Helper()

	in := map[string]string{}
	for p, body := range files {
		in["o-r-0123456/"+p] = body
	}

	body := tarball(t, in)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/commits/HEAD":
			_, _ = w.Write([]byte(commit))
		case "/o/r/tar.gz/" + commit:
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	return &GitHub{Client: srv.Client(), API: srv.URL, Codeload: srv.URL}
}

func found(t *testing.T, g *GitHub, name string) string {
	t.Helper()

	src, err := g.Find(t.Context(), Source{Owner: "o", Repo: "r"}, name)
	require.NoError(t, err)

	return src.Path
}

// Scenario: skills.sh gives a repository and a skill's name. The skill's
// folder is found where the skills CLI looks, whatever the folder is called,
// and is what the source keeps.
func TestASkillNamedIsFoundWhereTheSkillsCLILooks(t *testing.T) {
	g := repo(t, map[string]string{
		"README.md":                                 manifest("not-a-skill-file"),
		"skills/pdf-tools/SKILL.md":                 manifest("pdf"),
		"skills/pdf-tools/forms.md":                 "forms",
		".claude/skills/review/SKILL.md":            manifest("review-go"),
		"skills/web/frontend/react/SKILL.md":        manifest("react-best-practices"),
		"skills/web/frontend/react/deep/SKILL.md":   manifest("too-deep-under-a-skill"),
		"node_modules/x/skills/a/b/c/SKILL.md":      manifest("hidden"),
		"examples/demo/SKILL.md":                    manifest("demo"),
		"tools/catalog/lark/SKILL.md":               manifest("lark-docs"),
		"skills.sh.json":                            `{"paths": ["./tools/catalog", "../escape", "./"]}`,
		"skills/Shouty_Name/SKILL.md":               "---\nname: shouty-name\ndescription: x\n---\n",
		"docs/notes/also-pdf/SKILL.md":              manifest("pdf"),
		"skills/.curated/curated-one/SKILL.md":      manifest("curated-one"),
		".agents/skills/first-of-two/SKILL.md":      manifest("twice"),
		".windsurf/skills/second-of-two/SKILL.md":   manifest("twice"),
		"skills/web/frontend/react/deep/x/SKILL.md": manifest("x"),
	})

	assert.Equal(t, "skills/pdf-tools", found(t, g, "pdf"), "the folder under skills/ wins over one found anywhere")
	assert.Equal(t, ".claude/skills/review", found(t, g, "review-go"))
	assert.Equal(t, "skills/web/frontend/react", found(t, g, "react-best-practices"), "three levels under skills/")
	assert.Equal(t, "tools/catalog/lark", found(t, g, "lark-docs"), "a folder skills.sh.json names")
	assert.Equal(t, "skills/.curated/curated-one", found(t, g, "curated-one"))
	assert.Equal(t, "skills/Shouty_Name", found(t, g, "Shouty Name"), "spaces and case are forgiven")
	assert.Equal(t, ".agents/skills/first-of-two", found(t, g, "twice"), "the first in the CLI's order")

	for _, missing := range []string{"too-deep-under-a-skill", "hidden", "demo", "nothing"} {
		_, err := g.Find(t.Context(), Source{Owner: "o", Repo: "r"}, missing)
		require.ErrorIs(t, err, ErrInvalid, missing)
	}
}

// Scenario: a repository keeps its skills nowhere the CLI knows. Every
// SKILL.md down to five levels is looked at; a repository whose root is the
// skill gives the root.
func TestASkillOutsideTheKnownFoldersIsStillFound(t *testing.T) {
	g := repo(t, map[string]string{"a/b/c/d/my-skill/SKILL.md": manifest("my-skill")})
	assert.Equal(t, "a/b/c/d/my-skill", found(t, g, "my-skill"))

	root := repo(t, map[string]string{"SKILL.md": manifest("whole"), "run.sh": "echo"})
	assert.Empty(t, found(t, root, "whole"))
}
