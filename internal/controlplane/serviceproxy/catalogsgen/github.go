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
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
)

// githubFile is the declaration GitHub's catalog embeds (serviceproxy/github.go).
const githubFile = "github_operations.json"

// restOperation is one operation of GitHub's REST description, with the fields the catalog needs: its method,
// host and path, its x-github category and its operationId.
type restOperation struct {
	Method    string `json:"method"`
	Host      string `json:"host"`
	Path      string `json:"path"`
	Category  string `json:"category"`
	Operation string `json:"operation"`
}

// graphqlField is one root field of GitHub's GraphQL schema: a query or a mutation, with its @docsCategory.
type graphqlField struct {
	Type     string `json:"type"`
	Field    string `json:"field"`
	Category string `json:"category,omitempty"`
}

// githubDeclaration is github_operations.json: the gh version it was made for, and every operation.
type githubDeclaration struct {
	GH      string          `json:"gh"`
	REST    []restOperation `json:"rest"`
	GraphQL []graphqlField  `json:"graphql"`
}

func generateGitHub(ctx context.Context, client *http.Client, src sources, root *os.Root, out io.Writer) error {
	gh := src.GitHub

	rest, err := fetch(ctx, client, gh.REST)
	if err != nil {
		return err
	}

	schema, err := fetch(ctx, client, gh.GraphQL)
	if err != nil {
		return err
	}

	tarball, err := fetch(ctx, client, gh.CLI.source)
	if err != nil {
		return err
	}

	declaration, err := githubOperations(gh.CLI.Version, rest, schema)
	if err != nil {
		return err
	}

	sent, err := ghFields(tarball)
	if err != nil {
		return fmt.Errorf("gh %s: %w", gh.CLI.Version, err)
	}

	if lacking := lacks(declaration, sent); len(lacking) > 0 {
		return fmt.Errorf("gh %s sends GraphQL fields the schema does not have: %s", gh.CLI.Version,
			strings.Join(lacking, ", "))
	}

	written, err := encodeGitHub(declaration)
	if err != nil {
		return err
	}

	before, err := root.ReadFile(githubFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := root.WriteFile(githubFile, written, 0o644); err != nil {
		return err
	}

	var report strings.Builder

	fmt.Fprintf(&report, "%s: %d REST operations, %d GraphQL root fields; the %d fields gh %s sends with an "+
		"input are all in the schema\n",
		githubFile, len(declaration.REST), len(declaration.GraphQL), len(sent), gh.CLI.Version)

	if err := printChanges(&report, before, written); err != nil {
		return err
	}

	_, err = io.WriteString(out, report.String())

	return err
}

// githubOperations reads the REST description and the GraphQL schema into the declaration the catalog embeds.
func githubOperations(version string, rest, schema []byte) (githubDeclaration, error) {
	d := githubDeclaration{GH: version}

	ops, err := restOperations(rest)
	if err != nil {
		return d, fmt.Errorf("the REST description: %w", err)
	}

	fields, err := graphqlRootFields(schema)
	if err != nil {
		return d, fmt.Errorf("the GraphQL schema: %w", err)
	}

	d.REST, d.GraphQL = ops, fields

	return d, nil
}

// methods are the HTTP methods of an OpenAPI path item; its other keys (parameters, summary…) are not operations.
var methods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func restOperations(raw []byte) ([]restOperation, error) {
	type server struct {
		URL string `json:"url"`
	}

	var doc struct {
		Servers []server                              `json:"servers"`
		Paths   map[string]map[string]json.RawMessage `json:"paths"`
	}

	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}

	if len(doc.Servers) == 0 {
		return nil, errors.New("no servers")
	}

	defaultHost, err := hostOf(doc.Servers[0].URL)
	if err != nil {
		return nil, err
	}

	var out []restOperation

	for path, item := range doc.Paths {
		for method, raw := range item {
			if !slices.Contains(methods, method) {
				continue
			}

			var op struct {
				OperationID string   `json:"operationId"`
				Servers     []server `json:"servers"`
				GitHub      struct {
					Category string `json:"category"`
				} `json:"x-github"`
			}

			if err := json.Unmarshal(raw, &op); err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}

			if op.OperationID == "" {
				return nil, fmt.Errorf("%s %s has no operationId", method, path)
			}

			// A release's asset is uploaded to another host, named by the operation's own servers.
			host := defaultHost
			if len(op.Servers) > 0 {
				if host, err = hostOf(op.Servers[0].URL); err != nil {
					return nil, fmt.Errorf("%s: %w", op.OperationID, err)
				}
			}

			out = append(out, restOperation{
				Method: strings.ToUpper(method), Host: host, Path: path, Category: op.GitHub.Category,
				Operation: op.OperationID,
			})
		}
	}

	slices.SortFunc(out, func(a, b restOperation) int {
		return cmp.Or(cmp.Compare(a.Host, b.Host), cmp.Compare(a.Path, b.Path), cmp.Compare(a.Method, b.Method))
	})

	return out, nil
}

func hostOf(server string) (string, error) {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("server %q has no host", server)
	}

	return u.Host, nil
}

