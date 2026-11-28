// Package githubstore keeps what the control plane knows of the owner's GitHub App (issue #1262): the repositories it
// is installed on, the deliveries it was sent, and the CI of the pull requests someone waits for. It is a domain store
// of the orchestration database reached through the root package's GitHub(), as meetingstore is; it imports the
// generated store and never the root package. The App's credential is in the vault, which it never opens; calling
// GitHub and acting on its events is the githubapp package's.
package githubstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// Store keeps the GitHub App's installations, deliveries and the CI waited for.
type Store struct {
	db     *sql.DB
	clock  platform.Clock
	signal func()
}

// New returns the Store over the orchestration database.
func New(db *sql.DB, clock platform.Clock, signal func()) *Store {
	return &Store{db: db, clock: clock, signal: signal}
}

func (s *Store) write(ctx context.Context, fn func(q *store.Queries, now int64) error) error {
	return storeutil.Write(ctx, s.db, s.clock, s.signal, fn)
}

func (s *Store) read() *store.Queries { return store.New(s.db) }

func nullString(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }

// keepGitHubRows is how long a delivery, and the CI of a pull request nobody waited for since, are kept: GitHub
// redelivers within three days ("Redelivering webhooks"), and #1232 reads conversations as they come.
const keepGitHubRows = 30 * 24 * time.Hour

// Installation is a repository the App is installed on (owner/name in lower case), GitHub's id of it, and its
// installation.
type Installation struct {
	Repository     string
	RepositoryID   int64
	InstallationID int64
	Account        string
	UpdatedAt      time.Time
}

// SetInstallations replaces what is known of the App's installations with what GitHub said now.
func (s *Store) SetInstallations(ctx context.Context, installs []Installation) error {
	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.DeleteGitHubAppInstallations(ctx); err != nil {
			return err
		}

		for _, i := range installs {
			if err := q.PutGitHubAppInstallation(ctx, store.PutGitHubAppInstallationParams{
				Repository: strings.ToLower(i.Repository), RepositoryID: i.RepositoryID,
				InstallationID: i.InstallationID, Account: i.Account, Now: now,
			}); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("keeping the GitHub App's installations: %w", err)
	}

	return nil
}

// InstallationOf is the installation that covers a repository (owner/name), with GitHub's id of the repository, and
// false when none does.
func (s *Store) InstallationOf(ctx context.Context, repository string) (Installation, bool, error) {
	repository = strings.ToLower(repository)

	row, err := s.read().GitHubAppInstallationOf(ctx, repository)
	if errors.Is(err, sql.ErrNoRows) {
		return Installation{}, false, nil
	}

	if err != nil {
		return Installation{}, false, fmt.Errorf("reading the GitHub App's installation of %s: %w", repository, err)
	}

	return Installation{Repository: repository, RepositoryID: row.RepositoryID, InstallationID: row.InstallationID},
		true, nil
}

// Installations lists the repositories the App is installed on.
func (s *Store) Installations(ctx context.Context) ([]Installation, error) {
	rows, err := s.read().ListGitHubAppInstallations(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the GitHub App's installations: %w", err)
	}

	out := make([]Installation, 0, len(rows))
	for _, r := range rows {
		out = append(out, Installation{
			Repository: r.Repository, RepositoryID: r.RepositoryID, InstallationID: r.InstallationID,
			Account: r.Account, UpdatedAt: time.UnixMilli(r.UpdatedAt),
		})
	}

	return out, nil
}

// Delivery is a webhook delivery the App was sent. Payload is kept for the conversations' events only.
type Delivery struct {
	ID         string
	Event      string
	Action     string
	Repository string
	Payload    string
	ReceivedAt time.Time
}

// RecordDelivery records a delivery by its id, and says false when that id was recorded before: a delivery
// GitHub sends again is then handled once. Deliveries older than keepGitHubRows go.
func (s *Store) RecordDelivery(ctx context.Context, d Delivery) (bool, error) {
	var recorded bool

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.DeleteGitHubDeliveriesBefore(ctx, now-keepGitHubRows.Milliseconds()); err != nil {
			return err
		}

		n, err := q.RecordGitHubDelivery(ctx, store.RecordGitHubDeliveryParams{
			ID: d.ID, Event: d.Event, Action: d.Action, Repository: d.Repository, Payload: nullString(d.Payload),
			Now: now,
		})
		recorded = n == 1

		return err
	})
	if err != nil {
		return false, fmt.Errorf("recording GitHub's delivery %s: %w", d.ID, err)
	}

	return recorded, nil
}

// ForgetDelivery drops a delivery's record once it was handled and was no call of the App (issue #1232): what a
// refused sender wrote leaves no trace, not even its id. GitHub sends it again only when asked, which handles it
// again the same way.
func (s *Store) ForgetDelivery(ctx context.Context, id string) error {
	err := s.write(ctx, func(q *store.Queries, _ int64) error { return q.DeleteGitHubDelivery(ctx, id) })
	if err != nil {
		return fmt.Errorf("forgetting GitHub's delivery %s: %w", id, err)
	}

	return nil
}

// LatestDelivery is the last delivery received, and false before any.
func (s *Store) LatestDelivery(ctx context.Context) (Delivery, bool, error) {
	r, err := s.read().LatestGitHubDelivery(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, false, nil
	}

	if err != nil {
		return Delivery{}, false, fmt.Errorf("reading the last GitHub delivery: %w", err)
	}

	return Delivery{
		ID: r.ID, Event: r.Event, Action: r.Action, Repository: r.Repository, ReceivedAt: time.UnixMilli(r.ReceivedAt),
	}, true, nil
}

