// Package skills brings the skills an offer names to the node and puts them
// where a worker's CLI reads skills (issue #21).
//
// The node, not the worker, fetches: each revision once, under
// skills/<digest> in the node's home, downloaded from the control plane with
// the node's token, checked against its digest, unpacked into a staging
// folder and renamed into place, as Omnara's daemon does
// (internal/machinedaemon/skillsync/skillsync.go). Omnara fetches when the
// model calls its skill tool; a vendor CLI reads skills from disk as it
// starts, so here they are fetched before the worker starts.
//
// A Dispatch's skills are copied into the folder its CLI reads, as Orca
// copies a skill into each agent's own folder
// (src/main/skills/provider-placement): Claude Code gets a plugin folder of
// its own through --plugin-dir and Codex its Conversation's CODEX_HOME/skills,
// as Omnigent places them (inner/bundle_skills.py, inner/codex_staging.py).
package skills

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Dir is where a node keeps the files of the skill revision with digest.
func Dir(home, digest string) string { return filepath.Join(home, "skills", digest) }

// Cache fetches skills from the control plane into the node's home.
type Cache struct {
	Home   string
	URL    string
	Token  string
	Client *http.Client
}

// Ensure has every skill the offer names in the cache, fetching the ones it
// lacks. A skill that cannot be had fails the whole: the worker does not
// start without what it was given.
func (c Cache) Ensure(ctx context.Context, skills []nodeproto.Skill) error {
	for _, sk := range skills {
		if !digestPattern.MatchString(sk.Digest) {
			return fmt.Errorf("skill %s: %q is not a digest", sk.Name, sk.Digest)
		}

		if _, err := os.Stat(Dir(c.Home, sk.Digest)); err == nil {
			continue
		}

		if err := c.fetch(ctx, sk); err != nil {
			return fmt.Errorf("fetching skill %s revision %d: %w", sk.Name, sk.Revision, err)
		}
	}

	return nil
}

func (c Cache) fetch(ctx context.Context, sk nodeproto.Skill) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(c.URL, "/")+nodeproto.PathSkills+sk.Digest, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

		return fmt.Errorf("the control plane answered %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	archive, err := io.ReadAll(io.LimitReader(resp.Body, skillpack.MaxArchive+1))
	if err != nil {
		return fmt.Errorf("reading the archive: %w", err)
	}

	got, err := skillpack.FromArchive(archive)
	if err != nil {
		return err
	}

	if got.Digest != sk.Digest {
		return fmt.Errorf("the archive's digest is %s, not %s", got.Digest, sk.Digest)
	}

	root := filepath.Join(c.Home, "skills")
	if err := os.MkdirAll(root, 0o755); err != nil { //nolint:gosec // workers read it
		return fmt.Errorf("making %s: %w", root, err)
	}

	staging := filepath.Join(root, ".staging-"+sk.Digest[:12]+"-"+random())
	defer func() { _ = os.RemoveAll(staging) }()

	if err := got.WriteTo(staging); err != nil {
		return err
	}

	// Another Dispatch may have put the same revision in place meanwhile.
	if err := os.Rename(staging, Dir(c.Home, sk.Digest)); err != nil {
		if _, statErr := os.Stat(Dir(c.Home, sk.Digest)); statErr == nil {
			return nil
		}

		return fmt.Errorf("putting the skill in place: %w", err)
	}

	return nil
}

func random() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}

// Place copies the skills into dir, one folder each named after the skill,
// after taking away what dir held but its entries whose names start with a
// dot (Codex keeps its own skills in CODEX_HOME/skills/.system).
func Place(home, dir string, skills []nodeproto.Skill) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("making %s: %w", dir, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dir, err)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}

		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("clearing %s: %w", e.Name(), err)
		}
	}

	for _, sk := range skills {
		if strings.HasPrefix(sk.Name, ".") || strings.ContainsAny(sk.Name, `/\`) {
			return fmt.Errorf("skill %q: not a folder name", sk.Name)
		}

		if err := os.CopyFS(filepath.Join(dir, sk.Name), os.DirFS(Dir(home, sk.Digest))); err != nil {
			return fmt.Errorf("placing skill %s: %w", sk.Name, err)
		}
	}

	return nil
}

// pluginName is the name Claude Code shows the skills under (hypomnema:<skill>).
const pluginName = "hypomnema"

// PlacePlugin makes dir a Claude Code plugin holding the skills, for
// --plugin-dir, with the manifest Omnigent writes for its bundles.
func PlacePlugin(home, dir string, skills []nodeproto.Skill) error {
	if err := Place(home, filepath.Join(dir, "skills"), skills); err != nil {
		return err
	}

	manifest, err := json.Marshal(map[string]string{
		"name": pluginName, "description": "The skills Hypomnema gives this worker.",
	})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o700); err != nil {
		return fmt.Errorf("making the plugin's manifest folder: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), manifest, 0o600); err != nil {
		return fmt.Errorf("writing the plugin's manifest: %w", err)
	}

	return nil
}
