package clientapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// Routines (issue #211): work a Bot does on a schedule. Every token reads
// them. The owner makes, changes, pauses, runs and removes one; a Bot, with
// its pi's token (issue #260), makes or changes one of its own from its chat
// and runs it once, as OpenMausBot's bot does through its tool and applies
// at once (server/routine-requests.ts L962-966). Pausing, removing, undoing
// a Bot's change and taking a grant back are the owner's (policy.go).

// Routines starts and stops the runs the owner asks for: the control
// plane's runner.
type Routines interface {
	// RunNow starts a run by hand; a conflict while another still goes.
	RunNow(ctx context.Context, id string) (orchestration.RoutineRun, error)
	// Remove removes a Routine and stops its live run.
	Remove(ctx context.Context, id string) error
	// Undo undoes a Bot's last change; removed says the Routine is gone.
	Undo(ctx context.Context, id string) (r orchestration.Routine, removed bool, err error)
}

// WithRoutines sets what runs the Routines.
func (s *Server) WithRoutines(r Routines) *Server {
	s.routines = r

	return s
}

var errNoRoutines = fmt.Errorf("%w: this control plane runs no routines", orchestration.ErrUnavailable)

func toRoutineRun(r orchestration.RoutineRun) openapi.RoutineRun {
	return openapi.RoutineRun{
		Id: r.ID, RoutineId: r.RoutineID, StartedBy: openapi.RoutineRunStartedBy(r.StartedBy),
		State: openapi.RoutineRunState(r.State), StartedAt: r.StartedAt, EndedAt: r.EndedAt, ExitCode: r.ExitCode,
		Output: r.Output, Error: r.Error, ChatId: opt(r.ChatID),
	}
}

// routine is a Routine as the API shows it: with its schedule in words, its
// last run and what the owner allowed it always.
func (s *Server) routine(ctx context.Context, r orchestration.Routine) (openapi.Routine, error) {
	out := openapi.Routine{
		Id: r.ID, Bot: r.Bot, Name: r.Name, Kind: openapi.RoutineKind(r.Kind), Prompt: opt(r.Prompt),
		Script: opt(r.Script), ScriptText: opt(r.ScriptText), EveryMinutes: ptr(r.EveryMinutes), Cron: opt(r.Cron),
		Timezone: opt(r.Timezone), Schedule: orchestration.ScheduleWords(r), Sentence: orchestration.RoutineSentence(r),
		TimeoutSeconds: int(r.Timeout.Seconds()), State: openapi.RoutineState(r.State),
		PausedReason: opt(r.PausedReason), Summary: opt(r.Summary), ChatId: opt(r.ChatID), Announce: r.Announce,
		NextRunAt: r.NextRunAt, Failures: r.Failures, Skipped: r.Skipped, LastSkippedAt: r.LastSkippedAt,
		CreatedBy: r.CreatedBy, CanUndo: r.CanUndo, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Grants: []openapi.RoutineGrant{},
	}

	runs, err := s.svc.ListRoutineRuns(ctx, r.ID, 1)
	if err != nil {
		return openapi.Routine{}, err
	}

	if len(runs) > 0 {
		last := toRoutineRun(runs[0])
		out.LastRun = &last
	}

	grants, err := s.svc.RoutineGrants(ctx, r.ID)
	if err != nil {
		return openapi.Routine{}, err
	}

	for _, g := range grants {
		out.Grants = append(out.Grants, openapi.RoutineGrant{
			Integration: g.Integration, Label: g.Label, Action: g.Action, ActionName: g.ActionName, CreatedAt: g.CreatedAt,
		})
	}

	return out, nil
}

// ListRoutines lists the Routines, each with its last run.
func (s *Server) ListRoutines(ctx context.Context, req openapi.ListRoutinesRequestObject,
) (openapi.ListRoutinesResponseObject, error) {
	all, err := s.svc.ListRoutines(ctx, orchestration.RoutineFilter{
		Bot: val(req.Params.Bot), ChatID: val(req.Params.ChatId),
	})
	if err != nil {
		return nil, err
	}

	out := openapi.ListRoutines200JSONResponse{Data: make([]openapi.Routine, 0, len(all))}

	for _, r := range all {
		shown, err := s.routine(ctx, r)
		if err != nil {
			return nil, err
		}

		out.Data = append(out.Data, shown)
	}

	return out, nil
}

