package clientcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Config is where the client API is and the token it is called with.
type Config struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// DefaultHome is ~/.hypomnema.
func DefaultHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the home directory: %w", err)
	}

	return filepath.Join(home, ".hypomnema"), nil
}

func configPath(home string) string { return filepath.Join(home, "client.json") }

// login checks the token against the control plane and saves both. The
// token is read from a file, so it never appears on a command line.
func login(ctx context.Context, e *env, fs *flag.FlagSet) error {
	rawURL, tokenFile := str(fs, "url"), str(fs, "token-file")
	if rawURL == "" || tokenFile == "" || fs.NArg() != 0 {
		return fmt.Errorf("%w: hypomnema login --url URL --token-file FILE", ErrUsage)
	}

	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("the control plane URL %q must be http(s)://host", rawURL)
	}

	raw, err := os.ReadFile(tokenFile) //nolint:gosec // the owner names the file holding the token
	if err != nil {
		return fmt.Errorf("reading the token file: %w", err)
	}

	cfg := Config{URL: strings.TrimSuffix(rawURL, "/"), Token: strings.TrimSpace(string(raw))}
	if !strings.HasPrefix(cfg.Token, "hyp_") {
		return errors.New("the token file does not hold a client token (hyp_…)")
	}

	c, err := newClient(cfg)
	if err != nil {
		return err
	}

	me, err := c.GetMeWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reaching %s: %w", cfg.URL, err)
	}

	if me.JSON200 == nil {
		return apiError("checking the token", me.StatusCode(), me.JSONDefault)
	}

	if err := saveConfig(e.home, cfg); err != nil {
		return err
	}

	e.printf("logged in to %s as %s (%s)\n", cfg.URL, me.JSON200.Name, me.JSON200.Kind)

	return nil
}

func saveConfig(home string, cfg Config) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("making %s: %w", home, err)
	}

	if err := os.Chmod(home, 0o700); err != nil { //nolint:gosec // a directory needs its execute bit; 0700 is owner-only
		return fmt.Errorf("restricting %s: %w", home, err)
	}

	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the configuration: %w", err)
	}

	// Written whole and renamed into place, so a crash never leaves half a file.
	tmp := configPath(home) + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing the configuration: %w", err)
	}

	if err := os.Rename(tmp, configPath(home)); err != nil {
		return fmt.Errorf("saving the configuration: %w", err)
	}

	return nil
}

// LoadConfig reads the configuration `login` wrote.
func LoadConfig(home string) (Config, error) {
	raw, err := os.ReadFile(configPath(home))
	if err != nil {
		return Config{}, fmt.Errorf("reading the configuration (run `hypomnema login` first): %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("decoding %s: %w", configPath(home), err)
	}

	if cfg.URL == "" || cfg.Token == "" {
		return Config{}, fmt.Errorf("%s needs url and token", configPath(home))
	}

	return cfg, nil
}

// whoami prints the token this machine is logged in with.
func whoami(ctx context.Context, e *env, fs *flag.FlagSet) error {
	me, err := e.api.GetMeWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reaching the control plane: %w", err)
	}

	if me.JSON200 == nil {
		return apiError("checking the token", me.StatusCode(), me.JSONDefault)
	}

	return e.print(fs, me.JSON200, func() { e.printf("%s (%s, %s)\n", me.JSON200.Name, me.JSON200.Kind, me.JSON200.Id) })
}
