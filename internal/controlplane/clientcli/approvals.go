package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Approvals and the answers an action of an account has, from the terminal
// (issue #154). Everything the web does here is one of these commands.

func printApproval(e *env, a openapi.Approval) {
	where := ""

	switch {
	case a.ChatId != nil:
		where = " in chat " + *a.ChatId
	case a.TaskId != nil:
		where = " for task " + *a.TaskId
	}

	state := string(a.Status)
	if a.Scope != nil && *a.Scope == "session" {
		state += " for the session"
	}

	e.printf("%s  %s  %s\n  %s:%s · %s · asked by %s%s\n", a.Id, state, a.Title, a.Integration, a.Label,
		a.ActionName, a.AskedBy, where)

	for _, f := range a.Payload {
		value := f.Value
		if strings.Contains(value, "\n") {
			value = "\n    " + strings.ReplaceAll(value, "\n", "\n    ")
		}

		e.printf("  %s: %s\n", f.Label, value)
	}
}

func approvalsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	params := &openapi.ListApprovalsParams{}

	if status := str(fs, "status"); status != "all" {
		s := openapi.ApprovalStatus(status)
		params.Status = &s
	}

	resp, err := e.api.ListApprovalsWithResponse(ctx, params)
	if err != nil {
		return fmt.Errorf("listing approvals: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing approvals", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if len(resp.JSON200.Data) == 0 {
			e.printf("nothing waits for your approval\n")
		}

		for _, a := range resp.JSON200.Data {
			printApproval(e, a)
		}
	})
}

func decide(ctx context.Context, e *env, fs *flag.FlagSet, decision openapi.ApprovalDecisionDecision) error {
	resp, err := e.api.DecideApprovalWithResponse(ctx, fs.Arg(0), openapi.ApprovalDecision{Decision: decision})
	if err != nil {
		return fmt.Errorf("answering approval %s: %w", fs.Arg(0), err)
	}

	if resp.JSON200 == nil {
		return apiError("answering approval "+fs.Arg(0), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { printApproval(e, *resp.JSON200) })
}

func approvalsApprove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	if boolean(fs, "routine") {
		return decide(ctx, e, fs, openapi.ApproveRoutine)
	}

	if fs.Lookup("session").Value.String() == "true" {
		return decide(ctx, e, fs, openapi.ApproveSession)
	}

	return decide(ctx, e, fs, openapi.Approve)
}

func approvalsReject(ctx context.Context, e *env, fs *flag.FlagSet) error {
	return decide(ctx, e, fs, openapi.Reject)
}

// account splits "google:work".
func account(arg string) (integration, label string, err error) {
	integration, label, ok := strings.Cut(arg, ":")
	if !ok || integration == "" || label == "" {
		return "", "", errors.New("name the account as <integration>:<label>, such as google:work")
	}

	return integration, label, nil
}

func printAction(e *env, a openapi.ConnectionAction) {
	note := ""
	if a.Policy != a.Default {
		note = "  (default " + string(a.Default) + ")"
	}

	e.printf("%-18s %-6s %s: %s%s\n", a.Id, a.Policy, a.Service, a.Name, note)
}

// integrationsActions lists what agents may do with an account.
func integrationsActions(ctx context.Context, e *env, fs *flag.FlagSet) error {
	integration, label, err := account(fs.Arg(0))
	if err != nil {
		return err
	}

	resp, err := e.api.ListConnectionActionsWithResponse(ctx, integration, label)
	if err != nil {
		return fmt.Errorf("listing the actions of %s: %w", fs.Arg(0), err)
	}

	if resp.JSON200 == nil {
		return apiError("listing the actions of "+fs.Arg(0), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if len(resp.JSON200.Data) == 0 {
			e.printf("%s does not go through the service proxy: there are no actions to answer for\n", integration)
		}

		for _, a := range resp.JSON200.Data {
			printAction(e, a)
		}
	})
}

// integrationsActionsSet sets the answer of one action of an account.
func integrationsActionsSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	integration, label, err := account(fs.Arg(0))
	if err != nil {
		return err
	}

	policy := openapi.ActionPolicy(fs.Arg(2))
	if policy != openapi.Allow && policy != openapi.Ask && policy != openapi.Never {
		return errors.New("say allow, ask or never")
	}

	resp, err := e.api.SetConnectionActionWithResponse(ctx, integration, label, fs.Arg(1),
		openapi.ActionPolicyChange{Policy: policy})
	if err != nil {
		return fmt.Errorf("setting %s of %s: %w", fs.Arg(1), fs.Arg(0), err)
	}

	if resp.JSON200 == nil {
		return apiError("setting "+fs.Arg(1)+" of "+fs.Arg(0), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { printAction(e, *resp.JSON200) })
}
