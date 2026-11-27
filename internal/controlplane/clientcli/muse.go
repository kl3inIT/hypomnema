package clientcli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// The Muse pool (issue #782), as Settings → Gateway → Muse shows it: the
// accounts in the owner's order with their states and usage, and the tools'
// switch. An account is added by signing in on that page, in the owner's tab
// of the shared browser: a terminal has no view of it.

func museAccounts(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.GetMuseWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reading the Muse accounts: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("reading the Muse accounts", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printMuse(resp.JSON200) })
}

// printMuse prints the tools' switch, then a line an account: its turn, id,
// label, whether it is on, its state with its cause, and its usage.
func (e *env) printMuse(m *openapi.Muse) {
	tools := "off"
	if m.ToolsOn {
		tools = "on"
	}

	e.printf("tools %s\n", tools)

	for i, a := range m.Accounts {
		on := "on"
		if !a.On {
			on = "off"
		}

		state := strings.ReplaceAll(a.State, "_", " ")
		if a.Resting {
			state += ", resting"
		}

		if a.Cause != nil {
			state += " (" + *a.Cause + ")"
		}

		usage := a.Usage
		if a.UsageError != nil {
			usage += " (" + *a.UsageError + ")"
		}

		e.printf("%d\t%s\t%s\t%s\t%s\t%s\n", i+1, a.Id, a.Label, on, state, usage)
	}
}

func museTools(ctx context.Context, e *env, fs *flag.FlagSet) error {
	state := fs.Arg(0)
	if state != "on" && state != "off" {
		return fmt.Errorf("%w: muse tools on|off", ErrUsage)
	}

	resp, err := e.api.SetMuseToolsWithResponse(ctx, openapi.MuseSwitch{On: state == "on"})
	if err != nil {
		return fmt.Errorf("switching the Muse tools: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("switching the Muse tools", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("the Muse tools are %s\n", state) })
}

func museAccountSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	account, state := fs.Arg(0), fs.Arg(1)
	if state != "on" && state != "off" {
		return fmt.Errorf("%w: muse account set <account> on|off", ErrUsage)
	}

	resp, err := e.api.SetMuseAccountWithResponse(ctx, account, openapi.MuseSwitch{On: state == "on"})
	if err != nil {
		return fmt.Errorf("changing %s: %w", account, err)
	}

	if resp.JSON200 == nil {
		return apiError("changing "+account, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { e.printf("Muse account %s is %s\n", account, state) })
}
