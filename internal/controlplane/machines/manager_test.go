package machines_test

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/machines"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

const publicURL = "https://hypomnema.example"

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// cloud is a provider's account in memory: the machines it has, by node.
type cloud struct {
	machines  map[string]machines.Resource
	specs     []machines.Spec
	validate  error
	createErr error
	deleteErr error
	deletes   int
}

func (c *cloud) Validate(context.Context, string, string) error { return c.validate }

func (c *cloud) Create(_ context.Context, s machines.Spec) (string, error) {
	c.specs = append(c.specs, s)
	if c.createErr != nil {
		return "", c.createErr
	}

	r := machines.Resource{ID: "i-" + s.NodeID, NodeID: s.NodeID, Region: s.Region, Size: s.Size, State: "running"}
	c.machines[s.NodeID] = r

	return r.ID, nil
}

func (c *cloud) Find(_ context.Context, _, nodeID string) (machines.Resource, bool, error) {
	r, ok := c.machines[nodeID]

	return r, ok, nil
}

func (c *cloud) Delete(_ context.Context, _, nodeID string) error {
	c.deletes++
	if c.deleteErr != nil {
		return c.deleteErr
	}

	delete(c.machines, nodeID)

	return nil
}

func (c *cloud) List(context.Context, string) ([]machines.Resource, error) {
	out := make([]machines.Resource, 0, len(c.machines))
	for _, r := range c.machines {
		out = append(out, r)
	}

	return out, nil
}

type world struct {
	t        *testing.T
	svc      *orchestration.Service
	clock    *clock
	cloud    *cloud
	mgr      *machines.Manager
	provider orchestration.MachineProvider
}

func newWorld(t *testing.T, url string) *world {
	t.Helper()

	db, err := sqlite.Open(t.Context(), sqlite.Config{Path: filepath.Join(t.TempDir(), "hypomnema.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, orchestration.Migrate(t.Context(), db))

	w := &world{
		t:     t,
		clock: &clock{now: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)},
		cloud: &cloud{machines: map[string]machines.Resource{}},
	}
	w.svc = orchestration.New(db, w.clock, platform.NewID)

	w.provider, err = w.svc.AddMachineProvider(t.Context(), orchestration.NewMachineProvider{
		Name: "aws", Kind: orchestration.MachineProviderAWS, KeyRef: "vault:aws", Region: "ap-southeast-1",
		Size: "t3.medium", MaxMachines: 2,
	})
	require.NoError(t, err)

	connect := func(context.Context, orchestration.MachineProvider) (machines.Provider, error) { return w.cloud, nil }
	w.mgr = machines.New(w.svc, connect, url, w.clock, slog.New(slog.DiscardHandler))

	return w
}

func (w *world) create(name string) orchestration.Node {
	w.t.Helper()

	n, err := w.mgr.Create(w.t.Context(), machines.NewMachine{Name: name, Provider: "aws"})
	require.NoError(w.t, err)

	return n
}

func (w *world) node(id string) orchestration.Node {
	w.t.Helper()

	n, err := w.svc.GetNode(w.t.Context(), id)
	require.NoError(w.t, err)

	return n
}

func (w *world) listed() []string {
	w.t.Helper()

	nodes, err := w.svc.ListNodes(w.t.Context())
	require.NoError(w.t, err)

	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Name)
	}

	return names
}

var tokenInScript = regexp.MustCompile(`HYPOMNEMA_NODE_TOKEN='(hyn_[A-Za-z0-9_-]+)'`)

// Scenario: the owner asks for a machine. The provider is asked for one in
// the provider's default region and size, with a first-boot script that
// installs the node from this control plane and carries a token that is the
// new node's. When the node says hello, the machine is ready.
func TestCreatedMachineJoinsWithItsOwnToken(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	n := w.create("build-1")

	require.NotNil(t, n.Machine)
	assert.Equal(t, orchestration.MachineProvisioning, n.Machine.State)
	assert.Equal(t, "i-"+n.ID, n.Machine.ResourceID)

	require.Len(t, w.cloud.specs, 1)
	spec := w.cloud.specs[0]
	assert.Equal(t, "ap-southeast-1", spec.Region)
	assert.Equal(t, "t3.medium", spec.Size)
	assert.NotEmpty(t, spec.RequestID)
	assert.Contains(t, spec.UserData, "'"+publicURL+"/install/node.sh'")

	found := tokenInScript.FindStringSubmatch(spec.UserData)
	require.Len(t, found, 2, "the script sets the node's token")

	owner, err := w.svc.AuthenticateNode(t.Context(), found[1])
	require.NoError(t, err)
	assert.Equal(t, n.ID, owner.ID)

	require.NoError(t, w.svc.NodeHello(t.Context(), n.ID, orchestration.Hello{WorktreeRoot: "/home/hypomnema/wt"}))
	assert.Equal(t, orchestration.MachineReady, w.node(n.ID).Machine.State)
}

