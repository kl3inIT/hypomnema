package selfupdate

import (
	"io"
	"os"
)

// WithCopyData has an update copy a backup's bytes with f, so a test can fail it as a full disk would.
func WithCopyData(o Options, f func(dst io.Writer, src io.Reader) (int64, error)) Options {
	o.copyData = f

	return o
}

// WithLink has an update hard-link with f, so a test can fail it as two file systems would.
func WithLink(o Options, f func(root *os.Root, oldname, newname string) error) Options {
	o.link = f

	return o
}

// WithSyncDir has an update sync a folder with f, so a test can fail it.
func WithSyncDir(o Options, f func(root *os.Root, name string) error) Options {
	o.syncDir = f

	return o
}
