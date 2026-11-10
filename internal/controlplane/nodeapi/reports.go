package nodeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// errBadPayload marks a report whose payload does not decode; it can never apply.
var errBadPayload = errors.New("bad payload")

// apply runs one report as a node call and answers it.
func (s *Server) apply(ctx context.Context, nodeID string, r nodeproto.Report) nodeproto.ReportAck {
	call := orchestration.NodeCall{NodeID: nodeID, DispatchID: r.DispatchID, Epoch: r.Epoch, RequestID: r.ReportID}

	resp, err := s.call(ctx, call, r)
	ack := nodeproto.ReportAck{ReportID: r.ReportID, Status: nodeproto.AckCommitted}

	switch {
	case err == nil:
		if resp != nil {
			raw, encErr := json.Marshal(resp)
			if encErr != nil {
				return nodeproto.ReportAck{ReportID: r.ReportID, Status: nodeproto.AckTransient, Error: encErr.Error()}
			}

			ack.Response = raw
		}
	case permanent(err):
		ack.Status, ack.Error = nodeproto.AckRejected, err.Error()
	default:
		ack.Status, ack.Error = nodeproto.AckTransient, err.Error()
		s.logger.WarnContext(ctx, "report failed",
			slog.String("event", "nodeapi.report_failed"), slog.String("node_id", nodeID),
			slog.String("kind", r.Kind), slog.String("dispatch_id", r.DispatchID), slog.String("error", err.Error()))
	}

	return ack
}

// permanent reports whether an error means the report can never apply.
func permanent(err error) bool {
	for _, e := range []error{
		errBadPayload, orchestration.ErrInvalid, orchestration.ErrConflict, orchestration.ErrNotFound,
		orchestration.ErrCoolingDown, orchestration.ErrUnavailable,
	} {
		if errors.Is(err, e) {
			return true
		}
	}

	return false
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 {
		return v, nil
	}

	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("%w: %w", errBadPayload, err)
	}

	return v, nil
}

func (s *Server) call(ctx context.Context, call orchestration.NodeCall, r nodeproto.Report) (any, error) {
	svc := s.svc

	switch r.Kind {
	case nodeproto.ReportAccept:
		return svc.AcceptDispatch(ctx, call)
	case nodeproto.ReportHeartbeat:
		return svc.Heartbeat(ctx, call)
	case nodeproto.ReportProgress:
		return nil, svc.ReportProgress(ctx, call)
	case nodeproto.ReportExited:
		return nil, svc.ReportExited(ctx, call)
	case nodeproto.ReportStopped:
		return nil, svc.ReportStopped(ctx, call)
	case nodeproto.ReportStarted:
		p, err := decode[nodeproto.Started](r.Payload)
		if err != nil {
			return nil, err
		}

		return nil, svc.ReportStarted(ctx, call, p.ProcessRef, p.CLISessionRef)
	case nodeproto.ReportEffect:
		p, err := decode[nodeproto.Effect](r.Payload)
		if err != nil {
			return nil, err
		}

		return nil, svc.RecordEffect(ctx, call, orchestration.Effect{
			Seq: p.Seq, Kind: p.Kind, Ref: p.Ref, Phase: orchestration.EffectPhase(p.Phase), Payload: p.Payload,
		})
	case nodeproto.ReportResource, nodeproto.ReportReleaseResource:
		p, err := decode[nodeproto.Resource](r.Payload)
		if err != nil {
			return nil, err
		}

		if r.Kind == nodeproto.ReportReleaseResource {
			return nil, svc.ReleaseResource(ctx, call, p.Kind, p.Ref)
		}

		return nil, svc.RecordResource(ctx, call, orchestration.Resource{Kind: p.Kind, Ref: p.Ref, Custody: p.Custody})
	case nodeproto.ReportQuestion:
		p, err := decode[nodeproto.Ask](r.Payload)
		if err != nil {
			return nil, err
		}

		id, err := svc.AskQuestion(ctx, call, askOf(p))

		return map[string]string{"question_id": id}, err
	case nodeproto.ReportAnswerAck:
		p, err := decode[nodeproto.AnswerAck](r.Payload)
		if err != nil {
			return nil, err
		}

		return nil, svc.AckAnswer(ctx, call, p.QuestionID)
	case nodeproto.ReportFinished:
		p, err := decode[nodeproto.Finished](r.Payload)
		if err != nil {
			return nil, err
		}

		return nil, svc.ReportFinished(ctx, call, outcomeOf(p))
	case nodeproto.ReportReattach:
		p, err := decode[nodeproto.Reattach](r.Payload)
		if err != nil {
			return nil, err
		}

		return svc.ReattachDispatch(ctx, call, p.ProcessRef)
	default:
		return nil, fmt.Errorf("%w: unknown report kind %q", errBadPayload, r.Kind)
	}
}

func askOf(a nodeproto.Ask) orchestration.Ask {
	out := orchestration.Ask{Text: a.Text, To: orchestration.Addressee(a.To)}
	for _, o := range a.Options {
		out.Options = append(out.Options, orchestration.Option{Label: o.Label, AllowsText: o.AllowsText})
	}

	return out
}

func outcomeOf(f nodeproto.Finished) orchestration.Outcome {
	out := orchestration.Outcome{
		Class: orchestration.FailureClass(f.Class), ResetsAt: f.ResetsAt,
		Result: orchestration.Result{
			Summary: f.Summary,
			Exit:    orchestration.Exit{Kind: f.Exit.Kind, Code: f.Exit.Code, Signal: f.Exit.Signal},
		},
	}

	for _, e := range f.Evidence {
		out.Result.Evidence = append(out.Result.Evidence, orchestration.Evidence{Kind: e.Kind, Ref: e.Ref})
	}

	if f.Usage != nil {
		out.Result.Usage = &orchestration.Usage{
			Input: f.Usage.Input, Output: f.Usage.Output, CacheRead: f.Usage.CacheRead, CacheWrite: f.Usage.CacheWrite,
		}
	}

	if f.Gate != nil {
		g := askOf(*f.Gate)
		out.Gate = &g
	}

	return out
}

// items stores a batch of a worker's transcript and answers with the newest
// item held, so the node moves its cursor (issue #43).
func (s *Server) items(ctx context.Context, nodeID string, b nodeproto.Items) nodeproto.ItemsAck {
	items := make([]orchestration.Item, 0, len(b.Items))
	for _, it := range b.Items {
		items = append(items, orchestration.Item{
			Seq: it.Seq, Kind: orchestration.ItemKind(it.Kind), Text: it.Text, CallID: it.CallID, Tool: it.Tool,
			Input: it.Input, Failed: it.Failed,
		})
	}

	seq, err := s.svc.AddItems(ctx, nodeID, b.DispatchID, items)

	switch {
	case err == nil:
		return nodeproto.ItemsAck{DispatchID: b.DispatchID, Seq: seq, Status: nodeproto.AckCommitted}
	case permanent(err):
		return nodeproto.ItemsAck{DispatchID: b.DispatchID, Status: nodeproto.AckRejected, Error: err.Error()}
	default:
		s.logger.WarnContext(ctx, "transcript items failed",
			slog.String("event", "nodeapi.items_failed"), slog.String("node_id", nodeID),
			slog.String("dispatch_id", b.DispatchID), slog.String("error", err.Error()))

		return nodeproto.ItemsAck{DispatchID: b.DispatchID, Status: nodeproto.AckTransient, Error: err.Error()}
	}
}
