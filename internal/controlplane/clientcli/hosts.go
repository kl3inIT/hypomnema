package clientcli

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"
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

			token := "no token"
			if h.HasToken {
				token = "token"
			}

			e.printf("%s\t%s\t%s\t%s\n", h.Host, string(h.Kind), note, token)
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

// integrationsList shows each Integration with its Connections: the git
// hosts, then the services that sign in with an account (issue #103).
func integrationsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	services, err := signInIntegrations(ctx, e)
	if err != nil {
		return err
	}

	resp, err := e.api.ListGitHostsWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing integrations: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing integrations", resp.StatusCode(), resp.JSONDefault)
	}

	all := struct {
		Data     []openapi.GitHost     `json:"data"`
		Services []openapi.Integration `json:"services"`
	}{Data: resp.JSON200.Data, Services: services}

	return e.print(fs, all, func() {
		for _, h := range all.Data {
			connected := "not connected"
			if len(h.Connections) > 0 {
				connected = strings.Join(h.Connections, ",")
			}

			e.printf("%s\t%s\t%s\n", h.Host, h.Kind, connected)
		}

		for _, i := range all.Services {
			connected := "not connected"

			switch {
			case i.ClientId == nil && i.Kind == openapi.IntegrationKindOauth:
				connected = "no client"
			case len(i.Connections) > 0:
				notes := make([]string, 0, len(i.Connections))
				for _, c := range i.Connections {
					notes = append(notes, connectionNote(c))
				}

				connected = strings.Join(notes, "; ")
			}

			e.printf("%s\t%s\t%s\n", i.Id, i.Kind, connected)
		}
	})
}

// integrationsConnect sends a token read from a file, as a Connection under
// a label. The token is never an argument: arguments show in the shell's
// history and in the process list.
func integrationsConnect(ctx context.Context, e *env, fs *flag.FlagSet) error {
	host, label := hostOf(fs.Arg(0)), str(fs, "label")

	// A service that signs in with an account takes no token: the owner signs in. One that is an app has
	// nothing to sign in to.
	service, err := serviceOf(ctx, e, host)
	if err != nil {
		return err
	}

	if service != nil && service.Kind == openapi.IntegrationKindApp && fs.Lookup("as-user").Value.String() == "true" {
		return integrationsSignInAsUser(ctx, e, fs, host)
	}

	if service != nil && service.Kind == openapi.IntegrationKindApp {
		return integrationsConnectApp(ctx, e, fs, host)
	}

	if service != nil && service.Kind == openapi.IntegrationKindAccount {
		return integrationsConnectAccount(ctx, e, fs, host)
	}

	if service != nil && service.Kind == openapi.IntegrationKindRole {
		return integrationsConnectRole(ctx, e, fs, host)
	}

	if service != nil {
		return integrationsSignIn(ctx, e, fs, host)
	}

	file := str(fs, "token-file")
	if file == "" {
		return errors.New("pass --token-file with the file holding the token, or - to read it from standard input")
	}

	token, err := e.read(file)
	if err != nil {
		return err
	}

	token = strings.TrimSpace(token)

	resp, err := e.api.PutGitHostConnectionWithResponse(ctx, host, label, openapi.NewGitHostToken{Token: &token})
	if err != nil {
		return fmt.Errorf("connecting %s:%s: %w", host, label, err)
	}

	if resp.JSON200 == nil {
		return apiError("connecting "+host+":"+label, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("%s:%s is connected\n", host, label) })
}

