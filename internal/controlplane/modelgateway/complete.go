package modelgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// Complete asks one of the gateway's chat models from inside the control plane (issue #721: a meeting's notes),
// as the gateway serves POST /v1/chat/completions to a client: the model's targets in order, the next one asked
// while a target gives no answer or a retriable one, the answer in OpenAI's chat format, and the call recorded on
// the key keyID names as a client's request is recorded on its key. body is a chat completion request without
// stream. An answer that is not a success is a *CompletionError.
func (s *Server) Complete(ctx context.Context, keyID string, body []byte) ([]byte, error) {
	started := s.svc.clock.Now()

	req, err := parseChatRequest(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	if req.stream {
		return nil, fmt.Errorf("%w: an in-process completion is not streamed", ErrInvalid)
	}

	model, err := s.svc.GetModel(ctx, req.model)
	if errors.Is(err, ErrNotFound) || (err == nil && model.Capability != CapabilityChat) {
		return nil, fmt.Errorf("%w: the gateway does not serve a chat model named %q", ErrNotFound, req.model)
	}

	if err != nil {
		return nil, err
	}

	record := Usage{KeyID: keyID, Model: model.Name}
	ctx = context.WithValue(ctx, servedKey{}, &served{})

	answer, err := s.complete(ctx, req, model, keyID, &record)

	if sv, ok := ctx.Value(servedKey{}).(*served); ok {
		record.AccountID = sv.id
	}

	record.Duration = s.svc.clock.Now().Sub(started)

	if rerr := s.svc.recordUsage(context.WithoutCancel(ctx), record); rerr != nil {
		s.logger.ErrorContext(ctx, "recording gateway usage",
			slog.String("event", "modelgateway.usage_failed"), slog.String("error", rerr.Error()))
	}

	return answer, err
}

// CompletionError is a chat answer that is not a success: the provider's status and message, or no provider
// reached (Status 0).
type CompletionError struct {
	Status  int
	Message string
}

func (e *CompletionError) Error() string {
	if e.Status == 0 {
		return e.Message
	}

	return "the model answered " + strconv.Itoa(e.Status) + ": " + e.Message
}

// complete is serveChat without a client to relay to.
func (s *Server) complete(ctx context.Context, req chatRequest, model Model, keyID string, record *Usage,
) ([]byte, error) {
	var cooling modelCooling
	for i, target := range model.Targets {
		last := i == len(model.Targets)-1
		noteServed(ctx, "")
		record.Attempts, record.Provider, record.UpstreamModel = i+1, target.Provider, target.UpstreamModel
		record.Status = 0

		resp, err := s.send(ctx, chatCall{req: req, keyID: keyID, model: model.Name}, target)
		if err != nil {
			if ctx.Err() != nil {
				record.Outcome = OutcomeClientGone

				return nil, ctx.Err()
			}
			if cooling.add(err) {
				continue
			}

			s.logger.WarnContext(ctx, "a gateway target gave no answer",
				slog.String("event", "modelgateway.target_failed"), slog.String("provider", target.Provider),
				slog.String("model", model.Name), slog.String("error", err.Error()))

			continue
		}

		record.Status = resp.StatusCode
		cooling.other = true

		if retryable(resp.StatusCode) && !last {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBytes))
			_ = resp.Body.Close()

			continue
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()

		if err != nil {
			record.Outcome = OutcomeTransportError

			return nil, &CompletionError{Message: "reading the model's answer: " + err.Error()}
		}

		if resp.StatusCode < http.StatusOK || resp.StatusCode > 299 {
			record.Outcome = OutcomeUpstreamError
			record.Error = errorText(body)

			return nil, &CompletionError{Status: resp.StatusCode, Message: record.Error}
		}

		var answer struct {
			Usage json.RawMessage `json:"usage"`
		}

		if json.Unmarshal(body, &answer) == nil {
			record.Tokens.merge(answer.Usage)
		}

		record.Tokens.estimate(len(req.raw), len(body))
		record.Outcome = OutcomeOK

		return body, nil
	}

	if resp := s.coolingResponse(ctx, "/v1/chat/completions", cooling, record); resp != nil {
		_ = resp.Body.Close()
		return nil, &CompletionError{Status: resp.StatusCode, Message: record.Error}
	}
	record.Outcome = OutcomeTransportError

	return nil, &CompletionError{Message: fmt.Sprintf("no provider of model %q could be reached", model.Name)}
}

// meetingsKeyName names the key a meeting's speech and notes are recorded on.
const meetingsKeyName = "meetings"

// MeetingsKey returns the id of the key the meetings' calls are recorded on (issue #721), as a Bot's are on its
// chat's key: the one named meetings that still works, made the first time with no secret kept, since nothing
// outside calls with it.
func (s *Service) MeetingsKey(ctx context.Context) (string, error) {
	keys, err := s.ListKeys(ctx)
	if err != nil {
		return "", err
	}

	taken := map[string]bool{}

	for _, k := range keys {
		taken[k.Name] = true

		if k.RevokedAt == nil && k.DispatchID == "" && k.ChatID == "" &&
			(k.Name == meetingsKeyName || strings.HasPrefix(k.Name, meetingsKeyName+"-")) {
			return k.ID, nil
		}
	}

	name := meetingsKeyName
	for n := 2; taken[name]; n++ {
		name = meetingsKeyName + "-" + strconv.Itoa(n)
	}

	k, _, err := s.addKey(ctx, name, []string{AllModels}, keyOwner{endpoints: []Endpoint{EndpointAudio}})
	if err != nil {
		return "", err
	}

	return k.ID, nil
}
