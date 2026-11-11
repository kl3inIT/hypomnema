package clientcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Bots, from the terminal (issue #169): the owner makes and changes them; pi
// reads its Bot's prompt when it starts and writes its memory.

// botEnv names the Bot a pi process is, set by its launcher, so that
// `hypomnema memory` needs no name.
const botEnv = "HYPOMNEMA_BOT"

func botModel(b openapi.Bot) string {
	model := val(b.Model)
	if model == "" {
		return "pi's default model"
	}

	if p := val(b.Provider); p != "" {
		model = p + "/" + model
	}

	if e := val(b.Effort); e != "" {
		model += " (" + e + ")"
	}

	return model
}

func memoryEntries(b openapi.Bot) []string {
	if b.Memory == "" {
		return nil
	}

	return strings.Split(b.Memory, "\n")
}

func printBot(e *env, b *openapi.Bot) {
	e.printf("%s\t%s\tmemory %d/%d\n", b.Name, botModel(*b), utf8.RuneCountInString(b.Memory), b.MemoryLimit)

	if b.Persona != "" {
		e.printf("\n%s\n", b.Persona)
	}

	if entries := memoryEntries(*b); len(entries) > 0 {
		e.printf("\nmemory:\n")

		for _, m := range entries {
			e.printf("- %s\n", m)
		}
	}
}

func botsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListBotsWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing bots: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing bots", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, b := range resp.JSON200.Data {
			e.printf("%s\t%s\tmemory %d/%d\n", b.Name, botModel(b), utf8.RuneCountInString(b.Memory), b.MemoryLimit)
		}
	})
}

func getBot(ctx context.Context, e *env, name string) (*openapi.Bot, error) {
	resp, err := e.api.GetBotWithResponse(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("reading bot %s: %w", name, err)
	}

	if resp.JSON200 == nil {
		return nil, apiError("reading bot "+name, resp.StatusCode(), resp.JSONDefault)
	}

	return resp.JSON200, nil
}

func botsShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	b, err := getBot(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	return e.print(fs, b, func() { printBot(e, b) })
}

// botsSet makes a Bot or changes what the flags name: a flag left out keeps
// what the Bot had.
func botsSet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	name := fs.Arg(0)

	var set openapi.BotSettings

	resp, err := e.api.GetBotWithResponse(ctx, name)
	if err != nil {
		return fmt.Errorf("reading bot %s: %w", name, err)
	}

	if b := resp.JSON200; b != nil {
		set = openapi.BotSettings{Provider: b.Provider, Model: b.Model, Effort: b.Effort, Persona: &b.Persona}
	}

	var failed error

	fs.Visit(func(f *flag.Flag) {
		v := f.Value.String()

		switch f.Name {
		case "provider":
			set.Provider = &v
		case "model":
			set.Model = &v
		case "effort":
			set.Effort = &v
		case "persona-file":
			persona, err := e.read(v)
			if err != nil {
				failed = err
			}

			set.Persona = &persona
		}
	})

	if failed != nil {
		return failed
	}

	put, err := e.api.PutBotWithResponse(ctx, name, set)
	if err != nil {
		return fmt.Errorf("saving bot %s: %w", name, err)
	}

	if put.JSON200 == nil {
		return apiError("saving bot "+name, put.StatusCode(), put.JSONDefault)
	}

	return e.print(fs, put.JSON200, func() { printBot(e, put.JSON200) })
}

func botsRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	name := fs.Arg(0)

	resp, err := e.api.DeleteBotWithResponse(ctx, name)
	if err != nil {
		return fmt.Errorf("removing bot %s: %w", name, err)
	}

	if resp.StatusCode() != 204 {
		return apiError("removing bot "+name, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]bool{"removed": true}, func() { e.printf("bot %s is removed\n", name) })
}

