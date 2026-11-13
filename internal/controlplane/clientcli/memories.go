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
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Memories from the terminal: a Bot's (issue #169), an agent's and the one
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

func keptMemory(m *openapi.Memory) kept {
	title := "memory about the owner"
	if a := val(m.Agent); a != "" {
		title = "memory of agent " + a
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

// memoryOf is whose memory a command is about.
type memoryOf struct {
	bot   string
	agent string
	owner bool
}

func memoryFlags(fs *flag.FlagSet) {
	jsonFlag(fs)
	fs.String("bot", "", "a bot's memory; a bot itself leaves it out")
	fs.String("agent", "", "an agent's memory, which all its workers share")
	fs.Bool("owner", false, "the memory about the owner, which every worker and bot gets")
}

// memoryTarget is --owner, --agent, --bot, or the Bot the calling pi is.
func memoryTarget(fs *flag.FlagSet) (memoryOf, error) {
	owner, agent, bot := boolean(fs, "owner"), str(fs, "agent"), str(fs, "bot")

	n := 0

	for _, given := range []bool{owner, agent != "", bot != ""} {
		if given {
			n++
		}
	}

	switch {
	case n > 1:
		return memoryOf{}, fmt.Errorf("%w: name one memory: --owner, --agent or --bot", ErrUsage)
	case owner:
		return memoryOf{owner: true}, nil
	case agent != "":
		return memoryOf{agent: agent}, nil
	case bot != "":
		return memoryOf{bot: bot}, nil
	}

	if name := os.Getenv(botEnv); name != "" {
		return memoryOf{bot: name}, nil
	}

	return memoryOf{}, errors.New("name the memory with --bot, --agent or --owner")
}

func memoryShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	target, err := memoryTarget(fs)
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
	case target.agent != "":
		resp, err := e.api.GetAgentMemoryWithResponse(ctx, target.agent)
		if err != nil {
			return kept{}, fmt.Errorf("reading the memory of agent %s: %w", target.agent, err)
		}

		if resp.JSON200 == nil {
			return kept{}, apiError("reading the memory of agent "+target.agent, resp.StatusCode(), resp.JSONDefault)
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
	target, err := memoryTarget(fs)
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
	case target.agent != "":
		resp, err := e.api.SetAgentMemoryWithResponse(ctx, target.agent, body)
		if err != nil {
			return fmt.Errorf("setting the memory of agent %s: %w", target.agent, err)
		}

		if resp.JSON200 == nil {
			return apiError("setting the memory of agent "+target.agent, resp.StatusCode(), resp.JSONDefault)
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

		k = keptBot(resp.JSON200)
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
		target, err := memoryTarget(fs)
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
	case target.agent != "":
		resp, err := e.api.ChangeAgentMemoryWithResponse(ctx, target.agent, change)
		if err != nil {
			return kept{}, fmt.Errorf("changing the memory of agent %s: %w", target.agent, err)
		}

		if resp.JSON200 == nil {
			return kept{}, apiError("changing the memory of agent "+target.agent, resp.StatusCode(), resp.JSONDefault)
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

		return keptBot(resp.JSON200), nil
	}
}
