// Command catalogsgen regenerates the declarations the service proxy's catalogs are built from (issue #739, step 1
// of #713, decision 10 of its design). Each declaration is fetched at the revision or address
// catalog_sources.json pins, checked against its SHA-256, and written in the slim form the catalog embeds; what
// the pinned CLI itself sends is read from its source and checked against the declaration. It prints what changed:
// operations added and removed, and those whose action moved. It is run by hand, with `make catalogs-generate`,
// when a CLI or an API is bumped; CI runs only the offline checks (the catalogs' own tests), as
// github-mcp-server's docs-check regenerates and Hypomnema's sqlc-check and openapi-check diff.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"time"
)

// sourcesFile names, beside the catalogs, where each declaration is fetched from.
const sourcesFile = "catalog_sources.json"

// maxDeclaration bounds one download: GitHub's REST description is 14 MB, gh's source 10 MB, client-go's module
// 5 MB, a Discovery document under 1 MB.
const maxDeclaration = 128 << 20

// source is one thing fetched: its address, the revision it is at (or the day it was read, for an address that has
// none), and its SHA-256.
type source struct {
	URL      string `json:"url"`
	Revision string `json:"revision,omitempty"`
	Fetched  string `json:"fetched,omitempty"`
	SHA256   string `json:"sha256"`
}

// cli is the tool a catalog is for, at the version agenttool pins, and its source at that version.
type cli struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	source
}

// sources is catalog_sources.json.
type sources struct {
	GitHub struct {
		CLI     cli    `json:"cli"`
		REST    source `json:"rest"`
		GraphQL source `json:"graphql"`
	} `json:"github"`
	// GitLab's declaration is the route table of client-go, the Go client glab is built on (issue #740): the CLI's
	// source is its go.mod, which names client-go's version, and the client's its module at that version.
	GitLab struct {
		CLI    cli `json:"cli"`
		Client cli `json:"client"`
	} `json:"gitlab"`
	// Google's declaration is the Discovery documents of the APIs gws names (issue #741): the CLI's source is read
	// for that list (services.rs).
	Google struct {
		CLI       cli               `json:"cli"`
		Discovery []discoverySource `json:"discovery"`
	} `json:"google"`
	// Lark's declaration is lark-cli's own source (issue #742): its service catalog and its shortcut commands.
	Lark struct {
		CLI cli `json:"cli"`
	} `json:"lark"`
}

func main() {
	service := flag.String("service", "all", "the catalog to regenerate: github, gitlab, google, lark, or all")
	dir := flag.String("dir", "internal/controlplane/serviceproxy", "the directory of the catalogs and "+sourcesFile)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, *service, *dir); err != nil {
		fmt.Fprintln(os.Stderr, "catalogsgen:", err)
		stop()
		os.Exit(1)
	}
}

func run(ctx context.Context, service, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	raw, err := root.ReadFile(sourcesFile)
	if err != nil {
		return err
	}

	var src sources
	if err := json.Unmarshal(raw, &src); err != nil {
		return fmt.Errorf("%s: %w", sourcesFile, err)
	}

	client := &http.Client{Timeout: 5 * time.Minute}

	switch service {
	case "github":
		return generateGitHub(ctx, client, src, root, os.Stdout)
	case "gitlab":
		return generateGitLab(ctx, client, src, root, os.Stdout)
	case "google":
		return generateGoogle(ctx, client, src, root, os.Stdout)
	case "lark":
		return generateLark(ctx, client, src, root, os.Stdout)
	case "all":
		for _, generate := range []func(context.Context, *http.Client, sources, *os.Root, io.Writer) error{
			generateGitHub, generateGitLab, generateGoogle, generateLark,
		} {
			if err := generate(ctx, client, src, root, os.Stdout); err != nil {
				return err
			}
		}

		return nil
	}

	return fmt.Errorf("no catalog %q: github, gitlab, google and lark are the ones generated", service)
}

// fetch downloads a source and checks its SHA-256. A declaration that changed at an address with no revision fails
// here: the new one is read, then pinned by hand.
func fetch(ctx context.Context, client *http.Client, s source) ([]byte, error) {
	body, err := download(ctx, client, s.URL)
	if err != nil {
		return nil, err
	}

	if err := checkSum(body, s); err != nil {
		return nil, err
	}

	return body, nil
}

// download reads what an address serves, up to maxDeclaration.
func download(ctx context.Context, client *http.Client, address string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", address, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDeclaration+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", address, err)
	}

	if len(body) > maxDeclaration {
		return nil, fmt.Errorf("%s: larger than %d bytes", address, maxDeclaration)
	}

	return body, nil
}

// errChanged is a download whose SHA-256 is not the pinned one.
var errChanged = errors.New("the SHA-256 is not the one " + sourcesFile + " pins")

func checkSum(body []byte, s source) error {
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != s.SHA256 {
		return fmt.Errorf("%s: %w: got %s, want %s; read the new one, then pin it", s.URL, errChanged, got, s.SHA256)
	}

	return nil
}
