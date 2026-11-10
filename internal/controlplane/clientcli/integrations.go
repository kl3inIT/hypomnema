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
// signing in, in a browser on any machine.

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

// signsIn says whether id names an Integration that signs in with an account.
func signsIn(ctx context.Context, e *env, id string) (bool, error) {
	all, err := signInIntegrations(ctx, e)
	if err != nil {
		return false, err
	}

	for _, i := range all {
		if i.Id == id {
			return true, nil
		}
	}

	return false, nil
}

func connectionNote(c openapi.IntegrationConnection) string {
	note := c.Label + " (" + c.Account + ": " + strings.Join(c.Services, ",") + ")"
	if c.SignInRequired {
		note += " needs sign-in"
	}

	return note
}

// integrationsClientSet sends the OAuth client the owner created at the
// service, read from the file the service gave: Google's "client secret"
// JSON. The secret is never an argument: arguments show in the shell's
// history and in the process list.
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

	if c.ClientID == "" || c.ClientSecret == "" {
		return errors.New("the client file has no client_id and client_secret")
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

	return e.print(fs, resp.JSON200, func() { e.printf("%s signs in with client %s\n", id, c.ClientID) })
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
