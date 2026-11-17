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
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
)

// googleFile is the declaration Google's catalog embeds (serviceproxy/google.go).
const googleFile = "google_methods.json"

// gwsServicesFile is where gws lists the APIs it names (services.rs, SERVICES), in its source tarball.
const gwsServicesFile = "crates/google-workspace/src/services.rs"

// gwsOwnServices are the entries of gws's list that are no Google API: workflow is gws's own commands, built
// without a Discovery document (crates/google-workspace-cli/src/main.rs, `if api_name == "workflow"`).
var gwsOwnServices = []string{"workflow"}

// discoverySource is one Discovery document: the API and version it is, fetched where gws fetches it
// (crates/google-workspace/src/discovery.rs, fetch_discovery_document: www.googleapis.com/discovery/v1/apis/…, or
// <api>.googleapis.com/$discovery/rest for the APIs that are only there), its revision the document's own.
type discoverySource struct {
	API     string `json:"api"`
	Version string `json:"version"`
	source
}

// googleMethod is one method of a Discovery document, with the fields the catalog needs: the document's name, the
// method's id, its HTTP method, the host and the path a request to it has, and the paths its media is uploaded to.
type googleMethod struct {
	API     string   `json:"api"`
	ID      string   `json:"id"`
	Method  string   `json:"method"`
	Host    string   `json:"host"`
	Path    string   `json:"path"`
	Uploads []string `json:"uploads,omitempty"`
}

// googleDeclaration is google_methods.json: the gws version it was made for, and every method.
type googleDeclaration struct {
	GWS     string         `json:"gws"`
	Methods []googleMethod `json:"methods"`
}

func generateGoogle(ctx context.Context, client *http.Client, src sources, root *os.Root, out io.Writer) error {
	g := src.Google

	tarball, err := fetch(ctx, client, g.CLI.source)
	if err != nil {
		return err
	}

	named, err := gwsServices(tarball)
	if err != nil {
		return fmt.Errorf("gws %s: %w", g.CLI.Version, err)
	}

	// What gws names is what the catalog must have (decision 9c of #713): an API gws can call by its name with no
	// document pinned fails here.
	if lacking := unpinned(named, g.Discovery); len(lacking) > 0 {
		return fmt.Errorf("gws %s names APIs %s has no Discovery document of: %s", g.CLI.Version, sourcesFile,
			strings.Join(lacking, ", "))
	}

	// Every document is fetched before any fails, so that one run says each one that changed.
	var (
		methods []googleMethod
		failed  []error
	)

	for _, d := range g.Discovery {
		raw, err := fetchDiscovery(ctx, client, d.source)
		if err != nil {
			failed = append(failed, err)

			continue
		}

		ms, err := discoveryMethods(d, raw)
		if err != nil {
			failed = append(failed, fmt.Errorf("%s %s: %w", d.API, d.Version, err))

			continue
		}

		methods = append(methods, ms...)
	}

	if err := errors.Join(failed...); err != nil {
		return err
	}

	sortGoogle(methods)

	written, err := encodeGoogle(googleDeclaration{GWS: g.CLI.Version, Methods: methods})
	if err != nil {
		return err
	}

	before, err := root.ReadFile(googleFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := root.WriteFile(googleFile, written, 0o644); err != nil {
		return err
	}

	var report strings.Builder

	fmt.Fprintf(&report, "%s: %d methods of %d Discovery documents; the %d APIs gws %s names are all pinned\n",
		googleFile, len(methods), len(g.Discovery), len(named), g.CLI.Version)

	if err := printChanges(&report, serviceproxy.GoogleActionIDs, before, written); err != nil {
		return err
	}

	_, err = io.WriteString(out, report.String())

	return err
}

// fetchDiscovery downloads a Discovery document and checks the SHA-256 of its canonical form. Google serves the same
// document with its keys in another order each time (three fetches of Tasks' on 2026-11-18, three sums), so its
// bytes are pinned as Go writes the JSON back, keys sorted; what is pinned is still every value the document has.
func fetchDiscovery(ctx context.Context, client *http.Client, s source) ([]byte, error) {
	body, err := download(ctx, client, s.URL)
	if err != nil {
		return nil, err
	}

	canonical, err := canonicalJSON(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.URL, err)
	}

	if err := checkSum(canonical, s); err != nil {
		return nil, err
	}

	return canonical, nil
}

// canonicalJSON writes a JSON document back with its objects' keys sorted and its numbers as written.
func canonicalJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}

	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("more than one JSON value")
	}

	return json.Marshal(v)
}

// gwsService is one entry of gws's list: `api_name: "drive", version: "v3"`.
var gwsService = regexp.MustCompile(`api_name:\s*"([^"]+)",\s*version:\s*"([^"]+)"`)

// gwsServices reads the APIs gws names, "<api> <version>", from services.rs in its source tarball; gws's own
// workflow is left out.
func gwsServices(tarball []byte) ([]string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tarball))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no " + gwsServicesFile + " in the source")
		}

		if err != nil {
			return nil, err
		}

		// "<repository>-<version>/<file>".
		if _, name, _ := strings.Cut(h.Name, "/"); name != gwsServicesFile {
			continue
		}

		src, err := io.ReadAll(io.LimitReader(tr, maxDeclaration))
		if err != nil {
			return nil, err
		}

		return servicesOf(src)
	}
}

func servicesOf(src []byte) ([]string, error) {
	var out []string

	for _, m := range gwsService.FindAllSubmatch(src, -1) {
		if !slices.Contains(gwsOwnServices, string(m[1])) {
			out = append(out, string(m[1])+" "+string(m[2]))
		}
	}

	if len(out) == 0 {
		return nil, errors.New("no service in " + gwsServicesFile)
	}

	return out, nil
}

