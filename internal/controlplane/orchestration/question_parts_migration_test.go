package orchestration

import (
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// questionPartsVersions are the versions of the migration before issue #1219's and of its own, found by its name: it
// is named by its time until it is numbered right before its merge.
func questionPartsVersions(t *testing.T) (before, own int64) {
	t.Helper()

	entries, err := fs.ReadDir(migrations, "migrations")
	require.NoError(t, err)

	for _, e := range entries {
		version, err := strconv.ParseInt(strings.SplitN(e.Name(), "_", 2)[0], 10, 64)
		require.NoError(t, err)

		if strings.HasSuffix(e.Name(), "_question_header_and_more.sql") {
			return before, version
		}

		before = version
	}

	require.FailNow(t, "no question_header_and_more migration")

	return 0, 0
}

// Scenario: issue #1219 on a database that holds a Gate asked before it. The way back drops the header and the more
// decisions and keeps the Gate; the way up gives it an empty header and no more, so it reads, and is answered, as
// the one decision it was. A header is at most 30 characters.
func TestMigrationKeepsAGateAskedBeforeAsOneDecision(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newWorld(t)
	task := w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})[0]
	_, c := w.start(task.ID)

	gate := mergeGate()
	require.NoError(t, w.finish(c, Outcome{Gate: &gate, Result: Result{Summary: "PR #7 is green"}}))

	open, err := w.s.ListQuestions(ctx, QuestionFilter{Status: QuestionOpen})
	require.NoError(t, err)
	require.Len(t, open, 1)

	before, own := questionPartsVersions(t)
	p, err := provider(w.db)
	require.NoError(t, err)

	_, err = p.DownTo(ctx, before)
	require.NoError(t, err)

	var n int
	require.NoError(t, w.db.QueryRowContext(ctx, `SELECT count(*) FROM questions`).Scan(&n))
	assert.Equal(t, 1, n, "the Gate is kept")

	_, err = p.UpTo(ctx, own)
	require.NoError(t, err)
	_, err = p.Up(ctx)
	require.NoError(t, err)

	got := w.question(open[0].ID)
	assert.Empty(t, got.Header)
	assert.Nil(t, got.More)
	assert.Equal(t, gate.Text, got.Text)
	assert.Equal(t, gate.Options, got.Options)
	assert.Equal(t, "PR #7 is green", got.Summary)

	_, err = w.db.ExecContext(ctx, `UPDATE questions SET header = ? WHERE id = ?`, strings.Repeat("h", 31), got.ID)
	require.Error(t, err, "a header is at most 30 characters")

	_, err = w.s.AnswerQuestion(ctx, got.ID, AddressOwner, Answer{Option: choice(1), Text: "add a test"})
	require.NoError(t, err)
	assert.Equal(t, TaskReady, w.task(task.ID).Status)
}