// botsPrompt prints what pi is given about its Bot when it starts for a chat:
// the persona, then the memory and how to change it. The launcher appends it
// to pi's system prompt, so it is read once per pi process, as Hermes gives
// its memory once per session (tools/memory_tool.py).
func botsPrompt(ctx context.Context, e *env, fs *flag.FlagSet) error {
	b, err := getBot(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	e.printf("%s", botPrompt(*b))

	return nil
}

func botPrompt(b openapi.Bot) string {
	var out strings.Builder

	if b.Persona != "" {
		out.WriteString(b.Persona + "\n\n")
	}

	fmt.Fprintf(&out, "## Memory\n\n"+
		"You are the bot `%s`. You keep a short memory across your chats, the same in every chat with anyone: who "+
		"people are, what they prefer, standing decisions, where things are. %d of its %d characters are used. "+
		"Change it with:\n\n"+
		"- `hypomnema memory add \"<one line>\"`\n"+
		"- `hypomnema memory replace \"<part of an entry>\" \"<the new line>\"`\n"+
		"- `hypomnema memory remove \"<part of an entry>\"`\n\n"+
		"Keep no secret in it and nothing that matters to this chat only. When a change is refused for the limit, "+
		"replace or remove entries first. A change is in your prompt from your next chat on.\n\n",
		b.Name, utf8.RuneCountInString(b.Memory), b.MemoryLimit)

	entries := memoryEntries(b)
	if len(entries) == 0 {
		out.WriteString("Your memory is empty.\n")

		return out.String()
	}

	out.WriteString("Your memory now:\n\n")

	for _, m := range entries {
		out.WriteString("- " + m + "\n")
	}

	return out.String()
}

// memoryBot is the Bot a memory command is about: --bot, or the Bot the
// calling pi is.
func memoryBot(fs *flag.FlagSet) (string, error) {
	if name := str(fs, "bot"); name != "" {
		return name, nil
	}

	if name := os.Getenv(botEnv); name != "" {
		return name, nil
	}

	return "", errors.New("name the bot with --bot")
}

func memoryFlags(fs *flag.FlagSet) {
	jsonFlag(fs)
	fs.String("bot", "", "the bot; a bot itself leaves it out")
}

func memoryShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	name, err := memoryBot(fs)
	if err != nil {
		return err
	}

	b, err := getBot(ctx, e, name)
	if err != nil {
		return err
	}

	return e.print(fs, b, func() { printMemory(e, b) })
}

func printMemory(e *env, b *openapi.Bot) {
	e.printf("memory of %s: %d of %d characters\n", b.Name, utf8.RuneCountInString(b.Memory), b.MemoryLimit)

	for _, m := range memoryEntries(*b) {
		e.printf("- %s\n", m)
	}
}

// Kinds of a change of a Bot's memory, for the command table.
const (
	memoryAdd     = openapi.Add
	memoryReplace = openapi.Replace
	memoryRemove  = openapi.Remove
)

func memoryChange(kind openapi.MemoryChangeKind) func(context.Context, *env, *flag.FlagSet) error {
	return func(ctx context.Context, e *env, fs *flag.FlagSet) error {
		name, err := memoryBot(fs)
		if err != nil {
			return err
		}

		change := openapi.MemoryChange{Kind: kind}

		switch kind {
		case openapi.Add:
			change.Text = new(fs.Arg(0))
		case openapi.Replace:
			change.Match, change.Text = new(fs.Arg(0)), new(fs.Arg(1))
		case openapi.Remove:
			change.Match = new(fs.Arg(0))
		}

		resp, err := e.api.ChangeBotMemoryWithResponse(ctx, name, change)
		if err != nil {
			return fmt.Errorf("changing the memory of %s: %w", name, err)
		}

		if resp.JSON200 == nil {
			return apiError("changing the memory of "+name, resp.StatusCode(), resp.JSONDefault)
		}

		return e.print(fs, resp.JSON200, func() { printMemory(e, resp.JSON200) })
	}
}
