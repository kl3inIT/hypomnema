package controlplane

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/controlplane/machines"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
	"github.com/kl3inIT/hypomnema/internal/shared/platform"
)

// machineManager builds the manager of provider machines over the registry
// and the vault, which holds each provider's key.
func machineManager(
	svc *orchestration.Service, secrets *vault.Service, publicURL string, logger *slog.Logger,
) *machines.Manager {
	return machines.New(svc, machineConnect(secrets), publicURL, platform.SystemClock{}, logger)
}

// machineConnect opens a provider record's key in the vault and returns the
// provider of its kind.
func machineConnect(secrets *vault.Service) machines.Connect {
	return func(ctx context.Context, rec orchestration.MachineProvider) (machines.Provider, error) {
		name, ok := vault.NameFromRef(rec.KeyRef)
		if !ok {
			return nil, fmt.Errorf("%w: the key of machine provider %s is %q, not vault:<name>",
				orchestration.ErrInvalid, rec.Name, rec.KeyRef)
		}

		opened, err := secrets.Open(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("opening the key of machine provider %s: %w", rec.Name, err)
		}

		switch rec.Kind {
		case orchestration.MachineProviderAWS:
			return machines.NewAWS(rec, opened.Payload[vault.KeyValue])
		default:
			return nil, fmt.Errorf("%w: unknown machine provider kind %q", orchestration.ErrInvalid, rec.Kind)
		}
	}
}

// secretReferrers names everything that still names a vault secret: the
// model providers, the machine providers and the accounts logged in here.
func secretReferrers(svc *orchestration.Service) vault.Referrers {
	return func(ctx context.Context, ref string) ([]string, error) {
		models, err := svc.ProvidersUsingKeyRef(ctx, ref)
		if err != nil {
			return nil, err
		}

		machineProviders, err := svc.MachineProvidersUsingKeyRef(ctx, ref)
		if err != nil {
			return nil, err
		}

		accounts, err := svc.AccountsUsingSecretRef(ctx, ref)
		if err != nil {
			return nil, err
		}

		return slices.Concat(models, machineProviders, accounts), nil
	}
}

const machinesUsage = "machines providers add <name> --kind aws --key vault:<secret> --region R --size S " +
	"[--max N] [--option name=value]... | machines providers list | machines providers rm|check <name> | " +
	"machines create <name> --provider P [--region R] [--size S] [--max N] | machines stop|start <node>"

// machinesCommand is the owner's commands for provider machines. A machine
// is removed as any node is, with `nodes rm`.
func (o *owner) machinesCommand(ctx context.Context, args []string) error {
	switch {
	case len(args) >= 1 && args[0] == "providers":
		return o.machineProviders(ctx, args[1:])
	case len(args) >= 2 && args[0] == "create":
		fs := flag.NewFlagSet("machines create", flag.ContinueOnError)
		provider := fs.String("provider", "", "the machine provider's name")
		region := fs.String("region", "", "the region (default: the provider's)")
		size := fs.String("size", "", "the machine size (default: the provider's)")
		limit := fs.Int("max", 0, "Dispatches the node runs at once (0: the default)")

		if err := fs.Parse(args[2:]); err != nil || *provider == "" || fs.NArg() > 0 {
			return fmt.Errorf("%w: %s", ErrUsage, machinesUsage)
		}

		n, err := o.machines.Create(ctx, machines.NewMachine{
			Name: args[1], Provider: *provider, Region: *region, Size: *size, MaxDispatches: *limit,
		})
		if n.Machine != nil {
			o.printf("node %s (%s): %s %s in %s, %s\n", n.Name, n.ID, *provider, n.Machine.Size, n.Machine.Region,
				machineState(n.Machine))
		}

		if err != nil {
			return err
		}

		o.printf("it installs itself and says hello within %s; `nodes list` shows it\n", machines.HelloTimeout)

		return nil
	case len(args) == 2 && (args[0] == "stop" || args[0] == "start"):
		n, err := o.node(ctx, args[1])
		if err != nil {
			return err
		}

		change, done := o.machines.Stop, "stopped: it keeps its disk and costs no compute"
		if args[0] == "start" {
			change, done = o.machines.Start, "starting: it takes work again once its node connects"
		}

		if _, err := change(ctx, n.ID); err != nil {
			return err
		}

		o.printf("node %s (%s) %s\n", n.Name, n.ID, done)

		return nil
	default:
		return fmt.Errorf("%w: %s", ErrUsage, machinesUsage)
	}
}

