// Command installffmpeg installs the control plane's pinned ffmpeg into a folder, as the control plane does at start
// (issue #818, row 9), and prints where its programs are. CI runs it before the tests, so the tests that make videos
// run on the same build as the control plane; they find it with HYPOMNEMA_FFMPEG_DIR.
//
//	go run ./internal/controlplane/videos/ffmpeg/installffmpeg <dir>
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/kl3inIT/hypomnema/internal/controlplane/videos/ffmpeg"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: installffmpeg <dir>")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	tools, err := ffmpeg.New(ffmpeg.Config{Dir: os.Args[1]}).Ensure(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		stop()
		os.Exit(1) //nolint:gocritic // stop ran above.
	}

	fmt.Printf("ffmpeg %s\n%s\n%s\n", tools.Version, tools.FFmpeg, tools.FFprobe)
}
