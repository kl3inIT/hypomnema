package main

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
)

// gitlabFile is the declaration GitLab's catalog embeds (serviceproxy/gitlab.go).
const gitlabFile = "gitlab_operations.json"

// clientGoModule is the Go module of client-go, the client glab is built on.
const clientGoModule = "gitlab.com/gitlab-org/api/client-go/v3"

// gitlabOperation is one request client-go can make: its HTTP method and its route, the template client-go
// registers with route("…") (route.go), relative to /api/v4/, a parameter written as a fmt verb.
type gitlabOperation struct {
	Method string `json:"method"`
	Route  string `json:"route"`
}

// gitlabDeclaration is gitlab_operations.json: the glab version it was made for, the client-go version glab pins,
// and every operation.
type gitlabDeclaration struct {
	Glab       string            `json:"glab"`
	ClientGo   string            `json:"client_go"`
	Operations []gitlabOperation `json:"operations"`
}

func generateGitLab(ctx context.Context, client *http.Client, src sources, root *os.Root, out io.Writer) error {
	gl := src.GitLab

	mod, err := fetch(ctx, client, gl.CLI.source)
	if err != nil {
		return err
	}

	pinned, err := requiredVersion(mod, clientGoModule)
	if err != nil {
		return fmt.Errorf("glab %s's go.mod: %w", gl.CLI.Version, err)
	}

	// glab sends what client-go makes, so the client is read at the version glab pins (decision 9c of #713).
	if pinned != gl.Client.Version {
		return fmt.Errorf("glab %s pins client-go %s, but %s names %s: pin the client's module at %s", gl.CLI.Version,
			pinned, sourcesFile, gl.Client.Version, pinned)
	}

	module, err := fetch(ctx, client, gl.Client.source)
	if err != nil {
		return err
	}

	files, err := clientGoFiles(module)
	if err != nil {
		return fmt.Errorf("client-go %s: %w", gl.Client.Version, err)
	}

	ops, unnamed, err := clientGoOperations(files)
	if err != nil {
		return fmt.Errorf("client-go %s: %w", gl.Client.Version, err)
	}

	declaration := gitlabDeclaration{Glab: gl.CLI.Version, ClientGo: gl.Client.Version, Operations: ops}

	written, err := encodeGitLab(declaration)
	if err != nil {
		return err
	}

	before, err := root.ReadFile(gitlabFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := root.WriteFile(gitlabFile, written, 0o644); err != nil {
		return err
	}

	var report strings.Builder

	fmt.Fprintf(&report, "%s: %d operations of client-go %s, which glab %s pins\n", gitlabFile, len(ops),
		gl.Client.Version, gl.CLI.Version)

	// A route registered that no call names with its method is said, not guessed: a request to it asks as unlisted.
	for _, r := range unnamed {
		fmt.Fprintf(&report, "  route %q is registered, but no call names its method\n", r)
	}

	if err := printChanges(&report, serviceproxy.GitLabActionIDs, before, written); err != nil {
		return err
	}

	_, err = io.WriteString(out, report.String())

	return err
}

// requiredVersion reads the version a go.mod requires of a module.
func requiredVersion(mod []byte, module string) (string, error) {
	for line := range strings.SplitSeq(string(mod), "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require "))
		if len(fields) >= 2 && fields[0] == module {
			return fields[1], nil
		}
	}

	return "", fmt.Errorf("no requirement of %s", module)
}

// clientGoFiles reads the Go source of client-go's package, the module's root directory, from the module's zip as
// the Go module proxy serves it (golang.org/ref/mod#zip-files: every file under "<module>@<version>/"). Tests and
// the packages under it are left out.
func clientGoFiles(module []byte) (map[string][]byte, error) {
	z, err := zip.NewReader(bytes.NewReader(module), int64(len(module)))
	if err != nil {
		return nil, err
	}

	files := map[string][]byte{}

	for _, f := range z.File {
		name, ok := strings.CutPrefix(f.Name, clientGoModule+"@")
		if !ok {
			continue
		}

		// "<version>/<file>": a file of the root package has no other slash.
		if _, rest, _ := strings.Cut(name, "/"); strings.Contains(rest, "/") || !goSource(rest) {
			continue
		}

		r, err := f.Open()
		if err != nil {
			return nil, err
		}

		src, err := io.ReadAll(io.LimitReader(r, maxDeclaration))
		_ = r.Close()

		if err != nil {
			return nil, err
		}

		files[f.Name] = src
	}

	if len(files) == 0 {
		return nil, errors.New("no Go file in the module")
	}

	return files, nil
}

// clientGoOperations reads every method and route client-go calls (decision 3 of #713's design):
//
//   - a route is a package variable registered with route("…"), so that "Routes and MatchRoute describe the whole
//     client" (route.go);
//   - a call names the route with withPath(route…, …) and its method with withMethod(http.Method…) beside it, GET
//     when it names none, as do's own default (request_handler.go);
//   - a few calls build a request themselves, client.NewRequest(http.Method…, path, …), the path a string, a
//     fmt.Sprintf of one, a route, or a variable given one of those in the same function.
//
// unnamed are the routes registered that no call names with a method.
func clientGoOperations(files map[string][]byte) (ops []gitlabOperation, unnamed []string, err error) {
	fset := token.NewFileSet()
	parsed := make([]*ast.File, 0, len(files))
	routes := map[string]string{}

	for _, name := range slices.Sorted(maps.Keys(files)) {
		f, err := parser.ParseFile(fset, name, files[name], parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, err
		}

		parsed = append(parsed, f)

		ast.Inspect(f, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}

			for i, value := range spec.Values {
				if template, ok := routeCall(value); ok && i < len(spec.Names) {
					routes[spec.Names[i].Name] = template
				}
			}

			return true
		})
	}

	if len(routes) == 0 {
		return nil, nil, errors.New("no route registered with route(…)")
	}

	found := map[gitlabOperation]bool{}

	for _, f := range parsed {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			for _, op := range functionOperations(fn.Body, routes) {
				found[op] = true
			}
		}
	}

	named := map[string]bool{}

	for op := range found {
		ops = append(ops, op)
		named[op.Route] = true
	}

	slices.SortFunc(ops, func(a, b gitlabOperation) int {
		return cmp.Or(cmp.Compare(a.Route, b.Route), cmp.Compare(a.Method, b.Method))
	})

	for _, template := range routes {
		if !named[template] {
			unnamed = append(unnamed, template)
		}
	}

	slices.Sort(unnamed)

	return ops, unnamed, nil
}