// graphqlRootFields reads the root fields of the schema's query and mutation types, each with its @docsCategory.
// Subscriptions are left out: GitHub's API has none, and the catalog names none.
func graphqlRootFields(raw []byte) ([]graphqlField, error) {
	doc, err := parser.ParseSchema(&ast.Source{Name: "schema.docs.graphql", Input: string(raw)})
	if err != nil {
		return nil, err
	}

	roots := map[ast.Operation]string{ast.Query: "Query", ast.Mutation: "Mutation"}

	for _, s := range doc.Schema {
		for _, op := range s.OperationTypes {
			roots[op.Operation] = op.Type
		}
	}

	var out []graphqlField

	for _, operation := range []ast.Operation{ast.Query, ast.Mutation} {
		def := doc.Definitions.ForName(roots[operation])
		if def == nil {
			return nil, fmt.Errorf("no type %s", roots[operation])
		}

		for _, f := range def.Fields {
			field := graphqlField{Type: string(operation), Field: f.Name}

			if d := f.Directives.ForName("docsCategory"); d != nil {
				if name := d.Arguments.ForName("name"); name != nil && name.Value != nil {
					field.Category = name.Value.Raw
				}
			}

			out = append(out, field)
		}
	}

	slices.SortFunc(out, func(a, b graphqlField) int {
		return cmp.Or(cmp.Compare(a.Type, b.Type), cmp.Compare(a.Field, b.Field))
	})

	return out, nil
}

// inputField finds a root field called with an input object, as every mutation gh sends is: in a struct tag
// (`graphql:"addComment(input: $input)"`, `graphql:"m: addComment(input:$input)"`) or in a query it writes
// itself (`addProjectV2ItemById(input: $input_000)`).
var inputField = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\(input:`)

// ghFields reads the GraphQL fields gh's source sends with an input object, from the source tarball of the release
// agenttool pins. Tests are left out.
func ghFields(tarball []byte) ([]string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()

	found := map[string]bool{}
	tr := tar.NewReader(gz)

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		if h.Typeflag != tar.TypeReg || !strings.HasSuffix(h.Name, ".go") || strings.HasSuffix(h.Name, "_test.go") {
			continue
		}

		src, err := io.ReadAll(io.LimitReader(tr, maxDeclaration))
		if err != nil {
			return nil, err
		}

		for _, m := range inputField.FindAllSubmatch(src, -1) {
			found[string(m[1])] = true
		}
	}

	if len(found) == 0 {
		return nil, errors.New("no GraphQL field found in the source")
	}

	return slices.Sorted(maps.Keys(found)), nil
}

// lacks names the fields gh sends that are no root field of the schema.
func lacks(d githubDeclaration, sent []string) []string {
	have := map[string]bool{}
	for _, f := range d.GraphQL {
		have[f.Field] = true
	}

	var out []string

	for _, f := range sent {
		if !have[f] {
			out = append(out, f)
		}
	}

	return out
}

// encodeGitHub writes the declaration one operation per line, so that a regeneration's diff reads line by line.
func encodeGitHub(d githubDeclaration) ([]byte, error) {
	var b bytes.Buffer

	version, err := json.Marshal(d.GH)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(&b, "{\n  \"gh\": %s,\n  \"rest\": [\n", version)

	if err := lines(&b, d.REST); err != nil {
		return nil, err
	}

	b.WriteString("  ],\n  \"graphql\": [\n")

	if err := lines(&b, d.GraphQL); err != nil {
		return nil, err
	}

	b.WriteString("  ]\n}\n")

	return b.Bytes(), nil
}

func lines[T any](b *bytes.Buffer, items []T) error {
	for i, item := range items {
		line, err := json.Marshal(item)
		if err != nil {
			return err
		}

		b.WriteString("    ")
		b.Write(line)

		if i < len(items)-1 {
			b.WriteByte(',')
		}

		b.WriteByte('\n')
	}

	return nil
}

// printChanges says what a regeneration changed: operations added and removed with their action, and the
// operations whose action moved.
func printChanges(out *strings.Builder, before, after []byte) error {
	old := map[string]string{}

	if len(before) > 0 {
		var err error
		if old, err = serviceproxy.GitHubActionIDs(before); err != nil {
			// The file before may be from an older layout: everything is then new.
			fmt.Fprintf(out, "the file before could not be read (%v); every operation is listed as added\n", err)

			old = map[string]string{}
		}
	}

	now, err := serviceproxy.GitHubActionIDs(after)
	if err != nil {
		return err
	}

	changed := 0

	for _, key := range slices.Sorted(maps.Keys(now)) {
		switch was, ok := old[key]; {
		case !ok:
			fmt.Fprintf(out, "+ %s -> %s\n", key, now[key])
		case was != now[key]:
			fmt.Fprintf(out, "~ %s: %s -> %s\n", key, was, now[key])
		default:
			continue
		}

		changed++
	}

	for _, key := range slices.Sorted(maps.Keys(old)) {
		if _, ok := now[key]; !ok {
			fmt.Fprintf(out, "- %s (was %s)\n", key, old[key])

			changed++
		}
	}

	fmt.Fprintf(out, "%d operations added, removed or moved\n", changed)

	return nil
}
