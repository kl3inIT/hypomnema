package clientcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// The Lark bot, from the terminal (issue #136). The app's secret is read
// from a file and is never an argument: arguments show in the shell's
// history and in the process list.

func printLarkBot(e *env, b *openapi.LarkBot) {
	state := "off"

	switch {
	case b.Enabled && b.Connected:
		state = "on, connected"
	case b.Enabled && b.LastError != nil:
		state = "on, not connected: " + *b.LastError
	case b.Enabled:
		state = "on, not connected yet"
	}

	e.printf("app %s\t%s\tmode %s\towner %s\n", b.AppId, state, b.Mode, b.OwnerOpenId)

	if b.ConnectedAppId != nil {
		e.printf("connected as app %s until the control plane starts again\n", *b.ConnectedAppId)
	}

	for _, p := range b.People {
		e.printf("%s\t%s\t%s\n", p.OpenId, p.Email, p.Name)
	}
}

func larkBotShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.GetLarkBotWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("reading the Lark bot: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("reading the Lark bot", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { printLarkBot(e, resp.JSON200) })
}

func larkBotSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	var set openapi.LarkBotSettings

	if file := str(fs, "app-file"); file != "" {
		raw, err := e.read(file)
		if err != nil {
			return err
		}

		var app struct {
			AppID     string `json:"app_id"`
			AppSecret string `json:"app_secret"`
		}

		if err := json.Unmarshal([]byte(raw), &app); err != nil || app.AppID == "" || app.AppSecret == "" {
			return errors.New("the app file is a JSON object with app_id and app_secret")
		}

		set.AppId, set.AppSecret = &app.AppID, &app.AppSecret
	}

	if owner := str(fs, "owner"); owner != "" {
		set.Owner = &owner
	}

	if mode := str(fs, "mode"); mode != "" {
		m := openapi.LarkBotMode(mode)
		set.Mode = &m
	}

	on, off := boolean(fs, "on"), boolean(fs, "off")

	switch {
	case on && off:
		return errors.New("pass --on or --off, not both")
	case on || off:
		set.Enabled = &on
	}

	resp, err := e.api.PutLarkBotWithResponse(ctx, set)
	if err != nil {
		return fmt.Errorf("setting the Lark bot: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("setting the Lark bot", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { printLarkBot(e, resp.JSON200) })
}

func larkBotRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.DeleteLarkBotWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("forgetting the Lark bot: %w", err)
	}

	if resp.StatusCode() != 204 {
		return apiError("forgetting the Lark bot", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]bool{"removed": true}, func() { e.printf("the Lark bot is forgotten\n") })
}

func larkBotPersonAdd(ctx context.Context, e *env, fs *flag.FlagSet) error {
	who := fs.Arg(0)

	resp, err := e.api.AddLarkBotPersonWithResponse(ctx, openapi.NewLarkBotPerson{Who: who})
	if err != nil {
		return fmt.Errorf("adding %s to the Lark bot: %w", who, err)
	}

	if resp.JSON200 == nil {
		return apiError("adding "+who+" to the Lark bot", resp.StatusCode(), resp.JSONDefault)
	}

	p := resp.JSON200

	return e.print(fs, p, func() { e.printf("%s (%s) may talk to the Lark bot\n", p.OpenId, p.Name) })
}

func larkBotPersonRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	who := fs.Arg(0)

	resp, err := e.api.RemoveLarkBotPersonWithResponse(ctx, who)
	if err != nil {
		return fmt.Errorf("removing %s from the Lark bot: %w", who, err)
	}

	if resp.StatusCode() != 204 {
		return apiError("removing "+who+" from the Lark bot", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]string{"removed": who}, func() { e.printf("%s is off the Lark bot's list\n", who) })
}
