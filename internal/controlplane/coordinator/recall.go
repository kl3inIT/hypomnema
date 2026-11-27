package coordinator

import (
	"context"
	"log/slog"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The recall before a turn the owner starts (issue #805, row 4), after OpenMausBot's autoRecallPrompt
// (server/index.ts:1155-1161, 8384-8388): a turn of his own words, a message he wrote in the Bot's chat, an Inbox item
// he handed over with a prompt or his comment on one, is given with the passages of the Bot's other chats that share
// its words in front of it, fenced as the Bot's own notes. It rides in front of the turn's text, never in the system
// prompt, and is not kept in the chat. A turn of no words of his (a Run's wake-up, a Routine's run, a Bot that passed
// an item, the line that continues a cut turn) recalls nothing, as OpenMausBot recalls for no routine run, webhook
// or other bot; orchestration's Recall leaves out a Routine's chat and a Lark chat.

// recallText is the owner's words a turn of one kind holds: his messages, or those its wake-ups carry.
func recallText(msgs []orchestration.ChatMessage) string {
	var words []string

	for _, m := range msgs {
		switch {
		case m.Role == orchestration.ChatOwner && m.Sender == nil:
			words = append(words, m.Text)
		case m.Role == orchestration.ChatSystem && m.Recall != "":
			words = append(words, m.Recall)
		}
	}

	return strings.Join(words, "\n\n")
}

// turnRecall is what a chat's turn recalled, kept until pi reads the turn: a turn pi refused or queued is given again
// with the same recall, not one made again from chats that changed meanwhile (issue #1218, row 5).
type turnRecall struct {
	seqs  []int64
	block string
}

// withRecall is the turn's text with what it recalls in front, made once for the turn of rows seqs and given again
// only while the Bot's switches still give it chat_search. A recall that fails is logged and the turn goes without
// it, as OpenMausBot's buildRecall gives nothing on an error.
func (s *Supervisor) withRecall(ctx context.Context, chatID string, msgs []orchestration.ChatMessage, seqs []int64,
	text string,
) string {
	words := recallText(msgs)
	if words == "" {
		return text
	}

	s.mu.Lock()
	kept, ok := s.recalls[chatID]
	s.mu.Unlock()

	block := kept.block

	var err error

	if ok && slices.Equal(kept.seqs, seqs) {
		// The Bot's switches are read again: a recall kept from before the owner switched chats off is not given.
		var on bool
		if on, err = s.svc.Recalls(ctx, chatID); err == nil && !on {
			block = ""
		}
	} else {
		block, err = s.svc.Recall(ctx, chatID, words)
		if err == nil {
			s.mu.Lock()
			s.recalls[chatID] = turnRecall{seqs: slices.Clone(seqs), block: block}
			s.mu.Unlock()
		}
	}

	if err != nil {
		s.cfg.Logger.WarnContext(ctx, "recalling the Bot's earlier chats failed", slog.String("chat_id", chatID),
			slog.String("event", "coordinator.recall_failed"), slog.String("error", err.Error()))

		return text
	}

	if block == "" {
		return text
	}

	return block + "\n\n" + text
}

// recallRead forgets a chat's kept recall once pi has read its turn.
func (s *Supervisor) recallRead(chatID string) {
	s.mu.Lock()
	delete(s.recalls, chatID)
	s.mu.Unlock()
}
