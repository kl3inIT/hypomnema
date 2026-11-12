package clientcli

import (
	"context"
	"fmt"
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

// larkChannelSettings stands in for the Channels' settings: it keeps what
// the owner set of each Channel and answers as the real one does for a
// Channel that is not set up and for an app that another Channel has.
type larkChannelSettings struct {
	mu sync.Mutex
	// names are the Channels in the order they were set up.
	names    []string
	secrets  map[string]string
	statuses map[string]larkbot.Status
}

func newLarkChannelSettings() *larkChannelSettings {
	return &larkChannelSettings{secrets: map[string]string{}, statuses: map[string]larkbot.Status{}}
}

func (l *larkChannelSettings) List(context.Context) ([]larkbot.Status, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]larkbot.Status, 0, len(l.names))
	for _, name := range l.names {
		out = append(out, l.statuses[name])
	}

	return out, nil
}

func (l *larkChannelSettings) Status(_ context.Context, channel string) (larkbot.Status, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	st, ok := l.statuses[channel]
	if !ok {
		return larkbot.Status{}, orchestration.ErrNotFound
	}

	return st, nil
}

func (l *larkChannelSettings) Set(_ context.Context, channel string, set larkbot.Settings) (larkbot.Status, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	st, known := l.statuses[channel]
	if !known {
		st.Channel = orchestration.LarkChannel{
			Name: channel, Bot: orchestration.DefaultBot, Mode: orchestration.LarkChannelOwner,
		}
	}

	if set.AppID != "" {
		for name, other := range l.statuses {
			if name != channel && other.Channel.AppID == set.AppID {
				return larkbot.Status{}, fmt.Errorf("%w: the Lark app %s is the app of channel %s",
					orchestration.ErrConflict, set.AppID, name)
			}
		}

		st.Channel.AppID, l.secrets[channel] = set.AppID, set.AppSecret
	}

	if set.Owner != "" {
		st.Channel.OwnerOpenID = "ou_owner"
	}

	if set.Bot != "" {
		st.Channel.Bot = set.Bot
	}

	if set.Mode != "" {
		st.Channel.Mode = set.Mode
	}

	if set.Enabled != nil {
		st.Channel.Enabled = *set.Enabled
		st.State = larkbot.State{Connected: *set.Enabled, AppID: st.Channel.AppID}
	}

	if !known {
		l.names = append(l.names, channel)
	}

	l.statuses[channel] = st

	return st, nil
}

func (l *larkChannelSettings) Remove(_ context.Context, channel string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.statuses[channel]; !ok {
		return orchestration.ErrNotFound
	}

	delete(l.statuses, channel)
	l.names = slices.DeleteFunc(l.names, func(name string) bool { return name == channel })

	return nil
}

func (l *larkChannelSettings) AddPerson(_ context.Context, channel, who string,
) (orchestration.LarkChannelPerson, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	st, ok := l.statuses[channel]
	if !ok {
		return orchestration.LarkChannelPerson{}, orchestration.ErrNotFound
	}

	p := orchestration.LarkChannelPerson{OpenID: "ou_linh", Email: who, Name: "Linh"}
	st.People = append(st.People, p)
	l.statuses[channel] = st

	return p, nil
}

func (l *larkChannelSettings) RemovePerson(_ context.Context, channel, who string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	st := l.statuses[channel]
	st.People = slices.DeleteFunc(st.People, func(p orchestration.LarkChannelPerson) bool {
		return strings.EqualFold(p.Email, who) || p.OpenID == who
	})
	l.statuses[channel] = st

	return nil
}

func (l *larkChannelSettings) SetGroup(_ context.Context, channel, chatID string, enabled bool,
) (orchestration.LarkChannelGroup, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for i, g := range l.statuses[channel].Groups {
		if g.ChatID == chatID {
			l.statuses[channel].Groups[i].Enabled = enabled

			return l.statuses[channel].Groups[i], nil
		}
	}

	return orchestration.LarkChannelGroup{}, orchestration.ErrNotFound
}

