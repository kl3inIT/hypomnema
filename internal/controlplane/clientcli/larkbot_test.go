package clientcli

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/bearertoken"
	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/larkbot"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// larkBotSettings stands in for the bot's settings: it keeps what the owner
// set and answers as the real one does for a bot that is not set up.
type larkBotSettings struct {
	mu     sync.Mutex
	set    bool
	secret string
	status larkbot.Status
}

func (l *larkBotSettings) Status(context.Context) (larkbot.Status, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.set {
		return larkbot.Status{}, orchestration.ErrNotFound
	}

	return l.status, nil
}

func (l *larkBotSettings) Set(_ context.Context, set larkbot.Settings) (larkbot.Status, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if set.AppID != "" {
		l.status.Bot.AppID, l.secret = set.AppID, set.AppSecret
	}

	if set.Owner != "" {
		l.status.Bot.OwnerOpenID = "ou_owner"
	}

	if !l.set {
		l.status.Bot.Mode = orchestration.LarkBotOwner
	}

	if set.Mode != "" {
		l.status.Bot.Mode = set.Mode
	}

	if set.Enabled != nil {
		l.status.Bot.Enabled = *set.Enabled
		l.status.State = larkbot.State{Connected: *set.Enabled, AppID: l.status.Bot.AppID}
	}

	l.set = true

	return l.status, nil
}

func (l *larkBotSettings) Remove(context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.set, l.status = false, larkbot.Status{}

	return nil
}

func (l *larkBotSettings) AddPerson(_ context.Context, who string) (orchestration.LarkBotPerson, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	p := orchestration.LarkBotPerson{OpenID: "ou_linh", Email: who, Name: "Linh"}
	l.status.People = append(l.status.People, p)

	return p, nil
}

func (l *larkBotSettings) RemovePerson(_ context.Context, who string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.status.People = slices.DeleteFunc(l.status.People, func(p orchestration.LarkBotPerson) bool {
		return strings.EqualFold(p.Email, who) || p.OpenID == who
	})

	return nil
}

// Scenario: the owner sets his Lark bot up from the terminal: the app from a
// file, who he is at Lark, then he lets Linh in and switches it on. The
// app's secret is read from the file and never printed; what he typed for
// the mode is what the bot gets; a coordinator can neither read the bot, whose
// answer names people, nor change it.
func TestSettingTheLarkBotUpFromTheCLI(t *testing.T) {
	t.Parallel()

	bot := &larkBotSettings{}
	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithLarkBot(bot) })
	w.login(t, bearertoken.Owner)

	_, err := w.cmd(t, "channels", "lark", "show")
	require.Error(t, err, "no bot is set up")

	_, err = w.cmdIn(t, `{"app_id":"cli_a1b2"}`, "channels", "lark", "set", "--app-file", "-")
	require.ErrorContains(t, err, "app_id and app_secret")

	out, err := w.cmdIn(t, `{"app_id":"cli_a1b2","app_secret":"never-printed"}`, "channels", "lark", "set",
		"--app-file", "-", "--owner", "owner@example.com")
	require.NoError(t, err)
	assert.Equal(t, "app cli_a1b2\toff\tmode owner\towner ou_owner\n", out)
	assert.Equal(t, "never-printed", bot.secret)

	_, err = w.cmd(t, "channels", "lark", "set", "--on", "--off")
	require.ErrorContains(t, err, "not both")

	out, err = w.cmd(t, "channels", "lark", "allow", "linh@example.com")
	require.NoError(t, err)
	assert.Equal(t, "ou_linh (Linh) may talk to the Lark bot\n", out)

	out, err = w.cmd(t, "channels", "lark", "set", "--mode", "allowlist", "--on")
	require.NoError(t, err)
	assert.Equal(t, "app cli_a1b2\ton, connected\tmode allowlist\towner ou_owner\n"+
		"ou_linh\tlinh@example.com\tLinh\n", out)

	_, err = w.cmd(t, "channels", "lark", "disallow", "linh@example.com")
	require.NoError(t, err)

	out, err = w.cmd(t, "channels", "lark", "show", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"people":[]`)
	assert.NotContains(t, out, "never-printed")

	out, err = w.cmd(t, "channels", "lark", "rm")
	require.NoError(t, err)
	assert.Equal(t, "the Lark bot is forgotten\n", out)

	w.login(t, bearertoken.Coordinator)

	_, err = w.cmd(t, "channels", "lark", "show")
	require.Error(t, err, "only the owner reads the bot")

	_, err = w.cmdIn(t, `{"app_id":"cli_x","app_secret":"y"}`, "channels", "lark", "set", "--app-file", "-",
		"--owner", "owner@example.com")
	require.Error(t, err, "only the owner changes the bot")
	assert.False(t, bot.set)
}
