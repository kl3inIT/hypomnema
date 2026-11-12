package clientcli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
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
	github, file, named := str(fs, "github"), str(fs, "file"), str(fs, "skill")

	var (
		status  int
		skill   *openapi.Skill
		failure *openapi.Error
	)

	switch {
	case (github == "") == (file == ""):
		return fmt.Errorf("%w: give --github owner/repo[/path][@ref] or --file <zip or tar.gz>", ErrUsage)
	case named != "" && github == "":
		return fmt.Errorf("%w: --skill names a skill of the repository --github gives", ErrUsage)
	case github != "":
		body := openapi.SkillFromGitHub{Github: github}
		if named != "" {
			body.Skill = &named

			if !boolean(fs, "json") {
				printAudits(ctx, e, github, named)
			}
		}

		resp, err := e.api.AddSkillWithResponse(ctx, body)
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

	had, err := os.ReadFile(filepath.Join(target, digestFile)) //nolint:gosec // the folder the caller names
	if err == nil && string(had) == s.Digest {
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

// skillsSearch is the skills CLI's `skills find <query>` without a terminal
// (src/find.ts): each skill as owner/repo@name with its installs, and its
// page on skills.sh; those that match only some of the words after a line
// that says so.
func skillsSearch(ctx context.Context, e *env, fs *flag.FlagSet) error {
	query := strings.Join(fs.Args(), " ")
	params := &openapi.SearchSkillsParams{Q: query}

	if owner := str(fs, "owner"); owner != "" {
		params.Owner = &owner
	}

	resp, err := e.api.SearchSkillsWithResponse(ctx, params)
	if err != nil {
		return fmt.Errorf("searching skills.sh: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("searching skills.sh", resp.StatusCode(), resp.JSONDefault)
	}

	found := resp.JSON200

	return e.print(fs, found, func() {
		if len(found.Data) == 0 {
			e.printf("no skills found for %q\n", query)

			return
		}

		e.printf("add one with `hypomnema skills add --github <owner/repo> --skill <name>`\n\n")

		for i, s := range found.Data {
			if i == found.Exact {
				if found.Exact == 0 {
					e.printf("no skill matched every word of %q; some of them:\n\n", query)
				} else {
					e.printf("matching some of the words of %q:\n\n", query)
				}
			}

			line := s.Source + "@" + s.Name
			if n := installs(s.Installs); n != "" {
				line += "  " + n
			}

			e.printf("%s\n└ https://skills.sh/%s\n\n", line, s.Id)
		}
	})
}

// installs is the skills CLI's formatInstalls.
func installs(n int64) string {
	trim := func(f float64) string { return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0") }

	switch {
	case n <= 0:
		return ""
	case n >= 1_000_000:
		return trim(float64(n)/1_000_000) + "M installs"
	case n >= 1_000:
		return trim(float64(n)/1_000) + "K installs"
	case n == 1:
		return "1 install"
	default:
		return strconv.FormatInt(n, 10) + " installs"
	}
}

// printAudits shows what skills.sh's auditors said of the skill before it is
// added, as the skills CLI's `skills add` shows its security table
// (src/add.ts buildSecurityLines). It never stops the adding; when skills.sh
// says nothing, nothing is shown.
func printAudits(ctx context.Context, e *env, github, name string) {
	repo, _, _ := strings.Cut(strings.TrimPrefix(github, "github:"), "@")

	resp, err := e.api.ListSkillAuditsWithResponse(ctx, &openapi.ListSkillAuditsParams{
		Source: strings.ToLower(strings.Trim(repo, "/")), Skill: skillSlug(name),
	})
	if err != nil || resp.JSON200 == nil || len(resp.JSON200.Data) == 0 {
		return
	}

	e.printf("skills.sh's audits of %s:\n", name)

	for _, a := range resp.JSON200.Data {
		e.printf("  %-7s %s\n", auditor(a.Partner), auditResult(a))
	}

	e.printf("\n")
}

func auditor(partner string) string {
	switch partner {
	case "ath":
		return "Gen"
	case "socket":
		return "Socket"
	case "snyk":
		return "Snyk"
	default:
		return partner
	}
}

// auditResult is the CLI's riskLabel, and socketLabel for Socket's count of alerts.
func auditResult(a openapi.SkillAudit) string {
	if a.Partner == "socket" && a.Alerts != nil {
		if *a.Alerts == 1 {
			return "1 alert"
		}

		return strconv.Itoa(*a.Alerts) + " alerts"
	}

	switch a.Risk {
	case openapi.SkillAuditRiskCritical:
		return "Critical Risk"
	case openapi.SkillAuditRiskHigh:
		return "High Risk"
	case openapi.SkillAuditRiskMedium:
		return "Med Risk"
	case openapi.SkillAuditRiskLow:
		return "Low Risk"
	case openapi.SkillAuditRiskSafe:
		return "Safe"
	default:
		return "--"
	}
}

var (
	slugSpaces = regexp.MustCompile(`[\s_]+`)
	slugOther  = regexp.MustCompile(`[^a-z0-9-]`)
	slugDashes = regexp.MustCompile(`-+`)
)

// skillSlug is the skills CLI's toSkillSlug (src/blob.ts): a skill's name as
// skills.sh keys it.
func skillSlug(name string) string {
	s := slugSpaces.ReplaceAllString(strings.ToLower(name), "-")
	s = slugDashes.ReplaceAllString(slugOther.ReplaceAllString(s, ""), "-")

	return strings.Trim(s, "-")
}
