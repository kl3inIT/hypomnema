package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The takes the node's phone filmed (issue #817, rows 9 and 11): the node
// says at hello which it holds, ends one the control plane says holds no
// lease any more, drops one the owner deleted, and uploads each finished take
// on its own route, a file a request: the segments, then take.json, each
// with its length and SHA-256. A file the control plane did not take is sent
// again whole, from the start, at the next try; the node removes its copy
// once take.json is taken.

const (
	// uploadEvery is how often the node tries the uploads that wait.
	uploadEvery = 30 * time.Second
	// uploadFloor is the slowest an upload may be before it is given up: the control plane's public address
	// carried 145 to 400 KB a second (issue #578), so 64 KiB a second leaves room.
	uploadFloor = 64 << 10
)

// Takes are the takes the node's phone films; *device.Manager is the node's.
type Takes interface {
	HeldTakes() []nodeproto.TakeHeld
	Uploads() []nodeproto.TakeUpload
	UploadsReady() <-chan struct{}
	StopTake(id, endedBy, reason string) (devicetool.Take, error)
	DropTake(id string)
}

// heldTakes are the takes the node says it holds at hello; nil when it has no phone.
func (d *Daemon) heldTakes() []nodeproto.TakeHeld {
	if d.cfg.Takes == nil {
		return nil
	}

	return d.cfg.Takes.HeldTakes()
}

// settleTakes does what the control plane said of the takes at hello.
func (d *Daemon) settleTakes(ctx context.Context, resp nodeproto.HelloResponse) {
	if d.cfg.Takes == nil {
		return
	}

	for _, end := range resp.EndTakes {
		if _, err := d.cfg.Takes.StopTake(end.ID, end.EndedBy, ""); err != nil {
			d.logger.WarnContext(ctx, "a take the control plane ended could not be",
				slog.String("event", "node.take_end_failed"), slog.String("take", end.ID),
				slog.String("error", err.Error()))
		}
	}

	for _, id := range resp.DropTakes {
		d.cfg.Takes.DropTake(id)
	}

	d.uploadSoon()
}

// uploadSoon has the uploads tried now.
func (d *Daemon) uploadSoon() {
	select {
	case d.uploadNow <- struct{}{}:
	default:
	}
}

// uploadTakes uploads the finished takes until ctx ends: when one is ready,
// after each hello, and every uploadEvery.
func (d *Daemon) uploadTakes(ctx context.Context) {
	if d.cfg.Takes == nil {
		return
	}

	tick := time.NewTicker(uploadEvery)
	defer tick.Stop()

	for {
		for _, up := range d.cfg.Takes.Uploads() {
			if ctx.Err() != nil {
				return
			}

			if err := d.uploadTake(ctx, up); err != nil {
				d.logger.WarnContext(ctx, "a take could not be uploaded yet", slog.String("event", "node.take_upload"),
					slog.String("take", up.ID), slog.String("error", err.Error()))
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-d.cfg.Takes.UploadsReady():
		case <-d.uploadNow:
		case <-tick.C:
		}
	}
}

// errTakeGone is a take the control plane no longer has: the owner deleted it.
var errTakeGone = errors.New("the control plane no longer has the take")

// uploadTake uploads one take's files in order, and drops the node's copy
// once its take.json was taken, or once the owner deleted it.
func (d *Daemon) uploadTake(ctx context.Context, up nodeproto.TakeUpload) error {
	for _, name := range up.Files {
		err := d.uploadFile(ctx, up.ID, filepath.Join(up.Dir, name), name)
		if errors.Is(err, errTakeGone) {
			d.cfg.Takes.DropTake(up.ID)

			return nil
		}

		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}

	d.cfg.Takes.DropTake(up.ID)
	d.logger.InfoContext(ctx, "a take was uploaded", slog.String("event", "node.take_uploaded"),
		slog.String("take", up.ID))

	return nil
}

// uploadFile sends one file of a take, with its length and SHA-256, under a
// deadline of its own: a minute, and its size at uploadFloor.
func (d *Daemon) uploadFile(ctx context.Context, take, path, name string) error {
	sum, size, err := fileSum(path)
	if err != nil {
		return err
	}

	f, err := os.Open(path) //nolint:gosec // A file of the node's own take folder.
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // Read only.

	within := time.Minute + time.Duration(size/uploadFloor)*time.Second

	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()

	target := strings.TrimSuffix(d.cfg.URL, "/") + nodeproto.PathTakes + url.PathEscape(take) + "/files/" +
		url.PathEscape(name)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, f)
	if err != nil {
		return err
	}

	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(nodeproto.TakeSumHeader, sum)

	// The client's own timeout is for short calls; this one has its deadline in ctx.
	client := &http.Client{Transport: d.http.Transport}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	said, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated ||
		resp.StatusCode == http.StatusNoContent:
		return nil
	case resp.StatusCode == http.StatusGone:
		return errTakeGone
	default:
		return fmt.Errorf("the control plane answered %s: %s", resp.Status, strings.TrimSpace(string(said)))
	}
}

// fileSum is a file's SHA-256 and size.
func fileSum(path string) (string, int64, error) {
	f, err := os.Open(path) //nolint:gosec // A file of the node's own take folder.
	if err != nil {
		return "", 0, err
	}
	defer f.Close() //nolint:errcheck // Read only.

	hash := sha256.New()

	n, err := io.Copy(hash, f)
	if err != nil {
		return "", 0, err
	}

	return hex.EncodeToString(hash.Sum(nil)), n, nil
}
