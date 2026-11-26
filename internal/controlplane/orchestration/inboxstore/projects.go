package inboxstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// An item's priority and project (issue #1157), as Linear's issue has both (docs: Priority, Projects) and circle keeps
// them (MIT, Copyright (c) 2025 lndev-ui; mock-data/priorities.tsx, mock-data/issues.ts:40); its project is a
// department (issue #1188).

// The priorities, on Linear's 0-4 scale: none, then urgent first and low last.
const (
	NoPriority = 0
	Urgent     = 1
	High       = 2
	Medium     = 3
	Low        = 4
)

// PriorityNames are the priorities by their value, as Linear and circle name them.
var PriorityNames = []string{"none", "urgent", "high", "medium", "low"}

// NoProject is the Filter's Project of the items without one.
const NoProject = "none"

// The limit of a priority's reason.
const (
	MaxPriorityReason = 300
)

func checkPriority(p int, reason string) error {
	switch {
	case p < NoPriority || p > Low:
		return fmt.Errorf("%w: a priority is 0 (none), 1 (urgent), 2 (high), 3 (medium) or 4 (low), not %d",
			storeerr.ErrInvalid, p)
	case utf8.RuneCountInString(reason) > MaxPriorityReason || strings.ContainsAny(reason, "\r\n"):
		return fmt.Errorf("%w: a priority's reason is one line of at most %d characters", storeerr.ErrInvalid,
			MaxPriorityReason)
	}

	return nil
}

// Project is the department an item is in (issue #1188, owner 2026-11-26): one of the sections the sidebar lists
// Bots in (issue #471), made, renamed, ordered and removed there, not from the Inbox; an item is in one or none, as a
// Linear issue is in one project. A department removed leaves its items without one (the foreign key's ON DELETE SET
// NULL).
type Project struct {
	ID   string
	Name string
}

// Projects lists the departments, in the sidebar's order.
func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := store.New(s.db).ListDepartments(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the departments: %w", err)
	}

	out := make([]Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, Project{ID: r.ID, Name: r.Name})
	}

	return out, nil
}

// ProjectNamed is the department named, by its id or its name, in its case first and then in any case: what a Bot or
// the terminal names it by.
func (s *Store) ProjectNamed(ctx context.Context, named string) (Project, error) {
	named = strings.TrimSpace(named)

	all, err := s.Projects(ctx)
	if err != nil {
		return Project{}, err
	}

	for _, p := range all {
		if p.ID == named || p.Name == named {
			return p, nil
		}
	}

	names := make([]string, 0, len(all))

	for _, p := range all {
		if strings.EqualFold(p.Name, named) {
			return p, nil
		}

		names = append(names, p.Name)
	}

	if len(names) == 0 {
		return Project{}, fmt.Errorf("%w: project %q; the owner has made no department", storeerr.ErrNotFound, named)
	}

	return Project{}, fmt.Errorf("%w: project %q; the projects are the departments %s", storeerr.ErrNotFound, named,
		strings.Join(names, ", "))
}

// checkProjectOf checks that the department an item is put in is there; none is always.
func checkProjectOf(ctx context.Context, q *store.Queries, id string) error {
	if id == "" {
		return nil
	}

	if _, err := q.GetDepartment(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: project %s is no department", storeerr.ErrNotFound, id)
		}

		return fmt.Errorf("reading department %s: %w", id, err)
	}

	return nil
}

// botsDepartment is the department of a, the Bot an item is given to (issue #1188): the project an item without one
// takes when it is given or passed to a Bot in a department, as #1118's project Bots are. It is none for anyone else
// and for a Bot in no department.
func botsDepartment(ctx context.Context, q *store.Queries, a Assignee) (sql.NullString, error) {
	if a.Kind != ABot {
		return sql.NullString{}, nil
	}

	b, err := q.GetBot(ctx, a.Name)
	if err != nil {
		return sql.NullString{}, storeutil.NotFound(err, "Bot", a.Name)
	}

	return b.Department, nil
}
