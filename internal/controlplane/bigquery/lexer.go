package bigquery

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// The lexical gate (issue #976, design v3, decision 8): what a query must be before any request is sent. The lexer's
// states (single, double, triple and raw strings; `--`, `#` and `/* */` comments) and its reading of a dotted path
// whose parts may be backquoted, with comments between them, are adapted from MCP Toolbox's GoogleSQL lexer
// (googleapis/genai-toolbox, internal/tools/bigquery/bigquerycommon/table_name_parser.go:27-42, :270-292, :833-921,
// Apache-2.0; notice in third_party/genai-toolbox/NOTICE). The refusals made from its tokens are this package's own.
//
// Where the lexer cannot be sure, it errs towards seeing more code than BigQuery does, which can only refuse more:
// an unquoted identifier never holds a dash (so `a--'` is `a` and a comment, as BigQuery reads it), a raw or
// triple-quoted string ends at its first closing quote, and a string, comment or backquote left open is refused.

// ErrRefused marks a query refused before anything was sent, or by its dry run; nothing was billed.
var ErrRefused = errors.New("refused")

// refusedf is a refusal with what the caller is told.
func refusedf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// tokenKind is what a token is.
type tokenKind int

const (
	tokenWord   tokenKind = iota // an unquoted identifier or keyword
	tokenQuoted                  // a backquoted identifier, its text without the backquotes
	tokenString                  // a string or bytes literal, one token whatever it holds
	tokenNumber
	tokenPunct // one character: ; . ( ) , and the operators
)

type token struct {
	kind tokenKind
	text string
}

// lexState is the state of the lexer's machine.
type lexState int

const (
	stateNormal lexState = iota
	stateSingle
	stateDouble
	stateTripleSingle
	stateTripleDouble
	stateRawSingle
	stateRawDouble
	stateRawTripleSingle
	stateRawTripleDouble
	stateLineComment
	stateBlockComment
)

// tokenize reads GoogleSQL into tokens, dropping comments and whitespace and keeping each string literal whole.
func tokenize(sql string) ([]token, error) {
	runes := []rune(sql)
	state := stateNormal

	var (
		out   []token
		start int
	)

	for i := 0; i < len(runes); {
		c := runes[i]

		switch state {
		case stateNormal:
			switch {
			case unicode.IsSpace(c):
				i++
			case hasPrefix(runes, i, "--") || c == '#':
				state = stateLineComment
				i++
			case hasPrefix(runes, i, "/*"):
				state = stateBlockComment
				i += 2
			case c == '`':
				// A backslash escapes the next character, a backquote too, as in a string (Lexical structure,
				// "Quoted identifiers"); the name is compared once its escapes are decoded.
				end := i + 1
				for end < len(runes) && runes[end] != '`' {
					if runes[end] == '\\' {
						end++
					}

					end++
				}

				if end >= len(runes) {
					return nil, refusedf("a backquoted name is not closed")
				}

				out = append(out, token{kind: tokenQuoted, text: unescape(string(runes[i+1 : end]))})
				i = end + 1
			case c == '\'' || c == '"':
				state, start = stringState(runes, i, false), i
				i += quoteWidth(state)
			case unicode.IsLetter(c) || c == '_':
				end := i
				for end < len(runes) && (unicode.IsLetter(runes[end]) || unicode.IsDigit(runes[end]) || runes[end] == '_') {
					end++
				}

				word := string(runes[i:end])

				// r, b, rb and br before a quote make a raw or bytes literal of it.
				if end < len(runes) && (runes[end] == '\'' || runes[end] == '"') && isStringPrefix(word) {
					state, start = stringState(runes, end, strings.ContainsAny(word, "rR")), i
					i = end + quoteWidth(state)

					continue
				}

				out = append(out, token{kind: tokenWord, text: word})
				i = end
			case unicode.IsDigit(c) || (c == '.' && i+1 < len(runes) && unicode.IsDigit(runes[i+1])):
				end := i + 1
				for end < len(runes) {
					r := runes[end]
					if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' {
						end++

						continue
					}

					if (r == '+' || r == '-') && (runes[end-1] == 'e' || runes[end-1] == 'E') {
						end++

						continue
					}

					break
				}

				out = append(out, token{kind: tokenNumber, text: string(runes[i:end])})
				i = end
			default:
				out = append(out, token{kind: tokenPunct, text: string(c)})
				i++
			}
		case stateSingle, stateDouble:
			quote := '\''
			if state == stateDouble {
				quote = '"'
			}

			switch c {
			case '\\':
				i += 2
			case quote:
				i++
				out = append(out, token{kind: tokenString, text: string(runes[start:min(i, len(runes))])})
				state = stateNormal
			default:
				i++
			}
		case stateRawSingle, stateRawDouble:
			quote := '\''
			if state == stateRawDouble {
				quote = '"'
			}

			i++
			if c == quote {
				out = append(out, token{kind: tokenString, text: string(runes[start:i])})
				state = stateNormal
			}
		case stateTripleSingle, stateTripleDouble, stateRawTripleSingle, stateRawTripleDouble:
			closing := "'''"
			if state == stateTripleDouble || state == stateRawTripleDouble {
				closing = `"""`
			}

			if hasPrefix(runes, i, closing) {
				i += 3
				out = append(out, token{kind: tokenString, text: string(runes[start:i])})
				state = stateNormal
			} else {
				i++
			}
		case stateLineComment:
			if c == '\n' {
				state = stateNormal
			}

			i++
		case stateBlockComment:
			if hasPrefix(runes, i, "*/") {
				state = stateNormal
				i += 2
			} else {
				i++
			}
		}
	}

	switch state {
	case stateNormal, stateLineComment:
		return out, nil
	case stateBlockComment:
		return nil, refusedf("a /* comment is not closed")
	default:
		return nil, refusedf("a string is not closed")
	}
}

