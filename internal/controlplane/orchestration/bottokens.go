package orchestration

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
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
	// chat is empty then.
	routine   string
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

// AddRoutineToken makes the key of a script Routine's run starting (issue
// #211), under the coordinator token named coordinator, and returns its
// secret. It is bound to the Routine's Bot and to no chat, as #211's
// comment of 2026-11-12 says: a script answers no chat. It ends when the run
// does. The secret is shown only here.
func (s *Service) AddRoutineToken(ctx context.Context, coordinator, routineID string) (string, error) {
	parent, err := s.coordinatorTokenNamed(ctx, coordinator)
	if err != nil {
		return "", err
	}

	r, err := s.GetRoutine(ctx, routineID)
	if err != nil {
		return "", err
	}

	if r.Kind != RoutineScript {
		return "", fmt.Errorf("%w: routine %s runs no script", ErrInvalid, routineID)
	}

	return s.addBotToken(botToken{coordinatorID: parent.ID, bot: r.Bot, routine: r.ID})
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

	return Token{
		ID: parent.ID, Name: parent.Name, Kind: bearertoken.Bot, CreatedAt: b.createdAt, Chat: b.chat, Bot: b.bot,
		Routine: b.routine,
	}, nil
}

// coordinatorTokenNamed returns the coordinator token of that name, which
// must work.
func (s *Service) coordinatorTokenNamed(ctx context.Context, name string) (Token, error) {
	tokens, err := s.ListTokens(ctx)
	if err != nil {
		return Token{}, err
	}

	for _, t := range tokens {
		if t.Name != name {
			continue
		}

		if t.Kind != bearertoken.Coordinator || t.RevokedAt != nil {
			return Token{}, fmt.Errorf("%w: token %q is not a working coordinator token", ErrInvalid, name)
		}

		return t, nil
	}

	return Token{}, fmt.Errorf("%w: no coordinator token is named %q", ErrNotFound, name)
}
