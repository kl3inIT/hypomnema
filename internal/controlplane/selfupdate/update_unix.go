//go:build unix

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/kl3inIT/hypomnema/deploy/coordinator"
	"github.com/kl3inIT/hypomnema/internal/shared/release"
)

// Update holds the lock, and when the release is newer than the installed control plane downloads it, checks it
// and has it install itself. It says what it did on Out; nothing newer is not an error.
func Update(ctx context.Context, o Options) error {
	if err := os.MkdirAll(o.State, 0o700); err != nil {
		return fmt.Errorf("making %s: %w", o.State, err)
	}

	unlock, err := lock(filepath.Join(o.State, "lock"))
	if err != nil {
		return err
	}
	defer unlock()

	// The installed release is this binary's own version: the root-owned /usr/local/bin/hypomnema that runs this is
	// put in place with the control plane by every update. The control plane's own binary is its user's and is never
	// run as root.
	current := o.Current
	reports := o.reports()

	m, err := release.ReadManifest(ctx, o.HTTP, o.manifestURL())
	if err != nil {
		return err
	}

	if !release.Newer(m.Version, current) {
		o.printf("the control plane is at %s; the release is %s: nothing to do\n", current, m.Version)

		return nil
	}

	staged := filepath.Join(o.State, "hypomnema-"+m.Version)
	if err := release.Download(ctx, o.HTTP, m, staged); err != nil {
		return err
	}

	got, err := reports(ctx, staged)
	if err != nil {
		return fmt.Errorf("the downloaded control plane does not run: %w", err)
	}

	if got != m.Version {
		return fmt.Errorf("the downloaded control plane says it is %s, not the %s its manifest names", got, m.Version)
	}

	o.printf("updating the control plane from %s to %s\n", current, m.Version)

	// The release installs itself, with its own steps and files, while this one holds the lock.
	return o.run()(ctx, staged, "update", "install",
		"--from", staged, "--user", o.User, "--home", o.Home, "--unit", o.Unit, "--state", o.State, "--ready", o.Ready)
}

// Install puts the binary at from in place of the control plane, as described in the package's comment. It is run
// by Update, from that binary.
func Install(ctx context.Context, o Options, from string) error {
	run := o.run()
	stamp := time.Now().UTC().Format("20060102-150405")
	backup := filepath.Join(o.Home, "data", "predeploy-"+stamp)
	previous := o.binary() + ".previous"

	owner, err := lookup(o.User)
	if err != nil {
		return err
	}

	if err := run(ctx, "systemctl", "stop", o.Unit); err != nil {
		return err
	}

	if err := backUp(o.database(), backup, owner); err != nil {
		return errors.Join(err, start(ctx, run, o.Unit))
	}

	if err := place(o.binary(), previous, from, owner); err != nil {
		return errors.Join(err, start(ctx, run, o.Unit))
	}

	err = run(ctx, "runuser", "-u", o.User, "--", "sh", "-c",
		`. "$1/env" && exec "$1/bin/hypomnema" migrate`, "migrate", o.Home)
	if err == nil {
		err = run(ctx, "systemctl", "start", o.Unit)
	}

	if err == nil {
		err = o.waitReady(ctx)
	}

	if err != nil {
		o.printf("the new control plane did not start; putting the old one back: %v\n", err)

		return errors.Join(err, rollBack(ctx, o, run, previous, backup))
	}

	// The launchers and the root-owned binary of this release, now that it runs: a chat started before this used
	// the old ones with the old control plane.
	if err := setUpCoordinator(ctx, o, run, from); err != nil {
		return fmt.Errorf("the control plane runs the new release, but setting up the coordinator failed: %w", err)
	}

	o.printf("the control plane runs the new release; the database before it is in %s\n", backup)

	if err := pruneBackups(filepath.Join(o.Home, "data"), backupsKept); err != nil {
		o.printf("removing old database backups failed: %v\n", err)
	}

	return nil
}

// backupsKept is how many copies of the database an update keeps, the newest (issue #335; the number is inferred:
// a rollback needs only the newest, the others are for the owner).
const backupsKept = 5

// pruneBackups removes all but the newest keep `predeploy-*` folders in dir, by when they were made. It runs only
// after an update that worked.
func pruneBackups(dir string, keep int) error {
	matches, err := filepath.Glob(filepath.Join(dir, "predeploy-*"))
	if err != nil {
		return err
	}

	type backup struct {
		path string
		at   time.Time
	}

	var found []backup

	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil || !info.IsDir() {
			continue
		}

		found = append(found, backup{path: m, at: info.ModTime()})
	}

	if len(found) <= keep {
		return nil
	}

	slices.SortFunc(found, func(a, b backup) int { return b.at.Compare(a.at) })

	var errs []error

	for _, b := range found[keep:] {
		errs = append(errs, os.RemoveAll(b.path))
	}

	return errors.Join(errs...)
}

func (o Options) run() func(ctx context.Context, name string, args ...string) error {
	if o.Run != nil {
		return o.Run
	}

	return func(ctx context.Context, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: the update's own commands, from its options
		cmd.Stdout, cmd.Stderr = o.Out, o.Out

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s %v: %w", name, args, err)
		}

		return nil
	}
}

