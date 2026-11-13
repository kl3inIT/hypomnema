package clientapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/machines"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

var _ openapi.StrictServerInterface = (*Server)(nil)

// GetMe returns the calling token.
func (s *Server) GetMe(ctx context.Context, _ openapi.GetMeRequestObject) (openapi.GetMeResponseObject, error) {
	tok := principal(ctx)

	return openapi.GetMe200JSONResponse{Id: tok.ID, Name: tok.Name, Kind: openapi.TokenKind(tok.Kind)}, nil
}

// ListRuns returns a page of Runs, newest first.
func (s *Server) ListRuns(ctx context.Context, req openapi.ListRunsRequestObject,
) (openapi.ListRunsResponseObject, error) {
	cursor, err := decodeCursor(req.Params.Cursor)
	if err != nil {
		return nil, err
	}

	page, err := s.svc.ListRuns(ctx, orchestration.RunFilter{Status: orchestration.RunStatus(val(req.Params.Status))},
		cursor, val(req.Params.Limit))
	if err != nil {
		return nil, err
	}

	out := openapi.ListRuns200JSONResponse{
		Data: make([]openapi.Run, 0, len(page.Runs)), NextCursor: encodeCursor(page.Next),
	}
	for _, r := range page.Runs {
		out.Data = append(out.Data, toRun(r))
	}

	return out, nil
}

// CreateRun creates a Run. A coordinator token becomes its coordinator.
func (s *Server) CreateRun(ctx context.Context, req openapi.CreateRunRequestObject,
) (openapi.CreateRunResponseObject, error) {
	tok := principal(ctx)

	chat, err := chatOf(tok, val(req.Body.ChatId))
	if err != nil {
		return nil, err
	}

	nr := orchestration.NewRun{
		Objective: req.Body.Objective, CreatedBy: tok.Name, Repo: val(req.Body.Repo), Base: val(req.Body.Base),
		ChatID: chat, Uses: val(req.Body.Uses),
	}
	if tok.Kind == bearertoken.Coordinator || tok.Kind == bearertoken.Bot {
		nr.CoordinatorTokenID = tok.ID
	}

	run, err := s.svc.CreateRunOnce(ctx, once(tok, req.Params.IdempotencyKey), nr)
	if err != nil {
		return nil, err
	}

	return openapi.CreateRun201JSONResponse(toRun(run)), nil
}

func once(tok orchestration.Token, key *string) orchestration.Once {
	if key == nil {
		return orchestration.Once{}
	}

	return orchestration.Once{Caller: tok.ID, Key: *key}
}

// GetRun returns a Run with its Tasks and their latest Dispatches.
func (s *Server) GetRun(ctx context.Context, req openapi.GetRunRequestObject) (openapi.GetRunResponseObject, error) {
	run, err := s.svc.GetRun(ctx, req.RunId)
	if err != nil {
		return nil, err
	}

	tasks, err := s.svc.ListTasks(ctx, run.ID)
	if err != nil {
		return nil, err
	}

	r := toRun(run)
	out := openapi.GetRun200JSONResponse{
		Id: r.Id, Objective: r.Objective, Status: r.Status, CreatedBy: r.CreatedBy, Repo: r.Repo, Base: r.Base,
		CoordinatorTokenId: r.CoordinatorTokenId, ChatId: r.ChatId, Bot: r.Bot, CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt, FinishedAt: r.FinishedAt, Tasks: make([]openapi.TaskDetail, 0, len(tasks)),
	}

	for _, t := range tasks {
		detail, err := s.taskDetail(ctx, t)
		if err != nil {
			return nil, err
		}

		out.Tasks = append(out.Tasks, detail)
	}

	return out, nil
}

func (s *Server) taskDetail(ctx context.Context, t orchestration.Task) (openapi.TaskDetail, error) {
	d, err := s.svc.LatestDispatch(ctx, t.ID)
	if errors.Is(err, orchestration.ErrNotFound) {
		return toTaskDetail(t, nil), nil
	}

	if err != nil {
		return openapi.TaskDetail{}, err
	}

	return toTaskDetail(t, &d), nil
}

