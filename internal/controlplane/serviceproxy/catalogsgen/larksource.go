package main

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// larkShortcut is one of lark-cli's shortcut commands (shortcuts/common/types.go, Shortcut): its service and
// command, the risk it declares, the scopes it checks before it runs for a person (user) and for the app (app), and
// the API calls it makes.
type larkShortcut struct {
	Service, Command, Risk string
	// User and App are the scopes the command needs as each identity, all of them, or nil when it names none.
	User, App []string
	Calls     []larkCall
	// at is where the command is declared, which orders the commands.
	at string
}

// larkCall is one call a shortcut makes: its HTTP method and the path, each parameter "{name}".
type larkCall struct {
	Method, Path string
}

// larkPackage is one Go package of lark-cli's shortcuts: its functions, its methods by name, its constants, and
// what each of its files imports.
type larkPackage struct {
	dir     string
	funcs   map[string]*ast.FuncDecl
	methods map[string][]*ast.FuncDecl
	values  map[string]ast.Expr
}

// larkProgram is lark-cli's shortcuts, read from source.
type larkProgram struct {
	fset     *token.FileSet
	packages map[string]*larkPackage
	// imports names, per file, the directory each import of a shortcut package is ("common" →
	// "shortcuts/common"), and the import path of every other one ("http" → "net/http").
	imports map[*ast.File]map[string]string
	files   map[*ast.File]*larkPackage
	sinks   map[string]larkSink
}

// larkSink is a function that sends a request: which of its arguments is the HTTP method (or the method it always
// sends, at -1) and which the path.
type larkSink struct {
	methodArg int
	method    string
	pathArg   int
}

// larkModule is the import path of lark-cli's own packages.
const larkModule = "github.com/larksuite/cli/"

// larkPrimitiveSinks are the ways a shortcut sends a request (shortcuts/common/runner.go, typed_api.go), by name:
// RuntimeContext's CallAPITyped, RawAPI, callRaw and DoAPIJSONTyped, and the hosted commands' DoHostedAPIJSON,
// doHostedAPIJSONWithOptions and CallHostedAPI. DoAPI, DoAPIAsBot and DoAPIStream take a larkcore.ApiReq, read
// where it is written; a dry run's GET, POST, PUT, PATCH and DELETE (internal/cmdutil/dryrun.go) are read by their
// name.
var larkPrimitiveSinks = map[string]larkSink{
	"CallAPITyped":               {methodArg: 0, pathArg: 1},
	"RawAPI":                     {methodArg: 0, pathArg: 1},
	"callRaw":                    {methodArg: 0, pathArg: 1},
	"DoAPIJSONTyped":             {methodArg: 0, pathArg: 1},
	"DoHostedAPIJSON":            {methodArg: 2, pathArg: 3},
	"doHostedAPIJSONWithOptions": {methodArg: 2, pathArg: 3},
	"CallHostedAPI":              {methodArg: 2, pathArg: 3},
}

// larkDryRunVerbs are DryRunAPI's methods, each the HTTP method it describes.
var larkDryRunVerbs = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}

