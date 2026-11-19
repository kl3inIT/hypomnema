package modelgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

type streamIdle struct {
	timer   *time.Timer
	timeout time.Duration
	expired atomic.Bool
}

func watchStreamIdle(body io.Closer, timeout time.Duration) *streamIdle {
	idle := &streamIdle{timeout: timeout}
	idle.timer = time.AfterFunc(timeout, func() {
		idle.expired.Store(true)
		_ = body.Close()
	})

	return idle
}

func (idle *streamIdle) reset() {
	if idle.timer.Stop() {
		idle.timer.Reset(idle.timeout)
	}
}

func (idle *streamIdle) endReason(scanErr error, missingEnd string) string {
	idle.timer.Stop()

	if failure, ok := errors.AsType[upstreamStreamError](scanErr); ok {
		return string(failure)
	}

	if idle.expired.Load() {
		if idle.timeout%time.Minute == 0 {
			return fmt.Sprintf("no event for %dm", idle.timeout/time.Minute)
		}

		return "no event for " + idle.timeout.String()
	}

	return "upstream closed before " + missingEnd
}

type upstreamStreamError string

func (failure upstreamStreamError) Error() string { return string(failure) }

type eventError struct {
	Code    json.RawMessage `json:"code"`
	Type    string          `json:"type"`
	Message string          `json:"message"`
}

func streamEventError(payload []byte) string {
	if !bytes.Contains(payload, []byte(`"error"`)) && !bytes.Contains(payload, []byte(`"response.failed"`)) {
		return ""
	}

	var event struct {
		eventError
		Error    *eventError `json:"error"`
		Response struct {
			Error eventError `json:"error"`
		} `json:"response"`
	}

	if json.Unmarshal(payload, &event) != nil {
		return ""
	}

	failure := event.eventError
	fallback := "upstream sent an error event"

	switch event.Type {
	case "response.failed":
		failure = event.Response.Error
		fallback = "upstream sent response.failed"
	case "", "error":
		if event.Error != nil {
			failure = *event.Error
		} else if event.Type == "" {
			return ""
		} else {
			failure.Type = ""
		}
	default:
		return ""
	}

	var code string
	if json.Unmarshal(failure.Code, &code) != nil {
		var number int64
		if json.Unmarshal(failure.Code, &number) == nil {
			code = string(failure.Code)
		}
	}

	if code == "" {
		code = failure.Type
	}

	switch {
	case code != "" && failure.Message != "":
		return clip(code+": "+failure.Message, maxErrorText)
	case code != "":
		return clip(code, maxErrorText)
	case failure.Message != "":
		return clip(failure.Message, maxErrorText)
	default:
		return fallback
	}
}

func noteStreamFailure(reason *string, payload []byte) {
	if *reason == "" {
		*reason = streamEventError(payload)
	}
}