// Scenario: the first-boot script is run by sh on the new machine, where
// nobody sees a syntax error.
func TestFirstBootScriptIsValidShell(t *testing.T) {
	t.Parallel()

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}

	script, err := machines.FirstBoot(publicURL, "hyn_it's-quoted")
	require.NoError(t, err)

	cmd := exec.CommandContext(t.Context(), sh, "-n") //nolint:gosec // G204: the sh found on PATH, a fixed flag
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

// Scenario: a create that cannot work is refused before a node or a machine
// exists: no public https URL for the machine to call, a size the provider
// does not offer, or a provider at its limit.
func TestCreateIsRefusedBeforeAnythingExists(t *testing.T) {
	t.Parallel()

	for _, url := range []string{"", "http://hypomnema.example", "https://user@hypomnema.example"} {
		w := newWorld(t, url)

		_, err := w.mgr.Create(t.Context(), machines.NewMachine{Name: "build-1", Provider: "aws"})
		require.ErrorIs(t, err, orchestration.ErrInvalid, url)
		require.ErrorIs(t, err, machines.ErrNoPublicURL, url)
		assert.Empty(t, w.listed())
		assert.Empty(t, w.cloud.specs)
	}

	w := newWorld(t, publicURL)
	w.cloud.validate = errors.New("instance type t9.huge is not offered in this region")

	_, err := w.mgr.Create(t.Context(), machines.NewMachine{Name: "build-1", Provider: "aws", Size: "t9.huge"})
	require.ErrorIs(t, err, orchestration.ErrInvalid)
	assert.Empty(t, w.listed())

	w.cloud.validate = nil
	w.create("build-1")
	w.create("build-2")

	_, err = w.mgr.Create(t.Context(), machines.NewMachine{Name: "build-3", Provider: "aws"})
	require.ErrorIs(t, err, orchestration.ErrUnavailable)
	assert.Equal(t, []string{"build-1", "build-2"}, w.listed())
	assert.Len(t, w.cloud.specs, 2)
}

// Scenario: the provider refuses the create. The node stays listed as
// failed with the provider's reason, and removing it asks the provider to
// destroy whatever it may have made before the node is forgotten.
func TestRefusedCreateLeavesAFailedNodeTheOwnerRemoves(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	w.cloud.createErr = errors.New("VcpuLimitExceeded")

	n, err := w.mgr.Create(t.Context(), machines.NewMachine{Name: "build-1", Provider: "aws"})
	require.ErrorContains(t, err, "VcpuLimitExceeded")
	require.NotNil(t, n.Machine)
	assert.Equal(t, orchestration.MachineProvisionFailed, n.Machine.State)
	assert.Contains(t, n.Machine.Error, "VcpuLimitExceeded")

	require.NoError(t, w.mgr.Remove(t.Context(), n.ID))
	assert.Equal(t, 1, w.cloud.deletes)
	assert.Empty(t, w.listed())
}

// Scenario: the owner removes a provider's node while the provider is down.
// The node stops working at once but stays listed as not destroyed; nothing
// forgets it, the provider is not asked again before the wait is over, and
// once the provider answers the machine is destroyed and the node is gone.
func TestRemoveKeepsTheNodeUntilTheMachineIsDestroyed(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	n := w.create("build-1")
	token := tokenInScript.FindStringSubmatch(w.cloud.specs[0].UserData)[1]

	require.ErrorIs(t, w.svc.RemoveNode(t.Context(), n.ID), orchestration.ErrConflict,
		"a plain removal would leave the machine running")
	require.ErrorIs(t, w.svc.RemoveMachineProvider(t.Context(), w.provider.ID), orchestration.ErrConflict)

	w.cloud.deleteErr = errors.New("RequestLimitExceeded")
	require.ErrorContains(t, w.mgr.Remove(t.Context(), n.ID), "RequestLimitExceeded")

	got := w.node(n.ID)
	assert.Equal(t, orchestration.MachineDeleteFailed, got.Machine.State)
	assert.Equal(t, orchestration.NodeDrained, got.Status)
	assert.Contains(t, got.Machine.Error, "RequestLimitExceeded")

	_, err := w.svc.AuthenticateNode(t.Context(), token)
	require.ErrorIs(t, err, orchestration.ErrNotFound)

	w.cloud.deleteErr = nil
	w.clock.now = w.clock.now.Add(10 * time.Second)
	require.NoError(t, w.mgr.Settle(t.Context()))
	assert.Equal(t, 1, w.cloud.deletes, "not asked again before the wait is over")
	assert.Equal(t, []string{"build-1"}, w.listed())

	w.clock.now = w.clock.now.Add(time.Minute)
	require.NoError(t, w.mgr.Settle(t.Context()))
	assert.Empty(t, w.listed())
	assert.Empty(t, w.cloud.machines)

	require.NoError(t, w.svc.RemoveMachineProvider(t.Context(), w.provider.ID))
}

