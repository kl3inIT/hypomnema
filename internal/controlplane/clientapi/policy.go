package clientapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Who may call what (issue #41, section 3). Every token reads everything and
// may create a Run; a coordinator token creates the Runs it coordinates. A
// change to a Run, its Tasks or its questions needs the owner or the Run's
// coordinator. Omnara keeps one policy per operationId the same way
// (openapi_policies.go).

// scope says which Run an operation changes.
type scope int

const (
	// scopeNone: reads, and creating a Run.
	scopeNone scope = iota
	scopeRun
	scopeTask
	scopeQuestion
	// scopeOwner: only the owner's tokens.
	scopeOwner
)

type policy struct {
	scope scope
	// long: the request may outlive the server's write timeout.
	long bool
}

var policies = map[string]policy{
	"GetMe":               {},
	"ListRuns":            {},
	"CreateRun":           {},
	"GetRun":              {},
	"FinishRun":           {scope: scopeRun},
	"CancelRun":           {scope: scopeRun},
	"CreateTasks":         {scope: scopeRun},
	"GetTask":             {},
	"CancelTask":          {scope: scopeTask},
	"ReopenTask":          {scope: scopeTask},
	"ListQuestions":       {},
	"GetQuestion":         {},
	"AnswerQuestion":      {scope: scopeQuestion},
	"ForwardQuestion":     {scope: scopeQuestion},
	"ListEvents":          {long: true},
	"StreamEvents":        {long: true},
	"ListNodes":           {},
	"ListAccounts":        {},
	"ListProfiles":        {},
	"GetProfile":          {},
	"ListProfileVersions": {},
	"ApplyProfile":        {scope: scopeOwner},
	"ListProviders":       {},
	"ApplyProvider":       {scope: scopeOwner},
	"ListModels":          {},
}

var (
	errUnauthorized = errors.New("a valid bearer token is required")
	errForbidden    = errors.New("forbidden")
)

// checkPolicy refuses a coordinator token a change to a Run it does not
// coordinate. The owner may change anything.
func (s *Server) checkPolicy(ctx context.Context, p policy, tok orchestration.Token, request any) error {
	if p.scope == scopeNone || tok.Kind == bearertoken.Owner {
		return nil
	}

	if p.scope == scopeOwner {
		return fmt.Errorf("%w: only the owner changes profiles and providers", errForbidden)
	}

	runID, err := s.runOf(ctx, p.scope, request)
	if err != nil {
		return err
	}

	run, err := s.svc.GetRun(ctx, runID)
	if err != nil {
		return err
	}

	if run.CoordinatorTokenID != tok.ID {
		return fmt.Errorf("%w: run %s is coordinated by another token or by the owner", errForbidden, runID)
	}

	return nil
}

// runOf returns the Run a scoped request changes.
func (s *Server) runOf(ctx context.Context, sc scope, request any) (string, error) {
	switch r := request.(type) {
	case openapi.FinishRunRequestObject:
		return r.RunId, nil
	case openapi.CancelRunRequestObject:
		return r.RunId, nil
	case openapi.CreateTasksRequestObject:
		return r.RunId, nil
	case openapi.CancelTaskRequestObject:
		return s.runOfTask(ctx, r.TaskId)
	case openapi.ReopenTaskRequestObject:
		return s.runOfTask(ctx, r.TaskId)
	case openapi.AnswerQuestionRequestObject:
		return s.runOfQuestion(ctx, r.QuestionId)
	case openapi.ForwardQuestionRequestObject:
		return s.runOfQuestion(ctx, r.QuestionId)
	default:
		return "", fmt.Errorf("scope %d: no run for a %T", sc, request)
	}
}

func (s *Server) runOfTask(ctx context.Context, id string) (string, error) {
	t, err := s.svc.GetTask(ctx, id)

	return t.RunID, err
}

func (s *Server) runOfQuestion(ctx context.Context, id string) (string, error) {
	q, err := s.svc.GetQuestion(ctx, id)

	return q.RunID, err
}
