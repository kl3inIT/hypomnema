package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kl3inIT/hypomnema/internal/node/statedb"
	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

func (d *Daemon) handle(ctx context.Context, msg nodeproto.Message) error {
	switch msg.Type {
	case nodeproto.TypeOffer:
		var o nodeproto.Offer
		if err := json.Unmarshal(msg.Payload, &o); err != nil {
			return fmt.Errorf("decoding an offer: %w", err)
		}

		return d.offered(ctx, o)
	case nodeproto.TypeStop:
		var st nodeproto.Stop
		if err := json.Unmarshal(msg.Payload, &st); err != nil {
			return fmt.Errorf("decoding a stop: %w", err)
		}

		d.runner.Stop(st.DispatchID)

		return nil
	case nodeproto.TypeAnswer:
		var q nodeproto.Question
		if err := json.Unmarshal(msg.Payload, &q); err != nil {
			return fmt.Errorf("decoding an answer: %w", err)
		}

		d.runner.Answer(q.DispatchID, q)

		return nil
	case nodeproto.TypeReportAck:
		var ack nodeproto.ReportAck
		if err := json.Unmarshal(msg.Payload, &ack); err != nil {
			return fmt.Errorf("decoding a report ack: %w", err)
		}

		return d.acked(ctx, ack)
	case nodeproto.TypeItemsAck:
		var ack nodeproto.ItemsAck
		if err := json.Unmarshal(msg.Payload, &ack); err != nil {
			return fmt.Errorf("decoding a transcript ack: %w", err)
		}

		d.runner.ItemsAcked(ack)

		return nil
	case nodeproto.TypeError:
		var e nodeproto.Error
		_ = json.Unmarshal(msg.Payload, &e)

		if e.Code == nodeproto.ErrorReplaced {
			return errReplaced
		}

		d.logger.WarnContext(ctx, "the control plane reported an error",
			slog.String("event", "node.server_error"), slog.String("code", e.Code), slog.String("message", e.Message))

		return nil
	case nodeproto.TypeHeartbeat, nodeproto.TypeReport, nodeproto.TypeItems, nodeproto.TypeHeartbeatAck:
		fallthrough
	default:
		return nil
	}
}

// offered records an offer and queues its accept. The worker starts only once
// the accept is acknowledged (Omnara: accept, then accept_ack, then start).
func (d *Daemon) offered(ctx context.Context, o nodeproto.Offer) error {
	accept := nodeproto.Report{ReportID: d.newID("rep"), Kind: nodeproto.ReportAccept, DispatchID: o.DispatchID}

	added, err := d.db.AddOffer(ctx, o, accept)
	if err != nil || !added {
		return err
	}

	d.sendReport(accept)

	return nil
}

// acked handles the control plane's answer to a report.
func (d *Daemon) acked(ctx context.Context, ack nodeproto.ReportAck) error {
	d.mu.Lock()
	r, ephemeral := d.ephemeral[ack.ReportID]
	delete(d.ephemeral, ack.ReportID)
	d.mu.Unlock()

	if ephemeral {
		if r.Kind == nodeproto.ReportHeartbeat && ack.Status == nodeproto.AckCommitted {
			d.beat(r.DispatchID, ack.Response)
		}

		return nil
	}

	r, err := d.db.Report(ctx, ack.ReportID)
	if errors.Is(err, statedb.ErrNotFound) {
		return nil // already handled
	}

	if err != nil {
		return err
	}

	if ack.Status == nodeproto.AckTransient {
		time.AfterFunc(retryTransient, func() { d.sendReport(r) })

		return nil
	}

	if ack.Status == nodeproto.AckRejected {
		d.logger.WarnContext(ctx, "a report was rejected",
			slog.String("event", "node.report_rejected"), slog.String("kind", r.Kind),
			slog.String("dispatch_id", r.DispatchID), slog.String("error", ack.Error))
	}

	if err := d.applied(ctx, r, ack); err != nil {
		return err
	}

	return d.db.Done(ctx, r.ReportID)
}

