package clientcli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// answer answers a question as the token's holder.
func (o *owner) answer(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return ErrUsage
	}

	q, err := o.api.GetQuestionWithResponse(ctx, args[0])
	if err != nil {
		return fmt.Errorf("reading the question: %w", err)
	}

	if q.JSON200 == nil {
		return apiError("reading the question", q.StatusCode(), q.JSONDefault)
	}

	answered, err := o.api.AnswerQuestionWithResponse(ctx, q.JSON200.Id,
		ParseAnswer(q.JSON200.Options, strings.Join(args[1:], " ")))
	if err != nil {
		return fmt.Errorf("answering: %w", err)
	}

	if answered.JSON200 == nil {
		return apiError("answering", answered.StatusCode(), answered.JSONDefault)
	}

	o.printf("question %s is %s\n", answered.JSON200.Id, answered.JSON200.Status)

	return nil
}

// ParseAnswer reads what the owner typed: an option's number or its label
// (any case) picks it; any other text is the answer's text.
func ParseAnswer(options []openapi.Option, text string) openapi.Answer {
	pick := func(i int) openapi.Answer { return openapi.Answer{Option: &i} }

	if n, err := strconv.Atoi(text); err == nil {
		return pick(n - 1)
	}

	for i, opt := range options {
		if strings.EqualFold(strings.TrimSpace(text), opt.Label) {
			return pick(i)
		}
	}

	return openapi.Answer{Text: &text}
}

// questions lists the open questions addressed to the owner.
func (o *owner) questions(ctx context.Context) error {
	to, status := openapi.AddresseeOwner, openapi.QuestionStatusOpen

	resp, err := o.api.ListQuestionsWithResponse(ctx, &openapi.ListQuestionsParams{AddressedTo: &to, Status: &status})
	if err != nil {
		return fmt.Errorf("listing questions: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing questions", resp.StatusCode(), resp.JSONDefault)
	}

	for _, q := range resp.JSON200.Data {
		o.printQuestion(q)
	}

	return nil
}
