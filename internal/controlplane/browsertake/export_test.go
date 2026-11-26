package browsertake

import "context"

// What the package's tests outside it reach.
const (
	MinFreeBytes = minFreeBytes
	DiskEvery    = diskEvery
)

// Check ends the takes whose work ended, as Run does at each tick.
func (r *Recorder) Check(ctx context.Context) { r.check(ctx) }
