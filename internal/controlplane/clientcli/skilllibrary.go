package clientcli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// The skills the owner adds to the control plane (issue #21), after Omnara's
// `omnara skills create|list|get|delete` and Orca's `orca skills list|get|
// install`. `skills get` and `skills install` stay the built-in skills'.

func skillLine(s openapi.Skill) string {
	every := ""
	if s.Everyone {
		every = "\tfor everyone"
	}

	return fmt.Sprintf("%s\trevision %d\t%s%s", s.Name, s.Latest.Revision, s.Latest.Source, every)
}

func printSkill(e *env, s *openapi.Skill) {
	e.printf("%s\n\n%s\n", skillLine(*s), s.Latest.Description)

	if s.Revisions == nil {
		return
	}

	e.printf("\nrevisions:\n")

	for _, r := range *s.Revisions {
		commit := ""
		if c := val(r.Commit); len(c) >= 12 {
			commit = " (" + c[:12] + ")"
		}

		e.printf("%d\t%s\t%s%s\t%d files, %d bytes\tdigest %s\n", r.Revision, r.CreatedAt.Format("2006-01-02 15:04"),
			r.Source, commit, r.Files, r.Size, r.Digest[:12])
	}
}

func skillsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListSkillsWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing skills: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing skills", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if len(resp.JSON200.Data) == 0 {
			e.printf("no skills; add one with `hypomnema skills add`\n")
		}

		for _, s := range resp.JSON200.Data {
			e.printf("%s\n", skillLine(s))
		}
	})
}

func skillsShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	name := fs.Arg(0)

	resp, err := e.api.GetSkillWithResponse(ctx, name)
	if err != nil {
		return fmt.Errorf("reading skill %s: %w", name, err)
	}

	if resp.JSON200 == nil {
		return apiError("reading skill "+name, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() { printSkill(e, resp.JSON200) })
}

// skillsAdd keeps a skill from a repository or an archive: a new skill, or
// the next revision of the one its SKILL.md names.
func skillsAdd(ctx context.Context, e *env, fs *flag.FlagSet) error {
	github, file := str(fs, "github"), str(fs, "file")

	var (
		status  int
		skill   *openapi.Skill
		failure *openapi.Error
	)

	switch {
	case (github == "") == (file == ""):
		return fmt.Errorf("%w: give --github owner/repo[/path][@ref] or --file <zip or tar.gz>", ErrUsage)
	case github != "":
		resp, err := e.api.AddSkillWithResponse(ctx, openapi.SkillFromGitHub{Github: github})
		if err != nil {
			return fmt.Errorf("adding skill from %s: %w", github, err)
		}

		status, failure, skill = resp.StatusCode(), resp.JSONDefault, resp.JSON201
		if skill == nil {
			skill = resp.JSON200
		}
	default:
		archive, err := e.read(file)
		if err != nil {
			return err
		}

		resp, err := e.api.UploadSkillWithBodyWithResponse(ctx, "application/octet-stream",
			bytes.NewReader([]byte(archive)))
		if err != nil {
			return fmt.Errorf("adding skill from %s: %w", file, err)
		}

		status, failure, skill = resp.StatusCode(), resp.JSONDefault, resp.JSON201
		if skill == nil {
			skill = resp.JSON200
		}
	}

	if skill == nil {
		return apiError("adding the skill", status, failure)
	}

	return e.print(fs, skill, func() {
		if status == http.StatusCreated {
			e.printf("skill %s: revision %d added\n", skill.Name, skill.Latest.Revision)
		} else {
			e.printf("skill %s: the files are those of revision %d; nothing added\n", skill.Name, skill.Latest.Revision)
		}

		if !skill.Everyone {
			e.printf("no worker or Bot has it yet; `hypomnema skills everyone %s on` gives it to every one\n", skill.Name)
		}
	})
}

func skillsEveryone(ctx context.Context, e *env, fs *flag.FlagSet) error {
	name, state := fs.Arg(0), fs.Arg(1)
	if state != "on" && state != "off" {
		return fmt.Errorf("%w: skills everyone <skill> on|off", ErrUsage)
	}

	resp, err := e.api.SetSkillEveryoneWithResponse(ctx, name, openapi.SkillEveryone{Everyone: state == "on"})
	if err != nil {
		return fmt.Errorf("setting skill %s: %w", name, err)
	}

	if resp.JSON200 == nil {
		return apiError("setting skill "+name, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if resp.JSON200.Everyone {
			e.printf("skill %s is for every worker and Bot\n", name)
		} else {
			e.printf("skill %s is for no one by itself\n", name)
		}
	})
}

