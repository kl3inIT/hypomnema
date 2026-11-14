package device

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The AVD is written with the settings proven on the trial machine, and an
// AVD that is there is never written again: it holds the owner's sign-ins.
func TestTheAVDIsWrittenOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, writeAVD(dir))

	pointer, err := os.ReadFile(filepath.Join(dir, "avd", "phone.ini")) //nolint:gosec // The test's own directory.
	require.NoError(t, err)
	assert.Equal(t, "avd.ini.encoding=UTF-8\npath="+filepath.Join(dir, "avd", "phone.avd")+"\ntarget=android-34\n",
		string(pointer))

	config := filepath.Join(dir, "avd", "phone.avd", "config.ini")

	raw, err := os.ReadFile(config) //nolint:gosec // The test's own directory.
	require.NoError(t, err)

	for _, line := range []string{
		"AvdId=phone", "abi.type=x86_64", "hw.cpu.arch=x86_64",
		"image.sysdir.1=system-images/android-34/google_apis_playstore/x86_64/", "tag.id=google_apis_playstore",
		"PlayStore.enabled=true", "hw.lcd.width=720", "hw.lcd.height=1280", "hw.lcd.density=320",
		"hw.ramSize=4096", "disk.dataPartition.size=8G", "hw.keyboard=yes", "hw.gpu.enabled=yes", "hw.gpu.mode=host",
	} {
		assert.Contains(t, string(raw), line+"\n")
	}

	// What the emulator or the owner changed in the AVD stays.
	require.NoError(t, os.WriteFile(config, []byte("changed\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "avd", "phone.ini"), []byte("moved\n"), 0o600))
	require.NoError(t, writeAVD(dir))

	raw, err = os.ReadFile(config) //nolint:gosec // The test's own directory.
	require.NoError(t, err)
	assert.Equal(t, "changed\n", string(raw))

	pointer, err = os.ReadFile(filepath.Join(dir, "avd", "phone.ini")) //nolint:gosec // The test's own directory.
	require.NoError(t, err)
	assert.Equal(t, "moved\n", string(pointer))
}
