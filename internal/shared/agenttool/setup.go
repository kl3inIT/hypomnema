package agenttool

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// The guides that teach an agent each Integration's tool (issue #103,
// section 9) and the phone are skills of the control plane's skills store
// (issue #536, internal/controlplane/clientcli/skills): each requires its
// Integration in its frontmatter and reaches a worker with its offer's
// skills, only when its Run was given a Connection of it. A node writes no
// guide of its own (HelloRequest.StoreGuides).

// Superset is the Integration whose tools are the control plane's own
// superset_* tools (issue #536), not a command on the agent's PATH.
const Superset = "superset"

// Place is where a machine keeps the tools and what goes with them, under
// one home: the real tools (ToolPath) and the scripts agents call them by.
type Place struct {
	// Home is the directory everything is under.
	Home string
	// Exe is the binary whose `tool` command the scripts run.
	Exe string
}

// Bin is the directory to put first on an agent's PATH.
func (p Place) Bin() string { return filepath.Join(p.Home, "tools", "bin") }

// Prepare writes the script of each tool the Integrations need. It can be
// run again: a file is rewritten whole. The real tools are installed apart,
// by ToolInstaller.
func (p Place) Prepare(integrations []string) error {
	for _, integration := range integrations {
		if def, ok := Of(integration); ok {
			if err := writeWhole(filepath.Join(p.Bin(), def.Name), []byte(Shim(p.Exe, def.Name)), 0o700); err != nil {
				return fmt.Errorf("writing the script of %s: %w", def.Name, err)
			}
		}
	}

	return nil
}

// writeWhole writes a file beside its place and renames it into it, so that
// an agent never reads half of one.
func writeWhole(target string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}

	// A name of its own beside the target: two Dispatches started at once write the same guides.
	f, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".*.tmp")
	if err != nil {
		return err
	}

	tmp := f.Name()

	_, err = f.Write(content)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}

	if err == nil {
		err = os.Chmod(tmp, mode)
	}

	if err == nil {
		err = os.Rename(tmp, target)
	}

	if err != nil {
		_ = os.Remove(tmp)
	}

	return err
}

// Env is what an agent's environment gains to use its Connections: the
// scripts first on PATH, where the launcher finds the control plane and the
// real tools, and the agent's key.
func (p Place) Env(url, key, path string) []string {
	return []string{
		"PATH=" + p.Bin() + string(os.PathListSeparator) + path,
		EnvURL + "=" + url, EnvKey + "=" + key, EnvTools + "=" + p.Home,
	}
}

// IntegrationsOf names the Integrations of Connections ("google:work"), each
// once.
func IntegrationsOf(uses []string) []string {
	var out []string

	for _, use := range uses {
		integration, _, _ := strings.Cut(use, ":")
		if integration != "" && !slices.Contains(out, integration) {
			out = append(out, integration)
		}
	}

	return out
}

// Brief is what an agent is told about the accounts it may use: which, and
// the command for each Integration (Superset's tools for Superset); and about
// the phone when it is given one (device). It goes into a worker's first
// message. Each one's guide is a skill the worker's CLI finds by itself
// (issue #536), so it is not named here.
func Brief(uses []string, device string) string {
	var b strings.Builder

	if device == nodeproto.DeviceAndroid {
		b.WriteString("\nAn Android phone is shared by the workers; its device_* tools drive it, one call at a " +
			"time. Look with device_screen before each action and tap its numbered boxes. Never sign in to an " +
			"account on it, and never buy or pay: ask the owner.\n")
	}

	if len(uses) == 0 {
		return b.String()
	}

	b.WriteString("\nAccounts you may use for this task: " + strings.Join(uses, ", ") + ".\n")

	for _, integration := range IntegrationsOf(uses) {
		if integration == Superset {
			b.WriteString("- superset: your superset_* tools read its dashboards and charts. With several accounts " +
				"of it, name one on each call as account.\n")

			continue
		}

		def, ok := Of(integration)
		if !ok {
			continue
		}

		fmt.Fprintf(&b, "- %s: the `%s` command is on your PATH and already signed in; never log in yourself. ",
			integration, def.Name)

		b.WriteString("With several accounts of it, name one on each call: `" + def.Name + " --account <label> ...`.\n")
	}

	return b.String()
}

// ForDispatch prepares a node for a Dispatch that may use Connections and
// returns what the worker's environment gains. Without any it does nothing
// and adds nothing. home is the node's home, where its tools are installed;
// exe the node's binary; key the Dispatch's own.
func ForDispatch(home, exe, url string, uses []string, key string) ([]string, error) {
	needs := IntegrationsOf(uses)
	if len(needs) == 0 {
		return nil, nil
	}

	if url == "" || key == "" {
		return nil, fmt.Errorf("the offer names %s but the node has no key or control plane address to use "+
			"them with", strings.Join(needs, ", "))
	}

	place := Place{Home: home, Exe: exe}
	if err := place.Prepare(needs); err != nil {
		return nil, err
	}

	return place.Env(url, key, os.Getenv("PATH")), nil
}
