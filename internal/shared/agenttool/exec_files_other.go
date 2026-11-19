//go:build !darwin && !linux && !windows

package agenttool

import (
	"errors"
	"os"
)

var errNoExecHome = errors.New("this OS does not install a local service proxy CA")

func privateExecDir(string, bool) error { return errNoExecHome }

func lockExecFile(*os.File) error { return errNoExecHome }
