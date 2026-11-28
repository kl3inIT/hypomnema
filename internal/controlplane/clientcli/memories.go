package clientcli

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Memories from the terminal: a Bot's (issue #169), a profile's and the one
// about the owner (issue #21). A Bot's pi writes its own and the one about
// the owner; the owner reads and edits them all.

// kept is a memory as a command prints it.
type kept struct {
	title string
	text  string
	limit int
	// value is what --json prints.
	value any
}

func keptBot(b *openapi.Bot) kept {
	return kept{title: "memory of " + b.Name, text: b.Memory, limit: b.MemoryLimit, value: b}
}

// keptBotMemory is a Bot's memory as its memory endpoints return it (issue #1336), titled by the name the Bot was
// named by.
func keptBotMemory(m *openapi.Memory, label string) kept {
	return kept{title: "memory of " + cmp.Or(label, val(m.Bot)), text: m.Memory, limit: m.MemoryLimit, value: m}
}

func keptMemory(m *openapi.Memory) kept {
	title := "memory about the owner"
	if p := cmp.Or(val(m.Profile), val(m.Agent)); p != "" {
		title = "memory of profile " + p
	}

	return kept{title: title, text: m.Memory, limit: m.MemoryLimit, value: m}
}

func (k kept) entries() []string {
	if k.text == "" {
		return nil
	}

	return strings.Split(k.text, "\n")
}

func (k kept) print(e *env) {
	e.printf("%s: %d of %d characters\n", k.title, utf8.RuneCountInString(k.text), k.limit)

	for _, m := range k.entries() {
		e.printf("- %s\n", m)
	}
}

// memoryOf is whose memory a command is about: a Bot's by its id, with the name it was named by as label, a
// profile's, or the one about the owner.
type memoryOf struct {
	bot     string
	label   string
	profile string
	owner   bool
}

func memoryFlags(fs *flag.FlagSet) {
	jsonFlag(fs)
	fs.String("bot", "", "a bot's memory; a bot itself leaves it out")
	fs.String("profile", "", "a profile's memory, which all its workers share")
	fs.String("agent", "", "--profile under its name until 2026-11-28, kept for one release")
	fs.Bool("owner", false, "the memory about the owner, which every worker and bot gets")
}

// profileFlag is --profile, or --agent, its name until 2026-11-28 (issue #1336); naming two profiles is refused.
func profileFlag(fs *flag.FlagSet) (string, error) {
	profile, agent := str(fs, "profile"), str(fs, "agent")
	if profile != "" && agent != "" && profile != agent {
		return "", fmt.Errorf("%w: name the profile once, with --profile", ErrUsage)
	}

	return cmp.Or(profile, agent), nil
}

// memoryTarget is --owner, --profile, --bot (by id or name), or the Bot the calling pi is.
func memoryTarget(ctx context.Context, e *env, fs *flag.FlagSet) (memoryOf, error) {
	agent, err := profileFlag(fs)
	if err != nil {
		return memoryOf{}, err
	}

	owner, bot := boolean(fs, "owner"), str(fs, "bot")

	n := 0

	for _, given := range []bool{owner, agent != "", bot != ""} {
		if given {
			n++
		}
	}

	switch {
	case n > 1:
		return memoryOf{}, fmt.Errorf("%w: name one memory: --owner, --profile or --bot", ErrUsage)
	case owner:
		return memoryOf{owner: true}, nil
	case agent != "":
		return memoryOf{profile: agent}, nil
	case bot != "":
		id, err := botIDOf(ctx, e, bot)
		if err != nil {
			return memoryOf{}, err
		}

		return memoryOf{bot: id, label: bot}, nil
	}

	if name := os.Getenv(botEnv); name != "" {
		return memoryOf{bot: name, label: name}, nil
	}

	return memoryOf{}, errors.New("name the memory with --bot, --profile or --owner")
}

func memoryShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	target, err := memoryTarget(ctx, e, fs)
	if err != nil {
		return err
	}

	k, err := readKept(ctx, e, target)
	if err != nil {
		return err
	}

	return e.print(fs, k.value, func() { k.print(e) })
}

