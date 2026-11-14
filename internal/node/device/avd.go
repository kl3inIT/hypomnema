package device

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The phone is one AVD, written by hand rather than with avdmanager, which
// needs cmdline-tools and Java: an AVD is a pointer file, <name>.ini, and a
// folder holding config.ini, and the emulator reads nothing else to boot it
// (tried on 2026-11-14, issue #417). Orca boots an AVD made beforehand the
// same way (src/main/emulator/android/android-avd-boot.ts). The settings are
// the ones proven on the trial machine
// (docs/research/2026-11-14-android-device-for-agents.md, section 2).
//
// An AVD that exists is never written again: its folder holds the phone's
// disk, with the owner's sign-ins.

const (
	avdName = "phone"
	avdDir  = "avd"
)

// avdConfig is the phone's config.ini, in the order it is written.
var avdConfig = [][2]string{
	{"AvdId", avdName},
	{"abi.type", "x86_64"},
	{"hw.cpu.arch", "x86_64"},
	{"image.sysdir.1", "system-images/android-34/google_apis_playstore/x86_64/"},
	{"tag.id", "google_apis_playstore"},
	{"PlayStore.enabled", "true"},
	{"hw.lcd.width", "720"},
	{"hw.lcd.height", "1280"},
	{"hw.lcd.density", "320"},
	{"hw.ramSize", "4096"},
	{"disk.dataPartition.size", "8G"},
	{"hw.keyboard", "yes"},
	{"hw.gpu.enabled", "yes"},
	{"hw.gpu.mode", "host"},
}

// writeAVD writes the phone's two files under dir/avd, each only when it is
// not there.
func writeAVD(dir string) error {
	home := filepath.Join(dir, avdDir)
	folder := filepath.Join(home, avdName+".avd")

	if err := os.MkdirAll(folder, 0o700); err != nil {
		return fmt.Errorf("making the phone's folder: %w", err)
	}

	pointer := lines([][2]string{
		{"avd.ini.encoding", "UTF-8"}, {"path", folder}, {"target", "android-34"},
	})
	if err := writeOnce(filepath.Join(home, avdName+".ini"), pointer); err != nil {
		return err
	}

	return writeOnce(filepath.Join(folder, "config.ini"), lines(avdConfig))
}

func lines(pairs [][2]string) string {
	var b strings.Builder
	for _, p := range pairs {
		b.WriteString(p[0] + "=" + p[1] + "\n")
	}

	return b.String()
}

// writeOnce writes a file that is not there and leaves one that is.
func writeOnce(path, content string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // The device's own file.
	if errors.Is(err, fs.ErrExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("writing the phone's %s: %w", filepath.Base(path), err)
	}

	_, err = f.WriteString(content)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		_ = os.Remove(path)

		return fmt.Errorf("writing the phone's %s: %w", filepath.Base(path), err)
	}

	return nil
}
