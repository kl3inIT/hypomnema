package clientapi

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/coordinator"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// keptBash is a finished bash call as the supervisor keeps it.
const keptBash = `[{"type":"tool_result","id":"t1","name":"bash","args":{"command":"cat report.sql"},` +
	`"output":"412 rows","error":true,"duration_ms":42}]`

// Scenario: a Bot others use must not show its bash runs (issue #441). The owner sets what its chats show of its
// tool calls; a Bot made without the setting shows a row per call, and a change that leaves it out keeps it.
func TestTheOwnerSetsWhatABotsChatsShowOfItsTools(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	owner, _ := w.token("web", bearertoken.Owner)
	ctx := t.Context()

	persona := "You send the Superset report."
	made, err := owner.PutBotWithResponse(ctx, "reporter", openapi.BotSettings{Persona: &persona})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, made.StatusCode())
	assert.Equal(t, openapi.ToolDisplayOn, made.JSON200.ToolDisplay, "a new Bot shows a row per call")

	off := openapi.ToolDisplayOff
	set, err := owner.PutBotWithResponse(ctx, "reporter", openapi.BotSettings{Persona: &persona, ToolDisplay: &off})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, set.StatusCode())
	assert.Equal(t, openapi.ToolDisplayOff, set.JSON200.ToolDisplay)

	kept, err := owner.PutBotWithResponse(ctx, "reporter", openapi.BotSettings{Persona: &persona})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, kept.StatusCode())
	assert.Equal(t, openapi.ToolDisplayOff, kept.JSON200.ToolDisplay, "a change that leaves it out keeps it")

	read, err := owner.GetBotWithResponse(ctx, "reporter")
	require.NoError(t, err)
	assert.Equal(t, openapi.ToolDisplayOff, read.JSON200.ToolDisplay, "it is stored")

	loud := openapi.ToolDisplay("loud")
	bad, err := owner.PutBotWithResponse(ctx, "reporter", openapi.BotSettings{Persona: &persona, ToolDisplay: &loud})
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode())
}