// Scenario: a node the owner installed is removed through the same door and
// no provider is involved.
func TestRemoveOfAnOwnNodeAsksNoProvider(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	own, _, err := w.svc.AddNode(t.Context(), "zm", 0)
	require.NoError(t, err)
	assert.Nil(t, own.Machine)

	require.NoError(t, w.mgr.Remove(t.Context(), own.ID))
	assert.Empty(t, w.listed())
	assert.Zero(t, w.cloud.deletes)
}

// Scenario: a machine never says hello. After the wait it is marked failed,
// still listed for the owner to remove; if it says hello after all, it is ready.
func TestMachineThatNeverSaysHelloIsMarkedFailed(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	n := w.create("build-1")

	w.clock.now = w.clock.now.Add(machines.HelloTimeout - time.Second)
	require.NoError(t, w.mgr.Settle(t.Context()))
	assert.Equal(t, orchestration.MachineProvisioning, w.node(n.ID).Machine.State)

	w.clock.now = w.clock.now.Add(time.Second)
	require.NoError(t, w.mgr.Settle(t.Context()))

	got := w.node(n.ID)
	assert.Equal(t, orchestration.MachineProvisionFailed, got.Machine.State)
	assert.Contains(t, got.Machine.Error, "did not say hello")

	require.NoError(t, w.svc.NodeHello(t.Context(), n.ID, orchestration.Hello{}))

	got = w.node(n.ID)
	assert.Equal(t, orchestration.MachineReady, got.Machine.State)
	assert.Empty(t, got.Machine.Error)
}

// Scenario: the control plane stopped between recording a node and hearing
// the provider's answer. Later the provider is asked: a machine it made for
// the node is taken up, and a node it has nothing for is marked failed.
func TestCreateCutShortIsSettledFromTheProvider(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	begin := func(name string) orchestration.Node {
		n, _, _, err := w.svc.BeginMachine(t.Context(), orchestration.NewMachine{
			Name: name, ProviderID: w.provider.ID, Region: "ap-southeast-1", Size: "t3.medium",
		})
		require.NoError(t, err)

		return n
	}

	made, lost := begin("made"), begin("lost")
	w.cloud.machines[made.ID] = machines.Resource{ID: "i-made", NodeID: made.ID}

	require.NoError(t, w.mgr.Settle(t.Context()))
	assert.Empty(t, w.node(made.ID).Machine.ResourceID, "too young: its create may still be running")

	w.clock.now = w.clock.now.Add(3 * time.Minute)
	require.NoError(t, w.mgr.Settle(t.Context()))

	assert.Equal(t, "i-made", w.node(made.ID).Machine.ResourceID)
	assert.Equal(t, orchestration.MachineProvisioning, w.node(made.ID).Machine.State)
	assert.Equal(t, orchestration.MachineProvisionFailed, w.node(lost.ID).Machine.State)
}

// Scenario: the provider has a machine that no node stands for, which costs
// money and nothing destroys. A check names it and not the machines of live nodes.
func TestCheckNamesMachinesNoNodeStandsFor(t *testing.T) {
	t.Parallel()

	w := newWorld(t, publicURL)
	w.create("build-1")
	w.cloud.machines["nod_gone"] = machines.Resource{ID: "i-orphan", NodeID: "nod_gone"}

	orphans, err := w.mgr.Check(t.Context(), "aws")
	require.NoError(t, err)
	require.Len(t, orphans, 1)
	assert.Equal(t, "i-orphan", orphans[0].ID)
}