// unpinned are the APIs gws names that no pinned document is.
func unpinned(named []string, pinned []discoverySource) []string {
	var out []string

	for _, n := range named {
		if !slices.ContainsFunc(pinned, func(d discoverySource) bool { return d.API+" "+d.Version == n }) {
			out = append(out, n)
		}
	}

	return out
}

// discoveryDocument is what is read of a Discovery document: what gws reads to build a request's address
// (crates/google-workspace/src/discovery.rs, RestDescription and RestMethod; crates/google-workspace-cli/src/
// executor.rs, build_url).
type discoveryDocument struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Revision    string `json:"revision"`
	RootURL     string `json:"rootUrl"`
	ServicePath string `json:"servicePath"`
	BaseURL     string `json:"baseUrl"`
	discoveryResource
}

type discoveryResource struct {
	Methods   map[string]discoveryMethod   `json:"methods"`
	Resources map[string]discoveryResource `json:"resources"`
}

type discoveryMethod struct {
	ID          string `json:"id"`
	HTTPMethod  string `json:"httpMethod"`
	Path        string `json:"path"`
	FlatPath    string `json:"flatPath"`
	MediaUpload *struct {
		Protocols map[string]struct {
			Path string `json:"path"`
		} `json:"protocols"`
	} `json:"mediaUpload"`
}

// discoveryMethods reads every method of a document. A request's address is the document's base URL (rootUrl and
// servicePath, as gws builds it) and the method's flatPath: the path with each parameter one segment, as Google reads
// the request's address, whichever of path or flatPath gws filled in (a {+name} of path, filled with a resource's
// name, is the segments flatPath names). A method with no flatPath has a path of one segment per parameter
// (Calendar's). An upload goes to the root URL and the paths of mediaUpload, their {+…} the method's own segments.
func discoveryMethods(src discoverySource, raw []byte) ([]googleMethod, error) {
	var doc discoveryDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}

	if doc.Name != src.API || doc.Version != src.Version {
		return nil, fmt.Errorf("the document is %s %s", doc.Name, doc.Version)
	}

	if src.Revision != "" && doc.Revision != src.Revision {
		return nil, fmt.Errorf("the document's revision is %s, not %s", doc.Revision, src.Revision)
	}

	base := doc.BaseURL
	if base == "" {
		base = doc.RootURL + doc.ServicePath
	}

	baseURL, err := url.Parse(base)
	if err != nil || baseURL.Host == "" {
		return nil, fmt.Errorf("base URL %q has no host", base)
	}

	rootURL, err := url.Parse(doc.RootURL)
	if err != nil || rootURL.Host != baseURL.Host {
		return nil, fmt.Errorf("root URL %q is not on the base URL's host %s", doc.RootURL, baseURL.Host)
	}

	var out []googleMethod

	var walk func(r discoveryResource) error

	walk = func(r discoveryResource) error {
		for _, name := range slices.Sorted(maps.Keys(r.Methods)) {
			m, err := googleMethodOf(src.API, baseURL, r.Methods[name])
			if err != nil {
				return err
			}

			out = append(out, m)
		}

		for _, name := range slices.Sorted(maps.Keys(r.Resources)) {
			if err := walk(r.Resources[name]); err != nil {
				return err
			}
		}

		return nil
	}

	if err := walk(doc.discoveryResource); err != nil {
		return nil, err
	}

	if len(out) == 0 {
		return nil, errors.New("no method")
	}

	return out, nil
}

func googleMethodOf(api string, base *url.URL, m discoveryMethod) (googleMethod, error) {
	if m.ID == "" || m.HTTPMethod == "" {
		return googleMethod{}, fmt.Errorf("a method of path %q has no id or no HTTP method", m.Path)
	}

	template := cmp.Or(m.FlatPath, m.Path)
	if strings.Contains(template, "{+") {
		return googleMethod{}, fmt.Errorf("%s: path %q has a parameter of several segments and no flatPath", m.ID,
			template)
	}

	g := googleMethod{
		API: api, ID: m.ID, Method: strings.ToUpper(m.HTTPMethod), Host: base.Host,
		Path: strings.TrimSuffix(base.Path, "/") + "/" + strings.TrimPrefix(template, "/"),
	}

	if m.MediaUpload != nil {
		for _, protocol := range slices.Sorted(maps.Keys(m.MediaUpload.Protocols)) {
			upload := m.MediaUpload.Protocols[protocol].Path

			// "/upload/v1/{+parent}/attachments:upload" ends with the method's path; its flat form is the method's.
			if prefix, ok := strings.CutSuffix(upload, m.Path); ok && m.Path != template {
				upload = prefix + template
			}

			if strings.Contains(upload, "{+") || !strings.HasPrefix(upload, "/") {
				return googleMethod{}, fmt.Errorf("%s: upload path %q is not one of segments", m.ID, upload)
			}

			g.Uploads = append(g.Uploads, upload)
		}
	}

	return g, nil
}

func sortGoogle(methods []googleMethod) {
	slices.SortFunc(methods, func(a, b googleMethod) int {
		return cmp.Or(cmp.Compare(a.API, b.API), cmp.Compare(a.ID, b.ID))
	})
}

// encodeGoogle writes the declaration one method per line, so that a regeneration's diff reads line by line.
func encodeGoogle(d googleDeclaration) ([]byte, error) {
	var b bytes.Buffer

	fmt.Fprintf(&b, "{\n  \"gws\": %s,\n  \"methods\": [\n", strconv.Quote(d.GWS))

	if err := lines(&b, d.Methods); err != nil {
		return nil, err
	}

	b.WriteString("  ]\n}\n")

	return b.Bytes(), nil
}