// GetRoutine returns a Routine.
func (s *Server) GetRoutine(ctx context.Context, req openapi.GetRoutineRequestObject,
) (openapi.GetRoutineResponseObject, error) {
	r, err := s.svc.GetRoutine(ctx, req.RoutineId)
	if err != nil {
		return nil, err
	}

	shown, err := s.routine(ctx, r)
	if err != nil {
		return nil, err
	}

	return openapi.GetRoutine200JSONResponse(shown), nil
}

// PutRoutine makes a Routine of a Bot or changes the one of that name. A
// coordinator's token is a Bot's: its change is made from a chat the Bot
// talks in and can be undone; a Routine's own run is refused.
func (s *Server) PutRoutine(ctx context.Context, req openapi.PutRoutineRequestObject,
) (openapi.PutRoutineResponseObject, error) {
	b := req.Body
	tok := principal(ctx)

	// A pi's token makes the Routines of its own Bot, from its own chat.
	if err := botAllowed(tok, req.Name); err != nil {
		return nil, err
	}

	chat, err := chatOf(tok, val(b.ChatId))
	if err != nil {
		return nil, err
	}

	r, err := s.svc.PutRoutine(ctx, orchestration.RoutineSpec{
		Bot: req.Name, Name: req.Routine, Kind: orchestration.RoutineKind(b.Kind), Prompt: val(b.Prompt),
		Script: val(b.Script), ScriptText: val(b.ScriptText), EveryMinutes: val(b.EveryMinutes), Cron: val(b.Cron),
		Timezone: val(b.Timezone), TimeoutSeconds: val(b.TimeoutSeconds), Summary: val(b.Summary),
		ChatID: chat,
		// A run's result goes to the chat the Routine was made in unless the owner chose none.
		Announce: b.Announce == nil || *b.Announce,
	}, orchestration.RoutineAuthor{
		Bot: tok.Kind != bearertoken.Owner, FromRun: val(b.FromRoutine) != "" || tok.Routine != "",
	})
	if err != nil {
		return nil, err
	}

	shown, err := s.routine(ctx, r)
	if err != nil {
		return nil, err
	}

	return openapi.PutRoutine200JSONResponse(shown), nil
}

// UndoRoutine undoes a Bot's last change of a Routine.
func (s *Server) UndoRoutine(ctx context.Context, req openapi.UndoRoutineRequestObject,
) (openapi.UndoRoutineResponseObject, error) {
	if s.routines == nil {
		return nil, errNoRoutines
	}

	r, removed, err := s.routines.Undo(ctx, req.RoutineId)
	if err != nil {
		return nil, err
	}

	if removed {
		return openapi.UndoRoutine200JSONResponse{Removed: true}, nil
	}

	shown, err := s.routine(ctx, r)
	if err != nil {
		return nil, err
	}

	return openapi.UndoRoutine200JSONResponse{Routine: &shown}, nil
}

// LaunchRoutineScript gives the launcher of a script Routine's run what it
// runs and the run's key, once a run.
func (s *Server) LaunchRoutineScript(ctx context.Context, req openapi.LaunchRoutineScriptRequestObject,
) (openapi.LaunchRoutineScriptResponseObject, error) {
	// The launcher asks outside the box, with the coordinator's own token. A token of a box, a pi's or a
	// script's, is refused: a run's key would let it ask as that run (issue #260).
	if principal(ctx).Kind != bearertoken.Coordinator {
		return nil, fmt.Errorf("%w: only the launcher, with the coordinator's token, starts a routine's script",
			errForbidden)
	}

	launch, err := s.svc.ClaimRoutineScript(ctx, req.RoutineId, req.Body.Bot)
	if err != nil {
		return nil, err
	}

	// Who was given a run's key, and never the key.
	s.logger.InfoContext(ctx, "a routine's script was handed to its launcher",
		slog.String("event", "routines.launched"), slog.String("routine", req.RoutineId),
		slog.String("bot", req.Body.Bot), slog.String("token", principal(ctx).ID))

	return openapi.LaunchRoutineScript200JSONResponse{
		Script: launch.Script, ScriptText: launch.Text, TimeoutSeconds: int(launch.Timeout.Seconds()), Key: launch.Key,
	}, nil
}

