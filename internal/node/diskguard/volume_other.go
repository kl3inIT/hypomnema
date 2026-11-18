//go:build !linux && !darwin

package diskguard

import "errors"

func volume(string) (uint64, error) {
	return 0, errors.New("mount identity is unavailable")
}