// functionOperations reads the operations of one function's body.
func functionOperations(body *ast.BlockStmt, routes map[string]string) []gitlabOperation {
	// What a variable of the function was given: a path a NewRequest call may name by the variable, and the
	// options gathered in a slice before do is given them (reqOpts := []doOption{…}; reqOpts = append(reqOpts, …),
	// labels.go), which are read together, so that a method and a path appended apart make one operation.
	paths := map[string]string{}
	gathered := map[string][]ast.Expr{}
	inSlice := map[ast.Node]bool{}

	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}

		for i, rhs := range assign.Rhs {
			id, ok := assign.Lhs[min(i, len(assign.Lhs)-1)].(*ast.Ident)
			if !ok {
				continue
			}

			switch r := rhs.(type) {
			case *ast.CompositeLit:
				gathered[id.Name] = append(gathered[id.Name], r.Elts...)
				inSlice[r] = true
			case *ast.CallExpr:
				if calleeName(r) == "append" && len(r.Args) > 0 {
					if to, ok := r.Args[0].(*ast.Ident); ok && to.Name == id.Name {
						gathered[id.Name] = append(gathered[id.Name], r.Args[1:]...)
						inSlice[r] = true

						continue
					}
				}
			}

			if _, seen := paths[id.Name]; seen {
				continue
			}

			if p, ok := pathOf(rhs, routes, nil); ok {
				paths[id.Name] = p
			}
		}

		return true
	})

	var out []gitlabOperation

	for _, options := range gathered {
		out = append(out, optionsOperations(options, routes)...)
	}

	ast.Inspect(body, func(n ast.Node) bool {
		if inSlice[n] {
			return false
		}

		switch n := n.(type) {
		case *ast.CallExpr:
			if calleeName(n) == "NewRequest" && len(n.Args) >= 2 {
				if method, ok := httpMethod(n.Args[0]); ok {
					if p, ok := pathOf(n.Args[1], routes, paths); ok {
						out = append(out, gitlabOperation{Method: method, Route: p})
					}
				}

				return true
			}

			out = append(out, optionsOperations(n.Args, routes)...)
		case *ast.CompositeLit:
			out = append(out, optionsOperations(n.Elts, routes)...)
		}

		return true
	})

	return out
}

// optionsOperations reads the operations of a call's options: withPath names a route, withMethod the method, GET
// when none does. Options gathered in a slice may name two routes, one or the other taken at run time.
func optionsOperations(options []ast.Expr, routes map[string]string) []gitlabOperation {
	method := http.MethodGet

	var templates []string

	for _, arg := range options {
		option, ok := arg.(*ast.CallExpr)
		if !ok || len(option.Args) == 0 {
			continue
		}

		switch calleeName(option) {
		case "withMethod":
			if m, ok := httpMethod(option.Args[0]); ok {
				method = m
			}
		case "withPath":
			if id, ok := option.Args[0].(*ast.Ident); ok && routes[id.Name] != "" {
				templates = append(templates, routes[id.Name])
			}
		}
	}

	out := make([]gitlabOperation, 0, len(templates))
	for _, template := range templates {
		out = append(out, gitlabOperation{Method: method, Route: template})
	}

	return out
}

// routeCall reads route("template").
func routeCall(expr ast.Expr) (string, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok || calleeName(call) != "route" || len(call.Args) != 1 {
		return "", false
	}

	return stringLiteral(call.Args[0])
}

// pathOf reads a request's path: a string, a fmt.Sprintf of one, a route, or a variable of the function given one.
func pathOf(expr ast.Expr, routes, vars map[string]string) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return stringLiteral(e)
	case *ast.Ident:
		if template, ok := routes[e.Name]; ok {
			return template, true
		}

		p, ok := vars[e.Name]

		return p, ok
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sprintf" && len(e.Args) > 0 {
			return pathOf(e.Args[0], routes, nil)
		}
	}

	return "", false
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}

	s, err := strconv.Unquote(lit.Value)

	return s, err == nil && s != ""
}

// httpMethod reads http.MethodGet and its kind.
func httpMethod(expr ast.Expr) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || !strings.HasPrefix(sel.Sel.Name, "Method") {
		return "", false
	}

	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "http" {
		return "", false
	}

	return strings.ToUpper(strings.TrimPrefix(sel.Sel.Name, "Method")), true
}

// encodeGitLab writes the declaration one operation per line, so that a regeneration's diff reads line by line.
func encodeGitLab(d gitlabDeclaration) ([]byte, error) {
	var b bytes.Buffer

	fmt.Fprintf(&b, "{\n  \"glab\": %s,\n  \"client_go\": %s,\n  \"operations\": [\n", strconv.Quote(d.Glab),
		strconv.Quote(d.ClientGo))

	if err := lines(&b, d.Operations); err != nil {
		return nil, err
	}

	b.WriteString("  ]\n}\n")

	return b.Bytes(), nil
}
