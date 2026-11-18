//go:build !unix

package platform

import "errors"

// FreeBytes is not known here: no free disk is checked.
func FreeBytes(string) (uint64, error) {
	return 0, errors.New("the free disk is not known on this system")
}
