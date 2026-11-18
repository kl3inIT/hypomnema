package clientcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Importing a Claude Code session into the control plane (issue #828). The
// file is read on this machine and sent once over the client token's
// connection; it is never printed. --detect looks where Claude Code keeps its
// session, as OmniRoute's claudeAuthFile.ts and Hermes' anthropic_credentials.py
// look in ~/.claude: the default directory, $CLAUDE_CONFIG_DIR, and the
// per-account directories Orca gives each Claude profile.

// maxImportBytes bounds a credentials file read here, as the API does.
const maxImportBytes = 64 << 10

// serverShared are first words the control plane's own commands use too: one
// goes to this package only when one of its commands matches, as
// `accounts import` does while `accounts list` stays the control plane's.
var serverShared = map[string]bool{"accounts": true}

// Handles says whether args name one of this package's commands.
func Handles(args []string) bool {
	if len(args) == 0 {
		return false
	}

	if c, _ := find(args); c != nil {
		return true
	}

	return slices.Contains(Commands(), args[0]) && !serverShared[args[0]]
}

func accountsImportFlags(fs *flag.FlagSet) {
	fs.String("file", "", "Claude Code's .credentials.json, or - for standard input")
	fs.Bool("detect", false, "list the Claude Code sessions on this machine, with their e-mails")
	fs.String("email", "", "with --detect: import the session of this e-mail")
	fs.String("label", "", "the account's name for the owner (default its e-mail)")
	jsonFlag(fs)
}

// claudeSession is a credentials file found on this machine and whose it is.
type claudeSession struct {
	Path  string `json:"path"`
	Email string `json:"email"`
}

func accountsImport(ctx context.Context, e *env, fs *flag.FlagSet) error {
	if runtime := fs.Arg(0); runtime != "claude" {
		return fmt.Errorf("%w: only claude sessions are imported, from Claude Code's .credentials.json", ErrUsage)
	}

	file, email := str(fs, "file"), str(fs, "email")
	detect := boolean(fs, "detect")

	switch {
	case detect && file != "":
		return fmt.Errorf("%w: give --file or --detect, not both", ErrUsage)
	case !detect && file == "":
		return fmt.Errorf("%w: give --file <path> or --detect", ErrUsage)
	case email != "" && !detect:
		return fmt.Errorf("%w: --email picks one of the sessions --detect finds", ErrUsage)
	}

	if detect {
		sessions := detectClaudeSessions()
		if email == "" {
			return e.print(fs, sessions, func() { printSessions(e, sessions) })
		}

		found := slices.IndexFunc(sessions, func(s claudeSession) bool { return strings.EqualFold(s.Email, email) })
		if found < 0 {
			return fmt.Errorf("%w: no Claude Code session of %s on this machine; --detect lists them", ErrUsage, email)
		}

		file = sessions[found].Path
	}

	raw, err := readCredentials(e.in, file)
	if err != nil {
		return err
	}

	credentials := string(raw)
	body := openapi.NewAccountImport{Runtime: openapi.NewAccountImportRuntimeClaude, Credentials: &credentials}

	if label := str(fs, "label"); label != "" {
		body.Label = &label
	}

	resp, err := e.api.ImportAccountWithResponse(ctx, body)
	if err != nil {
		return fmt.Errorf("importing the session: %w", err)
	}

	if resp.JSON201 == nil {
		return apiError("importing the session", resp.StatusCode(), resp.JSONDefault)
	}

	a := resp.JSON201

	return e.print(fs, a, func() { e.printf("%s\t%s\t%s\n", a.Id, a.Runtime, a.Label) })
}

func printSessions(e *env, sessions []claudeSession) {
	if len(sessions) == 0 {
		e.printf("no Claude Code session on this machine\n")

		return
	}

	for _, s := range sessions {
		email := s.Email
		if email == "" {
			email = "(e-mail unknown)"
		}

		e.printf("%s\t%s\n", email, s.Path)
	}
}

func readCredentials(in io.Reader, file string) ([]byte, error) {
	var r io.Reader

	if file == "-" {
		r = in
	} else {
		f, err := os.Open(file) //nolint:gosec // the owner names the file to import
		if err != nil {
			return nil, fmt.Errorf("reading the credentials: %w", err)
		}
		defer func() { _ = f.Close() }()

		r = f
	}

	raw, err := io.ReadAll(io.LimitReader(r, maxImportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading the credentials: %w", err)
	}

	if len(raw) > maxImportBytes {
		return nil, errors.New("the credentials are larger than Claude Code's file")
	}

	return raw, nil
}

// claudeConfigDirs are the directories Claude Code may keep a session in on
// this machine: $CLAUDE_CONFIG_DIR, ~/.claude, and each Orca Claude profile's.
func claudeConfigDirs() []string {
	var dirs []string

	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		dirs = append(dirs, d)
	}

	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".claude"))
	}

	if config, err := os.UserConfigDir(); err == nil {
		profiles, _ := filepath.Glob(filepath.Join(config, "orca", "claude-profiles", "*", "home"))
		dirs = append(dirs, profiles...)
	}

	return dirs
}

// detectClaudeSessions finds the credentials files with a refresh token, each
// once, with the e-mail Claude Code recorded beside it. No token is read out.
func detectClaudeSessions() []claudeSession {
	var out []claudeSession

	seen := map[string]bool{}

	for _, dir := range claudeConfigDirs() {
		path := filepath.Join(dir, ".credentials.json")

		abs, err := filepath.Abs(path)
		if err != nil || seen[strings.ToLower(abs)] || !hasClaudeSession(abs) {
			continue
		}

		seen[strings.ToLower(abs)] = true

		out = append(out, claudeSession{Path: abs, Email: claudeEmail(dir)})
	}

	return out
}

func hasClaudeSession(path string) bool {
	raw, err := os.ReadFile(path) //nolint:gosec // Claude Code's own file, found by name
	if err != nil {
		return false
	}

	var file struct {
		ClaudeAiOauth struct {
			RefreshToken string `json:"refreshToken"`
		} `json:"claudeAiOauth"`
	}

	return json.Unmarshal(raw, &file) == nil && file.ClaudeAiOauth.RefreshToken != ""
}

// claudeEmail is the e-mail Claude Code recorded for the session in dir: its
// .claude.json, inside dir for a profile, or beside ~/.claude for the default.
func claudeEmail(dir string) string {
	for _, path := range []string{filepath.Join(dir, ".claude.json"), filepath.Join(filepath.Dir(dir), ".claude.json")} {
		raw, err := os.ReadFile(path) //nolint:gosec // Claude Code's own file, found by name
		if err != nil {
			continue
		}

		var state struct {
			OauthAccount struct {
				EmailAddress string `json:"emailAddress"`
			} `json:"oauthAccount"`
		}

		if json.Unmarshal(raw, &state) == nil && state.OauthAccount.EmailAddress != "" {
			return state.OauthAccount.EmailAddress
		}
	}

	return ""
}