// stringState is the state a quote at runes[i] starts.
func stringState(runes []rune, i int, raw bool) lexState {
	switch {
	case hasPrefix(runes, i, "'''") && raw:
		return stateRawTripleSingle
	case hasPrefix(runes, i, `"""`) && raw:
		return stateRawTripleDouble
	case hasPrefix(runes, i, "'''"):
		return stateTripleSingle
	case hasPrefix(runes, i, `"""`):
		return stateTripleDouble
	case runes[i] == '\'' && raw:
		return stateRawSingle
	case runes[i] == '"' && raw:
		return stateRawDouble
	case runes[i] == '\'':
		return stateSingle
	default:
		return stateDouble
	}
}

func quoteWidth(s lexState) int {
	switch s {
	case stateTripleSingle, stateTripleDouble, stateRawTripleSingle, stateRawTripleDouble:
		return 3
	default:
		return 1
	}
}

func isStringPrefix(word string) bool {
	switch strings.ToLower(word) {
	case "r", "b", "rb", "br":
		return true
	default:
		return false
	}
}

func hasPrefix(r []rune, at int, prefix string) bool {
	p := []rune(prefix)
	if at+len(p) > len(r) {
		return false
	}

	for i, c := range p {
		if r[at+i] != c {
			return false
		}
	}

	return true
}

func indexRune(r []rune, from int, c rune) int {
	for i := from; i < len(r); i++ {
		if r[i] == c {
			return i
		}
	}

	return -1
}

// refusedKeywords are the keywords that make a query more than one read: a script, a function or procedure made or
// called, a variable, an export, a query sent to another database.
var refusedKeywords = []string{"CREATE", "DECLARE", "SET", "BEGIN", "CALL", "EXECUTE", "EXPORT", "EXTERNAL_QUERY"}

// refusedFunctions are the first names of the function paths that call a model or a remote service, which the
// bytes cap does not cover.
var refusedFunctions = []string{"ML", "AI", "VECTOR_SEARCH"}

// Gate checks a query before any request is sent: one GoogleSQL SELECT, with no script, no function made or
// called by name of a model, no export and no legacy SQL. A refusal wraps ErrRefused.
func Gate(sql string) error {
	// The directive is a # comment, gone once comments are dropped: it is looked for in the raw text first
	// (Metabase reads it there, bigquery_cloud_sdk.clj:953).
	if strings.Contains(strings.ToLower(sql), "#legacysql") {
		return refusedf("legacy SQL is not run; write GoogleSQL without a #legacySQL line")
	}

	tokens, err := tokenize(sql)
	if err != nil {
		return err
	}

	if len(tokens) > 0 && tokens[len(tokens)-1].text == ";" && tokens[len(tokens)-1].kind == tokenPunct {
		tokens = tokens[:len(tokens)-1]
	}

	if len(tokens) == 0 {
		return refusedf("the query is empty")
	}

	first := tokens[0]
	if !(first.kind == tokenWord && (strings.EqualFold(first.text, "SELECT") || strings.EqualFold(first.text, "WITH")) ||
		first.kind == tokenPunct && first.text == "(") {
		return refusedf("a query is one SELECT: it starts with SELECT, WITH or (")
	}

	for _, t := range tokens {
		switch {
		case t.kind == tokenPunct && t.text == ";":
			return refusedf("a query is one statement; a ; is allowed only at its end")
		case t.kind == tokenWord && containsFold(refusedKeywords, t.text):
			return refusedf("%s is not allowed: a query is one SELECT, with no script, function or export",
				strings.ToUpper(t.text))
		}
	}

	return refusedCalls(splitQuoted(tokens))
}

