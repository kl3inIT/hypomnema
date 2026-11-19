package agenttool

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

const execCADir = "exec-ca"
const execCAName = "authority.json"

// ExecTrust configures the user-scoped CA of the desktop exec command.
type ExecTrust struct {
	Home  string
	Names func(context.Context) ([]string, error)
}

type execTrustStore interface {
	trusted(context.Context, *execAuthority) (bool, error)
	install(context.Context, string) error
	remove(context.Context, *execAuthority) error
}

// UserTrustSupported reports whether exec must use a user-installed CA.
func UserTrustSupported() bool { return userTrustStore() != nil }

func (t *ExecTrust) prepare(ctx context.Context, store execTrustStore, out io.Writer) (*execAuthority, error) {
	names, err := t.Names(ctx)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, errors.New("the service proxy has no DNS names for the local CA")
	}

	names = slices.Clone(names)
	slices.Sort(names)
	names = slices.Compact(names)

	dir, closeDir, err := openExecHome(t.Home, true)
	if err != nil {
		return nil, err
	}
	defer closeDir()

	authority, err := readExecAuthority(dir)
	if errors.Is(err, os.ErrNotExist) {
		authority, err = writeExecAuthority(dir, names)
	}
	if err != nil {
		return nil, fmt.Errorf("loading the local service proxy CA: %w", err)
	}
	if !authority.matches(names) {
		return nil, errors.New("the local CA's service hosts changed or it is about to expire; " +
			"run 'hypomnema exec remove-ca', then retry")
	}

	trusted, err := store.trusted(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("checking the local service proxy CA: %w", err)
	}
	if trusted {
		return authority, nil
	}

	path, err := writeCACert(authority.rootPEM())
	if err != nil {
		return nil, err
	}
	defer os.Remove(path) //nolint:errcheck // Only the public certificate is temporary.

	if out != nil {
		_, _ = fmt.Fprintln(out, "hypomnema: Trust the local service proxy CA in the OS prompt to continue (first use only).")
	}

	if err := store.install(ctx, path); err != nil {
		return nil, fmt.Errorf("installing the local service proxy CA: %w", err)
	}
	trusted, err = store.trusted(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("checking the installed service proxy CA: %w", err)
	}
	if !trusted {
		return nil, errors.New("the OS did not trust the local service proxy CA; accept the OS prompt and retry")
	}

	return authority, nil
}

// RemoveExecCA untrusts only the exact CA made by this CLI, before removing its local signing keys.
func RemoveExecCA(ctx context.Context, home string) (bool, error) {
	store := userTrustStore()
	if store == nil {
		return false, errors.New("this OS does not install a local service proxy CA")
	}

	return removeExecAuthority(ctx, home, store)
}

func removeExecAuthority(ctx context.Context, home string, store execTrustStore) (bool, error) {
	dir, closeDir, err := openExecHome(home, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer closeDir()

	authority, err := readExecAuthority(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading the local service proxy CA: %w", err)
	}
	if err := store.remove(ctx, authority); err != nil {
		return false, fmt.Errorf("untrusting the local service proxy CA: %w", err)
	}
	if err := dir.Remove(execCAName); err != nil {
		return false, fmt.Errorf("removing the local service proxy CA: %w", err)
	}

	return true, nil
}

func openExecHome(home string, create bool) (*os.Root, func(), error) {
	dir := filepath.Join(home, execCADir)
	if err := privateExecDir(dir, create); err != nil {
		return nil, nil, fmt.Errorf("opening the private service proxy CA directory: %w", err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("opening the service proxy CA directory: %w", err)
	}

	lock, err := root.OpenFile("lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, nil, fmt.Errorf("opening the service proxy CA lock: %w", err)
	}
	if err := lockExecFile(lock); err != nil {
		_ = lock.Close()
		_ = root.Close()
		return nil, nil, fmt.Errorf("the local service proxy CA is busy: %w", err)
	}

	return root, func() { _ = lock.Close(); _ = root.Close() }, nil
}

func readExecAuthority(dir *os.Root) (*execAuthority, error) {
	raw, err := dir.ReadFile(execCAName)
	if err != nil {
		return nil, err
	}

	return decodeExecAuthority(raw)
}

func writeExecAuthority(dir *os.Root, names []string) (*execAuthority, error) {
	record, err := createExecAuthority(names)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}

	temp := "new-" + rand.Text()
	defer dir.Remove(temp) //nolint:errcheck // If the rename failed, leave no partial file.
	file, err := dir.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, writeErr := file.Write(raw)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return nil, err
	}
	if err := dir.Rename(temp, execCAName); err != nil {
		return nil, err
	}

	return decodeExecAuthority(raw)
}
