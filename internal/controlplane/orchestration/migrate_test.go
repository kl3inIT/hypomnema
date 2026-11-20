package orchestration

import (
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// migrationVersion is the version of the one migration whose file name ends with suffix ("_inbox.sql"). A test
// that migrates to just before or after a migration finds it by name, so the migration keeps its test whether it is
// still named by its time on a branch or numbered by `make migration-fix` (issue #954).
func migrationVersion(t *testing.T, p *goose.Provider, suffix string) int64 {
	t.Helper()

	var found []int64

	for _, src := range p.ListSources() {
		if strings.HasSuffix(src.Path, suffix) {
			found = append(found, src.Version)
		}
	}

	require.Len(t, found, 1, "one migration ends with %s", suffix)

	return found[0]
}
