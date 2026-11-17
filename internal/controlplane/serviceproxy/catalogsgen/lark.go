package main

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
)

// larkFile is the declaration Lark's catalog embeds (serviceproxy/lark.go).
const larkFile = "lark_methods.json"

// Where lark-cli declares what it calls, in its source (issue #236): its service catalog, and its shortcut commands.
const (
	larkRegistryDir  = "internal/registry/catalog/services/"
	larkShortcutsDir = "shortcuts/"
)

// larkParts names the service of each part of Lark's API ("/open-apis/<part>/…") that only lark-cli's shortcuts
// call: the catalog's services are named by the registry's (its servicePath), and these by what the owner reads
// (serviceproxy/lark.go, larkServices; issue #236). A part a shortcut calls that is in neither fails the generation,
// so that a new one is named here by hand.
var larkParts = map[string]string{
	"authen":   "contact",
	"base":     "base",
	"board":    "whiteboard",
	"bot":      "contact",
	"contact":  "contact",
	"docs_ai":  "doc",
	"docx":     "doc",
	"search":   "search",
	"sheet_ai": "sheets",
}

// larkLeftOut are the parts of the API shortcuts call that the catalog leaves out, with why: a request there is no
// method of the catalog, and asks unless it is a GET (lark.other.read).
var larkLeftOut = map[string]string{
	// The application commands change the app's own slash commands (shortcuts/application), no service of the
	// catalog's; the app is set up by its owner in Lark's console.
	"application": "the app's own slash commands",
	// The apps commands are Feishu's only (shortcuts/register.go, brandRestrictedServices), and the Integration
	// declares Lark international.
	"spark": "Feishu's apps",
}

// larkMethod is one method of lark_methods.json: the service it belongs to, its HTTP method and path, the risk
// lark-cli gives it, and the scopes it needs as a person (user) and as the app (app), alternatives, each the scopes,
// space-separated, that are all needed.
type larkMethod struct {
	Service string   `json:"service"`
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	Risk    string   `json:"risk"`
	User    []string `json:"user,omitempty"`
	App     []string `json:"app,omitempty"`
}

func generateLark(ctx context.Context, client *http.Client, src sources, root *os.Root, out io.Writer) error {
	l := src.Lark

	tarball, err := fetch(ctx, client, l.CLI.source)
	if err != nil {
		return err
	}

	files, err := larkSource(tarball)
	if err != nil {
		return fmt.Errorf("lark-cli %s: %w", l.CLI.Version, err)
	}

	before, err := root.ReadFile(larkFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	// The paths written before keep their parameters' names; a file that does not read keeps none.
	var previous []larkMethod
	_ = json.Unmarshal(before, &previous)

	methods, report, err := larkMethods(files, previous)
	if err != nil {
		return fmt.Errorf("lark-cli %s: %w", l.CLI.Version, err)
	}

	written, err := encodeLark(methods)
	if err != nil {
		return err
	}

	if err := root.WriteFile(larkFile, written, 0o644); err != nil {
		return err
	}

	var b strings.Builder

	fmt.Fprintf(&b, "%s: %d methods of lark-cli %s: %d of its service catalog, %d calls of its %d shortcut commands\n",
		larkFile, len(methods), l.CLI.Version, report.registry, report.calls, report.shortcuts)

	if err := printChanges(&b, serviceproxy.LarkActionIDs, before, written); err != nil {
		return err
	}

	_, err = io.WriteString(out, b.String())

	return err
}

// larkSource reads, from lark-cli's source tarball, its service catalog and the Go files of its shortcuts, tests
// left out, by their names in the repository.
func larkSource(tarball []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()

	files := map[string][]byte{}
	tr := tar.NewReader(gz)

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		// "<repository>-<version>/<file>".
		_, name, _ := strings.Cut(h.Name, "/")

		registry := strings.HasPrefix(name, larkRegistryDir) && path.Ext(name) == ".json" &&
			path.Dir(name)+"/" == larkRegistryDir
		shortcut := strings.HasPrefix(name, larkShortcutsDir) && goSource(name)

		if h.Typeflag != tar.TypeReg || (!registry && !shortcut) {
			continue
		}

		src, err := io.ReadAll(io.LimitReader(tr, maxDeclaration))
		if err != nil {
			return nil, err
		}

		files[name] = src
	}

	return files, nil
}

// larkReport counts what a generation read.
type larkReport struct {
	registry, calls, shortcuts int
}