// applied acts on a durable report the control plane committed or rejected.
func (d *Daemon) applied(ctx context.Context, r nodeproto.Report, ack nodeproto.ReportAck) error {
	committed := ack.Status == nodeproto.AckCommitted

	switch r.Kind {
	case nodeproto.ReportAccept, nodeproto.ReportReattach:
		if !committed {
			d.runner.Release(r.DispatchID)
			d.forget(r.DispatchID)

			return d.db.Remove(ctx, r.DispatchID)
		}

		var a nodeproto.Accepted
		if err := json.Unmarshal(ack.Response, &a); err != nil {
			return fmt.Errorf("decoding the accept of %s: %w", r.DispatchID, err)
		}

		if err := d.db.Accept(ctx, r.DispatchID, a); err != nil {
			return err
		}

		if r.Kind == nodeproto.ReportReattach || !d.remember(r.DispatchID) {
			return nil
		}

		h, err := d.db.Get(ctx, r.DispatchID)
		if err != nil {
			return err
		}

		if err := d.runner.Start(ctx, h, d.worker(h.ID)); err != nil {
			return d.worker(h.ID).finish(ctx, nodeproto.Finished{
				Class: "start_failed", Summary: err.Error(), Exit: nodeproto.Exit{Kind: "not_started"},
			})
		}

		return nil
	case nodeproto.ReportQuestion:
		if !committed {
			return nil
		}

		var asked nodeproto.Asked
		if err := json.Unmarshal(ack.Response, &asked); err != nil {
			return fmt.Errorf("decoding the question id: %w", err)
		}

		d.runner.Asked(r.DispatchID, r.ReportID, asked.QuestionID)

		return nil
	case nodeproto.ReportFinished, nodeproto.ReportExited, nodeproto.ReportStopped:
		d.forget(r.DispatchID)

		return d.db.Remove(ctx, r.DispatchID)
	default:
		return nil
	}
}

// beat acts on a lease renewal's answer: a stop requested, answers waiting.
func (d *Daemon) beat(id string, raw json.RawMessage) {
	var b nodeproto.Beat
	if json.Unmarshal(raw, &b) != nil {
		return
	}

	if b.StopRequested {
		d.runner.Stop(id)
	}

	for _, q := range b.Answers {
		d.runner.Answer(id, q)
	}
}

// Worker is how a Runner reports what one worker does.
type Worker struct {
	d  *Daemon
	id string
}

func (d *Daemon) worker(id string) *Worker { return &Worker{d: d, id: id} }

// ID is the Dispatch the worker runs.
func (w *Worker) ID() string { return w.id }

func (w *Worker) report(ctx context.Context, kind string, payload any) (string, error) {
	h, err := w.d.db.Get(ctx, w.id)
	if err != nil {
		return "", err
	}

	r := nodeproto.Report{ReportID: w.d.newID("rep"), Kind: kind, DispatchID: w.id, Epoch: h.Epoch}
	if payload != nil {
		r.Payload = mustJSON(payload)
	}

	return r.ReportID, w.d.submit(ctx, r)
}

// Started reports the worker's process and its CLI session.
func (w *Worker) Started(ctx context.Context, processRef, cliSessionRef string) error {
	if err := w.d.db.SetProcess(ctx, w.id, processRef); err != nil {
		return err
	}

	_, err := w.report(ctx, nodeproto.ReportStarted,
		nodeproto.Started{ProcessRef: processRef, CLISessionRef: cliSessionRef})

	return err
}

// Progress reports that the worker is doing something. It is not resent.
func (w *Worker) Progress(ctx context.Context) error {
	h, err := w.d.db.Get(ctx, w.id)
	if err != nil {
		return err
	}

	w.d.ephemeralReport(nodeproto.Report{Kind: nodeproto.ReportProgress, DispatchID: w.id, Epoch: h.Epoch})

	return nil
}

// Ask asks a question the worker waits for; the Runner learns its id through
// Asked, with the report id returned here.
func (w *Worker) Ask(ctx context.Context, a nodeproto.Ask) (string, error) {
	return w.report(ctx, nodeproto.ReportQuestion, a)
}

// AnswerReceived acknowledges an answer the worker now has.
func (w *Worker) AnswerReceived(ctx context.Context, questionID string) error {
	_, err := w.report(ctx, nodeproto.ReportAnswerAck, nodeproto.AnswerAck{QuestionID: questionID})

	return err
}

// Finished reports the worker's outcome.
func (w *Worker) Finished(ctx context.Context, f nodeproto.Finished) error { return w.finish(ctx, f) }

func (w *Worker) finish(ctx context.Context, f nodeproto.Finished) error {
	return w.end(ctx, nodeproto.ReportFinished, f)
}

// Exited reports that the worker's process ended without a final report.
func (w *Worker) Exited(ctx context.Context) error { return w.end(ctx, nodeproto.ReportExited, nil) }

// Stopped confirms a requested stop.
func (w *Worker) Stopped(ctx context.Context) error { return w.end(ctx, nodeproto.ReportStopped, nil) }

func (w *Worker) end(ctx context.Context, kind string, payload any) error {
	h, err := w.d.db.Get(ctx, w.id)
	if err != nil {
		return err
	}

	r := nodeproto.Report{ReportID: w.d.newID("rep"), Kind: kind, DispatchID: w.id, Epoch: h.Epoch}
	if payload != nil {
		r.Payload = mustJSON(payload)
	}

	if err := w.d.db.End(ctx, w.id, r); err != nil {
		return err
	}

	w.d.sendReport(r)

	return nil
}