// larkShortcuts reads every shortcut command of lark-cli's source (files named shortcuts/<group>/….go, tests left
// out) with the calls it makes: those its dry run describes, and those its Validate, DryRun and Execute send,
// following each function they call, in its package or another of the shortcuts' (issue #742). A call whose path is
// not written in the source for certain (built at run time, or a part of a segment) is left out: the catalog then
// answers it by its service and its verb.
func larkShortcuts(files map[string][]byte) ([]larkShortcut, error) {
	p := &larkProgram{
		fset: token.NewFileSet(), packages: map[string]*larkPackage{},
		imports: map[*ast.File]map[string]string{}, files: map[*ast.File]*larkPackage{},
	}

	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := p.parse(name, files[name]); err != nil {
			return nil, err
		}
	}

	p.sinks = p.findSinks()

	var out []larkShortcut

	for _, f := range p.sortedFiles() {
		pkg := p.files[f]

		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}

			// []common.Shortcut{{…}, {…}}: the elements have no type of their own.
			if arr, ok := lit.Type.(*ast.ArrayType); ok && p.isShortcutType(f, pkg, arr.Elt) {
				for _, e := range lit.Elts {
					if el, ok := e.(*ast.CompositeLit); ok && el.Type == nil {
						out = append(out, p.shortcut(f, pkg, el, enclosingFunc(f, el)))
					}
				}

				return true
			}

			if p.isShortcutType(f, pkg, lit.Type) {
				out = append(out, p.shortcut(f, pkg, lit, enclosingFunc(f, lit)))
			}

			return true
		})
	}

	// A command is known by its service and name; one without either is no command (a template filled in later).
	out = slices.DeleteFunc(out, func(s larkShortcut) bool { return s.Service == "" || s.Command == "" })

	if len(out) == 0 {
		return nil, fmt.Errorf("no shortcut command in the source")
	}

	return out, nil
}

func (p *larkProgram) parse(name string, src []byte) error {
	f, err := parser.ParseFile(p.fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return err
	}

	dir := path.Dir(name)

	pkg := p.packages[dir]
	if pkg == nil {
		pkg = &larkPackage{
			dir: dir, funcs: map[string]*ast.FuncDecl{}, methods: map[string][]*ast.FuncDecl{},
			values: map[string]ast.Expr{},
		}
		p.packages[dir] = pkg
	}

	p.files[f] = pkg

	imports := map[string]string{}

	for _, spec := range f.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}

		local := path.Base(importPath)
		if spec.Name != nil {
			local = spec.Name.Name
		}

		if rest, ok := strings.CutPrefix(importPath, larkModule); ok {
			imports[local] = rest
		} else {
			imports[local] = importPath
		}
	}

	p.imports[f] = imports

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}

			if d.Recv == nil {
				pkg.funcs[d.Name.Name] = d
			} else {
				pkg.methods[d.Name.Name] = append(pkg.methods[d.Name.Name], d)
			}
		case *ast.GenDecl:
			if d.Tok != token.CONST && d.Tok != token.VAR {
				continue
			}

			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Values) != len(vs.Names) {
					continue
				}

				for i, n := range vs.Names {
					pkg.values[n.Name] = vs.Values[i]
				}
			}
		}
	}

	return nil
}

// sortedFiles are the parsed files in the order of their names, so that the commands come out in a fixed order.
func (p *larkProgram) sortedFiles() []*ast.File {
	out := make([]*ast.File, 0, len(p.files))
	for f := range p.files {
		out = append(out, f)
	}

	slices.SortFunc(out, func(a, b *ast.File) int {
		return cmp.Compare(p.fset.File(a.Pos()).Name(), p.fset.File(b.Pos()).Name())
	})

	return out
}

// isShortcutType is whether a type is common.Shortcut, or Shortcut in package common itself.
func (p *larkProgram) isShortcutType(f *ast.File, pkg *larkPackage, expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.SelectorExpr:
		x, ok := t.X.(*ast.Ident)

		return ok && t.Sel.Name == "Shortcut" && p.imports[f][x.Name] == "shortcuts/common"
	case *ast.Ident:
		return t.Name == "Shortcut" && pkg.dir == "shortcuts/common"
	}

	return false
}

// enclosingFunc is the function declaration a node is in, or nil at the top level.
func enclosingFunc(f *ast.File, n ast.Node) *ast.FuncDecl {
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Pos() <= n.Pos() && n.End() <= fn.End() {
			return fn
		}
	}

	return nil
}

