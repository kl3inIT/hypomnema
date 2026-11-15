package device

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// What scrcpy-server (github.com/Genymobile/scrcpy, Apache-2.0) speaks, at the
// version the node pins (release.go, 3.3.4), for the owner's view of the
// phone (issue #417, step 3). The server is started as Orca starts it
// (src/main/emulator/android/scrcpy-server-deploy.ts, MIT): pushed to the
// phone, run with app_process behind an adb forward of its abstract socket,
// with audio off and video and control on. The wire is the one serve-emu
// documents for scrcpy 3 (expo-device-hub, llp/0008-serve-emu-protocol.spec.md,
// Apache-2.0; notice in third_party/serve-emu): on the first socket a dummy
// byte, the device's name in 64 bytes, the codec, width and height; then
// packets of a 12-byte header and Annex-B data. Control messages are Orca's
// encoders (scrcpy-control-protocol.ts), with Back and the video reset of
// serve-emu (src/input.ts).

const (
	// scrcpyVersion must be the pinned server's: the server refuses a client of another version.
	scrcpyVersion = "3.3.4"
	// scrcpyJar is where the server is pushed on the phone (Orca's SCRCPY_DEVICE_JAR_PATH).
	scrcpyJar = "/data/local/tmp/scrcpy-server.jar"
	// The stream's limits (issue #417's design: H.264, max 1280, 8 Mbps; serve-emu's 1280 and 8 Mbps).
	scrcpyMaxSize = 1280
	scrcpyBitRate = 8_000_000
	// scrcpyMaxFps caps the frames the phone encodes a second (serve-emu's maxFps; Orca's max_fps).
	scrcpyMaxFps = 60
	// deviceNameBytes is the device's name at the stream's start.
	deviceNameBytes = 64
	// packetHeader is a packet's header: flags and time in a uint64, and the size in a uint32.
	packetHeader = 12
	// maxPacketBytes bounds a packet's data, as serve-emu and Orca do (16 MiB).
	maxPacketBytes = 16 << 20
	// maxTextBytes bounds one inject-text message (Orca's SCRCPY_INJECT_TEXT_MAX_BYTES).
	maxInjectText = 300
	// maxPasteText bounds the text of one set-clipboard message (scrcpy's
	// SC_CONTROL_MSG_CLIPBOARD_TEXT_MAX_LENGTH: 256 KiB less its 14 bytes of header).
	maxPasteText = 1<<18 - 14
	// codecH264 is the FourCC of H.264, the codec asked for.
	codecH264 = 0x68323634
)

// The flags of a scrcpy 3 packet's header (serve-emu's "scrcpy v3 frame header").
const (
	flagConfig = uint64(1) << 63
	flagKey    = uint64(1) << 62
	ptsMask    = flagKey - 1
)

// Control message types and actions (scrcpy app/src/control_msg.h; Orca, serve-emu).
const (
	msgInjectKeycode  = 0
	msgInjectText     = 1
	msgInjectTouch    = 2
	msgBackOrScreenOn = 4
	msgSetClipboard   = 9
	msgResetVideo     = 17

	actionDown = 0
	actionUp   = 1
	actionMove = 2

	// pointerFinger is POINTER_ID_GENERIC_FINGER, -2 as a uint64: the server injects it as a finger.
	pointerFinger = ^uint64(1)
	// buttonPrimary is AMOTION_EVENT_BUTTON_PRIMARY, as Orca sends for a touch.
	buttonPrimary = 1
)

// The Android key codes of the buttons (Orca's android-input-mapping.ts).
const (
	keycodeHome    = 3
	keycodeRecents = 187
	keycodePower   = 26
)

// newScid is a scrcpy session's id: eight hex digits of a positive int32,
// which the server parses as a signed number (Orca's newScid).
func newScid() string {
	var raw [4]byte

	_, _ = rand.Read(raw[:])

	return fmt.Sprintf("%08x", binary.BigEndian.Uint32(raw[:])&0x7fffffff)
}

