//go:build !unix

package agenttool

import "errors"

// The documents environment is built only where uv has a release in the
// catalog, Linux; elsewhere nothing builds it.
func lockFile(string) (func(), error) {
	return nil, errors.New("the documents environment is built on Linux only")
}

func syncDisk() {}
