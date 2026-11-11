package claude

import (
	"encoding/json"
)

// convertTools turns OpenAI tools into Messages tools (name, description,
// input_schema), as 9router-go does (convertOpenAIToolsToClaude). A tool
// already in Messages shape is kept.
func convertTools(raw []json.RawMessage, _ Names) []any {
	out := make([]any, 0, len(raw))

	for _, r := range raw {
		var m item
		if json.Unmarshal(r, &m) != nil {
			continue
		}

		if _, hasName := m["name"]; hasName {
			if _, hasSchema := m["input_schema"]; hasSchema {
				out = append(out, m)

				continue
			}
		}

		fn, ok := m["function"].(item)
		if !ok {
			out = append(out, m)

			continue
		}

		tool := item{}
		if name, ok := fn["name"].(string); ok {
			tool["name"] = name
		}

		if desc, ok := fn["description"].(string); ok && desc != "" {
			tool["description"] = desc
		}

		if params, ok := fn["parameters"]; ok && params != nil {
			tool["input_schema"] = params
		} else {
			tool["input_schema"] = item{"type": "object", "properties": item{}}
		}

		if cc, ok := m["cache_control"]; ok {
			tool["cache_control"] = cc
		}

		out = append(out, tool)
	}

	return out
}

// convertToolChoice turns an OpenAI tool_choice into a Messages tool_choice,
// and folds in parallel_tool_calls: false as disable_parallel_tool_use
// (9router-go convertToolChoiceToClaude, withDisabledParallelToolUse). A
// policy with no tools would only be noise, so it is dropped then.
func convertToolChoice(raw json.RawMessage, parallel *bool, hasTools bool) any {
	var choice any

	if len(raw) > 0 {
		var asString string
		if json.Unmarshal(raw, &asString) == nil {
			switch asString {
			case "required":
				choice = item{"type": "any"}
			case "none":
				choice = item{"type": "none"}
			default:
				choice = item{"type": "auto"}
			}
		} else {
			var obj item
			if json.Unmarshal(raw, &obj) == nil {
				if obj["type"] == "function" {
					if fn, ok := obj["function"].(item); ok {
						if name, ok := fn["name"].(string); ok {
							obj = item{"type": "tool", "name": name}
						}
					}
				}

				choice = obj
			}
		}
	}

	if parallel != nil && !*parallel && hasTools {
		c, ok := choice.(item)
		if !ok {
			c = item{"type": "auto"}
		}

		c["disable_parallel_tool_use"] = true
		choice = c
	}

	if !hasTools {
		return nil
	}

	return choice
}