// FinishRun closes a Run whose Tasks have all ended.
func (s *Server) FinishRun(ctx context.Context, req openapi.FinishRunRequestObject,
) (openapi.FinishRunResponseObject, error) {
	if err := s.svc.FinishRun(ctx, req.RunId); err != nil {
		return nil, err
	}

	run, err := s.svc.GetRun(ctx, req.RunId)
	if err != nil {
		return nil, err
	}

	return openapi.FinishRun200JSONResponse(toRun(run)), nil
}

// CancelRun cancels a Run and its unfinished Tasks.
func (s *Server) CancelRun(ctx context.Context, req openapi.CancelRunRequestObject,
) (openapi.CancelRunResponseObject, error) {
	if err := s.svc.CancelRun(ctx, req.RunId); err != nil {
		return nil, err
	}

	run, err := s.svc.GetRun(ctx, req.RunId)
	if err != nil {
		return nil, err
	}

	return openapi.CancelRun200JSONResponse(toRun(run)), nil
}

// CreateTasks adds Tasks to a Run.
func (s *Server) CreateTasks(ctx context.Context, req openapi.CreateTasksRequestObject,
) (openapi.CreateTasksResponseObject, error) {
	in := make([]orchestration.NewTask, 0, len(req.Body.Tasks))
	for _, t := range req.Body.Tasks {
		in = append(in, fromNewTask(t))
	}

	tasks, err := s.svc.CreateTasksOnce(ctx, once(principal(ctx), req.Params.IdempotencyKey), req.RunId, in)
	if err != nil {
		return nil, err
	}

	out := openapi.CreateTasks201JSONResponse{Data: make([]openapi.Task, 0, len(tasks))}
	for _, t := range tasks {
		out.Data = append(out.Data, toTask(t))
	}

	return out, nil
}

// GetTask returns a Task with its latest Dispatch.
func (s *Server) GetTask(ctx context.Context, req openapi.GetTaskRequestObject) (openapi.GetTaskResponseObject, error) {
	t, err := s.svc.GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}

	detail, err := s.taskDetail(ctx, t)
	if err != nil {
		return nil, err
	}

	return openapi.GetTask200JSONResponse(detail), nil
}

// boardWindow is how far back a board looks for Tasks of Runs no longer open.
const boardWindow = 24 * time.Hour

// ListTasks returns the Tasks a board shows, with their latest Dispatch and
// their Runs.
func (s *Server) ListTasks(ctx context.Context, req openapi.ListTasksRequestObject,
) (openapi.ListTasksResponseObject, error) {
	since := time.Now().Add(-boardWindow)
	if req.Params.Since != nil {
		since = *req.Params.Since
	}

	// Read first, so an event between it and the snapshot is followed, not lost.
	after, err := s.svc.LastEventSeq(ctx)
	if err != nil {
		return nil, err
	}

	tasks, err := s.svc.ListBoardTasks(ctx, since, val(req.Params.Limit))
	if err != nil {
		return nil, err
	}

	out := openapi.ListTasks200JSONResponse{
		Data: make([]openapi.TaskDetail, 0, len(tasks)), Runs: []openapi.Run{}, After: after,
	}
	seen := map[string]bool{}

	for _, t := range tasks {
		detail, err := s.taskDetail(ctx, t)
		if err != nil {
			return nil, err
		}

		out.Data = append(out.Data, detail)

		if seen[t.RunID] {
			continue
		}

		seen[t.RunID] = true

		run, err := s.svc.GetRun(ctx, t.RunID)
		if err != nil {
			return nil, err
		}

		out.Runs = append(out.Runs, toRun(run))
	}

	return out, nil
}

// CancelTask cancels a Task.
func (s *Server) CancelTask(ctx context.Context, req openapi.CancelTaskRequestObject,
) (openapi.CancelTaskResponseObject, error) {
	if err := s.svc.CancelTask(ctx, req.TaskId); err != nil {
		return nil, err
	}

	t, err := s.svc.GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}

	return openapi.CancelTask200JSONResponse(toTask(t)), nil
}

// ReopenTask gives a failed Task a new start.
func (s *Server) ReopenTask(ctx context.Context, req openapi.ReopenTaskRequestObject,
) (openapi.ReopenTaskResponseObject, error) {
	if err := s.svc.ReopenTask(ctx, req.TaskId); err != nil {
		return nil, err
	}

	t, err := s.svc.GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, err
	}

	return openapi.ReopenTask200JSONResponse(toTask(t)), nil
}

