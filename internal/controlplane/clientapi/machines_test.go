package clientapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/machines"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// fakeCloud is a provider's account: what it was asked to create and destroy.
type fakeCloud struct {
	created   []machines.Spec
	deleted   []string
	stopped   []string
	started   []string
	createErr error
}

func (c *fakeCloud) Validate(context.Context, string, string) error { return nil }

func (c *fakeCloud) Create(_ context.Context, s machines.Spec) (string, error) {
	if c.createErr != nil {
		return "", c.createErr
	}

	c.created = append(c.created, s)

	return "i-" + s.NodeID, nil
}

func (c *fakeCloud) Find(context.Context, string, string) (machines.Resource, bool, error) {
	return machines.Resource{}, false, nil
}

func (c *fakeCloud) Delete(_ context.Context, _, nodeID string) error {
	c.deleted = append(c.deleted, nodeID)

	return nil
}

func (c *fakeCloud) Stop(_ context.Context, _, nodeID string) error {
	c.stopped = append(c.stopped, nodeID)

	return nil
}

func (c *fakeCloud) Start(_ context.Context, _, nodeID string) error {
	c.started = append(c.started, nodeID)

	return nil
}

func (c *fakeCloud) List(context.Context, string) ([]machines.Resource, error) { return nil, nil }

// withCloud gives the world's server a machine provider named aws whose
// account is the returned fake.
func (w *world) withCloud() *fakeCloud {
	w.t.Helper()

	cloud := &fakeCloud{}
	connect := func(context.Context, orchestration.MachineProvider) (machines.Provider, error) { return cloud, nil }

	w.srv.WithMachines(machines.New(w.svc, connect, "https://hypomnema.example", platform.SystemClock{},
		slog.New(slog.DiscardHandler)))

	_, err := w.svc.AddMachineProvider(w.t.Context(), orchestration.NewMachineProvider{
		Name: "aws", Kind: orchestration.MachineProviderAWS, KeyRef: "vault:aws", Region: "ap-southeast-1",
		Size: "t3.medium", MaxMachines: 2,
	})
	require.NoError(w.t, err)

	return cloud
}

// Scenario: the owner creates a machine at a provider from the web. The
// providers are listed with their defaults and how many machines they have;
// a node created with a provider gets a machine in the provider's default
// region and size and no token to copy; removing it destroys the machine. A
// coordinator token does none of this.
func TestOwnerCreatesAMachineAtAProvider(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	cloud := w.withCloud()
	ctx := t.Context()
	owner, _ := w.token("web", bearertoken.Owner)
	laptop, _ := w.token("laptop", bearertoken.Coordinator)

	hidden, err := laptop.ListMachineProvidersWithResponse(ctx)
	require.NoError(t, err)
	assert.Equal(t, openapi.Forbidden, errorCode(t, hidden.Body, hidden.StatusCode(), http.StatusForbidden))

	aws := "aws"

	refused, err := laptop.CreateNodeWithResponse(ctx, openapi.NewNode{Name: "build-1", Provider: &aws})
	require.NoError(t, err)
	assert.Equal(t, openapi.Forbidden, errorCode(t, refused.Body, refused.StatusCode(), http.StatusForbidden))

	created, err := owner.CreateNodeWithResponse(ctx, openapi.NewNode{Name: "build-1", Provider: &aws})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, created.StatusCode(), string(created.Body))
	assert.Nil(t, created.JSON201.Token)

	machine := created.JSON201.Node.Machine
	require.NotNil(t, machine)
	assert.Equal(t, openapi.NodeMachineStateProvisioning, machine.State)
	assert.Equal(t, "ap-southeast-1", machine.Region)
	assert.Equal(t, "t3.medium", machine.Size)
	require.Len(t, cloud.created, 1)

	providers, err := owner.ListMachineProvidersWithResponse(ctx)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, providers.StatusCode(), string(providers.Body))
	require.Len(t, providers.JSON200.Data, 1)

	got := providers.JSON200.Data[0]
	assert.Equal(t, machine.ProviderId, got.Id)
	assert.Equal(t, "aws", got.Name)
	assert.Equal(t, 1, got.Machines)
	assert.Equal(t, 2, got.MaxMachines)

	removed, err := owner.RemoveNodeWithResponse(ctx, created.JSON201.Node.Id)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, removed.StatusCode(), string(removed.Body))
	assert.Equal(t, []string{created.JSON201.Node.Id}, cloud.deleted)
}

