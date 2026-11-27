package inboxstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// Waiting on others (issue #1142, the owner's rules of 2026-11-25): an item the owner waits on a person for is
// given to that person, open, with why_open Waiting. A Bot adds one so (Create with WaitingOn), or moves there an item
// it added when a later message shows someone took the work, promised it to him, or was given it by him. The move
// is made without asking the owner (owner, 2026-11-25, issue comment 7160150821): no reference moves a listed item
// by itself (Omi and Hermes only propose it, backend/utils/task_intelligence/capture_policy.py:55-60,
// skills/productivity/meeting-action-items/SKILL.md:63), so it is bounded here: only the Bot's own item from what it
// read, the person's own message as evidence, and Take it back, the owner giving it to himself, which clears it
// (Otter's "assign it back to yourself", Chatwoot's "Assign to me").

// WaitOn is what moves an item to Waiting on others: whom and the message that shows it, and the due, which is Due
// when given, else DueIn counted from the move (the day to chase them), else the item's own.
type WaitOn struct {
	WaitingOn

	Due   *Due
	DueIn *DueIn
}

// WaitOn moves an item to Waiting on others: a Bot does, on an item it added from what it read of the owner's (one
// of FoundKinds), proposed or open and not snoozed, given to the owner, nobody known or a person (the work went
// from one person to another). The item is given to the person By, opens, says why_open Waiting, and loses its draft
// (kept in the event's before): the owner has nothing to send. The event's after holds waiting_on.
func (s *Store) WaitOn(ctx context.Context, c Change, w WaitOn) (Todo, error) {
	w.By = strings.TrimSpace(w.By)

	if c.Actor.Kind != Bot {
		return Todo{}, fmt.Errorf("%w: a Bot moves a to-do it added to Waiting on others; the owner gives it to "+
			"a person by editing it", storeerr.ErrForbidden)
	}

	if err := checkActor(c.Actor); err != nil {
		return Todo{}, err
	}

	var due *dueColumns

	switch {
	case w.Due != nil && w.DueIn != nil:
		return Todo{}, fmt.Errorf("%w: a due, or a due a while after the move, not both", storeerr.ErrInvalid)
	case w.Due != nil:
		d, err := dueRow(*w.Due)
		if err != nil {
			return Todo{}, err
		}

		due = &d
	case w.DueIn != nil:
		_, zoneErr := time.LoadLocation(w.DueIn.Zone)
		if w.DueIn.After <= 0 || zoneErr != nil || w.DueIn.Zone == "" || w.DueIn.Zone == "Local" {
			return Todo{}, fmt.Errorf("%w: a due a while after the move is later, in an IANA time zone",
				storeerr.ErrInvalid)
		}
	}

	var got Todo

	err := s.write(ctx, func(q *store.Queries, now int64) error {
		var err error

		got, err = s.changeTx(ctx, q, now, c, EventEdited,
			func(q *store.Queries, cur store.Todo, next *store.Todo, now int64) (map[string]any, error) {
				switch {
				case cur.CreatedByKind != Bot || cur.CreatedBy != c.Actor.ID:
					return nil, fmt.Errorf("%w: a Bot moves to Waiting on others only a to-do it added",
						storeerr.ErrForbidden)
				case cur.AssigneeKind != Me && cur.AssigneeKind != Unknown && cur.AssigneeKind != Person:
					return nil, fmt.Errorf("%w: to-do %s is given to a Bot", storeerr.ErrForbidden, cur.ID)
				case !Found(cur.SourceKind):
					return nil, fmt.Errorf("%w: only a to-do from what was read (%s) waits on a person, not one "+
						"from %s", storeerr.ErrInvalid, strings.Join(FoundKinds, ", "), cur.SourceKind)
				case cur.State != Proposed && cur.State != Open:
					return nil, fmt.Errorf("%w: a %s to-do does not move to Waiting on others", storeerr.ErrConflict,
						cur.State)
				case cur.SnoozedUntil.Valid && cur.SnoozedUntil.Int64 > now:
					return nil, fmt.Errorf("%w: the owner snoozed to-do %s", storeerr.ErrConflict, cur.ID)
				}

				if err := checkWaitingOn(w.WaitingOn, cur.SourceKind, cur.SourceID); err != nil {
					return nil, err
				}

				if err := notABot(ctx, q, w.By); err != nil {
					return nil, err
				}

				// An item without a department takes its chat's in the move's own write, as a Bot's Edit does (issue
				// #1206, review round 5 of PR #1243); one with a department keeps it.
				if !next.ProjectID.Valid {
					src := Source{Kind: cur.SourceKind, ID: cur.SourceID, Account: cur.SourceAccount}

					project, err := chatDepartment(ctx, q, src)
					if err != nil {
						return nil, err
					}

					next.ProjectID = project
				}

				next.AssigneeKind, next.Assignee = Person, w.By
				next.State = Open
				next.WhyOpen = nullString(Waiting)
				next.Draft = ""

				set := due
				if w.DueIn != nil {
					d, err := dueRow(Due{Kind: DueInstant, At: time.UnixMilli(now).Add(w.DueIn.After),
						Zone: w.DueIn.Zone})
					if err != nil {
						return nil, err
					}

					set = &d
				}

				if set != nil {
					next.DueKind, next.DueDate, next.DueZone = set.kind, set.date, set.zone
					next.DueAt, next.OverdueAt = set.at, set.overdueAt
				}

				return map[string]any{"waiting_on": w.WaitingOn}, nil
			})

		return err
	})
	if err != nil {
		return Todo{}, err
	}

	return got, nil
}
