package device

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	require.NoError(t, err)

	return b
}

// The stream's start and its packets read as serve-emu's protocol note gives
// them for scrcpy 3 (LLP 0008, "Golden byte sequences": H.264, 1080 by 1920,
// a key frame at 42 µs), and a codec that is not H.264 is refused.
func TestTheStreamReadsAsScrcpy3Sends(t *testing.T) {
	t.Parallel()

	name := make([]byte, deviceNameBytes)
	copy(name, "sdk_gphone64_x86_64")

	size, err := readPreamble(bytes.NewReader(slicesJoin(name, unhex(t, "683236340000043800000780"))))
	require.NoError(t, err)
	assert.Equal(t, screenSize{Width: 1080, Height: 1920}, size)

	_, err = readPreamble(bytes.NewReader(slicesJoin(name, unhex(t, "683236350000043800000780"))))
	require.ErrorContains(t, err, "not H.264")

	stream := bufio.NewReader(bytes.NewReader(slicesJoin(
		unhex(t, "8000000000000000"+"00000003"), []byte{0, 0, 1},
		unhex(t, "400000000000002a00000004"), []byte{0, 0, 1, 0x65},
		unhex(t, "000000000000002b00000002"), []byte{0x41, 0x9a},
	)))

	config, err := readPacket(stream)
	require.NoError(t, err)
	assert.Equal(t, packet{Config: true, Data: []byte{0, 0, 1}}, config)

	key, err := readPacket(stream)
	require.NoError(t, err)
	assert.Equal(t, packet{Key: true, PTS: 42, Data: []byte{0, 0, 1, 0x65}}, key)

	delta, err := readPacket(stream)
	require.NoError(t, err)
	assert.Equal(t, packet{PTS: 43, Data: []byte{0x41, 0x9a}}, delta)

	_, err = readPacket(stream)
	require.ErrorIs(t, err, io.EOF)

	_, err = readPacket(bufio.NewReader(bytes.NewReader(unhex(t, "400000000000002a00000004000001"))))
	require.ErrorContains(t, err, "inside a packet")
}

func slicesJoin(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// A frame goes to a view with serve-emu's SEMU header (its golden v1 bytes)
// and, for a key frame, the codec's configuration before it.
func TestAFrameCarriesSEMUAndAKeyFrameItsConfiguration(t *testing.T) {
	t.Parallel()

	config := []byte{0, 0, 0, 1, 0x67}
	key := semuFrame(packet{Key: true, PTS: 42, Data: []byte{0, 0, 0, 1, 0x65}}, config)
	assert.Equal(t, "53454d5501010000000000000000002a"+"0000000167"+"0000000165", hex.EncodeToString(key))

	delta := semuFrame(packet{PTS: 43, Data: []byte{0x41}}, config)
	assert.Equal(t, "53454d5501000000000000000000002b"+"41", hex.EncodeToString(delta))
}

// The control messages are scrcpy's, byte for byte as Orca encodes them: a
// touch of 32 bytes with the finger's id, the point, the video's size, full
// pressure and the primary button (none when it lifts), a key code of 14
// bytes, a text of at most 300 bytes split between characters, and serve-emu's
// Back and video reset.
func TestTheControlMessagesAreScrcpys(t *testing.T) {
	t.Parallel()

	size := screenSize{Width: 720, Height: 1280}
	assert.Equal(t,
		"02"+"00"+"fffffffffffffffe"+"00000168"+"00000280"+"02d0"+"0500"+"ffff"+"00000001"+"00000001",
		hex.EncodeToString(touchMessage(actionDown, 360, 640, size)))
	assert.Equal(t,
		"02"+"01"+"fffffffffffffffe"+"00000168"+"00000280"+"02d0"+"0500"+"0000"+"00000001"+"00000000",
		hex.EncodeToString(touchMessage(actionUp, 360, 640, size)))

	assert.Equal(t, "00"+"00"+"00000003"+"00000000"+"00000000", hex.EncodeToString(keyMessage(actionDown, keycodeHome)))
	assert.Equal(t, "0401", hex.EncodeToString(backMessage(actionUp)))
	assert.Equal(t, "11", hex.EncodeToString(resetVideoMessage()))

	assert.Equal(t, [][]byte{append([]byte{1, 0, 0, 0, 8}, "Xin chà"...)}, textMessages("Xin chà"))

	long := textMessages(strings.Repeat("ạ", 150)) // 3 bytes each: 100 to a message.
	require.Len(t, long, 2)
	assert.Equal(t, []byte{1, 0, 0, 1, 0x2c}, long[0][:5])
	assert.Len(t, long[0], 5+300)
	assert.Len(t, long[1], 5+150)

	// A point from 0 to 1 is a pixel of the screen, the last one at size - 1.
	assert.Equal(t, 0, toPixel(-0.2, 720))
	assert.Equal(t, 360, toPixel(0.5, 720))
	assert.Equal(t, 719, toPixel(1, 720))
}

// The size after the phone turned is the SPS's, read as Orca reads it: its
// fixtures from x264, a high profile with cropping and a baseline behind a
// three-byte start code; a configuration with no SPS, or a cut one, has none.
func TestTheSizeIsReadFromTheSPS(t *testing.T) {
	t.Parallel()

	const pps = "0000000168ebe3cb22c0"

	size, ok := spsSize(unhex(t, "0000000167640032acd94044012de5f011000003000100000300320f183196"+pps))
	require.True(t, ok)
	assert.Equal(t, screenSize{Width: 1080, Height: 2400}, size)

	size, ok = spsSize(unhex(t, "0000016742c01fd900980a1e5f011000000300100000030320f1832480"+pps))
	require.True(t, ok)
	assert.Equal(t, screenSize{Width: 600, Height: 1280}, size)

	_, ok = spsSize(unhex(t, pps))
	assert.False(t, ok)

	_, ok = spsSize(unhex(t, "0000000167640032acd9"))
	assert.False(t, ok)
}

// The server runs as Orca runs it, at the pinned version, with the stream's
// limits of the design: H.264, at most 1280 pixels, 8 Mbps.
func TestTheServerRunsWithTheDesignsLimits(t *testing.T) {
	t.Parallel()

	args := strings.Join(scrcpyServerArgs("0badcafe"), " ")
	assert.Equal(t, "-s emulator-5554 shell CLASSPATH=/data/local/tmp/scrcpy-server.jar app_process / "+
		"com.genymobile.scrcpy.Server 3.3.4 scid=0badcafe log_level=info tunnel_forward=true audio=false "+
		"control=true cleanup=true clipboard_autosync=false video_codec=h264 max_size=1280 video_bit_rate=8000000", args)

	scid := newScid()
	assert.Len(t, scid, 8)
	assert.LessOrEqual(t, scid, "7fffffff")
}
