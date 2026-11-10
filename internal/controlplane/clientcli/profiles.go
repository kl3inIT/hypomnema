package clientcli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// Profiles, providers and models (issue #42): every token lists them; only
// the owner applies them.

func profilesList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListProfilesWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing profiles: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing profiles", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, p := range resp.JSON200.Data {
			e.printf("%s\tv%d\t%s\t%s%s\n  %s\n", p.Name, p.Version, p.Runtime, modelOf(p), cannotRun(p), p.Description)
		}
	})
}

// cannotRun is why no worker can start under a profile now, for a line of
// the list; empty when one can, or when the API does not say.
func cannotRun(p openapi.Profile) string {
	if p.Available == nil || *p.Available || p.UnavailableReason == nil {
		return ""
	}

	out := "\tcannot run now: " + string(*p.UnavailableReason)
	if p.AvailableAt != nil {
		out += " until " + p.AvailableAt.UTC().Format(time.RFC3339)
	}

	return out
}

func modelOf(p openapi.Profile) string {
	m := val(p.Model)
	if m == "" {
		m = "(runtime default)"
	}

	if val(p.Provider) != "" {
		m = val(p.Provider) + "/" + m
	}

	if val(p.Effort) != "" {
		m += " " + val(p.Effort)
	}

	return m
}

func profilesShow(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.GetProfileWithResponse(ctx, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("reading profile %s: %w", fs.Arg(0), err)
	}

	if resp.JSON200 == nil {
		return apiError("reading profile "+fs.Arg(0), resp.StatusCode(), resp.JSONDefault)
	}

	p := resp.JSON200

	return e.print(fs, p, func() { e.printf("# %s v%d (%s)\n%s", p.Name, p.Version, p.VersionId, p.Source) })
}

func profilesVersions(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListProfileVersionsWithResponse(ctx, fs.Arg(0))
	if err != nil {
		return fmt.Errorf("listing the versions of %s: %w", fs.Arg(0), err)
	}

	if resp.JSON200 == nil {
		return apiError("listing the versions of "+fs.Arg(0), resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, p := range resp.JSON200.Data {
			e.printf("v%d\t%s\t%s\t%s\n", p.Version, p.VersionId, p.CreatedAt.Local().Format("2006-01-02 15:04"),
				modelOf(p))
		}
	})
}

// nameIn reads the name a YAML document gives itself, for the request path.
func nameIn(source string) (string, error) {
	var doc struct {
		Name string `yaml:"name"`
	}

	if err := yaml.Unmarshal([]byte(source), &doc); err != nil || doc.Name == "" {
		return "", fmt.Errorf("%w: the file is not YAML with a name", ErrUsage)
	}

	return doc.Name, nil
}

func profilesApply(ctx context.Context, e *env, fs *flag.FlagSet) error {
	source, err := e.read(fs.Arg(0))
	if err != nil {
		return err
	}

	name, err := nameIn(source)
	if err != nil {
		return err
	}

	resp, err := e.api.ApplyProfileWithResponse(ctx, name, openapi.Source{Source: source})
	if err != nil {
		return fmt.Errorf("applying profile %s: %w", name, err)
	}

	p, verb := resp.JSON201, "new version"
	if p == nil {
		p, verb = resp.JSON200, "unchanged, still"
	}

	if p == nil {
		return apiError("applying profile "+name, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, p, func() { e.printf("profile %s: %s v%d (%s)\n", p.Name, verb, p.Version, p.VersionId) })
}

func providersList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListProvidersWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing providers: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing providers", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, p := range resp.JSON200.Data {
			e.printf("%s\t%s\t%s\t%v\n", p.Name, p.Kind, val(p.BaseUrl), p.Models)
		}
	})
}

func providersApply(ctx context.Context, e *env, fs *flag.FlagSet) error {
	source, err := e.read(fs.Arg(0))
	if err != nil {
		return err
	}

	name, err := nameIn(source)
	if err != nil {
		return err
	}

	resp, err := e.api.ApplyProviderWithResponse(ctx, name, openapi.Source{Source: source})
	if err != nil {
		return fmt.Errorf("applying provider %s: %w", name, err)
	}

	if resp.JSON200 == nil {
		return apiError("applying provider "+name, resp.StatusCode(), resp.JSONDefault)
	}

	p := resp.JSON200

	return e.print(fs, p, func() { e.printf("provider %s (%s): %v\n", p.Name, p.Kind, p.Models) })
}

func modelsList(ctx context.Context, e *env, fs *flag.FlagSet) error {
	resp, err := e.api.ListModelsWithResponse(ctx)
	if err != nil {
		return fmt.Errorf("listing models: %w", err)
	}

	if resp.JSON200 == nil {
		return apiError("listing models", resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		for _, m := range resp.JSON200.Data {
			e.printf("%s\t%s\t%s\n", m.Name, m.Provider, m.Kind)
		}
	})
}
