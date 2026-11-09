package orchestration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
)

func TestTokenLifecycle(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()

	tok, secret, err := s.AddToken(ctx, "laptop", bearertoken.Coordinator)
	require.NoError(t, err)
	assert.Equal(t, bearertoken.Coordinator, tok.Kind)

	got, err := s.AuthenticateToken(ctx, secret)
	require.NoError(t, err)
	assert.Equal(t, tok.ID, got.ID)

	_, _, err = s.AddToken(ctx, "laptop", bearertoken.Owner)
	require.ErrorIs(t, err, ErrConflict, "names are unique")

	_, _, err = s.AddToken(ctx, "web", "admin")
	require.ErrorIs(t, err, ErrInvalid)

	require.NoError(t, s.RevokeToken(ctx, tok.ID))
	require.ErrorIs(t, s.RevokeToken(ctx, tok.ID), ErrConflict)
	require.ErrorIs(t, s.RevokeToken(ctx, "tok_missing"), ErrNotFound)

	_, err = s.AuthenticateToken(ctx, secret)
	require.ErrorIs(t, err, ErrNotFound, "a revoked token no longer works")

	tokens, err := s.ListTokens(ctx)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.NotNil(t, tokens[0].RevokedAt)
}

func TestAuthenticateTokenRefusesMalformedAndUnknown(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)

	unknown, err := bearertoken.Generate(bearertoken.Owner)
	require.NoError(t, err)

	for _, secret := range []string{"", "hyn_node", unknown, unknown[:len(unknown)-1] + "x"} {
		_, err := s.AuthenticateToken(t.Context(), secret)
		require.ErrorIs(t, err, ErrNotFound, secret)
	}
}

func TestListRunsPagesNewestFirst(t *testing.T) {
	t.Parallel()

	s, _, clock := newTestService(t)
	ctx := t.Context()

	var ids []string

	for range 5 {
		ids = append(ids, newRun(t, s).ID)
		clock.now = clock.now.Add(time.Second)
	}

	require.NoError(t, s.CancelRun(ctx, ids[0]))

	page, err := s.ListRuns(ctx, RunFilter{}, nil, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{ids[4], ids[3]}, runIDs(page.Runs))
	require.NotNil(t, page.Next)

	page, err = s.ListRuns(ctx, RunFilter{}, page.Next, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{ids[2], ids[1]}, runIDs(page.Runs))

	page, err = s.ListRuns(ctx, RunFilter{}, page.Next, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{ids[0]}, runIDs(page.Runs))
	assert.Nil(t, page.Next)

	page, err = s.ListRuns(ctx, RunFilter{Status: RunCanceled}, nil, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{ids[0]}, runIDs(page.Runs))
}

func runIDs(runs []Run) []string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.ID)
	}

	return out
}

func TestCreateRunOnceReturnsTheFirstResult(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()
	o := Once{Caller: "tok_1", Key: "k1"}
	nr := NewRun{Objective: "fix the login", CreatedBy: "laptop", Repo: "kl3inIT/demo", CoordinatorTokenID: ""}

	first, err := s.CreateRunOnce(ctx, o, nr)
	require.NoError(t, err)

	again, err := s.CreateRunOnce(ctx, o, nr)
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID)

	nr.Objective = "something else"
	_, err = s.CreateRunOnce(ctx, o, nr)
	require.ErrorIs(t, err, ErrIdempotencyConflict)

	other, err := s.CreateRunOnce(ctx, Once{Caller: "tok_2", Key: "k1"}, nr)
	require.NoError(t, err, "a key belongs to its caller")
	assert.NotEqual(t, first.ID, other.ID)

	page, err := s.ListRuns(ctx, RunFilter{}, nil, 0)
	require.NoError(t, err)
	assert.Len(t, page.Runs, 2)
}

func TestCreateTasksOnceReturnsTheFirstTasks(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()
	run := newRun(t, s)
	o := Once{Caller: "tok_1", Key: "tasks-1"}
	tasks := []NewTask{
		{Ref: "a", Role: "implement", Spec: "a"},
		{Ref: "b", Role: "implement", Spec: "b", DependsOn: []string{"a"}},
	}

	first, err := s.CreateTasksOnce(ctx, o, run.ID, tasks)
	require.NoError(t, err)

	again, err := s.CreateTasksOnce(ctx, o, run.ID, tasks)
	require.NoError(t, err)
	require.Len(t, again, 2)
	assert.Equal(t, first[1].ID, again[1].ID)

	all, err := s.ListTasks(ctx, run.ID)
	require.NoError(t, err)
	assert.Len(t, all, 2, "the repeat created nothing")

	_, err = s.CreateTasksOnce(ctx, o, run.ID, tasks[:1])
	require.ErrorIs(t, err, ErrIdempotencyConflict)
}

func TestCreateRunRecordsItsCoordinator(t *testing.T) {
	t.Parallel()

	s, _, _ := newTestService(t)
	ctx := t.Context()

	tok, _, err := s.AddToken(ctx, "laptop", bearertoken.Coordinator)
	require.NoError(t, err)

	run, err := s.CreateRun(ctx, NewRun{Objective: "x", CreatedBy: tok.Name, CoordinatorTokenID: tok.ID})
	require.NoError(t, err)
	assert.Equal(t, tok.ID, run.CoordinatorTokenID)
}
