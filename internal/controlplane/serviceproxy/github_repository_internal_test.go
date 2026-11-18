package serviceproxy

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Scenario (issue #802): a repository's name is kept for its while only, and no more of them are kept than the
// bound: at the bound the expired go first, and when none has, all.
func TestRepositoryNamesAreKeptForAWhileAndBounded(t *testing.T) {
	t.Parallel()

	var names repositoryNames

	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	names.put("github.com:default 1", "o/r", now)

	got, ok := names.get("github.com:default 1", now.Add(githubRepositoryTTL-time.Second))
	assert.True(t, ok)
	assert.Equal(t, "o/r", got)

	_, ok = names.get("github.com:default 1", now.Add(githubRepositoryTTL))
	assert.False(t, ok, "an expired name is looked up again")

	_, ok = names.get("github.com:other 1", now)
	assert.False(t, ok, "a name is kept per Connection")

	for i := range githubRepositoriesKept + 10 {
		names.put("github.com:default "+strconv.Itoa(i+2), "o/r", now.Add(githubRepositoryTTL))
	}

	assert.LessOrEqual(t, len(names.names), githubRepositoriesKept)
}

// Scenario (issue #802): only /repositories/{id} and what follows it is read as a repository's id.
func TestARepositorysIDIsReadFromItsPathOnly(t *testing.T) {
	t.Parallel()

	for path, want := range map[string][2]string{
		"/repositories/1447911125":                    {"1447911125", ""},
		"/repositories/1447911125/pulls/786/files":    {"1447911125", "/pulls/786/files"},
		"/repositories/1447911125/contents/a%20b.txt": {"1447911125", "/contents/a%20b.txt"},
	} {
		id, rest, ok := githubRepositoryID(path)
		assert.True(t, ok, path)
		assert.Equal(t, want, [2]string{id, rest}, path)
	}

	for _, path := range []string{"/repositories", "/repositories/", "/repositories/0/pulls", "/repositories/x/pulls",
		"/repositories/12x/pulls", "/repos/o/r", "/repositories/12345678901234567890/pulls"} {
		_, _, ok := githubRepositoryID(path)
		assert.False(t, ok, path)
	}
}
