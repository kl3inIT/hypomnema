package inboxstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/storeutil"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// An item's priority and project (issue #1157), as Linear's issue has both (docs: Priority, Projects) and circle keeps
// them (MIT, Copyright (c) 2025 lndev-ui; mock-data/priorities.tsx, mock-data/issues.ts:40, mock-data/projects.ts).

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

// Limits of a priority's reason and a project.
const (
	MaxPriorityReason = 300
	MaxProjectName    = 60
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

// Project is one of the Inbox's projects: a name and a colour, #rrggbb.
type Project struct {
	ID        string
	Name      string
	Color     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

var colorPattern = regexp.MustCompile(`^#[0-9a-f]{6}$`)

func checkProject(name, color string) error {
	switch {
	case name == "" || utf8.RuneCountInString(name) > MaxProjectName || strings.ContainsAny(name, "\r\n"):
		return fmt.Errorf("%w: a project's name is one line of 1 to %d characters", storeerr.ErrInvalid,
			MaxProjectName)
	case !colorPattern.MatchString(color):
		return fmt.Errorf("%w: a project's colour is #rrggbb, not %q", storeerr.ErrInvalid, color)
	}

	return nil
}

func projectOf(r store.InboxProject) Project {
	return Project{
		ID: r.ID, Name: r.Name, Color: r.Color, CreatedAt: storeutil.Millis(r.CreatedAt),
		UpdatedAt: storeutil.Millis(r.UpdatedAt),
	}
}

// Projects lists the Inbox's projects, by name.
func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := store.New(s.db).ListInboxProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the inbox's projects: %w", err)
	}

	out := make([]Project, 0, len(rows))
	for _, r := range rows {
		out = append(out, projectOf(r))
	}

	return out, nil
}

// ProjectNamed is the project named, by its id or its name in any case: what a Bot or the terminal names it by.
func (s *Store) ProjectNamed(ctx context.Context, named string) (Project, error) {
	named = strings.TrimSpace(named)

	all, err := s.Projects(ctx)
	if err != nil {
		return Project{}, err
	}

	names := make([]string, 0, len(all))

	for _, p := range all {
		if p.ID == named || strings.EqualFold(p.Name, named) {
			return p, nil
		}

		names = append(names, p.Name)
	}

	if len(names) == 0 {
		return Project{}, fmt.Errorf("%w: project %q; the owner has made no project", storeerr.ErrNotFound, named)
	}

	return Project{}, fmt.Errorf("%w: project %q; the projects are %s", storeerr.ErrNotFound, named,
		strings.Join(names, ", "))
}

// sameName refuses a name another project has, in any case.
func sameName(ctx context.Context, q *store.Queries, id, name string) error {
	rows, err := q.ListInboxProjects(ctx)
	if err != nil {
		return fmt.Errorf("listing the inbox's projects: %w", err)
	}

	for _, r := range rows {
		if r.ID != id && strings.EqualFold(r.Name, name) {
			return fmt.Errorf("%w: a project is named %s already", storeerr.ErrConflict, r.Name)
		}
	}

	return nil
}

// CreateProject adds a project; the owner does, which the caller checks.
func (s *Store) CreateProject(ctx context.Context, name, color string) (Project, error) {
	name, color = strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(color))
	if err := checkProject(name, color); err != nil {
		return Project{}, err
	}

	var made Project

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		if err := sameName(ctx, q, "", name); err != nil {
			return err
		}

		id := s.newID("prj")
		if err := q.InsertInboxProject(ctx, store.InsertInboxProjectParams{ID: id, Name: name, Color: color,
			Now: now}); err != nil {
			return fmt.Errorf("adding the project: %w", err)
		}

		row, err := q.GetInboxProject(ctx, id)
		if err != nil {
			return fmt.Errorf("reading the project added: %w", err)
		}

		made = projectOf(row)

		return nil
	})

	return made, err
}

// EditProject renames a project or changes its colour; nil leaves it as it is. Its items keep it.
func (s *Store) EditProject(ctx context.Context, id string, name, color *string) (Project, error) {
	var got Project

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		row, err := q.GetInboxProject(ctx, id)
		if err != nil {
			return storeutil.NotFound(err, "project", id)
		}

		if name != nil {
			row.Name = strings.TrimSpace(*name)
		}

		if color != nil {
			row.Color = strings.ToLower(strings.TrimSpace(*color))
		}

		if err := checkProject(row.Name, row.Color); err != nil {
			return err
		}

		if err := sameName(ctx, q, id, row.Name); err != nil {
			return err
		}

		if _, err := q.UpdateInboxProject(ctx, store.UpdateInboxProjectParams{Name: row.Name, Color: row.Color,
			Now: now, ID: id}); err != nil {
			return fmt.Errorf("changing project %s: %w", id, err)
		}

		row, err = q.GetInboxProject(ctx, id)
		if err != nil {
			return fmt.Errorf("reading project %s: %w", id, err)
		}

		got = projectOf(row)

		return nil
	})

	return got, err
}

// DeleteProject removes a project, as the actor (the owner): every item of it, in any state, is left without one,
// each with an edited event that names the project it left, in the removal's transaction, so that no item names a
// project that is not there and its history says why it has none.
func (s *Store) DeleteProject(ctx context.Context, actor Actor, id string) error {
	if err := checkActor(actor); err != nil {
		return err
	}

	return s.write(ctx, func(q *store.Queries, now int64) error {
		p, err := q.GetInboxProject(ctx, id)
		if err != nil {
			return storeutil.NotFound(err, "project", id)
		}

		rows, err := q.ListTodosOfProject(ctx, nullString(id))
		if err != nil {
			return fmt.Errorf("reading the to-dos of project %s: %w", id, err)
		}

		for _, cur := range rows {
			n, err := q.ClearTodoProject(ctx, store.ClearTodoProjectParams{Now: now, ID: cur.ID, Revision: cur.Revision})
			if err != nil {
				return fmt.Errorf("taking to-do %s out of project %s: %w", cur.ID, id, err)
			}

			if n != 1 {
				return fmt.Errorf("%w: to-do %s changed meanwhile", storeerr.ErrConflict, cur.ID)
			}

			if err := addEvent(ctx, q, cur.ID, now, cur.Revision+1, actor, EventEdited,
				map[string]any{"project_id": id}, map[string]any{
					"project_id": nil, "reason": "Project " + p.Name + " was removed",
				}); err != nil {
				return err
			}
		}

		if _, err := q.DeleteInboxProject(ctx, id); err != nil {
			return fmt.Errorf("removing project %s: %w", id, err)
		}

		return nil
	})
}

// checkProjectOf checks that the project an item is put in is there; none is always.
func checkProjectOf(ctx context.Context, q *store.Queries, id string) error {
	if id == "" {
		return nil
	}

	if _, err := q.GetInboxProject(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: project %s", storeerr.ErrNotFound, id)
		}

		return fmt.Errorf("reading project %s: %w", id, err)
	}

	return nil
}