func readKept(ctx context.Context, e *env, target memoryOf) (kept, error) {
	switch {
	case target.owner:
		resp, err := e.api.GetOwnerMemoryWithResponse(ctx)
		if err != nil {
			return kept{}, fmt.Errorf("reading the memory about the owner: %w", err)
		}

		if resp.JSON200 == nil {
			return kept{}, apiError("reading the memory about the owner", resp.StatusCode(), resp.JSONDefault)
		}

		return keptMemory(resp.JSON200), nil
	case target.profile != "":
		resp, err := e.api.GetProfileMemoryWithResponse(ctx, target.profile)
		if err != nil {
			return kept{}, fmt.Errorf("reading the memory of profile %s: %w", target.profile, err)
		}

		if resp.JSON200 == nil {
			return kept{}, apiError("reading the memory of profile "+target.profile, resp.StatusCode(), resp.JSONDefault)
		}

		return keptMemory(resp.JSON200), nil
	default:
		b, err := getBot(ctx, e, target.bot)
		if err != nil {
			return kept{}, err
		}

		return keptBot(b), nil
	}
}

// memorySet replaces a memory with a file's lines: the owner edits it by hand.
func memorySet(ctx context.Context, e *env, fs *flag.FlagSet) error {
	target, err := memoryTarget(ctx, e, fs)
	if err != nil {
		return err
	}

	text, err := e.read(fs.Arg(0))
	if err != nil {
		return err
	}

	body := openapi.MemoryText{Memory: text}

	var k kept

	switch {
	case target.owner:
		resp, err := e.api.SetOwnerMemoryWithResponse(ctx, body)
		if err != nil {
			return fmt.Errorf("setting the memory about the owner: %w", err)
		}

		if resp.JSON200 == nil {
			return apiError("setting the memory about the owner", resp.StatusCode(), resp.JSONDefault)
		}

		k = keptMemory(resp.JSON200)
	case target.profile != "":
		resp, err := e.api.SetProfileMemoryWithResponse(ctx, target.profile, body)
		if err != nil {
			return fmt.Errorf("setting the memory of profile %s: %w", target.profile, err)
		}

		if resp.JSON200 == nil {
			return apiError("setting the memory of profile "+target.profile, resp.StatusCode(), resp.JSONDefault)
		}

		k = keptMemory(resp.JSON200)
	default:
		resp, err := e.api.SetBotMemoryWithResponse(ctx, target.bot, body)
		if err != nil {
			return fmt.Errorf("setting the memory of %s: %w", target.bot, err)
		}

		if resp.JSON200 == nil {
			return apiError("setting the memory of "+target.bot, resp.StatusCode(), resp.JSONDefault)
		}

		k = keptBotMemory(resp.JSON200, target.label)
	}

	return e.print(fs, k.value, func() { k.print(e) })
}

// Kinds of a change of a memory, for the command table.
const (
	memoryAdd     = openapi.Add
	memoryReplace = openapi.Replace
	memoryRemove  = openapi.Remove
)

// memoryChange makes one change and prints the memory with how full it is,
// and from 80% a request to make room (agenttool.MemoryUsage): a Bot reads it
// as a worker reads its tool's result.
func memoryChange(kind openapi.MemoryChangeKind) func(context.Context, *env, *flag.FlagSet) error {
	return func(ctx context.Context, e *env, fs *flag.FlagSet) error {
		target, err := memoryTarget(ctx, e, fs)
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

		k, err := changeKept(ctx, e, target, change)
		if err != nil {
			return err
		}

		return e.print(fs, k.value, func() {
			k.print(e)
			e.printf("%s\n", agenttool.MemoryUsage(utf8.RuneCountInString(k.text), k.limit))
		})
	}
}

func changeKept(ctx context.Context, e *env, target memoryOf, change openapi.MemoryChange) (kept, error) {
	switch {
	case target.owner:
		resp, err := e.api.ChangeOwnerMemoryWithResponse(ctx, change)
		if err != nil {
			return kept{}, fmt.Errorf("changing the memory about the owner: %w", err)
		}

		if resp.JSON200 == nil {
			return kept{}, apiError("changing the memory about the owner", resp.StatusCode(), resp.JSONDefault)
		}

		return keptMemory(resp.JSON200), nil
	case target.profile != "":
		resp, err := e.api.ChangeProfileMemoryWithResponse(ctx, target.profile, change)
		if err != nil {
			return kept{}, fmt.Errorf("changing the memory of profile %s: %w", target.profile, err)
		}

		if resp.JSON200 == nil {
			return kept{}, apiError("changing the memory of profile "+target.profile, resp.StatusCode(), resp.JSONDefault)
		}

		return keptMemory(resp.JSON200), nil
	default:
		resp, err := e.api.ChangeBotMemoryWithResponse(ctx, target.bot, change)
		if err != nil {
			return kept{}, fmt.Errorf("changing the memory of %s: %w", target.bot, err)
		}

		if resp.JSON200 == nil {
			return kept{}, apiError("changing the memory of "+target.bot, resp.StatusCode(), resp.JSONDefault)
		}

		return keptBotMemory(resp.JSON200, target.label), nil
	}
}
