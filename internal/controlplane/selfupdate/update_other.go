//go:build !unix

package selfupdate

import (
	"context"
	"errors"
)

var errUnix = errors.New("the control plane updates itself only on Linux, where systemd runs it")

// Update is not built for this platform.
func Update(context.Context, Options) error { return errUnix }

// Install is not built for this platform.
func Install(context.Context, Options, string) error { return errUnix }

// Updating is false: no update runs here.
func Updating(string) bool { return false }

// WriteMarker is not built for this platform.
func WriteMarker(string, Begin) error { return errUnix }
