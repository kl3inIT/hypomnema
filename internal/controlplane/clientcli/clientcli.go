// Package clientcli holds the hypomnema commands that call the client API
// (issue #41): login, run, answer and questions. They need no database, so
// they work from the owner's laptop as well as on the server. `login` keeps
// the URL and token in ~/.hypomnema/client.json, as `hypomnema-node init`
// keeps the node's.
package clientcli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// ErrUsage marks a command line that is wrong.
var ErrUsage = errors.New("usage")

// Commands lists the commands this package runs.
var Commands = []string{"login", "run", "answer", "questions"}

const (
	basePath = "/api/v1"
	// waitSeconds is how long one events request waits for the next event.
	waitSeconds = 50
	// requestTimeout covers that wait.
	requestTimeout = 70 * time.Second
)

// Command runs one client command with the configuration in home.
func Command(ctx context.Context, home string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return ErrUsage
	}

	if args[0] == "login" {
		return login(ctx, home, args[1:], stdout)
	}

	cfg, err := LoadConfig(home)
	if err != nil {
		return err
	}

	c, err := newClient(cfg)
	if err != nil {
		return err
	}

	o := &owner{api: c, out: stdout}

	switch args[0] {
	case "run":
		return o.run(ctx, args[1:])
	case "answer":
		return o.answer(ctx, args[1:])
	case "questions":
		if len(args) != 1 {
			return ErrUsage
		}

		return o.questions(ctx)
	default:
		return ErrUsage
	}
}

func newClient(cfg Config) (*openapi.ClientWithResponses, error) {
	c, err := openapi.NewClientWithResponses(cfg.URL+basePath,
		openapi.WithHTTPClient(&http.Client{Timeout: requestTimeout}),
		openapi.WithRequestEditorFn(func(_ context.Context, r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+cfg.Token)

			return nil
		}))
	if err != nil {
		return nil, fmt.Errorf("making the API client: %w", err)
	}

	return c, nil
}

// login checks the token against the control plane and saves both. The

// apiError turns an error answer into an error.
func apiError(doing string, status int, e *openapi.Error) error {
	if e == nil {
		return fmt.Errorf("%s: HTTP %d", doing, status)
	}

	return fmt.Errorf("%s: %s (%s)", doing, e.Message, e.Code)
}

// newKey makes an Idempotency-Key, so a create the CLI repeats happens once.
func newKey() *string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	k := hex.EncodeToString(b)

	return &k
}

type owner struct {
	api *openapi.ClientWithResponses
	out io.Writer
}

func (o *owner) printf(format string, a ...any) {
	_, _ = fmt.Fprintf(o.out, format, a...)
}
