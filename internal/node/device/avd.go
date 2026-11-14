package device

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The phone is one AVD, written without avdmanager, which needs cmdline-tools
// and Java: an AVD is a pointer file, <name>.ini, and a folder holding
// config.ini. Orca boots an AVD made beforehand the same way
// (src/main/emulator/android/android-avd-boot.ts).
//
// config.ini is the file avdmanager writes for a Pixel 6 with this system
// image, word for word, with the proven settings appended (an 8 GB data
// partition, a keyboard, 720x1280 at 320 dpi, 4 GB of RAM; a key given twice
// takes its last value). A minimal config.ini boots, but adb stays
// unauthorized forever, also with PlayStore.enabled=no; avdmanager's file is
// authorized and boots in 33 s (trial machine, 2026-11-14, issue #417). Its
// PlayStore.enabled=no is what avdmanager writes for a Play image too; the
// Play Store is still installed. The GPU mode is given on the command line.
//
// An AVD that exists is never written again: its folder holds the phone's
// disk, with the owner's sign-ins.

const (
	avdName = "phone"
	avdDir  = "avd"
)

//go:embed avd-config.ini
var avdTemplate string

// avdConfig is the phone's config.ini: its name, then avdmanager's template.
var avdConfig = lines([][2]string{{"AvdId", avdName}, {"avd.ini.displayname", avdName}}) + avdTemplate

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

	return writeOnce(filepath.Join(folder, "config.ini"), avdConfig)
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