func (o *owner) machineProviders(ctx context.Context, args []string) error {
	switch {
	case len(args) >= 2 && args[0] == "add":
		return o.addMachineProvider(ctx, args[1], args[2:])
	case len(args) == 1 && args[0] == "list":
		providers, err := o.svc.ListMachineProviders(ctx)
		if err != nil {
			return err
		}

		for _, p := range providers {
			o.printf("%s\t%s\t%s\t%s\t%s\tup to %d\t%s\n", p.ID, p.Name, p.Kind, p.Region, p.Size, p.MaxMachines,
				optionList(p.Options))
		}

		return nil
	case len(args) == 2 && args[0] == "rm":
		p, err := o.svc.GetMachineProviderByName(ctx, args[1])
		if err != nil {
			return err
		}

		if err := o.svc.RemoveMachineProvider(ctx, p.ID); err != nil {
			return err
		}

		o.printf("machine provider %s removed\n", p.Name)

		return nil
	case len(args) == 2 && args[0] == "check":
		name := args[1]

		orphans, err := o.machines.Check(ctx, name)
		if err != nil {
			return err
		}

		o.printf("machine provider %s answers; %d machines that no node stands for\n", name, len(orphans))

		for _, r := range orphans {
			o.printf("%s\t%s\t%s\t%s\tfor node %s\tsince %s\n", r.ID, r.Region, r.Size, r.State, r.NodeID,
				r.CreatedAt.Format(time.RFC3339))
		}

		if len(orphans) > 0 {
			o.printf("these cost money and nothing here destroys them: destroy them at the provider\n")
		}

		return nil
	default:
		return fmt.Errorf("%w: %s", ErrUsage, machinesUsage)
	}
}

func (o *owner) addMachineProvider(ctx context.Context, name string, args []string) error {
	fs := flag.NewFlagSet("machines providers add", flag.ContinueOnError)
	kind := fs.String("kind", "", "the provider's API: aws")
	key := fs.String("key", "", "where its key is: vault:<secret name>")
	region := fs.String("region", "", "the region a machine gets when none is named")
	size := fs.String("size", "", "the machine size a machine gets when none is named")
	limit := fs.Int("max", 0, "machines that may exist at once (0: the default)")
	options := map[string]string{}

	fs.Func("option", "name=value, what only this kind takes; may repeat", func(v string) error {
		name, value, ok := strings.Cut(v, "=")
		if !ok || name == "" {
			return errors.New("an option is name=value")
		}

		options[name] = value

		return nil
	})

	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return fmt.Errorf("%w: %s", ErrUsage, machinesUsage)
	}

	secret, ok := vault.NameFromRef(*key)
	if !ok {
		return fmt.Errorf("%w: --key is vault:<secret name>", orchestration.ErrInvalid)
	}

	if _, err := o.vault.Get(ctx, secret); err != nil {
		return fmt.Errorf("the key of the machine provider: %w", err)
	}

	if orchestration.MachineProviderKind(*kind) == orchestration.MachineProviderAWS {
		if err := machines.ValidateAWSOptions(options); err != nil {
			return err
		}
	}

	p, err := o.svc.AddMachineProvider(ctx, orchestration.NewMachineProvider{
		Name: name, Kind: orchestration.MachineProviderKind(*kind), KeyRef: *key, Region: *region, Size: *size,
		MaxMachines: *limit, Options: options,
	})
	if err != nil {
		return err
	}

	o.printf("machine provider %s (%s): %s, %s %s, up to %d machines\n", p.Name, p.ID, p.Kind, p.Region, p.Size,
		p.MaxMachines)
	o.printf("`machines providers check %s` proves its key works\n", p.Name)

	return nil
}

// machineState is a machine's state, or its power when it is not on, with
// why the last step failed, for one line.
func machineState(m *orchestration.Machine) string {
	state := string(m.State)
	if m.Power != "" {
		state = string(m.Power)
	}

	if m.Error == "" {
		return state
	}

	return state + ": " + m.Error
}

func optionList(options map[string]string) string {
	parts := make([]string, 0, len(options))
	for _, name := range slices.Sorted(maps.Keys(options)) {
		parts = append(parts, name+"="+options[name])
	}

	return strings.Join(parts, " ")
}
