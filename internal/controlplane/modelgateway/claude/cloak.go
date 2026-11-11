package claude

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// The disguise that makes a request from the gateway pass for one from Claude
// Code, which Anthropic requires of a Pro/Max OAuth token (9router-go
// claude_cloaking.go, open-sse/utils/claudeCloaking.js): a billing system
// block and a metadata.user_id, without which the API answers 429 to a valid
// body; and, so the tools do not read as a third party's, the client's tools
// renamed with a suffix and Claude Code's own tools added as unavailable decoys.

const (
	// CLIVersion is the Claude Code version the gateway presents (9router-go's
	// default; the backend may refuse one too old).
	CLIVersion = "2.1.280"
	// toolSuffix is added to a client tool's name so the tool list reads as
	// Claude Code's, whose tools end in it.
	toolSuffix = "_ide"
)

// decoyTools are Claude Code's own tools, added as unavailable, so the tool
// list looks like a Claude Code session's (9router-go CCDecoyTools).
var decoyTools = []string{
	"Task", "TaskOutput", "TaskStop", "TaskCreate", "TaskGet", "TaskUpdate",
	"TaskList", "Bash", "Glob", "Grep", "Read", "Edit", "Write",
	"NotebookEdit", "WebFetch", "WebSearch", "AskUserQuestion", "Skill",
	"EnterPlanMode", "ExitPlanMode",
}

// cloakTools renames the client's tools with the suffix and adds the decoys,
// filling names with the reverse mapping. A tool with a `type` is one of
// Anthropic's own (web search), kept under its reserved name. Nothing is
// renamed when the client declared no tool of its own: the decoys would then
// reach the backend with no client call to hide, and leak to the client.
func cloakTools(body item, names Names) {
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) == 0 {
		return
	}

	renamed := false
	out := make([]any, 0, len(tools)+len(decoyTools))

	for _, t := range tools {
		tool, ok := t.(item)
		if !ok {
			out = append(out, t)

			continue
		}

		if kind, has := tool["type"]; has && kind != nil && kind != "" {
			out = append(out, tool)

			continue
		}

		name, _ := tool["name"].(string)
		if name == "" {
			out = append(out, tool)

			continue
		}

		sent := name
		if !strings.HasSuffix(sent, toolSuffix) {
			sent += toolSuffix
		}

		names[sent] = name
		renamed = true

		copied := make(item, len(tool))
		for k, v := range tool {
			copied[k] = v
		}

		copied["name"] = sent
		out = append(out, copied)
	}

	if !renamed {
		return
	}

	for _, name := range decoyTools {
		out = append(out, item{
			"name": name, "description": "This tool is currently unavailable.",
			"input_schema": item{"type": "object", "properties": item{}},
		})
	}

	body["tools"] = out
	renameToolUses(body, names)
}

// renameToolUses follows the rename into the history, so a tool_use the client
// sent back resolves against the renamed tools.
func renameToolUses(body item, names Names) {
	reverse := map[string]string{}
	for sent, original := range names {
		reverse[original] = sent
	}

	messages, _ := body["messages"].([]any)

	for _, m := range messages {
		msg, ok := m.(item)
		if !ok {
			continue
		}

		for _, c := range blocksOf(msg["content"]) {
			block, ok := c.(item)
			if !ok {
				continue
			}

			if block["type"] == "tool_use" {
				if name, ok := block["name"].(string); ok {
					if sent, ok := reverse[name]; ok {
						block["name"] = sent
					}
				}
			}
		}
	}
}

// cloak adds the billing system block and the metadata.user_id over the body
// as it stands (9router-go applyClaudeCloaking).
func cloak(body item, token, session string) {
	// cch is the first five hex of the sha256 of the body before the block goes on.
	before, err := json.Marshal(body)
	if err != nil {
		return
	}

	cchSum := sha256.Sum256(before)
	cch := hex.EncodeToString(cchSum[:])[:5]

	build := make([]byte, 2)
	if _, err := rand.Read(build); err != nil {
		build = []byte{0, 0}
	}

	billing := item{"type": "text", "text": fmt.Sprintf(
		"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=sdk-cli; cch=%s;",
		CLIVersion, hex.EncodeToString(build)[:3], cch,
	)}

	switch sys := body["system"].(type) {
	case string:
		if sys != "" {
			body["system"] = []any{billing, item{"type": "text", "text": sys}}
		} else {
			body["system"] = []any{billing}
		}
	case []any:
		body["system"] = append([]any{billing}, sys...)
	default:
		body["system"] = []any{billing}
	}

	meta, _ := body["metadata"].(item)
	if meta == nil {
		meta = item{}
	}

	if _, has := meta["user_id"]; !has {
		meta["user_id"] = fakeUserID(token, session)
	}

	body["metadata"] = meta
}

// fakeUserID is the metadata.user_id Claude Code sends: a device id and an
// account id stable per token, a session id per conversation (9router-go
// generateFakeUserID).
func fakeUserID(token, session string) string {
	deviceSum := sha256.Sum256([]byte("device:" + token))
	device := hex.EncodeToString(deviceSum[:])
	account := deriveUUID("account:" + token)

	session = strings.TrimSpace(strings.TrimPrefix(session, "claude:"))
	if session == "" {
		session = randomUUID()
	}

	return fmt.Sprintf(`{"device_id":%q,"account_uuid":%q,"session_id":%q}`, device, account, session)
}

// deriveUUID is a UUID-shaped string stable for a seed (9router-go deriveUuid).
func deriveUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	d := hex.EncodeToString(sum[:])

	v := d[16]
	if v >= 'a' {
		v = v - 'a' + 10
	} else {
		v -= '0'
	}

	variant := "89ab"[(v & 0x3)]

	return fmt.Sprintf("%s-%s-4%s-%c%s-%s", d[0:8], d[8:12], d[13:16], variant, d[17:20], d[20:32])
}

func randomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}

	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
