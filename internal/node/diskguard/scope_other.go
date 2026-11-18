//go:build !linux && !darwin

package diskguard

import "errors"

func ScopeAvailable() bool { return false }

func ScopedExec([]string) error { return errors.New("scoped disk cleanup is unavailable") }