// shortcut reads one Shortcut literal.
func (p *larkProgram) shortcut(f *ast.File, pkg *larkPackage, lit *ast.CompositeLit, fn *ast.FuncDecl) larkShortcut {
	s := larkShortcut{at: p.fset.Position(lit.Pos()).String()}
	fields := map[string]ast.Expr{}

	for _, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok {
				fields[key.Name] = kv.Value
			}
		}
	}

	var body *ast.BlockStmt
	if fn != nil {
		body = fn.Body
	}

	first := func(name string) string {
		values := p.stringValues(pkg, body, fields[name])
		if len(values) == 0 {
			return ""
		}

		return values[0]
	}

	s.Service, s.Command = first("Service"), first("Command")
	s.Risk = p.risk(pkg, body, fields["Risk"])

	scopes := p.stringList(pkg, body, fields["Scopes"])
	s.User, s.App = scopes, scopes

	if user := p.stringList(pkg, body, fields["UserScopes"]); len(user) > 0 {
		s.User = user
	}

	if bot := p.stringList(pkg, body, fields["BotScopes"]); len(bot) > 0 {
		s.App = bot
	}

	w := &larkWalk{p: p, seen: map[*ast.BlockStmt]bool{}}

	for _, hook := range []string{"Validate", "DryRun", "Execute"} {
		if e, ok := fields[hook]; ok {
			w.reach(f, pkg, body, e)
		}
	}

	s.Calls = w.calls

	return s
}

// The risks lark-cli gives a command, from the least to the most careful (types.go: empty is read).
var larkRisks = []string{"read", "write", "high-risk-write"}

// risk is the risk a command declares. One set at run time (sheets' commands take it as a parameter) is the most
// careful it can be: what cannot be read for certain asks (docs/conventions.md, fail closed).
func (p *larkProgram) risk(pkg *larkPackage, body *ast.BlockStmt, expr ast.Expr) string {
	if expr == nil {
		return larkRisks[0]
	}

	values := p.stringValues(pkg, body, expr)
	if len(values) == 0 {
		return larkRisks[len(larkRisks)-1]
	}

	highest := 0

	for _, v := range values {
		i := slices.Index(larkRisks, v)
		if i < 0 {
			return larkRisks[len(larkRisks)-1]
		}

		highest = max(highest, i)
	}

	return larkRisks[highest]
}

// stringValues are the values a string expression can have: a literal, a constant, or a local variable of the
// function it is in, each of its assignments. Nil when one is not a string written in the source.
func (p *larkProgram) stringValues(pkg *larkPackage, body *ast.BlockStmt, expr ast.Expr) []string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if s, ok := stringLiteral(e); ok {
			return []string{s}
		}
	case *ast.Ident:
		if assigned := localAssignments(body, e.Name); len(assigned) > 0 {
			var out []string

			for _, a := range assigned {
				values := p.stringValues(pkg, body, a)
				if len(values) == 0 {
					return nil
				}

				out = append(out, values...)
			}

			return out
		}

		if v, ok := pkg.values[e.Name]; ok {
			return p.stringValues(pkg, nil, v)
		}
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return nil
		}

		left, right := p.stringValues(pkg, body, e.X), p.stringValues(pkg, body, e.Y)
		if len(left) != 1 || len(right) != 1 {
			return nil
		}

		return []string{left[0] + right[0]}
	}

	return nil
}

// stringList is a []string{…} of strings written in the source; nil when it is anything else.
func (p *larkProgram) stringList(pkg *larkPackage, body *ast.BlockStmt, expr ast.Expr) []string {
	if id, ok := expr.(*ast.Ident); ok {
		if v, ok := pkg.values[id.Name]; ok {
			return p.stringList(pkg, nil, v)
		}
	}

	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil
	}

	var out []string

	for _, e := range lit.Elts {
		values := p.stringValues(pkg, body, e)
		if len(values) != 1 {
			return nil
		}

		out = append(out, values[0])
	}

	return out
}

// localAssignments are the values a function assigns to a variable of that name: `x := …`, `x = …`, `var x = …`.
func localAssignments(body *ast.BlockStmt, name string) []ast.Expr {
	if body == nil {
		return nil
	}

	var out []ast.Expr

	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			if len(s.Lhs) != len(s.Rhs) {
				return true
			}

			for i, l := range s.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == name {
					out = append(out, s.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			if len(s.Values) != len(s.Names) {
				return true
			}

			for i, id := range s.Names {
				if id.Name == name {
					out = append(out, s.Values[i])
				}
			}
		}

		return true
	})

	return out
}

