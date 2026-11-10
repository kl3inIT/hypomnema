package clientcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Integrations that sign in with an account, from the terminal (issue #103).
// A git host is connected with a token (hosts.go); these are connected by
// signing in, in a browser on any machine, or, when the Integration is an app
// of the service (Lark, issue #135), with no sign-in at all.

// maxPastedBytes bounds the address pasted back after a sign-in.
const maxPastedBytes = 8192

// signInIntegrations returns the Integrations that sign in with an account.
func signInIntegrations(ctx context.Context, e *env) ([]openapi.Integration, error) {
	resp, err := e.api.ListIntegrationsWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing integrations: %w", err)
	}

	if resp.JSON200 == nil {
		return nil, apiError("listing integrations", resp.StatusCode(), resp.JSONDefault)
	}

	return resp.JSON200.Data, nil
}

// serviceOf returns the Integration id names when it is one that signs in
// with an account or is an app, and nil when it is not: a git host.
func serviceOf(ctx context.Context, e *env, id string) (*openapi.Integration, error) {
	all, err := signInIntegrations(ctx, e)
	if err != nil {
		return nil, err
	}

	for _, i := range all {
		if i.Id == id {
			return &i, nil
		}
	}

	return nil, nil //nolint:nilnil // No such service is an answer, not a failure: the name is then a git host's.
}

func connectionNote(c openapi.IntegrationConnection) string {
	note := c.Label + " (" + c.Account
	if len(c.Services) > 0 {
		note += ": " + strings.Join(c.Services, ",")
	}

	note += ")"

	if c.SignInRequired {
		note += " needs sign-in"
	}

	if c.Chats {
		note += " chats"
	}

	return note
}

// integrationsClientSet sends the client the owner created at the service,
// read from a file: the JSON Google gives for an OAuth client, or for an app
// (Lark) a JSON object with its app_id and app_secret, which the service
// shows on a page and gives no file for. The secret is never an argument:
// arguments show in the shell's history and in the process list.
func integrationsClientSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	file := str(fs, "client-file")
	if file == "" {
		return errors.New("pass --client-file with the JSON file the service gave for the client, or - to read it " +
			"from standard input")
	}

	raw, err := e.read(file)
	if err != nil {
		return err
	}

	// Google wraps the client in the kind it was created as: "installed" for a desktop app, "web" for a web one.
	type client struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}

	var parsed struct {
		client

		Installed *client `json:"installed"`
		Web       *client `json:"web"`
		AppID     string  `json:"app_id"`
		AppSecret string  `json:"app_secret"`
	}

	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return errors.New("the client file is not JSON; use the file downloaded from the service")
	}

	c := parsed.client
	if parsed.Installed != nil {
		c = *parsed.Installed
	} else if parsed.Web != nil {
		c = *parsed.Web
	}

	if parsed.AppID != "" {
		c = client{ClientID: parsed.AppID, ClientSecret: parsed.AppSecret}
	}

	if c.ClientID == "" || c.ClientSecret == "" {
		return errors.New("the client file has no client_id and client_secret, nor an app_id and app_secret")
	}

	resp, err := e.api.PutIntegrationClientWithResponse(ctx, id, openapi.NewIntegrationClient{
		ClientId: c.ClientID, ClientSecret: &c.ClientSecret,
	})
	if err != nil {
		return fmt.Errorf("keeping the client of %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("keeping the client of "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if resp.JSON200.Kind == openapi.App {
			e.printf("%s is app %s\n", id, c.ClientID)

			return
		}

		e.printf("%s signs in with client %s\n", id, c.ClientID)
	})
}

// integrationsConnectApp connects an Integration that is an app: there is
// nothing to sign in to, the control plane checks the app's id and secret
// with the service.
func integrationsConnectApp(ctx context.Context, e *env, fs *flag.FlagSet, id string) error {
	label := str(fs, "label")

	resp, err := e.api.PutIntegrationConnectionWithResponse(ctx, id, label)
	if err != nil {
		return fmt.Errorf("connecting %s:%s: %w", id, label, err)
	}

	if resp.JSON200 == nil {
		return apiError("connecting "+id+":"+label, resp.StatusCode(), resp.JSONDefault)
	}

	c := resp.JSON200

	return e.print(fs, c, func() { e.printf("%s:%s is connected as the app %s\n", id, c.Label, c.Account) })
}