// Scenario: the chat API and the stream leave out what the Bot's level hides, as OpenClaw drops a tool event below
// its level before delivery: off sends no tool row and no tool frame; on sends the row and the frame with the tool's
// name only; full sends the arguments, output, error and time.
func TestAChatSendsOfItsToolCallsWhatItsBotShows(t *testing.T) {
	t.Parallel()

	for _, level := range []openapi.ToolDisplay{openapi.ToolDisplayOff, openapi.ToolDisplayOn, openapi.ToolDisplayFull} {
		t.Run(string(level), func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			owner, _ := w.token("web", bearertoken.Owner)
			ctx := t.Context()

			persona := "You send the Superset report."
			put, err := owner.PutBotWithResponse(ctx, "reporter",
				openapi.BotSettings{Persona: &persona, ToolDisplay: &level})
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, put.StatusCode())

			chat, err := w.svc.CreateChat(ctx, "reporter", "report")
			require.NoError(t, err)

			frames := streamOf(t, w, chat.ID)
			assert.Equal(t, "state", frames.next().event)

			// A live tool frame, then a delta: once the delta is read, the tool frame was handled before it.
			w.pi.live <- coordinator.Frame{Event: coordinator.FrameTool, Data: map[string]any{
				"phase": "start", "id": "t1", "name": "bash", "args": map[string]any{"command": "cat report.sql"},
			}}
			w.pi.live <- coordinator.Frame{Event: coordinator.FrameDelta, Data: map[string]any{
				"kind": "text", "index": 0, "delta": "Sent",
			}}

			var liveTool *frame

			for f := frames.next(); f.event != "delta"; f = frames.next() {
				if f.event == "tool" {
					liveTool = &f
				}
			}

			_, err = w.svc.AddChatMessageParts(ctx, chat.ID, orchestration.ChatTool, "pi ran bash: cat report.sql",
				json.RawMessage(keptBash))
			require.NoError(t, err)
			_, err = w.svc.AddChatMessage(ctx, chat.ID, orchestration.ChatCoordinator, "Sent.")
			require.NoError(t, err)

			var streamed []openapi.ChatMessage

			for {
				f := frames.next()
				if f.event != "message" {
					continue
				}

				var m openapi.ChatMessage
				require.NoError(t, json.Unmarshal([]byte(f.data), &m))
				streamed = append(streamed, m)

				if m.Role == openapi.ChatRoleCoordinator {
					break
				}
			}

			list, err := owner.ListChatMessagesWithResponse(ctx, chat.ID, nil)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, list.StatusCode())
			assert.Equal(t, streamed[len(streamed)-1].Seq, list.JSON200.NextAfter, "the cursor passes what is hidden")

			for name, msgs := range map[string][]openapi.ChatMessage{"list": list.JSON200.Data, "stream": streamed} {
				tools := toolMessages(msgs)

				switch level {
				case openapi.ToolDisplayOff:
					assert.Empty(t, tools, name)
				case openapi.ToolDisplayOn:
					require.Len(t, tools, 1, name)
					assert.Equal(t, "pi used bash", tools[0].Text, name)
					require.NotNil(t, tools[0].Parts, name)
					p := (*tools[0].Parts)[0]
					assert.Equal(t, "bash", val(p.Name), name)
					assert.True(t, val(p.Error), name+": a failure is still marked")
					assert.Nil(t, p.Args, name)
					assert.Nil(t, p.Output, name)
					assert.Nil(t, p.DurationMs, name)
				case openapi.ToolDisplayFull:
					require.Len(t, tools, 1, name)
					assert.Equal(t, "pi ran bash: cat report.sql", tools[0].Text, name)
					p := (*tools[0].Parts)[0]
					assert.Equal(t, "412 rows", val(p.Output), name)
					assert.Equal(t, int64(42), val(p.DurationMs), name)
					assert.True(t, val(p.Error), name)
					require.NotNil(t, p.Args, name)
					assert.Equal(t, "cat report.sql", (*p.Args)["command"], name)
				}
			}

			switch level {
			case openapi.ToolDisplayOff:
				assert.Nil(t, liveTool, "no live tool frame")
			case openapi.ToolDisplayOn:
				require.NotNil(t, liveTool)
				assert.JSONEq(t, `{"phase":"start","id":"t1","name":"bash"}`, liveTool.data)
			case openapi.ToolDisplayFull:
				require.NotNil(t, liveTool)
				assert.JSONEq(t, `{"phase":"start","id":"t1","name":"bash","args":{"command":"cat report.sql"}}`,
					liveTool.data)
			}

			close(w.stop)
			frames.drain()
		})
	}
}

func toolMessages(msgs []openapi.ChatMessage) []openapi.ChatMessage {
	var out []openapi.ChatMessage

	for _, m := range msgs {
		if m.Role == openapi.ChatRoleTool {
			out = append(out, m)
		}
	}

	return out
}

// frame is one server-sent event of a chat's stream.
type frame struct{ event, data string }

type frameReader struct {
	t      *testing.T
	frames chan frame
}

// streamOf opens the chat's stream as the owner and reads its events.
func streamOf(t *testing.T, w *world, chatID string) *frameReader {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, w.url+BasePath+"/chats/"+chatID+"/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+bearerOf(t, w, "stream"))

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)

	r := &frameReader{t: t, frames: make(chan frame, 100)}

	go func() {
		defer close(r.frames)

		var f frame

		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			switch l := sc.Text(); {
			case strings.HasPrefix(l, "event: "):
				f.event = strings.TrimPrefix(l, "event: ")
			case strings.HasPrefix(l, "data: "):
				f.data = strings.TrimPrefix(l, "data: ")
				r.frames <- f
				f = frame{}
			}
		}
	}()

	return r
}

func (r *frameReader) next() frame {
	r.t.Helper()

	select {
	case f, ok := <-r.frames:
		require.True(r.t, ok, "the stream ended")

		return f
	case <-time.After(5 * time.Second):
		r.t.Fatal("no frame in time")

		return frame{}
	}
}

func (r *frameReader) drain() {
	for range r.frames {
		// Until the server ends the stream.
	}
}
