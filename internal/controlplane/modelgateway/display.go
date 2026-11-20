package modelgateway

import (
	"regexp"
	"strings"
)

// The families DisplayName knows (web/src/lib/model-name.ts).
var (
	gptName    = regexp.MustCompile(`(?i)^gpt-\d`)
	familyName = regexp.MustCompile(`(?i)^(claude-(opus|sonnet|haiku|fable)(-\d|$)|gemini-\d)`)
	// claudeVersion joins Claude's two version numbers; the third group is what follows them, kept.
	claudeVersion = regexp.MustCompile(`(?i)^(claude-[a-z]+-\d+)-(\d{1,2})(-|\[|$)`)
)

// DisplayName writes a model's name as people read it: "claude-opus-5-5" is "Claude Opus 5.5". It is the web
// client's rule (web/src/lib/model-name.ts modelName, after t3code's formatModelSlugName and formatCodexModelName,
// MIT License, https://github.com/pingdotgg/t3code packages/shared/src/model.ts): GPT keeps its hyphens with each
// part capitalized, Claude's two version numbers are joined by a point, the parts of a Claude or Gemini name become
// words, a prefix before a slash is kept, and any other name is left as it is.
func DisplayName(id string) string {
	at := strings.LastIndex(id, "/") + 1
	prefix, name := id[:at], id[at:]
	lower := strings.ToLower(name)

	if gptName.MatchString(name) {
		parts := strings.Split(name, "-")
		parts[0] = "GPT"

		for i := 1; i < len(parts); i++ {
			parts[i] = capitalize(parts[i])
		}

		return prefix + strings.Join(parts, "-")
	}

	if !familyName.MatchString(lower) {
		return id
	}

	name = claudeVersion.ReplaceAllString(name, "$1.$2$3")

	words := strings.Split(name, "-")
	for i, w := range words {
		words[i] = capitalize(w)
	}

	return prefix + strings.Join(words, " ")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}

	return strings.ToUpper(s[:1]) + s[1:]
}
