package orchestration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario: the company's GitLab server is inside the network. One node
// reaches it, another reaches only github.com, a third is from before nodes
// tried hosts. A Task on the company's server goes to a node that reaches it,
// never to the one that said it does not; a Run without a repository runs
// anywhere.
func TestATaskGoesOnlyToANodeThatReachesItsGitHost(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	const company = "gitlab.company.example"

	_, err := w.s.AddGitHost(ctx, company, "gitlab")
	require.NoError(t, err)

	outside := w.node // node_a, which reaches github.com only
	require.NoError(t, w.s.NodeReaches(ctx, outside.ID, []string{"github.com"}))

	run, err := w.s.CreateRun(ctx, NewRun{
		Objective: "fix the login", CreatedBy: "owner", Repo: "https://" + company + "/team/app",
	})
	require.NoError(t, err)

	w.run = run
	w.tasks(NewTask{Ref: "a", Profile: "implementer", Spec: "a"})

	out := w.schedule()
	assert.Empty(t, out.Created, "the only node does not reach the host: the Task waits")
	assert.Empty(t, out.Escalations, "not yet")

	w.advance(UnplaceableAfter)
	w.alive(outside)

	out = w.schedule()
	require.Len(t, out.Escalations, 1)
	assert.Equal(t, ReasonNoNodeReaches+company, out.Escalations[0].Reason, "the owner is told which host no node reaches")

	inside := w.addNode("node_inside", 2, "claude")
	require.NoError(t, w.s.NodeReaches(ctx, inside.ID, []string{strings.ToUpper(company), "github.com", "github.com"}))

	out = w.schedule()
	require.Len(t, out.Created, 1)
	assert.Equal(t, map[string]string{"a": inside.ID}, w.placed(out.Created))

	// A node that never said which hosts it reaches is one from before nodes tried: it takes any host.
	old := w.addNode("node_old", 2, "claude")
	w.tasks(NewTask{Ref: "b", Profile: "implementer", Spec: "b"}, NewTask{Ref: "c", Profile: "implementer", Spec: "c"})

	out = w.schedule()
	placed := w.placed(out.Created)
	require.Len(t, placed, 2)
	assert.ElementsMatch(t, []string{inside.ID, old.ID}, []string{placed["b"], placed["c"]},
		"the least loaded of the nodes that reach it, never the one that does not")

	// The node inside loses its way to the server: what it says now replaces what it said.
	require.NoError(t, w.s.NodeReaches(ctx, inside.ID, []string{}))
	require.NoError(t, w.s.DrainNode(ctx, old.ID))
	w.tasks(NewTask{Ref: "d", Profile: "implementer", Spec: "d"})
	assert.Empty(t, w.schedule().Created)

	// A Run without a repository has no host and runs on any node.
	free, err := w.s.CreateRun(ctx, NewRun{Objective: "read the mail", CreatedBy: "owner"})
	require.NoError(t, err)

	w.run = free
	w.tasks(NewTask{Ref: "m", Profile: "implementer", Spec: "m"})

	out = w.schedule()
	require.Len(t, out.Created, 1)
	assert.Contains(t, []string{outside.ID, inside.ID}, w.placed(out.Created)["m"])
}

// Scenario: a node sends what cannot be a list of hosts; it is refused and
// what it said before stays.
func TestWhatANodeSaysItReachesIsChecked(t *testing.T) {
	t.Parallel()

	w := newSchedWorld(t)
	ctx := t.Context()

	require.ErrorIs(t, w.s.NodeReaches(ctx, w.node.ID, []string{""}), ErrInvalid)
	require.ErrorIs(t, w.s.NodeReaches(ctx, w.node.ID, []string{strings.Repeat("a", 254)}), ErrInvalid)
	require.ErrorIs(t, w.s.NodeReaches(ctx, w.node.ID, make([]string, MaxNodeGitHosts+1)), ErrInvalid)
	require.ErrorIs(t, w.s.NodeReaches(ctx, "nod_missing", []string{"github.com"}), ErrNotFound)
}