func (o Options) reports() func(ctx context.Context, path string) (string, error) {
	if o.Reports != nil {
		return o.Reports
	}

	return release.Reported
}

func start(ctx context.Context, run func(context.Context, string, ...string) error, unit string) error {
	return run(ctx, "systemctl", "start", unit)
}

// lock takes the update's lock without waiting: a second update gives up (Hermes's update lock, a kernel flock
// held for the whole update).
func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // a root-owned lock file
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()

		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}

		return nil, fmt.Errorf("locking %s: %w", path, err)
	}

	return func() { _ = f.Close() }, nil
}

type ids struct{ uid, gid int }

func lookup(name string) (ids, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return ids{}, fmt.Errorf("finding user %s: %w", name, err)
	}

	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return ids{}, fmt.Errorf("user %s's id %q: %w", name, u.Uid, err)
	}

	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return ids{}, fmt.Errorf("user %s's group %q: %w", name, u.Gid, err)
	}

	return ids{uid, gid}, nil
}

// backUp copies the database and its WAL files into dir, owned by the control plane's user.
func backUp(database, dir string, owner ids) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("making %s: %w", dir, err)
	}

	if err := os.Lchown(dir, owner.uid, owner.gid); err != nil {
		return fmt.Errorf("giving %s to the control plane's user: %w", dir, err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		from := database + suffix
		to := filepath.Join(dir, filepath.Base(from))

		if err := copyFile(from, to, 0o600, owner); err != nil {
			if suffix != "" && errors.Is(err, fs.ErrNotExist) {
				continue
			}

			return fmt.Errorf("backing up the database: %w", err)
		}
	}

	return nil
}

// place keeps the binary as previous and puts from in its place, owned by the control plane's user.
func place(binary, previous, from string, owner ids) error {
	if err := copyFile(binary, previous, 0o755, owner); err != nil {
		return fmt.Errorf("keeping the old binary: %w", err)
	}

	staged := binary + ".new"
	if err := copyFile(from, staged, 0o755, owner); err != nil {
		return err
	}

	if err := os.Rename(staged, binary); err != nil {
		return fmt.Errorf("putting the new binary in place: %w", err)
	}

	return nil
}

func rollBack(
	ctx context.Context, o Options, run func(context.Context, string, ...string) error, previous, backup string,
) error {
	owner, err := lookup(o.User)
	if err != nil {
		return err
	}

	// Stopped, or it never started.
	_ = run(ctx, "systemctl", "stop", o.Unit)

	if err := os.Rename(previous, o.binary()); err != nil {
		return fmt.Errorf("putting the old binary back: %w", err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		saved := filepath.Join(backup, filepath.Base(o.database())+suffix)
		target := o.database() + suffix

		if err := copyFile(saved, target, 0o600, owner); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// A WAL file the backup did not have.
				_ = os.Remove(target)

				continue
			}

			return fmt.Errorf("putting the database back: %w", err)
		}
	}

	if err := start(ctx, run, o.Unit); err != nil {
		return err
	}

	return o.waitReady(ctx)
}

// setUpCoordinator writes this release's deploy/coordinator files and binary into a root-owned folder and runs its
// setup-coordinator.sh, which installs the launchers, the sudoers rule and /usr/local/bin/hypomnema.
func setUpCoordinator(
	ctx context.Context, o Options, run func(context.Context, string, ...string) error, from string,
) error {
	dir, err := os.MkdirTemp(o.State, "coordinator-")
	if err != nil {
		return fmt.Errorf("making a folder for the coordinator's files: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // a temporary folder

	if err := fs.WalkDir(coordinator.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := coordinator.Files.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		return os.WriteFile(filepath.Join(dir, path), data, 0o755) //nolint:gosec // scripts, in a root-owned folder
	}); err != nil {
		return err
	}

	if err := copyFile(from, filepath.Join(dir, "hypomnema"), 0o755, ids{os.Getuid(), os.Getgid()}); err != nil {
		return err
	}

	// setup-coordinator.sh cds to / itself; it needs a world-readable folder for nothing it runs as the coordinator.
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // its files are read by the commands the script runs
		return fmt.Errorf("opening %s: %w", dir, err)
	}

	return run(ctx, "sh", filepath.Join(dir, "setup-coordinator.sh"), o.User, dir)
}

func copyFile(from, to string, mode os.FileMode, owner ids) error {
	in, err := os.Open(from) //nolint:gosec // paths of the update's own layout
	if err != nil {
		return fmt.Errorf("reading %s: %w", from, err)
	}
	defer in.Close() //nolint:errcheck // read only

	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) //nolint:gosec // as above
	if err != nil {
		return fmt.Errorf("writing %s: %w", to, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()

		return fmt.Errorf("writing %s: %w", to, err)
	}

	if err := out.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", to, err)
	}

	if err := os.Chmod(to, mode); err != nil {
		return fmt.Errorf("setting the mode of %s: %w", to, err)
	}

	if err := os.Lchown(to, owner.uid, owner.gid); err != nil {
		return fmt.Errorf("giving %s to its owner: %w", to, err)
	}

	return nil
}
