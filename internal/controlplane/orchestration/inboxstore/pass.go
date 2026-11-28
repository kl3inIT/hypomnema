package inboxstore

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// Passing an item from Bot to Bot (issue #1118): the owner hands an item to the main Bot, which does it, starts a
// Run, or passes it to the project Bot whose accounts and context the work needs; that Bot gives it back to the main
// Bot or to the owner. As Paperclip's manager agent hands an issue down to the agents that report to it
// (server/src/services/agent-assignability.ts checks the chain an assignment walks), only the main Bot passes an item
// down, and a project Bot passes it only back up, so an item never goes round between project Bots. The owner gives
// an item to any Bot, as before (Hand, Edit).

// WithMainBot names the main Bot, the one Bot that passes an item to another Bot (issue #1118); without it no Bot
// passes one.
func (s *Store) WithMainBot(id string) *Store {
	s.mainBot = id

	return s
}

// mayPass says whether a Bot may give an item to the Bot to: the main Bot gives it to any Bot, and every Bot gives
// it back to the main Bot.
func (s *Store) mayPass(actor Actor, to Assignee) error {
	if actor.Kind != Bot || to.Kind != ABot || (s.mainBot != "" && (actor.ID == s.mainBot || to.Name == s.mainBot)) {
		return nil
	}

	return fmt.Errorf("%w: only the main Bot passes a to-do to another Bot; a Bot gives it back to the owner or the "+
		"main Bot", storeerr.ErrForbidden)
}

// mayGive says whether actor may add an item given to a: a Bot adds one for itself, the owner, a person or nobody
// known as before, and for another Bot only as it may pass one (mayPass).
func (s *Store) mayGive(actor Actor, a Assignee) error {
	if actor.Kind == Bot && a.Kind == ABot && a.Name == actor.ID {
		return nil
	}

	return s.mayPass(actor, a)
}

// passedBy is the Bot an item given to a passed through: the Bot that gives it to another Bot, else none.
func passedBy(actor Actor, a Assignee) sql.NullString {
	if actor.Kind == Bot && a.Kind == ABot && a.Name != actor.ID {
		return nullString(actor.ID)
	}

	return sql.NullString{}
}

// notPassed checks the main Bot's reason it passes an item to no project Bot (issue #1334): a to-do the main Bot
// cannot place is shown as not passed, with its reason, as Paperclip posts a visible "unresolved routing" comment when
// no route matches rather than dropping the import silently (docs/specs/external-task-protocol.md, section 8.1). Only
// the main Bot gives one, on an item given to it, and not in the edit that gives the item away.
func (s *Store) notPassed(c Change, cur store.Todo, e Edit) (string, error) {
	reason := strings.TrimSpace(*e.NotPassed)

	switch {
	case s.mainBot == "" || c.Actor.Kind != Bot || c.Actor.ID != s.mainBot:
		return "", fmt.Errorf("%w: only the main Bot says why it passes a to-do to no Bot", storeerr.ErrForbidden)
	case cur.AssigneeKind != ABot || cur.Assignee != s.mainBot:
		return "", fmt.Errorf("%w: to-do %s is not the main Bot's; it says why it passes only an item it has",
			storeerr.ErrConflict, cur.ID)
	case e.Assignee != nil:
		return "", fmt.Errorf("%w: not_passed keeps the item; it is not given with pass_to or hand_back",
			storeerr.ErrInvalid)
	}

	if err := checkReason(reason, true); err != nil {
		return "", err
	}

	return reason, nil
}
