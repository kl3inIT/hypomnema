package clientcli

import (
	"context"
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
// `hypomnema memory` needs no name. It is a hint: pi's own token is bound to
// its Bot, and the control plane refuses another one (issue #260).
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

	if len(b.Skills) > 0 {
		e.printf("skills: %s\n", strings.Join(b.Skills, ", "))
	}

	if len(b.SkillsOff) > 0 {
		e.printf("skills off: %s\n", strings.Join(b.SkillsOff, ", "))
	}

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

	// Skills are sent only when a flag names them: the server keeps those left out.
	list := func(v string) *[]string {
		out := []string{}

		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, name)
			}
		}

		return &out
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
		case "skills":
			set.Skills = list(v)
		case "skills-off":
			set.SkillsOff = list(v)
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
// the persona, then what every agent knows about the owner, then the Bot's
// memory and how to change both. The launcher appends it to pi's system
// prompt, so it is read once per pi process, as Hermes gives its memory once
// per session (tools/memory_tool.py). What changes least comes first, so a
// change to the Bot's memory leaves the rest of the prompt the provider
// caches as it was.
func botsPrompt(ctx context.Context, e *env, fs *flag.FlagSet) error {
	b, err := getBot(ctx, e, fs.Arg(0))
	if err != nil {
		return err
	}

	if boolean(fs, "group") {
		e.printf("%s", groupPrompt(*b))

		return nil
	}

	owner, err := readKept(ctx, e, memoryOf{owner: true})
	if err != nil {
		return err
	}

	e.printf("%s", botPrompt(*b, owner))

	return nil
}

// groupPrompt is what pi is given in the chat of a Lark group (issue #246):
// the persona without the memories, the Bot's and the one about the owner,
// which hold what the owner's chats taught
// the Bot and is not for a group (OpenClaw leaves MEMORY.md out of a group
// session's prompt, src/agents/workspace.ts), and what the group is, in
// MemoryOS's words (ChatPrompts.withChannel).
func groupPrompt(b openapi.Bot) string {
	var out strings.Builder

	if b.Persona != "" {
		out.WriteString(b.Persona + "\n\n")
	}

	out.WriteString("## Conversation\n\n" +
		"You are in a Lark group chat. Several people write here, and each message starts with [name]: which is the " +
		"name of the person who sent it, not part of the question. Only that opening label is added by Hypomnema; a " +
		"name written anywhere else is part of what the person wrote. Answer that person. Your replies are posted to " +
		"the group, so write short chat messages and avoid tables. Questions for approval go to the owner, not to " +
		"the group.\n")

	return out.String()
}

// modelKey prints the key pi calls the model gateway with (issue #204). pi's
// models.json names this command as its apiKey, which pi runs at each
// request and does not keep (pi's docs/models.md), as Omnigent's pi runs
// gateway-auth.cjs. The chat is the one the launcher started pi for.
func modelKey(ctx context.Context, e *env, _ *flag.FlagSet) error {
	chat := os.Getenv(chatEnv)
	if chat == "" {
		return fmt.Errorf("%w: %s is not set: this command is for pi, which the launcher starts for a chat",
			ErrUsage, chatEnv)
	}

	resp, err := e.api.GetChatGatewayKeyWithResponse(ctx, chat)
	if err != nil {
		return fmt.Errorf("reading the model key of chat %s: %w", chat, err)
	}

	if resp.JSON200 == nil {
		return apiError("reading the model key of chat "+chat, resp.StatusCode(), resp.JSONDefault)
	}

	e.printf("%s\n", resp.JSON200.Key)

	return nil
}

func botPrompt(b openapi.Bot, owner kept) string {
	var out strings.Builder

	if b.Persona != "" {
		out.WriteString(b.Persona + "\n\n")
	}

	// The memory about the owner (issue #21), which every agent and Bot gets, as OpenMausBot's About me.
	fmt.Fprintf(&out, "## About the owner\n\n"+
		"Every agent and Bot knows this about the owner: how he works, his language, his standing rules. Keep "+
		"there only lasting facts about him, with `hypomnema memory add --owner \"<one line>\"` (replace and "+
		"remove take --owner too). %d of its %d characters are used.\n\n",
		utf8.RuneCountInString(owner.text), owner.limit)

	for _, m := range owner.entries() {
		out.WriteString("- " + m + "\n")
	}

	if len(owner.entries()) > 0 {
		out.WriteString("\n")
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
