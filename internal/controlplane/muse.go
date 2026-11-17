package controlplane

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser"
	"github.com/kl3inIT/hypomnema/internal/controlplane/muse"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// The Muse pool (issue #782): the owner's Muse accounts, asked in the shared
// browser's Chrome, each in a browser context of its own, with muse.db and
// muse-media beside the control plane's database. A control plane without the
// browser or the vault's keys has no Muse, and says why.

// museBrowser gives the pool the shared browser's contexts.
type museBrowser struct{ *browser.Engine }

func (b museBrowser) Page(ctx context.Context, key string, cookies []browser.Cookie) (muse.Page, error) {
	return b.Engine.Page(ctx, key, cookies)
}

// openMuse opens the pool, or answers nil with why it cannot be had.
func openMuse(
	ctx context.Context, cfg Config, svc *orchestration.Service, secrets *vault.Service, shared *browser.Engine,
	logger *slog.Logger,
) *muse.Service {
	switch {
	case cfg.BrowserOff:
		logger.InfoContext(ctx, "Muse is off: the shared browser is off", slog.String("event", "muse.off"))

		return nil
	case cfg.VaultKeys == nil:
		logger.InfoContext(ctx, "Muse is off: its accounts' cookies are kept in the vault, which has no key",
			slog.String("event", "muse.off"))

		return nil
	}

	m, err := muse.Open(ctx, filepath.Join(filepath.Dir(cfg.Database.Path), "muse.db"), muse.Config{
		Accounts: svc, Secrets: secrets, Browser: museBrowser{shared}, SecretName: accountSecretName, Logger: logger,
	})
	if err != nil {
		logger.WarnContext(ctx, "Muse cannot be used", slog.String("error", err.Error()))

		return nil
	}

	return m
}
