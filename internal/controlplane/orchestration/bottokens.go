package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/routinestore"
)

// Bot tokens (issue #260): the key of one pi process, made when the
// coordinator's supervisor starts pi for a chat and ended when that pi ends,
// after OpenMausBot's broker token per process (server/bot-box/broker.ts:
// random, bound to the bot and the conversation, revoked when the process
// exits, kept in the server's memory). A Bot token may do what the
// coordinator token it is made under may do, limited to its chat and its
// Bot; the client API and the agent API read the chat and the Bot from it.
// It is kept in memory only, as its SHA-256, so a restart of the control
// plane ends every one, as no pi outlives it.

// botToken is what is kept of a Bot token.
type botToken struct {
	// coordinatorID is the coordinator token it is made under: Runs it
	// creates are that token's, so the next pi of the chat goes on with them.
	coordinatorID string
	chat          string
	bot           string
	// routine is the Routine whose script's run it is made for (issue #211);
	// chat is empty then. run is that run: the token works only while it is live (issue #1023).
	routine   string
	run       string
	createdAt time.Time
}

type botTokens struct {
	mu     sync.Mutex
	byHash map[string]botToken
}

func newBotTokens() *botTokens { return &botTokens{byHash: map[string]botToken{}} }

// AddBotToken makes the key of a pi starting for the chat as the Bot, under
// the coordinator token named coordinator, and returns its secret. The
// secret is shown only here.
func (s *Service) AddBotToken(ctx context.Context, coordinator, chatID, bot string) (string, error) {
	parent, err := s.coordinatorTokenNamed(ctx, coordinator)
	if err != nil {
		return "", err
	}

	chat, err := s.GetChat(ctx, chatID)
	if err != nil {
		return "", err
	}

	if bot == "" || chat.Bot != bot {
		return "", fmt.Errorf("%w: chat %s talks with bot %q, not %q", ErrInvalid, chatID, chat.Bot, bot)
	}

	return s.addBotToken(botToken{coordinatorID: parent.ID, chat: chatID, bot: bot})
}

// AddRoutineToken makes the key of a run starting of a Routine with a
// script, a script Routine or a prompt Routine's check (issues #211, #656), under the coordinator token named coordinator, and returns its
// secret. It is bound to the Routine's Bot and to no chat, as #211's
// comment of 2026-11-12 says: a script answers no chat. It ends when the run
// does. The secret is shown only here.
func (s *Service) AddRoutineToken(ctx context.Context, coordinator, routineID string) (string, error) {
	parent, err := s.coordinatorTokenNamed(ctx, coordinator)
	if err != nil {
		return "", err
	}

	r, err := s.routines.GetRoutine(ctx, routineID)
	if err != nil {
		return "", err
	}

	// Bound to the run that is starting, whose own copy of the Routine it acts by (issue #1023, reviews of #1037).
	run, err := routinestore.LiveRunTx(ctx, s.read(), routineID)

	switch {
	case err != nil:
		return "", err
	case run == nil:
		return "", fmt.Errorf("%w: no run of routine %s is live", ErrConflict, routineID)
	case run.Script == "":
		return "", fmt.Errorf("%w: run %s of routine %s runs no script", ErrInvalid, run.ID, routineID)
	}

	return s.addBotToken(botToken{coordinatorID: parent.ID, bot: r.Bot, routine: r.ID, run: run.ID})
}

// addBotToken keeps a new Bot token and returns its secret.
func (s *Service) addBotToken(b botToken) (string, error) {
	secret, err := bearertoken.Generate(bearertoken.Bot)
	if err != nil {
		return "", err
	}

	b.createdAt = s.clock.Now()

	s.botTokens.mu.Lock()
	s.botTokens.byHash[bearertoken.Hash(secret)] = b
	s.botTokens.mu.Unlock()

	return secret, nil
}

// endBotTokens ends every Bot token that ends says should, as what it was made for ends (issue #996): its chat or
// its Bot deleted, its coordinator token revoked. It is called after the commit that ends that.
func (s *Service) endBotTokens(ends func(botToken) bool) {
	s.botTokens.mu.Lock()
	defer s.botTokens.mu.Unlock()

	for hash, b := range s.botTokens.byHash {
		if ends(b) {
			delete(s.botTokens.byHash, hash)
		}
	}
}