// larkWalk follows a command's hooks through the functions they call, collecting the requests sent on the way.
type larkWalk struct {
	p     *larkProgram
	seen  map[*ast.BlockStmt]bool
	calls []larkCall
}

// reach follows a hook: a function literal, or a function named by it.
func (w *larkWalk) reach(f *ast.File, pkg *larkPackage, outer *ast.BlockStmt, e ast.Expr) {
	switch h := e.(type) {
	case *ast.FuncLit:
		// A literal's own variables and those of the function it is written in.
		w.body(f, pkg, h.Body, outer)
	case *ast.Ident, *ast.SelectorExpr:
		for _, d := range w.p.callees(f, pkg, h) {
			w.decl(d)
		}
	}
}

func (w *larkWalk) decl(d larkDecl) {
	w.body(d.file, d.pkg, d.fn.Body, nil)
}

// body walks a function's body: every request it sends, and every function of the shortcuts it names, called or
// passed on.
func (w *larkWalk) body(f *ast.File, pkg *larkPackage, body, outer *ast.BlockStmt) {
	if body == nil || w.seen[body] {
		return
	}

	w.seen[body] = true

	scope := func(name string) []ast.Expr {
		if a := localAssignments(body, name); len(a) > 0 {
			return a
		}

		return localAssignments(outer, name)
	}

	ast.Inspect(body, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.CallExpr:
			if c, ok := w.p.callOf(f, pkg, scope, e); ok {
				w.calls = append(w.calls, c)
			}

			for _, d := range w.p.callees(f, pkg, e.Fun) {
				w.decl(d)
			}
		case *ast.CompositeLit:
			if c, ok := w.p.apiReq(f, pkg, scope, e); ok {
				w.calls = append(w.calls, c)
			}
		case *ast.Ident:
			// A function passed on rather than called (a callback).
			if d, ok := pkg.funcs[e.Name]; ok {
				w.decl(larkDecl{file: w.p.fileOf(d), pkg: pkg, fn: d})
			}
		}

		return true
	})
}

// larkDecl is a function of the shortcuts with the file it is in.
type larkDecl struct {
	file *ast.File
	pkg  *larkPackage
	fn   *ast.FuncDecl
}

func (p *larkProgram) fileOf(fn *ast.FuncDecl) *ast.File {
	for f := range p.files {
		if f.Pos() <= fn.Pos() && fn.End() <= f.End() {
			return f
		}
	}

	return nil
}

// callees are the functions an expression names: f, pkg.F of another package of the shortcuts, or x.M, each method
// of that name in the package (the type of x is not read: more callers can only make a call's risk higher).
func (p *larkProgram) callees(f *ast.File, pkg *larkPackage, fun ast.Expr) []larkDecl {
	var out []larkDecl

	add := func(in *larkPackage, fn *ast.FuncDecl) {
		if file := p.fileOf(fn); file != nil {
			out = append(out, larkDecl{file: file, pkg: in, fn: fn})
		}
	}

	switch e := fun.(type) {
	case *ast.Ident:
		if fn, ok := pkg.funcs[e.Name]; ok {
			add(pkg, fn)
		}
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			if other, ok := p.packages[p.imports[f][x.Name]]; ok {
				if fn, ok := other.funcs[e.Sel.Name]; ok {
					add(other, fn)
				}

				return out
			}
		}

		for _, fn := range pkg.methods[e.Sel.Name] {
			add(pkg, fn)
		}
	}

	return out
}

