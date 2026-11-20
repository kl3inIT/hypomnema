package orchestration

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
)

// A department is a named section of the sidebar that lists Bots together (issue #471), as OpenMausBot's
// departments (Apache License 2.0, https://github.com/milind-soni/OpenMausBot; server/routes/sidebar-sections.ts,
// server/store.ts renameSection and deleteSection, server/section-context.ts). A Bot is in at most one; the main
// Bot, always first, is in none. A department shares nothing between its Bots: no memory, files or Connections.
// The owner alone makes, names, orders and removes them, as he orders the Bots (issue #470).

// maxDepartmentName is OpenMausBot's limit on a department's name (serverError.department.nameRule).
const maxDepartmentName = 60

// Department is one section of the sidebar.
type Department struct {
	ID   string
	Name string
	// Position is its place among the departments, 0 first.
	Position int
}

func departmentFromRow(r store.Department) Department {
	return Department{ID: r.ID, Name: r.Name, Position: int(r.Position)}
}

// departmentName is a department's name as OpenMausBot checks it: trimmed, 1 to 60 characters, no control character.
func departmentName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxDepartmentName || strings.ContainsFunc(name, unicode.IsControl) {
		return "", fmt.Errorf("%w: a department's name is 1 to %d characters without control characters",
			ErrInvalid, maxDepartmentName)
	}

	return name, nil
}

// ListDepartments is every department, in the order the owner set.
func (s *Service) ListDepartments(ctx context.Context) ([]Department, error) {
	rows, err := s.read().ListDepartments(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing departments: %w", err)
	}

	out := make([]Department, len(rows))
	for i, r := range rows {
		out[i] = departmentFromRow(r)
	}

	return out, nil
}

// CreateDepartment makes a department, last in the order, and moves the Bots named into it, as OpenMausBot's "New
// department…" on a Bot's menu makes one with that Bot in it. A name another department has is refused.
func (s *Service) CreateDepartment(ctx context.Context, name string, bots []string) (Department, error) {
	name, err := departmentName(name)
	if err != nil {
		return Department{}, err
	}

	id := s.newID("dep")

	err = s.write(ctx, func(q *store.Queries, now int64) error {
		if err := q.InsertDepartment(ctx, store.InsertDepartmentParams{ID: id, Name: name, Now: now}); err != nil {
			return fmt.Errorf("%w: another department is named %q: %w", ErrConflict, name, err)
		}

		for _, bot := range bots {
			if err := setBotDepartment(ctx, q, bot, id); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return Department{}, fmt.Errorf("making department %q: %w", name, err)
	}

	return s.getDepartment(ctx, id)
}

// RenameDepartment gives a department another name; its Bots stay in it. A name another department has is refused.
func (s *Service) RenameDepartment(ctx context.Context, id, name string) (Department, error) {
	name, err := departmentName(name)
	if err != nil {
		return Department{}, err
	}

	err = s.write(ctx, func(q *store.Queries, _ int64) error {
		n, err := q.RenameDepartment(ctx, store.RenameDepartmentParams{Name: name, ID: id})
		if err != nil {
			return fmt.Errorf("%w: another department is named %q: %w", ErrConflict, name, err)
		}

		if n != 1 {
			return fmt.Errorf("%w: department %s", ErrNotFound, id)
		}

		return nil
	})
	if err != nil {
		return Department{}, fmt.Errorf("renaming department %s: %w", id, err)
	}

	return s.getDepartment(ctx, id)
}

// DeleteDepartment removes a department; its Bots go back to the Bots in none, as OpenMausBot's deleteSection.
func (s *Service) DeleteDepartment(ctx context.Context, id string) error {
	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		row, err := q.GetDepartment(ctx, id)
		if err != nil {
			return notFound(err, "department", id)
		}

		if err := q.DeleteDepartment(ctx, id); err != nil {
			return err
		}

		return q.CloseDepartmentPosition(ctx, row.Position)
	})
	if err != nil {
		return fmt.Errorf("removing department %s: %w", id, err)
	}

	return nil
}

// ReorderDepartments moves the departments named and returns every department's id in the new order, as
// ReorderBots moves Bots: those named take, in the order given, the places they already hold together.
func (s *Service) ReorderDepartments(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: name the departments to move", ErrInvalid)
	}

	rank := make(map[string]int, len(ids))
	for i, id := range ids {
		if _, twice := rank[id]; twice {
			return nil, fmt.Errorf("%w: department %s is named twice", ErrInvalid, id)
		}

		rank[id] = i
	}

	var order []string

	err := s.write(ctx, func(q *store.Queries, _ int64) error {
		rows, err := q.ListDepartments(ctx)
		if err != nil {
			return err
		}

		had := make([]string, len(rows))
		for i, r := range rows {
			had[i] = r.ID
		}

		order, err = reorderStable(had, rank)
		if err != nil {
			return err
		}

		for i, id := range order {
			if err := q.SetDepartmentPosition(ctx, store.SetDepartmentPositionParams{Position: int64(i), ID: id}); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reordering departments: %w", err)
	}

	return order, nil
}

// SetBotDepartment moves a Bot into a department, or out of every one when department is empty. The main Bot is
// always first and in none.
func (s *Service) SetBotDepartment(ctx context.Context, bot, department string) (Bot, error) {
	if err := s.write(ctx, func(q *store.Queries, _ int64) error {
		return setBotDepartment(ctx, q, bot, department)
	}); err != nil {
		return Bot{}, fmt.Errorf("moving bot %s: %w", bot, err)
	}

	return s.GetBot(ctx, bot)
}

func setBotDepartment(ctx context.Context, q *store.Queries, bot, department string) error {
	if bot == DefaultBotID {
		return fmt.Errorf("%w: the main bot is always first, in no department", ErrInvalid)
	}

	if department != "" {
		if _, err := q.GetDepartment(ctx, department); err != nil {
			return notFound(err, "department", department)
		}
	}

	n, err := q.SetBotDepartment(ctx, store.SetBotDepartmentParams{Department: nullString(department), ID: bot})
	if err != nil {
		return err
	}

	if n != 1 {
		return fmt.Errorf("%w: bot %s", ErrNotFound, bot)
	}

	return nil
}

func (s *Service) getDepartment(ctx context.Context, id string) (Department, error) {
	row, err := s.read().GetDepartment(ctx, id)
	if err != nil {
		return Department{}, notFound(err, "department", id)
	}

	return departmentFromRow(row), nil
}