// larkMethods reads lark-cli's two declarations into the catalog's methods (issue #236, #567, #742):
//
//   - each method of the service catalog, with the service, risk and scopes it declares: it passes with any one of
//     its scopes, or with all of its requiredScopes when it names them (cmd/service/service.go,
//     checkServiceScopes);
//   - each call of a shortcut command that is no method of the catalog, with the command's risk (a GET is a read
//     whatever its command, a DELETE high-risk whatever its command, and a call commands of different risks make
//     has the highest), in the service whose part of the API it is; it passes with the scopes of any one of the
//     commands that make it (shortcuts/common/runner.go checkShortcutScopes, types.go ScopesForIdentity).
//
// A call is the same method as another when they differ only in the names of their parameters; it is written with
// the names it had in the file before (previous), so that a regeneration's diff shows what changed, or else with
// those of the first command that makes it. The catalog's methods come first, then the shortcuts', each sorted by
// service, path and HTTP method.
func larkMethods(files map[string][]byte, previous []larkMethod) ([]larkMethod, larkReport, error) {
	var report larkReport

	registry, parts, err := larkRegistry(files)
	if err != nil {
		return nil, report, err
	}

	report.registry = len(registry)

	shortcutFiles := map[string][]byte{}

	for name, src := range files {
		if strings.HasPrefix(name, larkShortcutsDir) {
			shortcutFiles[name] = src
		}
	}

	shortcuts, err := larkShortcuts(shortcutFiles)
	if err != nil {
		return nil, report, err
	}

	report.shortcuts = len(shortcuts)

	known := map[string]bool{}
	for _, m := range registry {
		known[larkShape(m.Method, m.Path)] = true
	}

	type merged struct {
		larkMethod

		user, app map[string]bool
	}

	calls := map[string]*merged{}

	var unnamed []string

	for _, s := range shortcuts {
		for _, c := range s.Calls {
			if !strings.HasPrefix(c.Path, "/open-apis/") || strings.ContainsAny(c.Path, " \t\n") {
				// A host of its own (an upload's presigned address), or a description written in place of a path.
				continue
			}

			c.Path = larkPathParameter.ReplaceAllString(c.Path, "{$1}")

			key := larkShape(c.Method, c.Path)
			if known[key] {
				continue
			}

			service, ok := larkServiceOf(c.Path, parts)
			if service == "" && ok {
				continue
			}

			if !ok {
				unnamed = append(unnamed, fmt.Sprintf("%s %s (%s %s)", c.Method, c.Path, s.Service, s.Command))

				continue
			}

			risk := s.Risk

			switch c.Method {
			case http.MethodGet:
				risk = "read"
			case http.MethodDelete:
				// A delete a command makes on its way (docs +media-insert removes the block it added when an upload
				// fails) is no less careful than any delete the catalog does not name (serviceproxy/lark.go): a
				// command's risk is the whole command's, and what cannot be read for certain asks.
				risk = "high-risk-write"
			}

			m, ok := calls[key]
			if !ok {
				m = &merged{
					larkMethod: larkMethod{Service: service, Method: c.Method, Path: c.Path, Risk: risk},
					user:       map[string]bool{}, app: map[string]bool{},
				}
				calls[key] = m
			} else if slices.Index(larkRisks, risk) > slices.Index(larkRisks, m.Risk) {
				m.Risk = risk
			}

			if len(s.User) > 0 {
				m.user[strings.Join(s.User, " ")] = true
			}

			if len(s.App) > 0 {
				m.app[strings.Join(s.App, " ")] = true
			}
		}
	}

	if len(unnamed) > 0 {
		slices.Sort(unnamed)

		return nil, report, fmt.Errorf("shortcuts call parts of the API no service is named for (larkParts): %s",
			strings.Join(slices.Compact(unnamed), "; "))
	}

	out := slices.Clone(registry)
	sortLark(out)

	written := map[string]string{}
	for _, m := range previous {
		written[larkShape(m.Method, m.Path)] = m.Path
	}

	var fromShortcuts []larkMethod

	for key, m := range calls {
		if p, ok := written[key]; ok {
			m.Path = p
		}

		m.User, m.App = slices.Sorted(maps.Keys(m.user)), slices.Sorted(maps.Keys(m.app))
		fromShortcuts = append(fromShortcuts, m.larkMethod)
	}

	report.calls = len(fromShortcuts)

	sortLark(fromShortcuts)

	return append(out, fromShortcuts...), report, nil
}

// larkParameter is a parameter of a path, as the catalog and the shortcuts' dry runs write it: {name} or :name.
var larkParameter = regexp.MustCompile(`\{[^}/]*\}|:[A-Za-z_][A-Za-z0-9_]*`)

// larkShape is a request's method and path with every parameter the same.
func larkShape(method, p string) string {
	return method + " " + larkParameter.ReplaceAllString(p, "{}")
}

// larkPathParameter is a parameter as a dry run writes it.
var larkPathParameter = regexp.MustCompile(`:([A-Za-z_][A-Za-z0-9_]*)`)

// larkServiceOf is the service a path belongs to, by its part of the API.
func larkServiceOf(p string, parts map[string]string) (string, bool) {
	segments := strings.Split(strings.TrimPrefix(p, "/open-apis/"), "/")

	if s, ok := parts[segments[0]]; ok {
		return s, true
	}

	if _, ok := larkLeftOut[segments[0]]; ok {
		return "", true
	}

	s, ok := larkParts[segments[0]]

	return s, ok
}