// RevokeBotToken ends a Bot token. Ending one that was ended is no error.
func (s *Service) RevokeBotToken(secret string) {
	s.botTokens.mu.Lock()
	delete(s.botTokens.byHash, bearertoken.Hash(secret))
	s.botTokens.mu.Unlock()
}

// authenticateBotToken returns the token a Bot token's secret belongs to:
// its coordinator token, bound to the chat and the Bot. A coordinator token
// that was revoked ends the Bot tokens made under it.
func (s *Service) authenticateBotToken(ctx context.Context, secret string) (Token, error) {
	s.botTokens.mu.Lock()
	b, ok := s.botTokens.byHash[bearertoken.Hash(secret)]
	s.botTokens.mu.Unlock()

	if !ok {
		return Token{}, fmt.Errorf("%w: no running pi has this token", ErrNotFound)
	}

	parent, err := s.GetToken(ctx, b.coordinatorID)
	if err != nil {
		return Token{}, err
	}

	if parent.RevokedAt != nil {
		return Token{}, fmt.Errorf("%w: token %s is revoked", ErrNotFound, parent.ID)
	}

	// What it is bound to is read too (issue #996): a chat or a Bot deleted ends it at once, before the deletion
	// takes it out of memory, and one made from a read of the chat before its deletion does not outlive it.
	// Only a binding read as gone forgets the token; a read that failed, or a request canceled, keeps it.
	if err := s.botTokenBound(ctx, b); err != nil {
		if errors.Is(err, ErrNotFound) {
			s.botTokens.mu.Lock()
			delete(s.botTokens.byHash, bearertoken.Hash(secret))
			s.botTokens.mu.Unlock()
		}

		return Token{}, err
	}

	return Token{
		ID: parent.ID, Name: parent.Name, Kind: bearertoken.Bot, CreatedAt: b.createdAt, Chat: b.chat, Bot: b.bot,
		Routine: b.routine, RoutineRun: b.run,
	}, nil
}

// botTokenBound refuses a Bot token whose chat or Bot is gone, ErrNotFound: its chat must still be the Bot's, and a
// Routine's run's Bot must still be there. Another error is a read that failed, which says nothing of the binding.
func (s *Service) botTokenBound(ctx context.Context, b botToken) error {
	q := s.read()

	if b.chat != "" {
		chat, err := q.GetChat(ctx, b.chat)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: chat %s is gone", ErrNotFound, b.chat)
		}

		if err != nil {
			return fmt.Errorf("reading chat %s: %w", b.chat, err)
		}

		if chat.BotID != b.bot {
			return fmt.Errorf("%w: chat %s is no longer bot %s's", ErrNotFound, b.chat, b.bot)
		}

		return nil
	}

	// A Routine's run's token ends with its run: the run's end, in its own transaction, is the token's end, so no
	// token outlives the snapshot it acts by (issue #1023, reviews of #1037).
	if b.run != "" {
		run, err := q.GetRoutineRun(ctx, b.run)

		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: run %s is gone", ErrNotFound, b.run)
		case err != nil:
			return fmt.Errorf("reading run %s: %w", b.run, err)
		case run.State != string(routinestore.RunRunning) && run.State != string(routinestore.RunWaiting):
			return fmt.Errorf("%w: run %s ended", ErrNotFound, b.run)
		}
	}

	_, err := q.GetBot(ctx, b.bot)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: bot %s is gone", ErrNotFound, b.bot)
	}

	if err != nil {
		return fmt.Errorf("reading bot %s: %w", b.bot, err)
	}

	return nil
}

// coordinatorTokenNamed returns the coordinator token of that name, which
// must work.
func (s *Service) coordinatorTokenNamed(ctx context.Context, name string) (Token, error) {
	return coordinatorTokenNamed(ctx, s.read(), name)
}

func coordinatorTokenNamed(ctx context.Context, q *store.Queries, name string) (Token, error) {
	row, err := q.GetClientTokenByName(ctx, name)
	if err != nil {
		return Token{}, notFound(err, "coordinator token", name)
	}
	token := tokenFromRow(row)
	if token.Kind != bearertoken.Coordinator || token.RevokedAt != nil {
		return Token{}, fmt.Errorf("%w: token %q is not a working coordinator token", ErrInvalid, name)
	}

	return token, nil
}