func integrationsDisconnect(ctx context.Context, e *env, fs *flag.FlagSet) error {
	given, label, ok := strings.Cut(fs.Arg(0), ":")
	if !ok || label == "" {
		return errors.New("name the account as <integration>:<label>, such as github.com:work")
	}

	host := hostOf(given)

	if service, err := serviceOf(ctx, e, host); err != nil {
		return err
	} else if service != nil {
		return integrationsSignOut(ctx, e, fs, host, label)
	}

	resp, err := e.api.DeleteGitHostConnectionWithResponse(ctx, host, label)
	if err != nil {
		return fmt.Errorf("disconnecting %s:%s: %w", host, label, err)
	}

	if resp.JSON200 == nil {
		return apiError("disconnecting "+host+":"+label, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("%s:%s is forgotten\n", host, label) })
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

	// Each repository's Bot (issue #1334), by its name when the Bots can be read, else by its id.
	bots := map[string]string{}
	if listed, err := e.api.ListBotsWithResponse(ctx); err == nil && listed.JSON200 != nil {
		for _, b := range listed.JSON200.Data {
			bots[b.Id] = b.Name
		}
	}

	return e.print(fs, resp.JSON200, func() {
		for _, r := range resp.JSON200.Data {
			bot := "main bot"
			if id := val(r.Bot); id != "" {
				bot = cmp.Or(bots[id], id)
			}

			e.printf("%s\t%s\t%s\t%s\t%s\tbot: %s\n", r.Name, githost.Repo{Host: r.Host, Path: r.Path}.String(),
				val(r.Base), val(r.Connection), val(r.SshUrl), bot)
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

	hostKey, err := sshHostKey(e, fs)
	if err != nil {
		return err
	}

	resp, err := e.api.PutRepoWithResponse(ctx, name, openapi.NewRepo{
		Repo: repo, Base: optional(fs, "base"), Connection: optional(fs, "connection"), SshUrl: optional(fs, "ssh-url"),
		SshHostKey: hostKey,
	})
	if err != nil {
		return fmt.Errorf("registering repository %s: %w", name, err)
	}

	if resp.JSON200 == nil {
		return apiError("registering repository "+name, resp.StatusCode(), resp.JSONDefault)
	}

	r := resp.JSON200

	return e.print(fs, r, func() {
		e.printf("repository %s is %s\n", r.Name, githost.Repo{Host: r.Host, Path: r.Path}.String())
		printHostKeys(e, r.SshHostKey)
	})
}

// sshHostKey reads --ssh-host-key-file: the SSH host's keys, as ssh-keyscan
// prints them. Nil when it is not given.
func sshHostKey(e *env, fs *flag.FlagSet) (*string, error) {
	file := str(fs, "ssh-host-key-file")
	if file == "" {
		return nil, nil //nolint:nilnil // No file, no keys: the flag is optional.
	}

	keys, err := e.read(file)
	if err != nil {
		return nil, err
	}

	return &keys, nil
}

// printHostKeys prints the fingerprints of the SSH host's keys a repository
// trusts, for the owner to check against the host's own (issue #625): a host
// is trusted under them alone, never on first use.
func printHostKeys(e *env, keys *string) {
	if keys == nil {
		return
	}

	for _, fingerprint := range githost.HostKeyFingerprints(*keys) {
		e.printf("  trusts the SSH host key %s\n", fingerprint)
	}
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

// reposSet changes the flags given of a registered repository and keeps the
// rest: the API registers a name whole, so what it says now is read first.
func reposSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	name := fs.Arg(0)

	list, err := e.api.ListReposWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reading repository %s: %w", name, err)
	}

	if list.JSON200 == nil {
		return apiError("reading repository "+name, list.StatusCode(), list.JSONDefault)
	}

	i := slices.IndexFunc(list.JSON200.Data, func(r openapi.Repo) bool { return r.Name == name })
	if i < 0 {
		return fmt.Errorf("no repository is registered as %s; register it with `hypomnema repos add`", name)
	}

	r := list.JSON200.Data[i]
	body := openapi.NewRepo{
		Repo: githost.Repo{Host: r.Host, Path: r.Path}.String(), Base: r.Base, Connection: r.Connection,
		SshUrl: r.SshUrl, SshHostKey: r.SshHostKey,
	}

	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "base":
			body.Base = optional(fs, "base")
		case "connection":
			body.Connection = optional(fs, "connection")
		case "ssh-url":
			// Cloned over HTTPS again, the host's keys go with the address.
			if body.SshUrl = optional(fs, "ssh-url"); body.SshUrl == nil {
				body.SshHostKey = nil
			}
		}
	})

	if hostKey, err := sshHostKey(e, fs); err != nil {
		return err
	} else if hostKey != nil {
		body.SshHostKey = hostKey
	}

	resp, err := e.api.PutRepoWithResponse(ctx, name, body)
	if err != nil {
		return fmt.Errorf("changing repository %s: %w", name, err)
	}

	if resp.JSON200 == nil {
		return apiError("changing repository "+name, resp.StatusCode(), resp.JSONDefault)
	}

	changed := resp.JSON200

	return e.print(fs, changed, func() {
		over := "over HTTPS"
		if changed.SshUrl != nil {
			over = "over SSH from " + *changed.SshUrl
		}

		e.printf("repository %s is %s, cloned %s\n", changed.Name,
			githost.Repo{Host: changed.Host, Path: changed.Path}.String(), over)
		printHostKeys(e, changed.SshHostKey)
	})
}

// gitSSHKey prints the public key nodes clone over SSH with (issue #625),
// one line to paste into the git host's SSH keys.
func gitSSHKey(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.GetGitSSHKeyWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reading the SSH key: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("reading the SSH key", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("%s\n", resp.JSON200.PublicKey) })
}
