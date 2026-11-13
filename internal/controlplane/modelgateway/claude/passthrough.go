package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// PassOptions are what the gateway knows of a Messages request it passes on.
type PassOptions struct {
	// Model is the model's name at Anthropic.
	Model string
	// Token and Session are as in Options.
	Token   string
	Session string
	// Disguise dresses the body as Claude Code's, for a client that is not
	// Claude Code: the billing block, the identity and a metadata.user_id
	// (CLIProxyAPI cloaks only a client it does not recognize as Claude Code,
	// claude_executor_cloaking.go). The client's tools keep their names.
	Disguise bool
}

// PassThrough prepares a client's own Messages body for the backend: the
// model becomes the target's, and the disguise goes on when asked. Nothing
// else of the body changes.
func PassThrough(raw []byte, opt PassOptions) ([]byte, error) {
	if opt.Model == "" {
		return nil, errors.New("the model's name is missing")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var body item
	if err := decoder.Decode(&body); err != nil || body == nil {
		return nil, errors.New("the request is not a JSON object")
	}

	body["model"] = opt.Model

	if opt.Disguise {
		cloak(body, opt.Token, opt.Session)
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding the Messages request: %w", err)
	}

	return encoded, nil
}

// PassUsage counts the tokens of a Messages answer passed on as it came.
type PassUsage struct {
	Input, Output, CacheRead, CacheCreation int64
}

// Note reads the usage an event, or a whole answer, carries: message_start
// has it under message, message_delta and an answer that is not a stream at
// the top (9router usageTracking.js). A later count replaces an earlier one.
func (u *PassUsage) Note(data []byte) {
	var event struct {
		Message struct {
			Usage *rawUsage `json:"usage"`
		} `json:"message"`
		Usage *rawUsage `json:"usage"`
	}

	if json.Unmarshal(data, &event) != nil {
		return
	}

	for _, got := range []*rawUsage{event.Message.Usage, event.Usage} {
		if got == nil {
			continue
		}

		if got.InputTokens > 0 {
			u.Input = got.InputTokens
		}

		if got.OutputTokens > 0 {
			u.Output = got.OutputTokens
		}

		if got.CacheReadInput > 0 {
			u.CacheRead = got.CacheReadInput
		}

		if got.CacheCreationInput > 0 {
			u.CacheCreation = got.CacheCreationInput
		}
	}
}
