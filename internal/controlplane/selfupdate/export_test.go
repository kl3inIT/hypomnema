package selfupdate

import "io"

// WithCopyData has an update copy a backup's bytes with f, so a test can fail it as a full disk would.
func WithCopyData(o Options, f func(dst io.Writer, src io.Reader) (int64, error)) Options {
	o.copyData = f

	return o
}
