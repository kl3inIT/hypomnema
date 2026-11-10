// Package machines creates and destroys the machines of a provider (issue
// #69, step 3) and keeps the registry in orchestration true to what the
// provider has.
//
// It follows Omnara's internal/machinepool: one Provider per kind behind an
// interface (providers/provider.go); the node and its token are recorded
// before the provider is called (manager.go); a create is safe to repeat
// because the machine is named after the node; and the new machine joins by
// running the installer with the control plane's URL and its token
// (providers/managed_daemon_launcher.go). The first-boot script is cloud-init
// user data, as in Coder's AWS and DigitalOcean templates.
package machines

import (
	"context"
	"time"
)

// Spec is a machine to create for a node.
type Spec struct {
	// NodeID names the machine at the provider; a create for the same node
	// must end with one machine.
	NodeID string
	// Name is the node's name, for the provider's console.
	Name   string
	Region string
	Size   string
	// UserData is the script the machine runs at first boot.
	UserData string
	// RequestID is the same for every repeat of one create.
	RequestID string
}

// Resource is a machine as the provider lists it.
type Resource struct {
	ID string
	// NodeID is the node it was created for.
	NodeID    string
	Region    string
	Size      string
	State     string
	CreatedAt time.Time
}

// Provider is one account at a provider. Every call may be repeated after an
// error.
type Provider interface {
	// Validate says whether a machine of this size can be created in the
	// region with this account, without creating anything.
	Validate(ctx context.Context, region, size string) error
	// Create asks for the node's machine and returns the provider's id for
	// it. Repeated for the same node it returns the same machine.
	Create(ctx context.Context, s Spec) (string, error)
	// Find returns the node's machine, when the provider has one that is not
	// already destroyed.
	Find(ctx context.Context, region, nodeID string) (Resource, bool, error)
	// Delete destroys the node's machine. A machine already gone is not an error.
	Delete(ctx context.Context, region, nodeID string) error
	// Stop turns the node's machine off, keeping its disk. A machine already
	// off is not an error; one that does not exist is.
	Stop(ctx context.Context, region, nodeID string) error
	// Start turns the node's machine on. A machine already on is not an
	// error; one that does not exist is.
	Start(ctx context.Context, region, nodeID string) error
	// List returns every machine in the region that this provider record created.
	List(ctx context.Context, region string) ([]Resource, error)
}