// Scenario: the provider refuses a machine. The answer is the node as it is
// now listed, failed with the provider's reason, so the page can show why. A
// request that names no such provider, or a region with no provider, is
// refused and leaves no node.
func TestRefusedMachineIsAnsweredAsAFailedNode(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	cloud := w.withCloud()
	ctx := t.Context()
	owner, _ := w.token("web", bearertoken.Owner)

	aws, missing, region := "aws", "gcp", "eu-west-1"

	unknown, err := owner.CreateNodeWithResponse(ctx, openapi.NewNode{Name: "build-1", Provider: &missing})
	require.NoError(t, err)
	assert.Equal(t, openapi.NotFound, errorCode(t, unknown.Body, unknown.StatusCode(), http.StatusNotFound))

	stray, err := owner.CreateNodeWithResponse(ctx, openapi.NewNode{Name: "build-1", Region: &region})
	require.NoError(t, err)
	assert.Equal(t, openapi.InvalidRequest, errorCode(t, stray.Body, stray.StatusCode(), http.StatusBadRequest))

	list, err := owner.ListNodesWithResponse(ctx)
	require.NoError(t, err)
	assert.Empty(t, list.JSON200.Data)

	cloud.createErr = errors.New("VcpuLimitExceeded")

	failed, err := owner.CreateNodeWithResponse(ctx, openapi.NewNode{Name: "build-1", Provider: &aws})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, failed.StatusCode(), string(failed.Body))
	require.NotNil(t, failed.JSON201.Node.Machine)
	assert.Equal(t, openapi.NodeMachineStateProvisionFailed, failed.JSON201.Node.Machine.State)
	assert.Contains(t, *failed.JSON201.Node.Machine.Error, "VcpuLimitExceeded")
}

// Scenario: the owner turns a machine off from the web and on again. The
// answers show the machine stopped, then starting; a coordinator token turns
// nothing off; and a node the owner installed has no machine to stop.
func TestOwnerStopsAndStartsAMachine(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	cloud := w.withCloud()
	ctx := t.Context()
	owner, _ := w.token("web", bearertoken.Owner)
	laptop, _ := w.token("laptop", bearertoken.Coordinator)

	aws := "aws"

	created, err := owner.CreateNodeWithResponse(ctx, openapi.NewNode{Name: "build-1", Provider: &aws})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, created.StatusCode(), string(created.Body))

	id := created.JSON201.Node.Id
	require.NoError(t, w.svc.NodeHello(ctx, id, orchestration.Hello{}))

	refused, err := laptop.StopNodeMachineWithResponse(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, openapi.Forbidden, errorCode(t, refused.Body, refused.StatusCode(), http.StatusForbidden))

	stopped, err := owner.StopNodeMachineWithResponse(ctx, id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, stopped.StatusCode(), string(stopped.Body))
	require.NotNil(t, stopped.JSON200.Machine.Power)
	assert.Equal(t, openapi.NodeMachinePowerStopped, *stopped.JSON200.Machine.Power)
	assert.Equal(t, openapi.NodeStatusDrained, stopped.JSON200.Status)
	assert.Equal(t, []string{id}, cloud.stopped)

	started, err := owner.StartNodeMachineWithResponse(ctx, id)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, started.StatusCode(), string(started.Body))
	assert.Equal(t, openapi.NodeMachinePowerStarting, *started.JSON200.Machine.Power)
	assert.Equal(t, []string{id}, cloud.started)

	own, _, err := w.svc.AddNode(ctx, "zm", 0)
	require.NoError(t, err)

	none, err := owner.StopNodeMachineWithResponse(ctx, own.ID)
	require.NoError(t, err)
	assert.Equal(t, openapi.Conflict, errorCode(t, none.Body, none.StatusCode(), http.StatusConflict))
}
