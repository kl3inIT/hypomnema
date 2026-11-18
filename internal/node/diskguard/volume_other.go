//go:build !linux && !darwin

package diskguard

import (
	"errors"
	"os"
)

type mountID = uint64

func volume(string) (mountID, error) {
	return 0, errors.New("mount identity is unavailable")
}

func volumeAt(*os.Root, string) (mountID, error) { return volume("") }
