// Package clientcli holds the hypomnema commands that call the client API
// (issue #41). They need no database, so they work from the owner's laptop as
// well as on the server. `login` keeps the URL and token in
// ~/.hypomnema/client.json, as `hypomnema-node init` keeps the node's.
//
// Coordinators drive orchestration with these commands and the coordinator
// skill, as Orca's coordinators drive `orca orchestration`
// (docs/research/2026-11-09-coordinator-cli-and-skill.md): every command takes
// --json, long text comes from a file or standard input, and `runs wait`
// prints the next step.
package clientcli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// ErrUsage marks a command line that is wrong.
var ErrUsage = errors.New("usage")

const (
	basePath = "/api/v1"
	// waitSeconds is how long one events request waits for the next event.
	waitSeconds = 50
	// requestTimeout covers that wait.
	requestTimeout = 70 * time.Second
)

// command is one hypomnema command that this package runs. Its flags are
// declared once, in flags, so a test can check every command a skill names.
type command struct {
	// name is the words that select it, such as "runs wait".
	name string
	// args names the positional arguments, for the usage line.
	args string
	// min and max bound the positional arguments; max -1 is no bound.
	min, max int
	summary  string
	flags    func(fs *flag.FlagSet)
	// offline commands need no login.
	offline bool
	run     func(ctx context.Context, e *env, fs *flag.FlagSet) error
}

// Commands lists the first word of every command this package runs.
func Commands() []string {
	seen := map[string]bool{}

	var out []string

	for _, c := range commands {
		first := strings.Fields(c.name)[0]
		if !seen[first] {
			seen[first] = true

			out = append(out, first)
		}
	}

	return out
}

// env is what a command runs with.
type env struct {
	home string
	in   io.Reader
	out  io.Writer
	api  *openapi.ClientWithResponses
}

// Command runs one client command with the configuration in home.
func Command(ctx context.Context, home string, args []string, in io.Reader, out io.Writer) error {
	c, rest := find(args)
	if c == nil {
		return fmt.Errorf("%w: hypomnema %s\n%s", ErrUsage, strings.Join(args, " "), usage(args))
	}

	fs := flagSet(c, out)
	if err := parse(fs, rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return fmt.Errorf("%w: %w", ErrUsage, err)
	}

	if n := fs.NArg(); n < c.min || (c.max >= 0 && n > c.max) {
		return fmt.Errorf("%w: hypomnema %s %s", ErrUsage, c.name, c.args)
	}

	e := &env{home: home, in: in, out: out}

	if !c.offline {
		cfg, err := LoadConfig(home)
		if err != nil {
			return err
		}

		if e.api, err = newClient(cfg); err != nil {
			return err
		}
	}

	return c.run(ctx, e, fs)
}

// find returns the command args select and the arguments after its name. A
// two-word name wins over a one-word one.
func find(args []string) (*command, []string) {
	// The longest name wins: "hosts token set" before "hosts token".
	for n := min(len(args), 3); n >= 1; n-- {
		name := strings.Join(args[:n], " ")

		for i := range commands {
			if commands[i].name == name {
				return &commands[i], args[n:]
			}
		}
	}

	return nil, nil
}

func flagSet(c *command, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("hypomnema "+c.name, flag.ContinueOnError)
	fs.SetOutput(out)

	if c.flags != nil {
		c.flags(fs)
	}

	fs.Usage = func() {
		_, _ = fmt.Fprintf(out, "hypomnema %s %s\n  %s\n", c.name, c.args, c.summary)
		fs.PrintDefaults()
	}

	return fs
}

// parse reads flags wherever they are, so `tasks create <run_id> --json`
// works as well as `tasks create --json <run_id>`; the standard flag package
// stops at the first argument. After "--" everything is an argument.
func parse(fs *flag.FlagSet, args []string) error {
	var flags, positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]

		switch {
		case a == "--":
			positional = append(positional, args[i+1:]...)
			i = len(args)
		case len(a) > 1 && a[0] == '-':
			flags = append(flags, a)

			name := strings.TrimLeft(a, "-")
			if strings.Contains(name, "=") {
				continue
			}

			f := fs.Lookup(name)
			if f == nil {
				continue // fs.Parse reports the unknown flag
			}

			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
				continue
			}

			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		default:
			positional = append(positional, a)
		}
	}

	return fs.Parse(append(append(flags, "--"), positional...))
}

