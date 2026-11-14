package clientcli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// The Android phones the nodes hold (issue #417).

// deviceStatus prints each node that holds a phone: one line with its state,
// then what is installed.
func deviceStatus(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.GetDeviceWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("asking for the phones: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("asking for the phones", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if len(resp.JSON200.Data) == 0 {
			e.printf("no node holds a phone: `hypomnema machines create <name> --provider aws --device android` makes one\n")
		}

		for _, d := range resp.JSON200.Data {
			e.printf("%s\n", deviceLine(d))
		}
	})
}

// deviceLine is one node's phone in a line.
func deviceLine(d openapi.DeviceNode) string {
	state := "offline"

	switch {
	case d.Connected && d.State == nil:
		state = "not said yet"
	case d.State != nil:
		state = string(*d.State)
	}

	if d.Reason != nil && *d.Reason != "" {
		state += ": " + *d.Reason
	}

	installed := make([]string, 0, len(d.Installed))
	for _, c := range d.Installed {
		installed = append(installed, c.Name+" "+c.Version)
	}

	kvm := "kvm"
	if !d.Kvm {
		kvm = "no kvm"
	}

	return fmt.Sprintf("%s\t%s\t%s\t%s\t%s", d.NodeId, d.Name, kvm, state, strings.Join(installed, ", "))
}

func deviceStart(ctx context.Context, e *env, fs *flag.FlagSet) error {
	nodeID, err := deviceNodeID(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	resp, err := e.api.StartNodeDeviceWithResponse(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("starting the phone: %w", err)
	}

	if resp.JSON202 == nil {
		return apiError("starting the phone", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON202, func() {
		e.printf("asked %s to start its phone; the first start installs it and takes minutes: "+
			"`hypomnema device status` shows how it goes\n", resp.JSON202.Name)
	})
}

func deviceStop(ctx context.Context, e *env, fs *flag.FlagSet) error {
	nodeID, err := deviceNodeID(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	resp, err := e.api.StopNodeDeviceWithResponse(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("stopping the phone: %w", err)
	}

	if resp.JSON202 == nil {
		return apiError("stopping the phone", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON202, func() { e.printf("asked %s to stop its phone\n", resp.JSON202.Name) })
}

// deviceNodeID is the id of the node a name or an id names, among those
// that hold a phone; anything else is passed on as it is, for the API to
// answer.
func deviceNodeID(ctx context.Context, e *env, node string) (string, error) {
	resp, err := e.api.GetDeviceWithResponse(ctx)
	if err != nil {
		return "", fmt.Errorf("asking for the phones: %w", err)
	}

	if resp.JSON200 == nil {
		return "", apiError("asking for the phones", resp.StatusCode(), resp.JSONDefault)
	}

	for _, d := range resp.JSON200.Data {
		if d.NodeId == node || d.Name == node {
			return d.NodeId, nil
		}
	}

	return node, nil
}