// larkRegistryService is a service of lark-cli's catalog (internal/registry/catalog/services/<name>.json).
type larkRegistryService struct {
	Name        string                  `json:"name"`
	ServicePath string                  `json:"servicePath"`
	Resources   map[string]larkResource `json:"resources"`
}

type larkResource struct {
	Methods   map[string]larkRegistryMethod `json:"methods"`
	Resources map[string]larkResource       `json:"resources"`
}

type larkRegistryMethod struct {
	Path           string   `json:"path"`
	HTTPMethod     string   `json:"httpMethod"`
	Risk           string   `json:"risk"`
	Scopes         []string `json:"scopes"`
	RequiredScopes []string `json:"requiredScopes"`
}

// larkRegistry reads every method of lark-cli's service catalog, and the part of the API each service is
// ("/open-apis/im/v1" is im's "im").
func larkRegistry(files map[string][]byte) ([]larkMethod, map[string]string, error) {
	var (
		out   []larkMethod
		parts = map[string]string{}
	)

	for _, name := range slices.Sorted(maps.Keys(files)) {
		if !strings.HasPrefix(name, larkRegistryDir) {
			continue
		}

		var s larkRegistryService
		if err := json.Unmarshal(files[name], &s); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", name, err)
		}

		part, _, _ := strings.Cut(strings.TrimPrefix(s.ServicePath, "/open-apis/"), "/")
		if s.Name == "" || part == "" || !strings.HasPrefix(s.ServicePath, "/open-apis/") {
			return nil, nil, fmt.Errorf("%s: no name or no servicePath under /open-apis/", name)
		}

		if other, ok := parts[part]; ok && other != s.Name {
			return nil, nil, fmt.Errorf("%s: /open-apis/%s is both %s's and %s's", name, part, other, s.Name)
		}

		parts[part] = s.Name

		var walk func(r larkResource) error

		walk = func(r larkResource) error {
			for _, key := range slices.Sorted(maps.Keys(r.Methods)) {
				m := r.Methods[key]
				if !slices.Contains(larkRisks, m.Risk) || m.HTTPMethod == "" || m.Path == "" {
					return fmt.Errorf("%s: method %s has risk %q, HTTP method %q, path %q", name, key, m.Risk,
						m.HTTPMethod, m.Path)
				}

				scopes := m.Scopes
				if len(m.RequiredScopes) > 0 {
					scopes = []string{strings.Join(m.RequiredScopes, " ")}
				}

				out = append(out, larkMethod{
					Service: s.Name, Method: strings.ToUpper(m.HTTPMethod), Path: s.ServicePath + "/" + m.Path,
					Risk: m.Risk, User: scopes, App: scopes,
				})
			}

			for _, key := range slices.Sorted(maps.Keys(r.Resources)) {
				if err := walk(r.Resources[key]); err != nil {
					return err
				}
			}

			return nil
		}

		if err := walk(larkResource{Resources: s.Resources}); err != nil {
			return nil, nil, err
		}
	}

	if len(out) == 0 {
		return nil, nil, errors.New("no method in " + larkRegistryDir)
	}

	return out, parts, nil
}

func sortLark(methods []larkMethod) {
	slices.SortFunc(methods, func(a, b larkMethod) int {
		return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(a.Path, b.Path), cmp.Compare(a.Method, b.Method))
	})
}

// encodeLark writes the declaration one method per line, as lark_methods.json has been written since #175: its
// keys in order, a space after each colon and comma, and its paths as written (<event_id> is not escaped).
func encodeLark(methods []larkMethod) ([]byte, error) {
	var b bytes.Buffer

	b.WriteString("[\n")

	for i, m := range methods {
		fields := [][2]any{{"service", m.Service}, {"method", m.Method}, {"path", m.Path}, {"risk", m.Risk}}
		if len(m.User) > 0 || len(m.App) > 0 {
			fields = append(fields, [2]any{"user", m.User}, [2]any{"app", m.App})
		}

		b.WriteString("  {")

		for j, f := range fields {
			if j > 0 {
				b.WriteString(", ")
			}

			key, err := larkJSON(f[0])
			if err != nil {
				return nil, err
			}

			value, err := larkJSON(f[1])
			if err != nil {
				return nil, err
			}

			b.Write(key)
			b.WriteString(": ")
			b.Write(value)
		}

		b.WriteString("}")

		if i < len(methods)-1 {
			b.WriteByte(',')
		}

		b.WriteByte('\n')
	}

	b.WriteString("]\n")

	return b.Bytes(), nil
}

// larkJSON writes a value as JSON with no HTML escaping, and a list with a space after each comma.
func larkJSON(v any) ([]byte, error) {
	if list, ok := v.([]string); ok {
		if list == nil {
			list = []string{}
		}

		parts := make([]string, 0, len(list))

		for _, s := range list {
			one, err := larkJSON(s)
			if err != nil {
				return nil, err
			}

			parts = append(parts, string(one))
		}

		return []byte("[" + strings.Join(parts, ", ") + "]"), nil
	}

	var b bytes.Buffer

	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, err
	}

	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}
