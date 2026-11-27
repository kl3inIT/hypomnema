package nodeapi

import (
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// partsSaid records whether a node tells its worker every decision of a question (issue #1219).
func (s *Server) partsSaid(nodeID string, parts bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !parts {
		delete(s.questionParts, nodeID)

		return
	}

	s.questionParts[nodeID] = true
}

// appSaid records whether a node gives its workers fresh tokens of the owner's GitHub App (issue #1262).
func (s *Server) appSaid(nodeID string, app bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !app {
		delete(s.githubApp, nodeID)

		return
	}

	s.githubApp[nodeID] = true
}

// takesAppTokens says whether a node said at its last hello that it gives its workers fresh App tokens.
func (s *Server) takesAppTokens(nodeID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.githubApp[nodeID]
}

// acceptedFor is an accept or reattach's answer as the node is sent it: its resolved Gate as questionFor gives it.
func (s *Server) acceptedFor(nodeID string, acc orchestration.Accepted) orchestration.Accepted {
	if acc.Gate != nil {
		gate := s.questionFor(nodeID, *acc.Gate)
		acc.Gate = &gate
	}

	return acc
}

// questionFor is an answered question as a node is sent it. A node of a release before issue #1219, which did not
// say at hello that it reads several decisions, tells its worker only a question's text and its one answer: a
// question of several decisions is folded into one for it, every decision in the text and every answer in the
// answer's text, so none is lost. A node that said so, and a question of one decision, are sent as they are. The
// capability is said at hello and the sender adapts to it, as the node's StoreGuides did (issue #536, until
// issue #1220) and as MCP's initialize capabilities are.
func (s *Server) questionFor(nodeID string, q orchestration.Question) orchestration.Question {
	s.mu.Lock()
	parts := s.questionParts[nodeID]
	s.mu.Unlock()

	if parts || len(q.More) == 0 {
		return q
	}

	return folded(q)
}

// folded is a question of several decisions as one: "1. Header: text" a line in its text, and its answer's text
// "1. Header: the chosen option and its text" a line, with no option to pick.
func folded(q orchestration.Question) orchestration.Question {
	decisions := append([]orchestration.Part{{Header: q.Header, Text: q.Text, Options: q.Options}}, q.More...)

	var choices []orchestration.Choice
	if q.Answer != nil {
		choices = append([]orchestration.Choice{{Option: q.Answer.Option, Text: q.Answer.Text}}, q.Answer.More...)
	}

	asked := make([]string, 0, len(decisions))
	answers := make([]string, 0, len(decisions))

	for i, d := range decisions {
		header := ""
		if d.Header != "" {
			header = d.Header + ": "
		}

		asked = append(asked, fmt.Sprintf("%d. %s%s", i+1, header, d.Text))

		if i < len(choices) {
			answers = append(answers, fmt.Sprintf("%d. %s%s", i+1, header, chosen(d.Options, choices[i])))
		}
	}

	out := q
	out.Header, out.Text, out.Options, out.More = "", strings.Join(asked, "\n"), []orchestration.Option{}, nil

	if q.Answer != nil {
		out.Answer = &orchestration.Answer{Text: strings.Join(answers, "\n")}
	}

	return out
}

// chosen is one decision's answer in words: the option's label, then its text.
func chosen(options []orchestration.Option, c orchestration.Choice) string {
	label := ""
	if c.Option != nil && *c.Option >= 0 && *c.Option < len(options) {
		label = options[*c.Option].Label
	}

	return strings.TrimSpace(label + " " + c.Text)
}