// CIState is where a pull request's CI is.
type CIState string

// The states of a pull request's CI: its head's required checks are still running, all passed, or one failed; or
// the request merged or closed, and has no more CI to wait for.
const (
	CIPending CIState = "pending"
	CIPassed  CIState = "passed"
	CIFailed  CIState = "failed"
	CIMerged  CIState = "merged"
	CIClosed  CIState = "closed"
)

// Done says the state is one a wait ends on.
func (c CIState) Done() bool { return c != CIPending && c != "" }

// CI is the CI of a pull request someone waits for: its head as last read or told, the state of that
// head's checks, the required checks that failed, and when GitHub was last read for it (zero: never).
type CI struct {
	Repository string
	Number     int
	Head       string
	State      CIState
	Failed     []string
	ReadAt     time.Time
	UpdatedAt  time.Time
	// Version grows with each change of the row: a read of GitHub's that began at one keeps what it found only while
	// the row is still at it (KeepRead).
	Version int64
	// Completed is when the head's last check ended, as GitHub says it (RFC 3339, UTC); "" while none did, or when an
	// event set the CI. Of two reads of a head that ended, the later end is kept (KeepRead).
	Completed string
}

// WatchCI starts keeping the CI of a pull request, unless it is kept already, and returns it. Pull
// requests nobody waited for in keepGitHubRows go.
func (s *Store) WatchCI(ctx context.Context, repository string, number int) (CI, error) {
	repository = strings.ToLower(repository)

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.DeletePullRequestCIBefore(ctx, now-keepGitHubRows.Milliseconds()); err != nil {
			return err
		}

		return q.WatchPullRequestCI(ctx, store.WatchPullRequestCIParams{
			Repository: repository, Number: int64(number), Now: now,
		})
	})
	if err != nil {
		return CI{}, fmt.Errorf("keeping the CI of %s#%d: %w", repository, number, err)
	}

	ci, _, err := s.CIOf(ctx, repository, number)

	return ci, err
}

// CIOf is the CI kept of a pull request, and false when nobody waits for it.
func (s *Store) CIOf(ctx context.Context, repository string, number int) (CI, bool, error) {
	r, err := s.read().GetPullRequestCI(ctx, store.GetPullRequestCIParams{
		Repository: strings.ToLower(repository), Number: int64(number),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return CI{}, false, nil
	}

	if err != nil {
		return CI{}, false, fmt.Errorf("reading the CI of %s#%d: %w", repository, number, err)
	}

	ci := CI{
		Repository: r.Repository, Number: int(r.Number), Head: r.Head, State: CIState(r.State),
		UpdatedAt: time.UnixMilli(r.UpdatedAt), Version: r.Version, Completed: r.Completed,
	}
	if r.Failed != "" {
		ci.Failed = strings.Split(r.Failed, "\n")
	}

	if r.ReadAt > 0 {
		ci.ReadAt = time.UnixMilli(r.ReadAt)
	}

	return ci, true, nil
}

// SetCI keeps a pull request's CI, when someone waits for it, and says whether someone does. read says it
// was read from GitHub now, which a wait that hears no event counts from.
func (s *Store) SetCI(ctx context.Context, ci CI, read bool) (bool, error) {
	var kept bool

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		readAt := ci.ReadAt.UnixMilli()
		if ci.ReadAt.IsZero() {
			readAt = 0
		}

		if read {
			readAt = now
		}

		n, err := q.SetPullRequestCI(ctx, store.SetPullRequestCIParams{
			Head: strings.ToLower(ci.Head), State: string(ci.State), Failed: strings.Join(ci.Failed, "\n"),
			ReadAt: readAt, Completed: ci.Completed, Now: now, Repository: strings.ToLower(ci.Repository),
			Number: int64(ci.Number),
		})
		kept = n == 1

		return err
	})
	if err != nil {
		return false, fmt.Errorf("keeping the CI of %s#%d: %w", ci.Repository, ci.Number, err)
	}

	return kept, nil
}

// KeepRead keeps what a read of GitHub's found of a pull request's CI, read now, when its row is still at the version
// the read began from, and says whether it was kept. An event that changed the row while GitHub was read, such as a
// new head's reset, wins: the read found the old head (issue #1262, review round 2). One exception: a read that found
// the row's own head passed or failed is kept when the row says pending (review round 3) or ended earlier by GitHub's
// completed time, a rerun (review round 4), when both have one: a result without a time never loses to one with a
// time (review round 5). A read that found the pull request merged or closed never wins so: a
// reopen's pending stands, and only the pull_request events, or a read with nothing changed since it began, close
// it (review round 4).
func (s *Store) KeepRead(ctx context.Context, ci CI, version int64) (bool, error) {
	var kept bool

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		n, err := q.SetPullRequestCIIfUnchanged(ctx, store.SetPullRequestCIIfUnchangedParams{
			Head: strings.ToLower(ci.Head), State: string(ci.State), Failed: strings.Join(ci.Failed, "\n"), Now: now,
			Completed: ci.Completed, Repository: strings.ToLower(ci.Repository), Number: int64(ci.Number),
			Version: version,
		})
		kept = n == 1

		return err
	})
	if err != nil {
		return false, fmt.Errorf("keeping the CI read of %s#%d: %w", ci.Repository, ci.Number, err)
	}

	return kept, nil
}
