package skillssh

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hit struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Installs int64  `json:"installs"`
	Source   string `json:"source"`
}

func skill(slug string, installs int64) hit {
	return hit{ID: "o/r/" + slug, Name: slug, Installs: installs, Source: "o/r"}
}

// directory answers each query with its skills and keeps the queries asked.
func directory(t *testing.T, answers map[string][]hit, audits string) (*Client, *[]string) {
	t.Helper()

	var asked []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/search":
			q := r.URL.Query().Get("q")
			asked = append(asked, q+"|"+r.URL.Query().Get("owner"))

			if q == "broken" {
				w.WriteHeader(http.StatusBadGateway)

				return
			}

			_ = json.NewEncoder(w).Encode(map[string]any{"skills": answers[q]})
		case "/tele/audit":
			assert.Equal(t, "o/r", r.URL.Query().Get("source"))
			_, _ = w.Write([]byte(audits))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	return &Client{HTTP: srv.Client(), Search: srv.URL + "/api/search", Audit: srv.URL + "/tele/audit"}, &asked
}

func ids(rs []Result) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Name)
	}

	return out
}

// Scenario: the owner searches "next bundle analyzer" and skills.sh finds
// one skill matching every word. The query is asked again without each
// word; what those find follows, the skills more of them found first, then
// the better placed, then the more installed.
func TestASearchWithFewResultsAddsThoseMatchingSomeWords(t *testing.T) {
	c, asked := directory(t, map[string][]hit{
		"next bundle analyzer": {skill("exact", 5)},
		"bundle analyzer":      {skill("analyzer-a", 1), skill("both", 3), skill("exact", 5)},
		"next analyzer":        {skill("both", 3), skill("popular", 900)},
		"next bundle":          {skill("popular-late", 9000), skill("rare", 1)},
	}, "")

	found, exact, err := c.Find(t.Context(), " next bundle analyzer ", "")
	require.NoError(t, err)
	assert.Equal(t, 1, exact)
	assert.Equal(t, []string{"exact", "both", "popular-late", "analyzer-a", "popular", "rare"}, ids(found))
	assert.Equal(t,
		[]string{"next bundle analyzer|", "bundle analyzer|", "next analyzer|", "next bundle|"}, *asked)
}

// Scenario: a search of one word, or one that finds ten, is not asked again;
// an owner narrows it; skills.sh failing is said.
func TestASearchThatIsEnoughIsAskedOnce(t *testing.T) {
	ten := make([]hit, 10)
	for i := range ten {
		ten[i] = skill(string(rune('a'+i)), 1)
	}

	c, asked := directory(t, map[string][]hit{"pdf": {skill("pdf", 1)}, "pdf forms": ten}, "")

	_, exact, err := c.Find(t.Context(), "pdf", "anthropics")
	require.NoError(t, err)
	assert.Equal(t, 1, exact)

	found, exact, err := c.Find(t.Context(), "pdf forms", "")
	require.NoError(t, err)
	assert.Equal(t, 10, exact)
	assert.Len(t, found, 10)
	assert.Equal(t, []string{"pdf|anthropics", "pdf forms|"}, *asked)

	_, _, err = c.Find(t.Context(), "broken", "")
	require.ErrorIs(t, err, ErrUnavailable)
}

// Scenario: skills.sh's auditors said something of the skill: each is given,
// by auditor; another skill's are not.
func TestTheAuditsOfASkillAreRead(t *testing.T) {
	c, _ := directory(t, nil, `{
		"pdf": {
			"snyk": {"risk": "low", "analyzedAt": "2026-11-01"},
			"ath": {"risk": "safe", "analyzedAt": "2026-11-02"},
			"socket": {"risk": "medium", "alerts": 2, "analyzedAt": "2026-11-03"}
		},
		"other": {"snyk": {"risk": "critical"}}
	}`)

	audits, err := c.Audits(t.Context(), "o/r", "pdf")
	require.NoError(t, err)

	two := 2
	assert.Equal(t, []Audit{
		{Partner: "ath", Risk: "safe", AnalyzedAt: "2026-11-02"},
		{Partner: "snyk", Risk: "low", AnalyzedAt: "2026-11-01"},
		{Partner: "socket", Risk: "medium", Alerts: &two, AnalyzedAt: "2026-11-03"},
	}, audits)

	none, err := c.Audits(t.Context(), "o/r", "missing")
	require.NoError(t, err)
	assert.Empty(t, none)
}
