package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/shared/githost"
)

// Git hosts and registered repositories from the terminal (issue #44).

func hostsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListGitHostsWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing hosts", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, h := range resp.JSON200.Data {
			note := "declared"
			if h.Known {
				note = "known"
			}

			e.printf("%s\t%s\t%s\n", h.Host, string(h.Kind), note)
		}
	})
}

func hostsAdd(ctx context.Context, e *env, fs *flag.FlagSet) error {
	host := hostOf(fs.Arg(0))

	resp, err := e.api.PutGitHostWithResponse(ctx, host, openapi.NewGitHost{Kind: openapi.GitHostKind(str(fs, "kind"))})
	if err != nil {
		return fmt.Errorf("declaring host %s: %w", host, err)
	}

	if resp.JSON200 == nil {
		return apiError("declaring host "+host, resp.StatusCode(), resp.JSONDefault)
	}

	h := resp.JSON200

	return e.print(fs, h, func() { e.printf("host %s is %s\n", h.Host, string(h.Kind)) })
}

// hostOf takes a host as the owner types it: its name, or a URL copied from the browser.
func hostOf(s string) string {
	s = strings.TrimSpace(s)
	if _, rest, ok := strings.Cut(s, "://"); ok {
		s = rest
	}

	host, _, _ := strings.Cut(s, "/")

	return strings.ToLower(host)
}

func hostsRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	host := hostOf(fs.Arg(0))

	resp, err := e.api.DeleteGitHostWithResponse(ctx, host)
	if err != nil {
		return fmt.Errorf("removing host %s: %w", host, err)
	}

	if resp.StatusCode() != 204 {
		return apiError("removing host "+host, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]string{"removed": host}, func() { e.printf("host %s is forgotten\n", host) })
}

func reposList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListReposWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing repositories: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing repositories", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, r := range resp.JSON200.Data {
			e.printf("%s\t%s\t%s\n", r.Name, githost.Repo{Host: r.Host, Path: r.Path}.String(), val(r.Base))
		}
	})
}

func reposAdd(ctx context.Context, e *env, fs *flag.FlagSet) error {
	repo := fs.Arg(0)

	name := str(fs, "name")
	if name == "" {
		parsed, err := githost.Parse(repo)
		if err != nil {
			return errors.New("give the repository as owner/name or its URL, or pass --name")
		}

		// The last part of its path: kl3inIT/second-bite is second-bite.
		name = strings.ToLower(parsed.Path[strings.LastIndex(parsed.Path, "/")+1:])
	}

	resp, err := e.api.PutRepoWithResponse(ctx, name, openapi.NewRepo{Repo: repo, Base: optional(fs, "base")})
	if err != nil {
		return fmt.Errorf("registering repository %s: %w", name, err)
	}

	if resp.JSON200 == nil {
		return apiError("registering repository "+name, resp.StatusCode(), resp.JSONDefault)
	}

	r := resp.JSON200

	return e.print(fs, r, func() {
		e.printf("repository %s is %s\n", r.Name, githost.Repo{Host: r.Host, Path: r.Path}.String())
	})
}

func reposRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	name := fs.Arg(0)

	resp, err := e.api.DeleteRepoWithResponse(ctx, name)
	if err != nil {
		return fmt.Errorf("removing repository %s: %w", name, err)
	}

	if resp.StatusCode() != 204 {
		return apiError("removing repository "+name, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]string{"removed": name}, func() { e.printf("repository %s is forgotten\n", name) })
}
