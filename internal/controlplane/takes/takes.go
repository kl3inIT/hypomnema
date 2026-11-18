// Package takes keeps the takes agents film of the phone on the control
// plane (issue #817, rows 9 to 12): each in a folder of its own under the
// data folder, takes/<id>/, directories 0700 and files 0600, with its
// take.json and, once the node uploaded them, its segments. A take is
// recorded as it starts, bound to the node that films it, so only that node
// may upload it; what the node says of it while it is filmed is kept; it is
// available once every segment and its take.json arrived whole. Takes are
// kept until the owner deletes them (owner, 2026-11-18); a deleted take
// leaves a tombstone, so a late upload of it is refused and the node told to
// drop its copy.
package takes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"

	"github.com/kl3inIT/hypomnema/internal/controlplane/phone"
	"github.com/kl3inIT/hypomnema/internal/shared/devicetool"
)

const (
	takeFile = "take.json"
	// tombstone marks a deleted take's folder, which holds nothing else.
	tombstone = "deleted"
)

// ids are the ids the phone gives takes.
var ids = regexp.MustCompile(`^tk_[0-9a-f]{16}$`)

// ErrDeleted reports a take the owner deleted.
var ErrDeleted = errors.New("the take was deleted")

// Store keeps the takes in a folder.
type Store struct {
	dir string
	mu  sync.Mutex
	// FreeBytes says how much disk is free for an upload; nil: the file system's own.
	FreeBytes func(path string) (uint64, error)
}

// Open opens the takes' folder, making it.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("making the takes' folder: %w", err)
	}

	return &Store{dir: dir}, nil
}

// folder is a take's folder; empty for an id that is not one.
func (s *Store) folder(id string) string {
	if !ids.MatchString(id) {
		return ""
	}

	return filepath.Join(s.dir, id)
}

// Create records a take as it starts.
func (s *Store) Create(t devicetool.Take) error {
	dir := s.folder(t.ID)
	if dir == "" {
		return fmt.Errorf("%w: %s", phone.ErrNoTake, t.ID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("making the take's folder: %w", err)
	}

	t.Segments, t.Steps, t.Pauses = orEmpty(t.Segments), orEmpty(t.Steps), orEmpty(t.Pauses)

	return write(dir, t)
}

// Get reads a take: phone.ErrNoTake when there is none, ErrDeleted when the
// owner deleted it.
func (s *Store) Get(id string) (devicetool.Take, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.read(id)
}

// read reads a take. The caller holds mu.
func (s *Store) read(id string) (devicetool.Take, error) {
	dir := s.folder(id)
	if dir == "" {
		return devicetool.Take{}, fmt.Errorf("%w: %s", phone.ErrNoTake, id)
	}

	if _, err := os.Stat(filepath.Join(dir, tombstone)); err == nil {
		return devicetool.Take{}, fmt.Errorf("%w: %s", ErrDeleted, id)
	}

	raw, err := os.ReadFile(filepath.Join(dir, takeFile)) //nolint:gosec // A take's folder, by a checked id.
	if errors.Is(err, fs.ErrNotExist) {
		return devicetool.Take{}, fmt.Errorf("%w: %s", phone.ErrNoTake, id)
	}

	if err != nil {
		return devicetool.Take{}, err
	}

	var t devicetool.Take
	if err := json.Unmarshal(raw, &t); err != nil {
		return devicetool.Take{}, fmt.Errorf("reading take %s: %w", id, err)
	}

	return t, nil
}

// Update keeps what the node said of a take: its state, its end, its
// timeline and steps. The control plane's own fields stay (title, agent,
// node, start), and a take that is available or deleted no longer changes.
func (s *Store) Update(said devicetool.Take) (devicetool.Take, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, err := s.read(said.ID)
	if err != nil {
		return devicetool.Take{}, err
	}

	if t.State == devicetool.TakeAvailable {
		return t, nil
	}

	t = merge(t, said)

	return t, write(s.folder(t.ID), t)
}

// merge is a take with what the node said of it.
func merge(t, said devicetool.Take) devicetool.Take {
	if said.State != "" {
		t.State = said.State
	}

	if said.EndedBy != "" {
		t.EndedBy = said.EndedBy
	}

	if said.Reason != "" {
		t.Reason = said.Reason
	}

	if said.AppPackage != "" {
		t.AppPackage, t.AppVersion = said.AppPackage, said.AppVersion
	}

	if said.DurationMs > 0 {
		t.DurationMs = said.DurationMs
	}

	if said.Segments != nil {
		t.Segments = said.Segments
	}

	if said.Steps != nil {
		t.Steps = said.Steps
	}

	if said.Pauses != nil {
		t.Pauses = said.Pauses
	}

	return t
}

// ByRequest finds an agent's take by the request id it was started with.
func (s *Store) ByRequest(agent, requestID string) (devicetool.Take, bool) {
	for _, t := range s.List() {
		if t.Agent == agent && t.RequestID == requestID {
			return t, true
		}
	}

	return devicetool.Take{}, false
}

// List lists the takes not deleted, the newest first.
func (s *Store) List() []devicetool.Take {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}

	var out []devicetool.Take

	for _, e := range entries {
		if t, err := s.read(e.Name()); err == nil {
			out = append(out, t)
		}
	}

	slices.SortFunc(out, func(a, b devicetool.Take) int { return b.StartedAt.Compare(a.StartedAt) })

	return out
}

// write writes a take's take.json, whole or not at all.
func write(dir string, t devicetool.Take) error {
	raw, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}

	path := filepath.Join(dir, takeFile)

	err = os.WriteFile(path+".tmp", raw, 0o600)
	if err == nil {
		err = os.Rename(path+".tmp", path)
	}

	return err
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}