// DeleteRoutine removes a Routine and stops its live run.
func (s *Server) DeleteRoutine(ctx context.Context, req openapi.DeleteRoutineRequestObject,
) (openapi.DeleteRoutineResponseObject, error) {
	if s.routines == nil {
		return nil, errNoRoutines
	}

	if err := s.routines.Remove(ctx, req.RoutineId); err != nil {
		return nil, err
	}

	return openapi.DeleteRoutine204Response{}, nil
}

// PauseRoutine stops a Routine's schedule.
func (s *Server) PauseRoutine(ctx context.Context, req openapi.PauseRoutineRequestObject,
) (openapi.PauseRoutineResponseObject, error) {
	r, err := s.svc.PauseRoutine(ctx, req.RoutineId)
	if err != nil {
		return nil, err
	}

	shown, err := s.routine(ctx, r)
	if err != nil {
		return nil, err
	}

	return openapi.PauseRoutine200JSONResponse(shown), nil
}

// ResumeRoutine switches a paused Routine on again.
func (s *Server) ResumeRoutine(ctx context.Context, req openapi.ResumeRoutineRequestObject,
) (openapi.ResumeRoutineResponseObject, error) {
	r, err := s.svc.ResumeRoutine(ctx, req.RoutineId)
	if err != nil {
		return nil, err
	}

	shown, err := s.routine(ctx, r)
	if err != nil {
		return nil, err
	}

	return openapi.ResumeRoutine200JSONResponse(shown), nil
}

// ListRoutineRuns lists a Routine's runs, the newest first.
func (s *Server) ListRoutineRuns(ctx context.Context, req openapi.ListRoutineRunsRequestObject,
) (openapi.ListRoutineRunsResponseObject, error) {
	offset := val(req.Params.Offset)

	runs, total, err := s.svc.RoutineRunPage(ctx, req.RoutineId, val(req.Params.Limit), offset)
	if err != nil {
		return nil, err
	}

	out := openapi.ListRoutineRuns200JSONResponse{Data: make([]openapi.RoutineRun, 0, len(runs)), Total: total}
	for _, r := range runs {
		out.Data = append(out.Data, toRoutineRun(r))
	}

	if next := max(offset, 0) + len(runs); len(runs) > 0 && next < total {
		out.NextOffset = &next
	}

	return out, nil
}

// PreviewRoutineSchedule says a schedule in words with its next times.
func (s *Server) PreviewRoutineSchedule(_ context.Context, req openapi.PreviewRoutineScheduleRequestObject,
) (openapi.PreviewRoutineScheduleResponseObject, error) {
	p, err := s.svc.PreviewSchedule(val(req.Body.EveryMinutes), val(req.Body.Cron), val(req.Body.Timezone))
	if err != nil {
		return nil, err
	}

	return openapi.PreviewRoutineSchedule200JSONResponse{Schedule: p.Words, Next: p.Next}, nil
}

// RunRoutine runs a Routine now.
func (s *Server) RunRoutine(ctx context.Context, req openapi.RunRoutineRequestObject,
) (openapi.RunRoutineResponseObject, error) {
	tok := principal(ctx)
	if tok.Routine != "" {
		return nil, fmt.Errorf("%w: a routine's run starts no routines", errForbidden)
	}

	if tok.Kind == bearertoken.Bot {
		r, err := s.svc.GetRoutine(ctx, req.RoutineId)
		if err != nil {
			return nil, err
		}

		if err := botAllowed(tok, r.Bot); err != nil {
			return nil, err
		}
	}

	if s.routines == nil {
		return nil, errNoRoutines
	}

	run, err := s.routines.RunNow(ctx, req.RoutineId)
	if err != nil {
		return nil, err
	}

	return openapi.RunRoutine200JSONResponse(toRoutineRun(run)), nil
}

// RevokeRoutineGrant takes back an "always for this Routine".
func (s *Server) RevokeRoutineGrant(ctx context.Context, req openapi.RevokeRoutineGrantRequestObject,
) (openapi.RevokeRoutineGrantResponseObject, error) {
	if _, err := s.svc.GetRoutine(ctx, req.RoutineId); err != nil {
		return nil, err
	}

	err := s.svc.RevokeRoutineGrant(ctx, req.RoutineId, req.Integration, req.Label, req.Action)
	if err != nil && !errors.Is(err, orchestration.ErrNotFound) {
		return nil, err
	}

	// Taking back what is already gone is done.
	return openapi.RevokeRoutineGrant204Response{}, nil
}