// ListQuestions returns questions in the order they were asked.
func (s *Server) ListQuestions(ctx context.Context, req openapi.ListQuestionsRequestObject,
) (openapi.ListQuestionsResponseObject, error) {
	qs, err := s.svc.ListQuestions(ctx, orchestration.QuestionFilter{
		RunID: val(req.Params.RunId), TaskID: val(req.Params.TaskId),
		AddressedTo: orchestration.Addressee(val(req.Params.AddressedTo)),
		Status:      orchestration.QuestionStatus(val(req.Params.Status)),
	})
	if err != nil {
		return nil, err
	}

	out := openapi.ListQuestions200JSONResponse{Data: make([]openapi.Question, 0, len(qs))}
	for _, q := range qs {
		out.Data = append(out.Data, toQuestion(q))
	}

	return out, nil
}

// GetQuestion returns one question.
func (s *Server) GetQuestion(ctx context.Context, req openapi.GetQuestionRequestObject,
) (openapi.GetQuestionResponseObject, error) {
	q, err := s.svc.GetQuestion(ctx, req.QuestionId)
	if err != nil {
		return nil, err
	}

	return openapi.GetQuestion200JSONResponse(toQuestion(q)), nil
}

// AnswerQuestion answers as the owner or as the Run's coordinator, by the
// caller's token.
func (s *Server) AnswerQuestion(ctx context.Context, req openapi.AnswerQuestionRequestObject,
) (openapi.AnswerQuestionResponseObject, error) {
	by := orchestration.AddressOwner
	if k := principal(ctx).Kind; k == bearertoken.Coordinator || k == bearertoken.Bot {
		by = orchestration.AddressCoordinator
	}

	q, err := s.svc.AnswerQuestion(ctx, req.QuestionId, by,
		orchestration.Answer{Option: req.Body.Option, Text: val(req.Body.Text)})
	if err != nil {
		return nil, err
	}

	return openapi.AnswerQuestion200JSONResponse(toQuestion(q)), nil
}

// ForwardQuestion passes a coordinator's question to the owner.
func (s *Server) ForwardQuestion(ctx context.Context, req openapi.ForwardQuestionRequestObject,
) (openapi.ForwardQuestionResponseObject, error) {
	if err := s.svc.ForwardQuestion(ctx, req.QuestionId); err != nil {
		return nil, err
	}

	q, err := s.svc.GetQuestion(ctx, req.QuestionId)
	if err != nil {
		return nil, err
	}

	return openapi.ForwardQuestion200JSONResponse(toQuestion(q)), nil
}

