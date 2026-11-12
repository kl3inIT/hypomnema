package serviceproxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// A relayed request (issue #203). An Integration whose Connections each have
// their own address, such as a self-hosted Superset, has no vendor tool and
// no tunnel: a caller names a method and a path, and the control plane sends
// the request to the Connection's address with its token, as treg's
// `treg call` relays a request to an endpoint and adds its credential on the
// server (treg, README: "relays, never models"). The request is answered by
// the same decision as one that came through a tunnel (judge): the action it
// is, the Connection's answer for that action, the owner's Approval when it
// asks, and the log line.

// Sent is a relayed request as it goes to the service: the path and the
// query as they were checked.
type Sent struct {
	Method      string
	Path        string
	RawQuery    string
	ContentType string
	Body        []byte
}

// Answer is the service's answer to a relayed request, unchanged.
type Answer struct {
	Status      int
	ContentType string
	Body        []byte
}

// Sender sends a request to a Connection's own address with its token: the
// control plane's wiring over the service that signs accounts in.
type Sender func(ctx context.Context, integration, label string, r Sent) (Answer, error)

// Call is a request to relay for a Connection.
type Call struct {
	// Caller is who asks; the API that takes the request has checked that
	// it may use the Connection.
	Caller      Caller
	Integration string
	Label       string
	// ChatID is the chat a coordinator works in, so that the Approval it
	// waits for is answered there.
	ChatID string
	Method string
	// Target is the path and the query, as the service reads them:
	// /api/v1/dashboard/?q=(page_size:5).
	Target      string
	ContentType string
	Body        []byte
}

// RefusedError is a relayed request that was not sent: the owner's answer
// for its action is never, it waits for or was refused an Approval, or it
// could be read two ways. Message is what the caller is told.
type RefusedError struct{ Message string }

func (e *RefusedError) Error() string { return e.Message }

// relayed are the methods a request may be relayed with.
var relayed = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// Relay answers one request for a Connection that has its own address and,
// when the decision lets it run, sends it there and returns the service's
// answer unchanged, with the action the request is. A *RefusedError when it
// was not sent.
func (p *Proxy) Relay(ctx context.Context, c Call) (Answer, Action, error) {
	catalog, ok := CatalogOf(c.Integration)
	if !ok || p.cfg.Send == nil {
		return Answer{}, Action{}, fmt.Errorf("%w: %s takes no relayed request", orchestration.ErrInvalid, c.Integration)
	}

	if !slices.Contains(relayed, c.Method) {
		return Answer{}, Action{}, fmt.Errorf("%w: a request is a GET, POST, PUT, PATCH or DELETE", orchestration.ErrInvalid)
	}

	if len(c.Body) > maxBody {
		return Answer{}, Action{}, fmt.Errorf("%w: the request is larger than is passed on", orchestration.ErrInvalid)
	}

	target, err := url.Parse(c.Target)
	if err != nil {
		return Answer{}, Action{}, &RefusedError{Message: "Refused: the path is not one. The request was not run"}
	}

	req := &http.Request{Method: c.Method, URL: target, Header: http.Header{}}
	if c.ContentType != "" {
		req.Header.Set("Content-Type", c.ContentType)
	}

	s := session{caller: c.Caller, integration: c.Integration, label: c.Label, chatID: c.ChatID, catalog: catalog}

	action, message := p.judge(ctx, req, c.Body, s)
	if message != "" {
		return Answer{}, action, &RefusedError{Message: message}
	}

	// Only the path and the query are the caller's, and judge has checked them; the address is the
	// Connection's own.
	answer, err := p.cfg.Send(ctx, c.Integration, c.Label, Sent{
		Method: c.Method, Path: target.EscapedPath(), RawQuery: target.RawQuery, ContentType: c.ContentType, Body: c.Body,
	})
	if err != nil {
		if !errors.Is(err, orchestration.ErrConflict) && !errors.Is(err, orchestration.ErrNotFound) &&
			!errors.Is(err, orchestration.ErrUnavailable) && !errors.Is(err, orchestration.ErrInvalid) {
			p.cfg.Logger.ErrorContext(ctx, "the proxy could not send a relayed request",
				slog.String("event", "serviceproxy.failed"), slog.String("connection", s.connection()),
				slog.String("error", err.Error()))
		}

		return Answer{}, action, err
	}

	return answer, action, nil
}