// sinkOf is the function a call calls when it sends a request.
func (p *larkProgram) sinkOf(f *ast.File, pkg *larkPackage, fun ast.Expr) (larkSink, bool) {
	switch e := fun.(type) {
	case *ast.Ident:
		s, ok := p.sinks[pkg.dir+"."+e.Name]

		return s, ok
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			if dir, ok := p.imports[f][x.Name]; ok && p.packages[dir] != nil {
				if s, ok := larkPrimitiveSinks[e.Sel.Name]; ok && dir == "shortcuts/common" {
					return s, true
				}

				s, ok := p.sinks[dir+"."+e.Sel.Name]

				return s, ok
			}
		}

		if s, ok := larkPrimitiveSinks[e.Sel.Name]; ok {
			return s, true
		}

		if slices.Contains(larkDryRunVerbs, e.Sel.Name) {
			return larkSink{methodArg: -1, method: e.Sel.Name, pathArg: 0}, true
		}

		s, ok := p.sinks[pkg.dir+".("+e.Sel.Name]

		return s, ok
	}

	return larkSink{}, false
}

// callOf reads a call that sends a request, when its method and path are written in the source.
func (p *larkProgram) callOf(f *ast.File, pkg *larkPackage, scope func(string) []ast.Expr, call *ast.CallExpr,
) (larkCall, bool) {
	s, ok := p.sinkOf(f, pkg, call.Fun)
	if !ok || s.pathArg >= len(call.Args) || s.methodArg >= len(call.Args) {
		return larkCall{}, false
	}

	method := s.method
	if s.methodArg >= 0 {
		if method, ok = p.methodOf(pkg, scope, call.Args[s.methodArg]); !ok {
			return larkCall{}, false
		}
	}

	pth, ok := p.pathOf(f, pkg, scope, call.Args[s.pathArg])
	if !ok {
		return larkCall{}, false
	}

	return larkCall{Method: method, Path: pth}, true
}

// apiReq reads a larkcore.ApiReq{HttpMethod: …, ApiPath: …} whose method and path are written in the source.
func (p *larkProgram) apiReq(f *ast.File, pkg *larkPackage, scope func(string) []ast.Expr, lit *ast.CompositeLit,
) (larkCall, bool) {
	method, pth := apiReqFields(lit)
	if method == nil || pth == nil {
		return larkCall{}, false
	}

	m, ok := p.methodOf(pkg, scope, method)
	if !ok {
		return larkCall{}, false
	}

	pt, ok := p.pathOf(f, pkg, scope, pth)
	if !ok {
		return larkCall{}, false
	}

	return larkCall{Method: m, Path: pt}, true
}

func apiReqFields(lit *ast.CompositeLit) (method, pth ast.Expr) {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "ApiReq" {
		return nil, nil
	}

	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		switch key, _ := kv.Key.(*ast.Ident); {
		case key == nil:
		case key.Name == "HttpMethod":
			method = kv.Value
		case key.Name == "ApiPath":
			pth = kv.Value
		}
	}

	return method, pth
}

// methodOf reads an HTTP method: "POST", http.MethodPost, or a constant or variable of one.
func (p *larkProgram) methodOf(pkg *larkPackage, scope func(string) []ast.Expr, expr ast.Expr) (string, bool) {
	if m, ok := httpMethod(expr); ok {
		return m, true
	}

	if id, ok := expr.(*ast.Ident); ok {
		if assigned := scope(id.Name); len(assigned) == 1 {
			return p.methodOf(pkg, func(string) []ast.Expr { return nil }, assigned[0])
		}
	}

	values := p.stringValues(pkg, nil, expr)
	if len(values) != 1 || !slices.Contains(larkDryRunVerbs, strings.ToUpper(values[0])) {
		return "", false
	}

	return strings.ToUpper(values[0]), true
}

// larkVerb is a verb of fmt.Sprintf's format.
var larkVerb = regexp.MustCompile(`%[sdvq]`)