// ListNodes returns the nodes.
func (s *Server) ListNodes(ctx context.Context, _ openapi.ListNodesRequestObject,
) (openapi.ListNodesResponseObject, error) {
	nodes, err := s.svc.ListNodes(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListNodes200JSONResponse{Data: make([]openapi.Node, 0, len(nodes))}
	for _, n := range nodes {
		out.Data = append(out.Data, toNode(n))
	}

	return out, nil
}

// CreateNode adds a node and answers with its first token, shown only here.
func (s *Server) CreateNode(ctx context.Context, req openapi.CreateNodeRequestObject,
) (openapi.CreateNodeResponseObject, error) {
	if provider := val(req.Body.Provider); provider != "" {
		return s.createMachine(ctx, provider, req.Body)
	}

	if val(req.Body.Region) != "" || val(req.Body.Size) != "" {
		return nil, fmt.Errorf("%w: a region or a size needs a provider", orchestration.ErrInvalid)
	}

	n, token, err := s.svc.AddNode(ctx, req.Body.Name, val(req.Body.MaxDispatches))
	if err != nil {
		return nil, err
	}

	return openapi.CreateNode201JSONResponse{Node: toNode(n), Token: &token}, nil
}

// createMachine asks a provider for the node's machine. When the provider
// refuses after the node was recorded, the node is answered as it is listed:
// failed, with the reason.
func (s *Server) createMachine(ctx context.Context, provider string, body *openapi.NewNode,
) (openapi.CreateNodeResponseObject, error) {
	if s.machines == nil {
		return nil, fmt.Errorf("%w: this control plane creates no machines", orchestration.ErrInvalid)
	}

	n, err := s.machines.Create(ctx, machines.NewMachine{
		Name: body.Name, Provider: provider, Region: val(body.Region), Size: val(body.Size),
		MaxDispatches: val(body.MaxDispatches),
	})
	if err != nil && n.Machine == nil {
		return nil, err
	}

	return openapi.CreateNode201JSONResponse{Node: toNode(n)}, nil
}

// StopNodeMachine turns a provider's machine off.
func (s *Server) StopNodeMachine(ctx context.Context, req openapi.StopNodeMachineRequestObject,
) (openapi.StopNodeMachineResponseObject, error) {
	n, err := s.power(ctx, req.NodeId, false)
	if err != nil {
		return nil, err
	}

	return openapi.StopNodeMachine200JSONResponse(toNode(n)), nil
}

// StartNodeMachine turns a stopped machine on.
func (s *Server) StartNodeMachine(ctx context.Context, req openapi.StartNodeMachineRequestObject,
) (openapi.StartNodeMachineResponseObject, error) {
	n, err := s.power(ctx, req.NodeId, true)
	if err != nil {
		return nil, err
	}

	return openapi.StartNodeMachine200JSONResponse(toNode(n)), nil
}

// power stops or starts a machine. When the change was recorded and the
// provider did not act yet, the node is answered as it is: between the two,
// with the reason.
func (s *Server) power(ctx context.Context, nodeID string, on bool) (orchestration.Node, error) {
	if s.machines == nil {
		return orchestration.Node{}, fmt.Errorf("%w: this control plane creates no machines", orchestration.ErrInvalid)
	}

	change := s.machines.Stop
	if on {
		change = s.machines.Start
	}

	n, err := change(ctx, nodeID)
	if err != nil && n.Machine == nil {
		return orchestration.Node{}, err
	}

	return n, nil
}

// ListMachineProviders returns the machine providers, which hold no secret,
// each with how many machines it has.
func (s *Server) ListMachineProviders(ctx context.Context, _ openapi.ListMachineProvidersRequestObject,
) (openapi.ListMachineProvidersResponseObject, error) {
	providers, err := s.svc.ListMachineProviders(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]openapi.MachineProvider, 0, len(providers))

	for _, p := range providers {
		nodes, err := s.svc.ListMachinesOfProvider(ctx, p.ID)
		if err != nil {
			return nil, err
		}

		out = append(out, openapi.MachineProvider{
			Id: p.ID, Name: p.Name, Kind: openapi.MachineProviderKind(p.Kind), Region: p.Region, Size: p.Size,
			MaxMachines: p.MaxMachines, Machines: len(nodes),
		})
	}

	return openapi.ListMachineProviders200JSONResponse{Data: out}, nil
}

// UpdateNode renames a node.
func (s *Server) UpdateNode(ctx context.Context, req openapi.UpdateNodeRequestObject,
) (openapi.UpdateNodeResponseObject, error) {
	n, err := s.svc.RenameNode(ctx, req.NodeId, req.Body.Name)
	if err != nil {
		return nil, err
	}

	return openapi.UpdateNode200JSONResponse(toNode(n)), nil
}

// RemoveNode takes a node out of the registry.
func (s *Server) RemoveNode(ctx context.Context, req openapi.RemoveNodeRequestObject,
) (openapi.RemoveNodeResponseObject, error) {
	remove := s.svc.RemoveNode
	if s.machines != nil {
		remove = s.machines.Remove
	}

	if err := remove(ctx, req.NodeId); err != nil {
		return nil, err
	}

	return openapi.RemoveNode204Response{}, nil
}

// DrainNode stops new work on a node.
func (s *Server) DrainNode(ctx context.Context, req openapi.DrainNodeRequestObject,
) (openapi.DrainNodeResponseObject, error) {
	if err := s.svc.DrainNode(ctx, req.NodeId); err != nil {
		return nil, err
	}

	n, err := s.svc.GetNode(ctx, req.NodeId)
	if err != nil {
		return nil, err
	}

	return openapi.DrainNode200JSONResponse(toNode(n)), nil
}

// ResumeNode lets a node take new work again.
func (s *Server) ResumeNode(ctx context.Context, req openapi.ResumeNodeRequestObject,
) (openapi.ResumeNodeResponseObject, error) {
	if err := s.svc.ResumeNode(ctx, req.NodeId); err != nil {
		return nil, err
	}

	n, err := s.svc.GetNode(ctx, req.NodeId)
	if err != nil {
		return nil, err
	}

	return openapi.ResumeNode200JSONResponse(toNode(n)), nil
}
