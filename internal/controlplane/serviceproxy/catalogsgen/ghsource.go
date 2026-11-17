package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"reflect"
	"strconv"
	"strings"
	"unicode"
)

// ghFiles is gh's Go source, by file name, tests left out.
type ghFiles map[string][]byte

// ghRootFields reads the root fields of the GraphQL operations gh's source sends (decision 9c of #713's design):
//
//   - a struct gh passes to its client's Query or Mutate (api/client.go), whose fields are the operation's root
//     fields, named by their `graphql:"…"` tag or, without one, by the field's name in lower camel case, as
//     shurcooL/graphql names them (`var query struct { Repository … `graphql:"repository(owner: $owner, …)"` }`);
//   - a query gh writes itself, a string that starts with `query` or `mutation` (`query RepositoryInfo(…) {
//     repository(owner: $owner, name: $name) {%s} }`, api/queries_repo.go), read by its tokens, so that the
//     fmt verbs in it do not stop the read;
//   - a field called with an input object anywhere (`addProjectV2ItemById(input: $input_000)`), a mutation.
//
// Fragments and introspection fields are left out: a fragment's fields are not root fields, and the introspection
// fields are no field of the schema.
func ghRootFields(files ghFiles) ([]graphqlField, error) {
	fset := token.NewFileSet()
	parsed := map[string]*ast.File{}
	types := map[string]*ast.StructType{}

	for name, src := range files {
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}

		parsed[name] = f

		ast.Inspect(f, func(n ast.Node) bool {
			if spec, ok := n.(*ast.TypeSpec); ok {
				if st, ok := spec.Type.(*ast.StructType); ok {
					types[path.Dir(name)+"."+spec.Name.Name] = st
				}
			}

			return true
		})
	}

	found := map[graphqlField]bool{}
	add := func(typ, field string) {
		if field != "" && !strings.HasPrefix(field, "__") {
			found[graphqlField{Type: typ, Field: field}] = true
		}
	}

	for name, f := range parsed {
		for _, m := range inputField.FindAllSubmatch(files[name], -1) {
			add("mutation", string(m[1]))
		}

		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					if s, err := strconv.Unquote(n.Value); err == nil {
						for _, op := range operationRoots(s) {
							add(op.Type, op.Field)
						}
					}
				}
			case *ast.FuncDecl:
				if n.Body != nil {
					structCalls(n.Body, path.Dir(name), types, add)
				}
			}

			return true
		})
	}

	out := make([]graphqlField, 0, len(found))
	for f := range found {
		out = append(out, f)
	}

	sortFields(out)

	return out, nil
}

// structCalls finds, in a function's body, the structs passed by address to a client's Query or Mutate, and adds
// their fields as root fields.
func structCalls(body *ast.BlockStmt, dir string, types map[string]*ast.StructType, add func(typ, field string)) {
	vars := map[string]*ast.StructType{}

	ast.Inspect(body, func(n ast.Node) bool {
		if spec, ok := n.(*ast.ValueSpec); ok {
			if st := structOf(spec.Type, dir, types); st != nil {
				for _, id := range spec.Names {
					vars[id.Name] = st
				}
			}
		}

		return true
	})

	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		var typ string

		switch sel.Sel.Name {
		case "Query", "QueryWithContext":
			typ = "query"
		case "Mutate", "MutateWithContext":
			typ = "mutation"
		default:
			return true
		}

		for _, arg := range call.Args {
			ref, ok := arg.(*ast.UnaryExpr)
			if !ok || ref.Op != token.AND {
				continue
			}

			id, ok := ref.X.(*ast.Ident)
			if !ok || vars[id.Name] == nil {
				continue
			}

			for _, field := range vars[id.Name].Fields.List {
				for _, name := range field.Names {
					add(typ, fieldName(name.Name, field.Tag))
				}
			}
		}

		return true
	})
}

// structOf is the struct a variable's type is: a struct literal, or a struct type of the same package.
func structOf(expr ast.Expr, dir string, types map[string]*ast.StructType) *ast.StructType {
	switch t := expr.(type) {
	case *ast.StructType:
		return t
	case *ast.Ident:
		return types[dir+"."+t.Name]
	}

	return nil
}

