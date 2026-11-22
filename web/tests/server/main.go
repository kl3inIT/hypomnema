// Command server is the control plane the browser tests of the built web client run against when what they check
// is the server's own answer, its headers above all (issue #737): `hypomnema serve` itself, on a fresh database in a
// temporary folder, with the build embedded as on a server. Before it serves, it adds the owner the tests sign in as
// and a Bot's chat with the pages under pages/, shown as a Bot shows them (artifactpack.Read, then ShowArtifact).
//
// It is run by playwright.config.ts, after the build, from web/: go run ./tests/server.
package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/kl3inIT/hypomnema/internal/controlplane"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/artifactpack"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
	"github.com/kl3inIT/hypomnema/internal/shared/sqlite"
)

// Addr, Owner and Password are what playwright.config.ts and the tests name.
const (
	Addr     = "127.0.0.1:4180"
	Owner    = "owner"
	Password = "browser tests"
)

//go:embed pages
var embedded embed.FS

// pages are the artifacts shown, by the folder they are read from (empty for pages/), their path in it and their
// title. The last is the built-in pages skill's dashboard (issue #423), where it is from web/.
var pages = []struct{ dir, path, title string }{
	{"", "page.html", "One page"},
	{"", "folder", "Folder page"},
	{"../internal/controlplane/clientcli/skills/pages", "dashboard.html", "Sales dashboard"},
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dir, err := os.MkdirTemp("", "hypomnema-web-tests-")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(dir) }()

	// A vault with a root key made for this run: a view-only link is kept only with its token sealed (issue #996).
	key, err := vault.GenerateKey("browser-tests")
	if err != nil {
		return err
	}

	env := map[string]string{
		"HYPOMNEMA_DATABASE_PATH":       filepath.Join(dir, "hypomnema.db"),
		"HYPOMNEMA_HTTP_ADDR":           Addr,
		"HYPOMNEMA_PUBLIC_URL":          "https://browser-tests.example",
		"HYPOMNEMA_BROWSER":             "0",
		"HYPOMNEMA_FFMPEG":              "0",
		"HYPOMNEMA_VAULT_KEYS":          key,
		"HYPOMNEMA_VAULT_ACTIVE_KEY_ID": "browser-tests",
	}

	cfg, err := controlplane.LoadConfig(func(name string) string { return env[name] })
	if err != nil {
		return err
	}

	if err := controlplane.Migrate(ctx, cfg, io.Discard); err != nil {
		return err
	}

	if err := seed(ctx, cfg.Database.Path, dir); err != nil {
		return fmt.Errorf("seeding: %w", err)
	}

	err = controlplane.Serve(ctx, cfg, os.Stdout)
	if errors.Is(err, context.Canceled) {
		return nil
	}

	return err
}

// seed writes the pages into dir, where a Bot's folder would hold them, and shows them from there.
func seed(ctx context.Context, path, dir string) (err error) {
	sub, err := fs.Sub(embedded, "pages")
	if err != nil {
		return err
	}

	folder := filepath.Join(dir, "pages")
	if err := os.CopyFS(folder, sub); err != nil {
		return err
	}

	db, err := sqlite.Open(ctx, sqlite.Config{Path: path})
	if err != nil {
		return err
	}

	defer func() { err = errors.Join(err, db.Close()) }()

	svc := orchestration.New(db, platform.SystemClock{}, platform.NewID)

	if _, err := svc.Users().AddUser(ctx, Owner, Password); err != nil {
		return err
	}

	chat, err := svc.CreateChat(ctx, "", "Pages")
	if err != nil {
		return err
	}

	for _, p := range pages {
		from := folder
		if p.dir != "" {
			from = filepath.FromSlash(p.dir)
		}

		files, skipped, err := artifactpack.Read(from, p.path)
		if err != nil {
			return err
		}

		if len(skipped) > 0 {
			return fmt.Errorf("%s: %s", p.path, artifactpack.SkippedNote(skipped))
		}

		pack, err := artifactpack.Check(files)
		if err != nil {
			return err
		}

		by := orchestration.ArtifactMaker{Bot: chat.Bot, Chat: chat.ID}
		if _, _, err := svc.ShowArtifact(ctx, by, orchestration.ShowArtifact{Title: p.title, Pack: pack}); err != nil {
			return err
		}
	}

	return nil
}
