package clientcli

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

func getQuestion(ctx context.Context, e *env, id string) (openapi.Question, error) {
	resp, err := e.api.GetQuestionWithResponse(ctx, id)
	if err != nil {
		return openapi.Question{}, fmt.Errorf("reading question %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return openapi.Question{}, apiError("reading question "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return *resp.JSON200, nil
}

func printQuestion(e *env, q openapi.Question) {
	e.printf("  question %s (%s, to the %s): %s\n", q.Id, q.Mode, q.AddressedTo, q.Text)

	for i, opt := range q.Options {
		e.printf("    %d. %s\n", i+1, opt.Label)
	}

	e.printf("  answer with: hypomnema answer %s <number, option or text>\n", q.Id)
}

// questionsList lists questions; by default the open ones to the owner.
func questionsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	to, status := openapi.Addressee(str(fs, "to")), openapi.QuestionStatus(str(fs, "status"))

	params := &openapi.ListQuestionsParams{AddressedTo: &to, Status: &status, RunId: optional(fs, "run")}

	resp, err := e.api.ListQuestionsWithResponse(ctx, params)
	if err != nil {
		return fmt.Errorf("listing questions: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing questions", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, q := range resp.JSON200.Data {
			printQuestion(e, q)
		}
	})
}

func questionsShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	q, err := getQuestion(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	return e.print(fs, q, func() { printQuestion(e, q) })
}

func questionsForward(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ForwardQuestionWithResponse(ctx, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("forwarding the question: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("forwarding the question", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("question %s is now the owner's\n", resp.JSON200.Id) })
}

// answer answers a question as the token's holder: the owner, or the Run's
// coordinator.
func answer(ctx context.Context, e *env, fs *flag.FlagSet) error {
	text := strings.Join(fs.Args()[1:], " ")

	if str(fs, "text-file") != "" {
		var err error
		if text, err = e.text(fs, "text"); err != nil {
			return err
		}
	}

	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%w: hypomnema answer <question_id> <answer> or --text-file <file>", ErrUsage)
	}

	q, err := getQuestion(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	resp, err := e.api.AnswerQuestionWithResponse(ctx, q.Id, ParseAnswer(q.Options, text))
	if err != nil {
		return fmt.Errorf("answering: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("answering", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("question %s is %s\n", resp.JSON200.Id, resp.JSON200.Status) })
}

// ParseAnswer reads an answer as typed: an option's number or its label (any
// case) picks it; any other text is the answer's text.
func ParseAnswer(options []openapi.Option, text string) openapi.Answer {
	pick := func(i int) openapi.Answer { return openapi.Answer{Option: &i} }

	if n, err := strconv.Atoi(strings.TrimSpace(text)); err == nil && len(options) > 0 {
		return pick(n - 1)
	}

	for i, opt := range options {
		if strings.EqualFold(strings.TrimSpace(text), opt.Label) {
			return pick(i)
		}
	}

	return openapi.Answer{Text: &text}
}
