package clientcli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

func execService(ctx context.Context, env *env, fs *flag.FlagSet) error {
	integration, label, ok := orchestration.SplitUse(fs.Arg(0))
	if !ok || fs.Arg(1) != "--" {
		return fmt.Errorf("%w: hypomnema exec <integration>:<label> -- <command> [args...]", ErrUsage)
	}

	cfg, err := LoadConfig(env.home)
	if err != nil {
		return err
	}

	launcher := agenttool.Launcher{
		URL: cfg.URL, Key: cfg.Token, Stdin: env.in, Stdout: env.out, Stderr: os.Stderr,
		Client: &http.Client{CheckRedirect: sameOrigin},
	}
	return launcher.Exec(ctx, integration, label, fs.Args()[2:])
}