// scrcpyServerArgs are adb's arguments that run the server on the phone,
// Orca's (startScrcpyServerArgs) with the stream's limits of the design and
// serve-emu's 60 frames a second at most (SCRCPY_DEFAULTS.maxFps). A key
// frame every 10 s, serve-emu's keyFrameInterval, is scrcpy 3.3.4's own
// default (SurfaceEncoder.java, DEFAULT_I_FRAME_INTERVAL), so it is not passed.
func scrcpyServerArgs(scid string) []string {
	return []string{
		"-s", serial, "shell", "CLASSPATH=" + scrcpyJar, "app_process", "/", "com.genymobile.scrcpy.Server",
		scrcpyVersion, "scid=" + scid, "log_level=info", "tunnel_forward=true", "audio=false", "control=true",
		"cleanup=true", "clipboard_autosync=false", "video_codec=h264",
		fmt.Sprintf("max_size=%d", scrcpyMaxSize), fmt.Sprintf("max_fps=%d", scrcpyMaxFps),
		fmt.Sprintf("video_bit_rate=%d", scrcpyBitRate),
	}
}

// screenSize is a screen's size in pixels.
type screenSize struct{ Width, Height int }

// readPreamble reads what the video socket says before its packets, after
// the dummy byte: the device's name, the codec and the size. A codec other
// than H.264 is refused.
func readPreamble(r io.Reader) (screenSize, error) {
	var raw [deviceNameBytes + 12]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return screenSize{}, fmt.Errorf("reading the stream's start: %w", err)
	}

	meta := raw[deviceNameBytes:]
	codec := binary.BigEndian.Uint32(meta[0:4])
	size := screenSize{Width: int(binary.BigEndian.Uint32(meta[4:8])), Height: int(binary.BigEndian.Uint32(meta[8:12]))}

	switch {
	case codec != codecH264:
		return screenSize{}, fmt.Errorf("the phone streams codec %08x, not H.264", codec)
	case size.Width < 1 || size.Height < 1 || size.Width > 16384 || size.Height > 16384:
		return screenSize{}, fmt.Errorf("the phone streams a screen of %dx%d", size.Width, size.Height)
	}

	return size, nil
}

// packet is one packet of the stream: the codec's configuration (SPS and
// PPS), or a frame.
type packet struct {
	Config bool
	Key    bool
	PTS    uint64
	Data   []byte
}

// readPacket reads the next packet. The stream ends cleanly only between
// packets.
func readPacket(r *bufio.Reader) (packet, error) {
	var header [packetHeader]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return packet{}, errors.New("the stream ended inside a packet's header")
		}

		return packet{}, err
	}

	meta := binary.BigEndian.Uint64(header[0:8])
	size := binary.BigEndian.Uint32(header[8:12])

	if size == 0 || size > maxPacketBytes {
		return packet{}, fmt.Errorf("a packet of %d bytes: the stream is out of step", size)
	}

	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return packet{}, fmt.Errorf("the stream ended inside a packet: %w", err)
	}

	return packet{Config: meta&flagConfig != 0, Key: meta&flagKey != 0, PTS: meta & ptsMask, Data: data}, nil
}

// semuFrame is a frame as the view sends it: serve-emu's SEMU header
// (version 1), then the configuration when the frame is a key frame, then
// the frame, so that every key frame can start a decoder (serve-emu's
// withFrameMeta and cachedConfig).
func semuFrame(p packet, config []byte) []byte {
	if !p.Key {
		config = nil
	}

	out := make([]byte, nodeproto.DeviceFrameHeader, nodeproto.DeviceFrameHeader+len(config)+len(p.Data))
	copy(out, "SEMU")
	out[4] = 1

	if p.Key {
		out[5] = 1
	}

	binary.BigEndian.PutUint64(out[8:16], p.PTS)

	return append(append(out, config...), p.Data...)
}