// integrationsSignIn signs an account in: it prints the link, waits for the
// address the browser ended on, and completes the sign-in with it. Nothing is
// opened on this machine, so the browser can be on any other.
func integrationsSignIn(ctx context.Context, e *env, fs *flag.FlagSet, id string) error {
	label := str(fs, "label")

	var services []string
	if given := str(fs, "services"); given != "" {
		services = strings.Split(given, ",")
	}

	begun, err := e.api.BeginIntegrationSignInWithResponse(ctx, id, openapi.NewSignIn{Label: label, Services: services})
	if err != nil {
		return fmt.Errorf("starting the sign-in of %s:%s: %w", id, label, err)
	}

	if begun.JSON200 == nil {
		return apiError("starting the sign-in of "+id+":"+label, begun.StatusCode(), begun.JSONDefault)
	}

	e.printf("Open this link in a browser on any machine and sign in:\n\n  %s\n\n"+
		"The browser ends on a page that does not load. Copy its whole address and paste it here, within %d "+
		"minutes:\n", begun.JSON200.Url, begun.JSON200.ExpiresInSeconds/60)

	// A read that ends without a line still gave what was typed before it ended.
	pasted, _ := bufio.NewReader(io.LimitReader(e.in, maxPastedBytes)).ReadString('\n')

	pasted = strings.TrimSpace(pasted)
	if pasted == "" {
		return errors.New("no address was pasted")
	}

	done, err := e.api.CompleteIntegrationSignInWithResponse(ctx, id, begun.JSON200.Id, openapi.SignInCallback{
		Callback: &pasted,
	})
	if err != nil {
		return fmt.Errorf("completing the sign-in of %s:%s: %w", id, label, err)
	}

	if done.JSON200 == nil {
		return apiError("completing the sign-in of "+id+":"+label, done.StatusCode(), done.JSONDefault)
	}

	c := done.JSON200

	return e.print(fs, c, func() {
		e.printf("%s:%s is connected as %s, with %s\n", id, c.Label, c.Account, strings.Join(c.Services, ","))
	})
}

func integrationsSignOut(ctx context.Context, e *env, fs *flag.FlagSet, id, label string) error {
	resp, err := e.api.DeleteIntegrationConnectionWithResponse(ctx, id, label)
	if err != nil {
		return fmt.Errorf("disconnecting %s:%s: %w", id, label, err)
	}

	if resp.JSON200 == nil {
		return apiError("disconnecting "+id+":"+label, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		e.printf("%s:%s is forgotten\n", id, label)

		if !resp.JSON200.Revoked {
			e.printf("the service could not be asked to end the access; remove it in the account's own settings\n")
		}
	})
}

// integrationsChats says whether the coordinator may use an account in chats.
// No agent uses an account until the owner says so.
func integrationsChats(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id, label, ok := strings.Cut(fs.Arg(0), ":")
	if !ok || label == "" {
		return errors.New("name the account as <integration>:<label>, such as google:work")
	}

	var allowed bool

	switch fs.Arg(1) {
	case "on":
		allowed = true
	case "off":
	default:
		return errors.New("say on or off")
	}

	resp, err := e.api.PatchIntegrationConnectionWithResponse(ctx, id, label, openapi.ConnectionUse{Chats: allowed})
	if err != nil {
		return fmt.Errorf("changing %s:%s: %w", id, label, err)
	}

	if resp.JSON200 == nil {
		return apiError("changing "+id+":"+label, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if allowed {
			e.printf("the coordinator may use %s:%s in chats\n", id, label)
		} else {
			e.printf("the coordinator may not use %s:%s\n", id, label)
		}
	})
}
