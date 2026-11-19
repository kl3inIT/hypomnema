package clientcli

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/serviceproxy"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
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
	if agenttool.UserTrustSupported() {
		launcher.ExecTrust = &agenttool.ExecTrust{Home: env.home, Names: func(ctx context.Context) ([]string, error) {
			return execTrustNames(ctx, cfg)
		}}
	}

	return launcher.Exec(ctx, integration, label, fs.Args()[2:])
}

func execTrustNames(ctx context.Context, cfg Config) ([]string, error) {
	client, err := newClient(cfg, requestTimeout)
	if err != nil {
		return nil, err
	}

	resp, err := client.ListGitHostsWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing service proxy hosts: %w", err)
	}

	if resp.JSON200 == nil {
		return nil, apiError("listing service proxy hosts", resp.StatusCode(), resp.JSONDefault)
	}

	names := serviceproxy.ExecTrustHosts()
	for _, host := range resp.JSON200.Data {
		if host.Kind == openapi.Gitlab && !host.Known && len(host.Connections) != 0 &&
			githost.ValidHost(host.Host) && net.ParseIP(host.Host) == nil {
			names = append(names, host.Host)
		}
	}

	return names, nil
}

func removeExecCA(ctx context.Context, env *env, _ *flag.FlagSet) error {
	removed, err := agenttool.RemoveExecCA(ctx, env.home)
	if err == nil && removed {
		env.printf("Removed the Hypomnema service proxy CA.\n")
	}

	return err
}
