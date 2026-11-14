package clientapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/skillsource"
	"github.com/kl3inIT/hypomnema/internal/controlplane/skillssh"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// Errors are {code, message}, as Omnara's apierror: the code is stable for
// clients, the message is for people.

func statusOf(code openapi.ErrorCode) int {
	switch code {
	case openapi.ValidationFailed, openapi.InvalidRequest:
		return http.StatusBadRequest
	case openapi.Unauthorized:
		return http.StatusUnauthorized
	case openapi.Forbidden:
		return http.StatusForbidden
	case openapi.NotFound:
		return http.StatusNotFound
	case openapi.Conflict, openapi.IdempotencyKeyConflict:
		return http.StatusConflict
	case openapi.RequestTooLarge:
		return http.StatusRequestEntityTooLarge
	case openapi.Unavailable:
		return http.StatusServiceUnavailable
	case openapi.RateLimited:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// codeOf maps the orchestration core's errors, and this package's, to a code.
func codeOf(err error) openapi.ErrorCode {
	var (
		tooLarge *http.MaxBytesError
		locked   *lockedError
	)

	if code, ok := webCode(err); ok {
		return code
	}

	if code, ok := imageCode(err); ok {
		return code
	}

	switch {
	case errors.As(err, &locked):
		return openapi.RateLimited
	case errors.Is(err, errUnauthorized):
		return openapi.Unauthorized
	case errors.Is(err, errForbidden), errors.Is(err, orchestration.ErrForbidden):
		return openapi.Forbidden
	case errors.Is(err, orchestration.ErrIdempotencyConflict):
		return openapi.IdempotencyKeyConflict
	case errors.Is(err, orchestration.ErrNotFound):
		return openapi.NotFound
	case errors.Is(err, orchestration.ErrInvalid), errors.Is(err, skillpack.ErrInvalid),
		errors.Is(err, skillsource.ErrInvalid):
		return openapi.InvalidRequest
	case errors.Is(err, orchestration.ErrConflict):
		return openapi.Conflict
	case errors.Is(err, orchestration.ErrUnavailable), errors.Is(err, orchestration.ErrCoolingDown),
		errors.Is(err, skillsource.ErrUnavailable), errors.Is(err, errNoSkillSource),
		errors.Is(err, skillssh.ErrUnavailable), errors.Is(err, errNoSkillDirectory):
		return openapi.Unavailable
	case errors.As(err, &tooLarge):
		return openapi.RequestTooLarge
	default:
		return openapi.InternalError
	}
}

// writeError answers a handler's error. An internal error is logged and its
// details are not shown.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	code := codeOf(err)
	message := err.Error()

	if code == openapi.InternalError {
		s.logger.ErrorContext(r.Context(), "client API request failed",
			slog.String("event", "clientapi.internal_error"), slog.String("path", r.URL.Path),
			slog.String("error", err.Error()))

		message = "internal error"
	}

	if code == openapi.Unauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="hypomnema"`)
	}

	var locked *lockedError
	if errors.As(err, &locked) {
		w.Header().Set("Retry-After", retryAfter(locked.wait))
	}

	if wait, ok := s.restingWait(err); ok {
		w.Header().Set("Retry-After", wait)
	}

	if wait, ok := imageWait(err); ok {
		w.Header().Set("Retry-After", wait)
	}

	writeJSONError(w, statusOf(code), code, message)
}

// writeRequestError answers a request the generated code could not decode.
func (s *Server) writeRequestError(w http.ResponseWriter, _ *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeJSONError(w, http.StatusRequestEntityTooLarge, openapi.RequestTooLarge, err.Error())

		return
	}

	writeJSONError(w, http.StatusBadRequest, openapi.ValidationFailed, err.Error())
}

func writeJSONError(w http.ResponseWriter, status int, code openapi.ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(openapi.Error{Code: code, Message: message}); err != nil {
		return // The client left; there is no one to tell.
	}
}