// fieldName is the GraphQL field a struct field asks: its tag's field, past an alias, or its name in lower camel
// case. An inline fragment (`graphql:"... on User"`) is no field.
func fieldName(goName string, tag *ast.BasicLit) string {
	if tag != nil {
		raw, err := strconv.Unquote(tag.Value)
		if err == nil {
			if q, ok := reflect.StructTag(raw).Lookup("graphql"); ok {
				if strings.HasPrefix(strings.TrimSpace(q), "...") {
					return ""
				}

				tokens := graphqlTokens(q)
				if len(tokens) >= 3 && tokens[1] == ":" {
					return tokens[2]
				}

				if len(tokens) > 0 {
					return tokens[0]
				}

				return ""
			}
		}
	}

	return lowerCamel(goName)
}

// lowerCamel is shurcooL/graphql's name of a Go field: "Repository" is "repository", "URL" "url", "HTMLURL"
// "htmlurl" read word by word ("HTMLUrl" is "htmlUrl").
func lowerCamel(name string) string {
	r := []rune(name)

	for i := range r {
		if !unicode.IsUpper(r[i]) {
			break
		}

		if i > 0 && i+1 < len(r) && unicode.IsLower(r[i+1]) {
			break
		}

		r[i] = unicode.ToLower(r[i])
	}

	return string(r)
}

// operationRoots reads the root fields of the operations a GraphQL document gh writes asks, from its tokens: a
// string that does not start as an operation is not one.
func operationRoots(s string) []graphqlField {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "query") && !strings.HasPrefix(trimmed, "mutation") {
		return nil
	}

	tokens := graphqlTokens(trimmed)

	var out []graphqlField

	for i := 0; i < len(tokens); {
		typ := tokens[i]
		if typ != "query" && typ != "mutation" {
			// A fragment, or what follows an operation: skip to the next operation at the top level.
			i = skipDefinition(tokens, i)

			continue
		}

		i++
		// The operation's name and variables.
		for i < len(tokens) && tokens[i] != "{" {
			if tokens[i] == "(" {
				i = skipBalanced(tokens, i, "(", ")")

				continue
			}

			i++
		}

		if i >= len(tokens) {
			break
		}

		i++ // {

		for i < len(tokens) && tokens[i] != "}" {
			switch tok := tokens[i]; {
			case tok == "...":
				// A fragment spread or an inline fragment at the root: its fields are not read here.
				i++
				if i < len(tokens) && tokens[i] == "on" {
					i += 2
				} else {
					i++
				}
			case tok == "(":
				i = skipBalanced(tokens, i, "(", ")")
			case tok == "{":
				i = skipBalanced(tokens, i, "{", "}")
			case isName(tok):
				if i+2 < len(tokens) && tokens[i+1] == ":" && isName(tokens[i+2]) {
					i += 2
					tok = tokens[i]
				}

				out = append(out, graphqlField{Type: typ, Field: tok})
				i++
			default:
				i++
			}
		}

		i++ // }
	}

	return out
}

// skipDefinition skips a top-level definition that is no operation, up to the end of its selection set.
func skipDefinition(tokens []string, i int) int {
	for i < len(tokens) && tokens[i] != "{" {
		i++
	}

	if i >= len(tokens) {
		return i
	}

	return skipBalanced(tokens, i, "{", "}")
}

// skipBalanced skips from an opening token to past its closing one.
func skipBalanced(tokens []string, i int, open, closing string) int {
	depth := 0

	for ; i < len(tokens); i++ {
		switch tokens[i] {
		case open:
			depth++
		case closing:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}

	return i
}

func isName(tok string) bool {
	if tok == "" {
		return false
	}

	for i, r := range tok {
		if r != '_' && !unicode.IsLetter(r) && (i == 0 || !unicode.IsDigit(r)) {
			return false
		}
	}

	return true
}

// graphqlTokens splits a GraphQL document into names, punctuation and strings. A fmt verb (%s, %[1]s) is one token
// of its own, never a name; a comment and commas are dropped.
func graphqlTokens(s string) []string {
	var out []string

	for i := 0; i < len(s); {
		c := s[i]

		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',':
			i++
		case c == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}

				j++
			}

			out = append(out, `""`)
			i = j + 1
		case c == '%':
			j := i + 1
			for j < len(s) && !unicode.IsLetter(rune(s[j])) && s[j] != '%' {
				j++
			}

			out = append(out, "%")
			i = j + 1
		case strings.HasPrefix(s[i:], "..."):
			out = append(out, "...")
			i += 3
		case c == '_' || unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c)) || c == '$':
			j := i
			for j < len(s) && (s[j] == '_' || s[j] == '$' || unicode.IsLetter(rune(s[j])) ||
				unicode.IsDigit(rune(s[j]))) {
				j++
			}

			out = append(out, s[i:j])
			i = j
		default:
			out = append(out, string(c))
			i++
		}
	}

	return out
}