// pathOf reads a request's path written in the source: a literal or a constant, a variable assigned once, one
// joined with + or by fmt.Sprintf, whose every part that is not written is a whole segment (a parameter, named
// after the expression that fills it).
func (p *larkProgram) pathOf(f *ast.File, pkg *larkPackage, scope func(string) []ast.Expr, expr ast.Expr,
) (string, bool) {
	parts, ok := p.pathParts(f, pkg, scope, expr, 0)
	if !ok {
		return "", false
	}

	var b strings.Builder

	for i, part := range parts {
		if !part.param {
			b.WriteString(part.text)

			continue
		}

		// A parameter fills a whole segment: after a slash, and before a slash, a query or the end.
		before := b.String()
		after := ""

		for _, next := range parts[i+1:] {
			if !next.param {
				after = next.text

				break
			}

			return "", false
		}

		if !strings.HasSuffix(before, "/") || (after != "" && after[0] != '/' && after[0] != '?') {
			return "", false
		}

		b.WriteString("{" + part.text + "}")
	}

	return b.String(), true
}

// larkPart is a piece of a path: text written in the source, or a parameter with its name.
type larkPart struct {
	text  string
	param bool
}

func (p *larkProgram) pathParts(f *ast.File, pkg *larkPackage, scope func(string) []ast.Expr, expr ast.Expr,
	depth int,
) ([]larkPart, bool) {
	if depth > 8 {
		return nil, false
	}

	switch e := expr.(type) {
	case *ast.BasicLit:
		if s, ok := stringLiteral(e); ok {
			return []larkPart{{text: s}}, true
		}
	case *ast.Ident:
		if assigned := scope(e.Name); len(assigned) > 0 {
			if len(assigned) != 1 {
				return nil, false
			}

			return p.pathParts(f, pkg, scope, assigned[0], depth+1)
		}

		if v, ok := pkg.values[e.Name]; ok {
			return p.pathParts(f, pkg, func(string) []ast.Expr { return nil }, v, depth+1)
		}
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return nil, false
		}

		left, ok := p.pathParts(f, pkg, scope, e.X, depth+1)
		if !ok {
			return nil, false
		}

		right, ok := p.pathParts(f, pkg, scope, e.Y, depth+1)
		if !ok {
			right = []larkPart{{text: paramName(e.Y), param: true}}
		}

		return append(left, right...), true
	case *ast.CallExpr:
		if !p.isSprintf(f, e) || len(e.Args) == 0 {
			return nil, false
		}

		format, ok := p.pathParts(f, pkg, scope, e.Args[0], depth+1)
		if !ok || len(format) != 1 || format[0].param {
			return nil, false
		}

		text := format[0].text
		verbs := larkVerb.FindAllStringIndex(text, -1)

		if len(verbs) != len(e.Args)-1 || strings.Count(text, "%") != len(verbs) {
			return nil, false
		}

		var out []larkPart

		last := 0

		for i, v := range verbs {
			out = append(out, larkPart{text: text[last:v[0]]}, larkPart{text: paramName(e.Args[i+1]), param: true})
			last = v[1]
		}

		return append(out, larkPart{text: text[last:]}), true
	}

	return nil, false
}

func (p *larkProgram) isSprintf(f *ast.File, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Sprintf" {
		return false
	}

	x, ok := sel.X.(*ast.Ident)

	return ok && p.imports[f][x.Name] == "fmt"
}

// paramName names a parameter after what fills it: runtime.Str("base-token") is base_token, documentID document_id,
// item.FileToken file_token, through validate.EncodePathSegment(…) and the like.
func paramName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return snake(e.Name)
	case *ast.SelectorExpr:
		return snake(e.Sel.Name)
	case *ast.CallExpr:
		if len(e.Args) == 1 {
			if s, ok := stringLiteral(e.Args[0]); ok {
				return strings.ReplaceAll(s, "-", "_")
			}

			return paramName(e.Args[0])
		}
	case *ast.IndexExpr:
		return paramName(e.X)
	}

	return "id"
}

// snake writes a Go name in snake case: documentID is document_id, FileToken file_token.
func snake(name string) string {
	r := []rune(name)

	var b strings.Builder

	for i, c := range r {
		if unicode.IsUpper(c) {
			prevLower := i > 0 && (unicode.IsLower(r[i-1]) || unicode.IsDigit(r[i-1]))
			nextLower := i > 0 && i+1 < len(r) && unicode.IsLower(r[i+1]) && unicode.IsUpper(r[i-1])

			if prevLower || nextLower {
				b.WriteByte('_')
			}

			b.WriteRune(unicode.ToLower(c))

			continue
		}

		b.WriteRune(c)
	}

	return b.String()
}