// Usage lists every command this package runs.
func Usage() string { return usage(nil) }

// usage lists the commands that start with the first word given, or all.
func usage(args []string) string {
	var lines []string

	for _, c := range commands {
		if len(args) > 0 && strings.Fields(c.name)[0] != args[0] {
			continue
		}

		lines = append(lines, strings.TrimSpace(fmt.Sprintf("  hypomnema %s %s", c.name, c.args)))
	}

	if len(lines) == 0 {
		return usage(nil)
	}

	sort.Strings(lines)

	return "commands:\n" + strings.Join(lines, "\n")
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

// apiError turns an error answer into an error.
func apiError(doing string, status int, e *openapi.Error) error {
	if e == nil {
		return fmt.Errorf("%s: HTTP %d", doing, status)
	}

	return fmt.Errorf("%s: %s (%s)", doing, e.Message, e.Code)
}

// key returns the Idempotency-Key for a create: the one given with --key, so
// a create whose result was lost can be repeated, or a new one.
func key(fs *flag.FlagSet) *string {
	if k := str(fs, "key"); k != "" {
		return &k
	}

	b := make([]byte, 16)
	_, _ = rand.Read(b)
	k := hex.EncodeToString(b)

	return &k
}

// printf prints text for a person. Every string it is given may come from a
// worker, a coordinator or a model (a question, a result, a transcript), so
// each goes through plain: such text is shown, never obeyed by the terminal.
func (e *env) printf(format string, a ...any) {
	for i, v := range a {
		if s, ok := v.(string); ok {
			a[i] = plain(s)
		}
	}

	_, _ = fmt.Fprintf(e.out, format, a...)
}

// print writes v as one line of JSON when --json is set, and text otherwise.
func (e *env) print(fs *flag.FlagSet, v any, text func()) error {
	if !boolean(fs, "json") {
		text()

		return nil
	}

	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encoding the output: %w", err)
	}

	_, err = fmt.Fprintf(e.out, "%s\n", b)

	return err
}

// text returns the value of a text flag, or the content of the file named by
// its -file flag, where "-" is standard input. Text travels through a file
// because shell quoting can change it.
func (e *env) text(fs *flag.FlagSet, name string) (string, error) {
	file := str(fs, name+"-file")
	if file == "" {
		return str(fs, name), nil
	}

	return e.read(file)
}

// read returns a file's content; "-" is standard input.
func (e *env) read(file string) (string, error) {
	switch file {
	case "-":
		b, err := io.ReadAll(e.in)
		if err != nil {
			return "", fmt.Errorf("reading standard input: %w", err)
		}

		return string(b), nil
	default:
		b, err := os.ReadFile(file) //nolint:gosec // the caller names the file it wants read
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", file, err)
		}

		return string(b), nil
	}
}

// Flag helpers: commands declare their flags in one place and read them here.

func jsonFlag(fs *flag.FlagSet) { fs.Bool("json", false, "print the result as JSON") }

func str(fs *flag.FlagSet, name string) string {
	if f := fs.Lookup(name); f != nil {
		return f.Value.String()
	}

	return ""
}

func optional(fs *flag.FlagSet, name string) *string {
	if v := str(fs, name); v != "" {
		return &v
	}

	return nil
}

// uses reads --use: the accounts a Run's workers may use, as integration:label
// separated by commas ("google:work,google:personal"). Nil when none is given.
func uses(fs *flag.FlagSet) *[]string {
	given := str(fs, "use")
	if given == "" {
		return nil
	}

	var out []string

	for use := range strings.SplitSeq(given, ",") {
		if use = strings.TrimSpace(use); use != "" {
			out = append(out, use)
		}
	}

	return &out
}

func boolean(fs *flag.FlagSet, name string) bool {
	b, _ := strconv.ParseBool(str(fs, name))

	return b
}

func number(fs *flag.FlagSet, name string) int {
	n, _ := strconv.Atoi(str(fs, name))

	return n
}