// touchMessage is an inject-touch message (Orca's encodeInjectTouchEvent):
// the point in the video's pixels, with the video's size, which the server
// checks against its own and drops a touch sized for another.
func touchMessage(action byte, x, y int, size screenSize) []byte {
	out := make([]byte, 32)
	out[0] = msgInjectTouch
	out[1] = action
	binary.BigEndian.PutUint64(out[2:10], pointerFinger)
	binary.BigEndian.PutUint32(out[10:14], uint32(int32(x)))    //nolint:gosec // A pixel of the screen, clamped.
	binary.BigEndian.PutUint32(out[14:18], uint32(int32(y)))    //nolint:gosec // A pixel of the screen, clamped.
	binary.BigEndian.PutUint16(out[18:20], uint16(size.Width))  //nolint:gosec // At most 16384.
	binary.BigEndian.PutUint16(out[20:22], uint16(size.Height)) //nolint:gosec // At most 16384.

	// A finger that lifts has no pressure, and no button held.
	if action != actionUp {
		binary.BigEndian.PutUint16(out[22:24], math.MaxUint16)
		binary.BigEndian.PutUint32(out[28:32], buttonPrimary)
	}

	binary.BigEndian.PutUint32(out[24:28], buttonPrimary)

	return out
}

// keyMessage is an inject-keycode message.
func keyMessage(action byte, keycode int) []byte {
	out := make([]byte, 14)
	out[0] = msgInjectKeycode
	out[1] = action
	binary.BigEndian.PutUint32(out[2:6], uint32(int32(keycode))) //nolint:gosec // An Android key code.

	return out
}

// textMessages are a text as control messages. A text of ASCII is injected,
// in inject-text messages of at most 300 bytes split between characters
// (Orca's splitScrcpyInjectText). The server types an injected text with the
// phone's key map, which has no key for most of Vietnamese ("Could not inject
// char u+1edf" for ở, tried on the Play image on 2026-11-15), so a text with
// any other character is pasted, as scrcpy's own client pastes (set-clipboard
// with paste, app/src/control_msg.c; ws-scrcpy's createSetClipboardCommand).
// A paste goes through the phone's clipboard, which any app can read
// (scrcpy's doc/control.md), so text that can be injected is not pasted.
func textMessages(text string) [][]byte {
	ascii := true

	for _, r := range text {
		if r >= utf8.RuneSelf {
			ascii = false

			break
		}
	}

	var out [][]byte

	if ascii {
		for _, chunk := range splitText(text, maxInjectText) {
			msg := make([]byte, 5, 5+len(chunk))
			msg[0] = msgInjectText
			binary.BigEndian.PutUint32(msg[1:5], uint32(len(chunk))) //nolint:gosec // At most 300.
			out = append(out, append(msg, chunk...))
		}

		return out
	}

	for _, chunk := range splitText(text, maxPasteText) {
		// Sequence 0 asks for no acknowledgement (scrcpy's SC_SEQUENCE_INVALID); paste presses Paste after.
		msg := make([]byte, 14, 14+len(chunk))
		msg[0] = msgSetClipboard
		msg[9] = 1
		binary.BigEndian.PutUint32(msg[10:14], uint32(len(chunk))) //nolint:gosec // At most 256 KiB.
		out = append(out, append(msg, chunk...))
	}

	return out
}

// splitText splits a text between characters into parts of at most limit
// bytes, leaving out what is not UTF-8.
func splitText(text string, limit int) []string {
	var (
		out   []string
		chunk strings.Builder
	)

	for _, r := range text {
		if r == utf8.RuneError {
			continue
		}

		if chunk.Len()+utf8.RuneLen(r) > limit {
			out = append(out, chunk.String())
			chunk.Reset()
		}

		chunk.WriteRune(r)
	}

	if chunk.Len() > 0 {
		out = append(out, chunk.String())
	}

	return out
}

// backMessage is Back, which serve-emu sends as BACK_OR_SCREEN_ON rather than
// a key code: it also wakes a screen that is off.
func backMessage(action byte) []byte { return []byte{msgBackOrScreenOn, action} }

// resetVideoMessage asks the server for a new key frame at once (serve-emu's
// requestVideoReset).
func resetVideoMessage() []byte { return []byte{msgResetVideo} }

// toPixel is a point from 0 to 1 in pixels, the last pixel being size - 1
// (Orca's normalizedToDevicePixels).
func toPixel(normalized float64, size int) int {
	return min(max(int(math.Round(normalized*float64(size))), 0), size-1)
}
