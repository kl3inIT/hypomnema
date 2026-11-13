package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// What the Bot coordinator made, changed or removed of Bots and profiles from
// a chat (issue #169), and its undo: the owner's, as on the change's card.

func printSetupChange(e *env, c openapi.SetupChange) {
	state := "can be undone"

	switch {
	case c.UndoneAt != nil:
		state = "undone"
	case !c.CanUndo:
		state = val(c.Problem)
	}

	e.printf("%s\t%s %s %s\t%s\t%s\n", c.Id, c.Action, c.Kind, c.Name, c.CreatedAt.Local().Format("2006-01-02 15:04"),
		state)
}

func setupList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	chat := str(fs, "chat")
	if chat == "" {
		return errors.New("name the chat with --chat")
	}

	resp, err := e.api.ListSetupChangesWithResponse(ctx, &openapi.ListSetupChangesParams{ChatId: chat})
	if err != nil {
		return fmt.Errorf("listing the setup changes of chat %s: %w", chat, err)
	}

	if resp.JSON200 == nil {
		return apiError("listing the setup changes of chat "+chat, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, c := range resp.JSON200.Data {
			printSetupChange(e, c)
		}
	})
}

func setupUndo(ctx context.Context, e *env, fs *flag.FlagSet) error {
	id := fs.Arg(0)

	resp, err := e.api.UndoSetupChangeWithResponse(ctx, id)
	if err != nil {
		return fmt.Errorf("undoing setup change %s: %w", id, err)
	}

	if resp.JSON200 == nil {
		return apiError("undoing setup change "+id, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { printSetupChange(e, *resp.JSON200) })
}
