package inboxstore

import (
	"database/sql"
	"fmt"

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