func skillsRemove(ctx context.Context, e *env, fs *flag.FlagSet) error {
	name := fs.Arg(0)

	resp, err := e.api.DeleteSkillWithResponse(ctx, name)
	if err != nil {
		return fmt.Errorf("removing skill %s: %w", name, err)
	}

	if resp.StatusCode() != http.StatusNoContent {
		return apiError("removing skill "+name, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, map[string]bool{"removed": true}, func() { e.printf("skill %s is removed\n", name) })
}

// digestFile beside a skill's files says which revision they are, as
// Omnara's daemon keeps a .digest beside a skill it unpacked.
const digestFile = ".hypomnema-digest"

// skillsSync puts the skills a Bot gets in a folder for pi's --skill: each
// at its newest revision, fetched only when the folder holds another, and a
// skill the Bot no longer gets taken out. The launcher runs it as pi starts
// for a chat (deploy/coordinator/hypomnema-pi), with pi's own token.
func skillsSync(ctx context.Context, e *env, fs *flag.FlagSet) error {
	bot, dir := str(fs, "bot"), str(fs, "dir")
	if bot == "" || dir == "" {
		return fmt.Errorf("%w: skills sync --bot <bot> --dir <folder>", ErrUsage)
	}

	resp, err := e.api.ListBotSkillsWithResponse(ctx, bot)
	if err != nil {
		return fmt.Errorf("listing the skills of bot %s: %w", bot, err)
	}

	if resp.JSON200 == nil {
		return apiError("listing the skills of bot "+bot, resp.StatusCode(), resp.JSONDefault)
	}

	given := resp.JSON200.Data

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("making %s: %w", dir, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dir, err)
	}

	for _, d := range entries {
		keep := strings.HasPrefix(d.Name(), ".") ||
			slices.ContainsFunc(given, func(s openapi.SkillRef) bool { return s.Name == d.Name() })
		if !keep {
			if err := os.RemoveAll(filepath.Join(dir, d.Name())); err != nil {
				return fmt.Errorf("taking skill %s away: %w", d.Name(), err)
			}
		}
	}

	for _, s := range given {
		if err := syncSkill(ctx, e, dir, s); err != nil {
			return err
		}
	}

	e.printf("bot %s: %d skills in %s\n", bot, len(given), dir)

	return nil
}

func syncSkill(ctx context.Context, e *env, dir string, s openapi.SkillRef) error {
	if strings.HasPrefix(s.Name, ".") || strings.ContainsAny(s.Name, `/\`) {
		return fmt.Errorf("skill %q: not a folder name", s.Name)
	}

	target := filepath.Join(dir, s.Name)

	if had, err := os.ReadFile(filepath.Join(target, digestFile)); err == nil && string(had) == s.Digest { //nolint:gosec // the folder the caller names
		return nil
	}

	resp, err := e.api.GetSkillArchiveWithResponse(ctx, s.Name, s.Revision)
	if err != nil {
		return fmt.Errorf("fetching skill %s: %w", s.Name, err)
	}

	if resp.StatusCode() != http.StatusOK {
		return apiError("fetching skill "+s.Name, resp.StatusCode(), resp.JSONDefault)
	}

	sk, err := skillpack.FromArchive(resp.Body)
	if err != nil {
		return fmt.Errorf("skill %s: %w", s.Name, err)
	}

	if sk.Digest != s.Digest {
		return fmt.Errorf("skill %s: the archive's digest is %s, not %s", s.Name, sk.Digest, s.Digest)
	}

	staging := filepath.Join(dir, ".staging-"+s.Name)
	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("clearing %s: %w", staging, err)
	}

	if err := sk.WriteTo(staging); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(staging, digestFile), []byte(s.Digest), 0o600); err != nil {
		return fmt.Errorf("writing the digest of skill %s: %w", s.Name, err)
	}

	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("replacing skill %s: %w", s.Name, err)
	}

	if err := os.Rename(staging, target); err != nil {
		return fmt.Errorf("putting skill %s in place: %w", s.Name, err)
	}

	return nil
}
