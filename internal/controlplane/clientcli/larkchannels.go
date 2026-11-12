package clientcli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// The Lark Channels, from the terminal (issues #136 and #169). An app's
// secret is read from a file and is never an argument: arguments show in the
// shell's history and in the process list.

func larkChannelState(c *openapi.LarkChannel) string {
	switch {
	case c.Enabled && c.Connected:
		return "on, connected"
	case c.Enabled && c.LastError != nil:
		return "on, not connected: " + *c.LastError
	case c.Enabled:
		return "on, not connected yet"
	default:
		return "off"
	}
}

func printLarkChannel(e *env, c *openapi.LarkChannel) {
	e.printf("%s\tbot %s\tapp %s\t%s\tmode %s\towner %s\n", c.Name, c.Bot, c.AppId, larkChannelState(c), c.Mode,
		c.OwnerOpenId)

	if c.ConnectedAppId != nil {
		e.printf("connected as app %s until the control plane starts again\n", *c.ConnectedAppId)
	}

	for _, p := range c.People {
		e.printf("%s\t%s\t%s\n", p.OpenId, p.Email, p.Name)
	}
}

func larkChannelsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListLarkChannelsWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing the Lark channels: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing the Lark channels", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for i := range resp.JSON200.Data {
			c := &resp.JSON200.Data[i]
			e.printf("%s\tbot %s\tapp %s\t%s\tmode %s\n", c.Name, c.Bot, c.AppId, larkChannelState(c), c.Mode)
		}
	})
}

func larkChannelShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	channel := fs.Arg(0)

	resp, err := e.api.GetLarkChannelWithResponse(ctx, channel)
	if err != nil {
		return fmt.Errorf("reading Lark channel %s: %w", channel, err)
	}

	if resp.JSON200 == nil {
		return apiError("reading Lark channel "+channel, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { printLarkChannel(e, resp.JSON200) })
}

func larkChannelSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	var set openapi.LarkChannelSettings

	channel := fs.Arg(0)

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

	if bot := str(fs, "bot"); bot != "" {
		set.Bot = &bot
	}

	if owner := str(fs, "owner"); owner != "" {
		set.Owner = &owner
	}

	if mode := str(fs, "mode"); mode != "" {
		m := openapi.LarkChannelMode(mode)
		set.Mode = &m
	}

	on, off := boolean(fs, "on"), boolean(fs, "off")

	switch {
	case on && off:
		return errors.New("pass --on or --off, not both")
	case on || off:
		set.Enabled = &on
	}

	resp, err := e.api.PutLarkChannelWithResponse(ctx, channel, set)
	if err != nil {
		return fmt.Errorf("setting Lark channel %s: %w", channel, err)
	}

	if resp.JSON200 == nil {
		return apiError("setting Lark channel "+channel, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { printLarkChannel(e, resp.JSON200) })
}

func larkChannelRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	channel := fs.Arg(0)

	resp, err := e.api.DeleteLarkChannelWithResponse(ctx, channel)
	if err != nil {
		return fmt.Errorf("forgetting Lark channel %s: %w", channel, err)
	}

	if resp.StatusCode() != 204 {
		return apiError("forgetting Lark channel "+channel, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]string{"removed": channel}, func() {
		e.printf("Lark channel %s is forgotten\n", channel)
	})
}

func larkChannelPersonAdd(ctx context.Context, e *env, fs *flag.FlagSet) error {
	channel, who := fs.Arg(0), fs.Arg(1)

	resp, err := e.api.AddLarkChannelPersonWithResponse(ctx, channel, openapi.NewLarkChannelPerson{Who: who})
	if err != nil {
		return fmt.Errorf("adding %s to Lark channel %s: %w", who, channel, err)
	}

	if resp.JSON200 == nil {
		return apiError("adding "+who+" to Lark channel "+channel, resp.StatusCode(), resp.JSONDefault)
	}

	p := resp.JSON200

	return e.print(fs, p, func() { e.printf("%s (%s) may talk through Lark channel %s\n", p.OpenId, p.Name, channel) })
}

func larkChannelPersonRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	channel, who := fs.Arg(0), fs.Arg(1)

	resp, err := e.api.RemoveLarkChannelPersonWithResponse(ctx, channel, who)
	if err != nil {
		return fmt.Errorf("removing %s from Lark channel %s: %w", who, channel, err)
	}

	if resp.StatusCode() != 204 {
		return apiError("removing "+who+" from Lark channel "+channel, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]string{"removed": who}, func() {
		e.printf("%s is off the list of Lark channel %s\n", who, channel)
	})
}
