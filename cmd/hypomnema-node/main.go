// Command hypomnema-node runs on every machine that does work. Pairing and the
// outbound connection to the control plane come with the node's own issue;
// for now it reports its version.
//
//	hypomnema-node version   print the version
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

var errUsage = errors.New("usage: hypomnema-node version")

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "hypomnema-node:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] != "version" {
		return errUsage
	}

	_, err := fmt.Fprintln(stdout, platform.Version)

	return err
}