// findSinks names the functions of the shortcuts that send a request with an HTTP method and a path they are given:
// those that pass a parameter of their own on as the method or the path of a request (a wrapper such as
// callTaskAPITyped), to a fixed point, as gh's GraphQL wrappers are read (ghsource.go).
func (p *larkProgram) findSinks() map[string]larkSink {
	sinks := map[string]larkSink{}

	for changed := true; changed; {
		changed = false

		for _, pkg := range p.packages {
			for _, group := range [][]*ast.FuncDecl{funcsOf(pkg), methodsOf(pkg)} {
				for _, fn := range group {
					key := pkg.dir + "." + fn.Name.Name
					if fn.Recv != nil {
						key = pkg.dir + ".(" + fn.Name.Name
					}

					if _, known := sinks[key]; known {
						continue
					}

					if _, primitive := larkPrimitiveSinks[fn.Name.Name]; primitive {
						continue
					}

					if s, ok := p.wrapper(fn, pkg, sinks); ok {
						sinks[key], changed = s, true
					}
				}
			}
		}
	}

	return sinks
}

func funcsOf(pkg *larkPackage) []*ast.FuncDecl {
	out := make([]*ast.FuncDecl, 0, len(pkg.funcs))
	for _, name := range slices.Sorted(maps.Keys(pkg.funcs)) {
		out = append(out, pkg.funcs[name])
	}

	return out
}

func methodsOf(pkg *larkPackage) []*ast.FuncDecl {
	var out []*ast.FuncDecl
	for _, name := range slices.Sorted(maps.Keys(pkg.methods)) {
		out = append(out, pkg.methods[name]...)
	}

	return out
}

// wrapper is whether a function sends a request whose path is one of its parameters, and with which method: one
// of its parameters too, or one it writes.
func (p *larkProgram) wrapper(fn *ast.FuncDecl, pkg *larkPackage, known map[string]larkSink) (larkSink, bool) {
	params := map[string]int{}
	i := 0

	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			params[name.Name] = i
			i++
		}

		if len(field.Names) == 0 {
			i++
		}
	}

	if len(params) == 0 {
		return larkSink{}, false
	}

	f := p.fileOf(fn)
	saved := p.sinks
	p.sinks = known

	defer func() { p.sinks = saved }()

	var (
		found larkSink
		ok    bool
	)

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if ok {
			return false
		}

		var method, pth ast.Expr

		fixed := ""

		switch e := n.(type) {
		case *ast.CallExpr:
			s, isSink := p.sinkOf(f, pkg, e.Fun)
			if !isSink || s.pathArg >= len(e.Args) || s.methodArg >= len(e.Args) {
				return true
			}

			pth = e.Args[s.pathArg]
			if s.methodArg >= 0 {
				method = e.Args[s.methodArg]
			} else {
				fixed = s.method
			}
		case *ast.CompositeLit:
			method, pth = apiReqFields(e)
			if method == nil || pth == nil {
				return true
			}
		default:
			return true
		}

		pathParam, isParam := identParam(pth, params)
		if !isParam {
			return true
		}

		s := larkSink{methodArg: -1, method: fixed, pathArg: pathParam}

		if method != nil {
			if m, isParam := identParam(method, params); isParam {
				s.methodArg = m
			} else if m, written := httpMethod(method); written {
				s.method = m
			} else if values := p.stringValues(pkg, nil, method); len(values) == 1 {
				s.method = strings.ToUpper(values[0])
			} else {
				return true
			}
		}

		found, ok = s, true

		return false
	})

	return found, ok
}

func identParam(expr ast.Expr, params map[string]int) (int, bool) {
	id, ok := expr.(*ast.Ident)
	if !ok {
		return 0, false
	}

	i, ok := params[id.Name]

	return i, ok
}
