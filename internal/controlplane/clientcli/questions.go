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
	more := val(q.More)
	if len(more) == 0 {
		e.printf("  question %s (%s, to the %s): %s\n", q.Id, q.Mode, q.AddressedTo, titled(val(q.Header), q.Text))
		printOptions(e, q.Options)
		e.printf("  answer with: hypomnema answer %s <number, option or text>\n", q.Id)

		return
	}

	// A question of several decisions (issue #1219): each one with its options, answered one argument each.
	e.printf("  question %s (%s, to the %s), %d decisions:\n", q.Id, q.Mode, q.AddressedTo, len(more)+1)

	for i, p := range decisionsOf(q) {
		e.printf("   %d) %s\n", i+1, titled(val(p.Header), p.Text))
		printOptions(e, p.Options)
	}

	e.printf("  answer with: hypomnema answer %s <one quoted number, option or text for each decision>\n", q.Id)
}

// titled is a decision's text after its header, when it has one.
func titled(header, text string) string {
	if header == "" {
		return text
	}

	return "[" + header + "] " + text
}

func printOptions(e *env, options []openapi.Option) {
	for i, opt := range options {
		if d := val(opt.Description); d != "" {
			e.printf("    %d. %s: %s\n", i+1, opt.Label, d)

			continue
		}

		e.printf("    %d. %s\n", i+1, opt.Label)
	}
}

// decisionsOf is a question's decisions in order: the first, then the more.
func decisionsOf(q openapi.Question) []openapi.QuestionPart {
	return append([]openapi.QuestionPart{{Header: q.Header, Text: q.Text, Options: q.Options}}, val(q.More)...)
}

// questionsList lists questions; by default the open ones to the owner.
func questionsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	to, status := openapi.Addressee(str(fs, "to")), openapi.QuestionStatus(str(fs, "status"))

	params := &openapi.ListQuestionsParams{
		AddressedTo: &to, Status: &status, RunId: optional(fs, "run"), ChatId: optional(fs, "chat"),
	}

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

	var payload openapi.Answer
	if len(val(q.More)) == 0 {
		payload, err = ParseAnswer(q.Options, text)
	} else {
		payload, err = parseAnswers(q, fs.Args()[1:])
	}

	if err != nil {
		return err
	}

	resp, err := e.api.AnswerQuestionWithResponse(ctx, q.Id, payload)
	if err != nil {
		return fmt.Errorf("answering: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("answering", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("question %s is %s\n", resp.JSON200.Id, resp.JSON200.Status) })
}

// parseAnswers reads one argument for each decision of a question that asks
// several (issue #1219), each as ParseAnswer reads one.
func parseAnswers(q openapi.Question, args []string) (openapi.Answer, error) {
	decisions := decisionsOf(q)
	if len(args) != len(decisions) {
		return openapi.Answer{}, fmt.Errorf("%w: this question asks %d decisions: give one quoted answer for each",
			ErrUsage, len(decisions))
	}

	var out openapi.Answer

	for i, typed := range args {
		a, err := ParseAnswer(decisions[i].Options, typed)
		if err != nil {
			return openapi.Answer{}, fmt.Errorf("decision %d: %w", i+1, err)
		}

		if i == 0 {
			out.Option, out.Text = a.Option, a.Text

			continue
		}

		out.More = new(append(val(out.More), openapi.Choice{Option: a.Option, Text: a.Text}))
	}

	return out, nil
}

// ParseAnswer accepts an option number or label, or sends free text through
// the first option that allows it. Questions without options accept text alone.
func ParseAnswer(options []openapi.Option, text string) (openapi.Answer, error) {
	if len(options) == 0 {
		return openapi.Answer{Text: &text}, nil
	}

	typed := strings.TrimSpace(text)

	if n, err := strconv.Atoi(typed); err == nil {
		return openapi.Answer{Option: new(n - 1)}, nil
	}

	for i, opt := range options {
		if strings.EqualFold(typed, opt.Label) {
			return openapi.Answer{Option: &i}, nil
		}
	}

	for i, opt := range options {
		if val(opt.AllowsText) {
			return openapi.Answer{Option: &i, Text: &text}, nil
		}
	}

	var choices strings.Builder
	for i, opt := range options {
		fmt.Fprintf(&choices, "\n  %d. %s", i+1, opt.Label)
	}

	return openapi.Answer{}, fmt.Errorf("%w: pick one of the following options:%s", ErrUsage, choices.String())
}