// splitQuoted spells a backquoted path as its parts: `AI.GENERATE` is AI . GENERATE, as BigQuery reads a
// backquoted name that holds dots as a path.
func splitQuoted(tokens []token) []token {
	out := make([]token, 0, len(tokens))

	for _, t := range tokens {
		if t.kind != tokenQuoted || !strings.Contains(t.text, ".") {
			out = append(out, t)

			continue
		}

		for i, part := range strings.Split(t.text, ".") {
			if i > 0 {
				out = append(out, token{kind: tokenPunct, text: "."})
			}

			out = append(out, token{kind: tokenQuoted, text: part})
		}
	}

	return out
}

func isName(t token) bool { return t.kind == tokenWord || t.kind == tokenQuoted }

func isPunct(t token, p string) bool { return t.kind == tokenPunct && t.text == p }

// refusedCalls refuses a call of a model or a remote service, wherever its name stands: AI.GENERATE(…),
// SAFE.AI.GENERATE(…), a chained call x.(AI.GENERATE)(…) whose multi-part name is in parentheses (GoogleSQL,
// "Chained function calls"), and VECTOR_SEARCH(…); and EXTERNAL_QUERY as any part of a name. A name is ML or AI
// used as the first part of a path whose end is followed, past closing parentheses, by an opening one.
func refusedCalls(tokens []token) error {
	for i, t := range tokens {
		if !isName(t) {
			continue
		}

		if strings.EqualFold(t.text, "EXTERNAL_QUERY") {
			return refusedf("EXTERNAL_QUERY is not allowed")
		}

		namespace := containsFold([]string{"ML", "AI"}, t.text) && i+2 < len(tokens) && isPunct(tokens[i+1], ".") &&
			isName(tokens[i+2])
		search := strings.EqualFold(t.text, "VECTOR_SEARCH")

		if !namespace && !search {
			continue
		}

		// The end of the path, then any closing parentheses of a chained call's name.
		next := i + 1
		for next+1 < len(tokens) && isPunct(tokens[next], ".") && isName(tokens[next+1]) {
			next += 2
		}

		for next < len(tokens) && isPunct(tokens[next], ")") {
			next++
		}

		if next < len(tokens) && isPunct(tokens[next], "(") {
			name := t.text
			if namespace {
				name += "." + tokens[i+2].text
			}

			return refusedf("%s is not allowed: it calls a model or a service the bytes cap does not cover",
				strings.ToUpper(name))
		}
	}

	return nil
}

var simpleEscapes = map[rune]rune{
	'a': '\a', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v', '\\': '\\', '?': '?', '"': '"',
	'\'': '\'', '`': '`',
}

// unescape decodes the escape sequences of a quoted name as GoogleSQL does (Lexical structure, "Escape sequences
// for string and bytes literals"): \a \b \f \n \r \t \v, a backslash before \ ? " ' or `, \ooo, \xhh,
// \uhhhh and \Uhhhhhhhh. A sequence it cannot read is kept as it is: BigQuery refuses it.
func unescape(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}

	in := []rune(s)

	var b strings.Builder

	for i := 0; i < len(in); i++ {
		if in[i] != '\\' || i+1 >= len(in) {
			b.WriteRune(in[i])

			continue
		}

		c := in[i+1]

		if r, ok := simpleEscapes[c]; ok {
			b.WriteRune(r)
			i++

			continue
		}

		width, base := 0, 16

		switch {
		case c >= '0' && c <= '7':
			width, base = 3, 8
		case c == 'x' || c == 'X':
			width = 2
		case c == 'u':
			width = 4
		case c == 'U':
			width = 8
		}

		digits := i + 2
		if base == 8 {
			digits = i + 1
		}

		if width == 0 || digits+width > len(in) {
			b.WriteRune(in[i])

			continue
		}

		n, err := strconv.ParseUint(string(in[digits:digits+width]), base, 32)
		if err != nil {
			b.WriteRune(in[i])

			continue
		}

		b.WriteRune(rune(n))
		i = digits + width - 1
	}

	return b.String()
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}

	return false
}