// Scenario: the owner sets his Lark Channels up from the terminal. The first,
// main, gets its app from a file and who he is at Lark, then he lets Linh in
// and switches it on. A second, maps, talks to the Bot vmap through another
// app; the app of main is refused for it. An app's secret is read from the
// file and never printed; what he typed for the mode is what the Channel
// gets; each Channel has its own people. A coordinator can neither read a
// Channel, whose answer names people, nor change one.
func TestSettingLarkChannelsUpFromTheCLI(t *testing.T) {
	t.Parallel()

	channels := newLarkChannelSettings()
	w := newWorldWith(t, nil, func(s *clientapi.Server) { s.WithLarkChannels(channels) })
	w.login(t, bearertoken.Owner)

	out, err := w.cmd(t, "channels", "lark", "list")
	require.NoError(t, err)
	assert.Empty(t, out, "no Channel is set up")

	_, err = w.cmd(t, "channels", "lark", "show", "main")
	require.Error(t, err, "no Channel is set up")

	_, err = w.cmdIn(t, `{"app_id":"cli_a1b2"}`, "channels", "lark", "set", "main", "--app-file", "-")
	require.ErrorContains(t, err, "app_id and app_secret")

	out, err = w.cmdIn(t, `{"app_id":"cli_a1b2","app_secret":"never-printed"}`, "channels", "lark", "set", "main",
		"--app-file", "-", "--owner", "owner@example.com")
	require.NoError(t, err)
	assert.Equal(t, "main\tbot coordinator\tapp cli_a1b2\toff\tmode owner\towner ou_owner\n", out)
	assert.Equal(t, "never-printed", channels.secrets["main"])

	_, err = w.cmd(t, "channels", "lark", "set", "main", "--on", "--off")
	require.ErrorContains(t, err, "not both")

	out, err = w.cmd(t, "channels", "lark", "allow", "main", "linh@example.com")
	require.NoError(t, err)
	assert.Equal(t, "ou_linh (Linh) may talk through Lark channel main\n", out)

	out, err = w.cmd(t, "channels", "lark", "set", "main", "--mode", "allowlist", "--on")
	require.NoError(t, err)
	assert.Equal(t, "main\tbot coordinator\tapp cli_a1b2\ton, connected\tmode allowlist\towner ou_owner\n"+
		"ou_linh\tlinh@example.com\tLinh\n", out)

	_, err = w.cmdIn(t, `{"app_id":"cli_a1b2","app_secret":"again"}`, "channels", "lark", "set", "maps",
		"--app-file", "-", "--owner", "owner@example.com", "--bot", "vmap")
	require.ErrorContains(t, err, "the app of channel main")

	out, err = w.cmdIn(t, `{"app_id":"cli_m4p5","app_secret":"never-printed-either"}`, "channels", "lark", "set",
		"maps", "--app-file", "-", "--owner", "owner@example.com", "--bot", "vmap")
	require.NoError(t, err)
	assert.Equal(t, "maps\tbot vmap\tapp cli_m4p5\toff\tmode owner\towner ou_owner\n", out)

	out, err = w.cmd(t, "channels", "lark", "list")
	require.NoError(t, err)
	assert.Equal(t, "main\tbot coordinator\tapp cli_a1b2\ton, connected\tmode allowlist\n"+
		"maps\tbot vmap\tapp cli_m4p5\toff\tmode owner\n", out)

	_, err = w.cmd(t, "channels", "lark", "disallow", "main", "linh@example.com")
	require.NoError(t, err)

	out, err = w.cmd(t, "channels", "lark", "show", "main", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"people":[]`)
	assert.NotContains(t, out, "never-printed")

	_, err = w.cmd(t, "channels", "lark", "set", "Not-A-Name", "--on")
	require.Error(t, err, "a Channel is named as a Bot is")

	out, err = w.cmd(t, "channels", "lark", "rm", "maps")
	require.NoError(t, err)
	assert.Equal(t, "Lark channel maps is forgotten\n", out)

	w.login(t, bearertoken.Coordinator)

	_, err = w.cmd(t, "channels", "lark", "list")
	require.Error(t, err, "only the owner reads the Channels")

	_, err = w.cmd(t, "channels", "lark", "show", "main")
	require.Error(t, err, "only the owner reads a Channel")

	_, err = w.cmdIn(t, `{"app_id":"cli_x","app_secret":"y"}`, "channels", "lark", "set", "other", "--app-file", "-",
		"--owner", "owner@example.com")
	require.Error(t, err, "only the owner changes a Channel")

	_, known := channels.statuses["other"]
	assert.False(t, known)
}
