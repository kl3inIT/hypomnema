package clientcli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

// The settings every Bot's pi starts with (issue #395, part 1 of #366;
// docs/research/2026-11-14-pi-defaults.md). What is not named keeps pi's
// default, which matches most references: 20,000 recent tokens kept, the
// thinking level, the tool output limits and the retries.
const (
	// piReserveShare and piReserveFloor: pi compacts a chat once fewer than
	// max(15% of the model's window, 16,384) tokens are free, as omp
	// (packages/agent/src/compaction/compaction.ts) and opencode
	// (packages/core/src/session/compaction.ts) reserve. pi's own reserve is
	// 16,384 whatever the window.
	piReserveShare = 0.15
	piReserveFloor = 16384
)

// piDefaultTools are pi's read-only search tools, built in and off, turned on
// beside read, bash, edit and write, as omp, opencode, OpenClaw and Hermes
// give them by default.
var piDefaultTools = []string{"+grep", "+find", "+ls"}

// piSettingsFile is the part of pi's settings.json written here (pi
// docs/settings.md).
type piSettingsFile struct {
	DefaultTools []string          `json:"defaultTools"`
	Compaction   piCompactionGroup `json:"compaction"`
}

type piCompactionGroup struct {
	// ModelOverrides is keyed "<provider>/<model>"; pi reads the one of the
	// model in use at each check, so a model changed during a chat needs
	// nothing more (settings-manager.ts getCompactionTokenSetting).
	ModelOverrides map[string]piCompactionOverride `json:"modelOverrides,omitempty"`
}

type piCompactionOverride struct {
	ReserveTokens int64 `json:"reserveTokens"`
}

// piSettings prints the settings.json a chat's pi starts with, from the
// models.json the launcher wrote for it just before (pi-models): each model
// with a window gets a reserve that scales with it; a model without one
// keeps pi's.
func piSettings(_ context.Context, e *env, fs *flag.FlagSet) error {
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("reading pi's models: %w", err)
	}

	var models piModelsFile
	if err := json.Unmarshal(data, &models); err != nil {
		return fmt.Errorf("reading pi's models: %w", err)
	}

	out, err := json.MarshalIndent(piSettingsOf(models), "", "  ")
	if err != nil {
		return fmt.Errorf("encoding pi's settings: %w", err)
	}

	e.printf("%s\n", out)

	return nil
}

func piSettingsOf(models piModelsFile) piSettingsFile {
	settings := piSettingsFile{DefaultTools: piDefaultTools}

	for provider, p := range models.Providers {
		for _, m := range p.Models {
			if m.ContextWindow <= 0 {
				continue
			}

			if settings.Compaction.ModelOverrides == nil {
				settings.Compaction.ModelOverrides = map[string]piCompactionOverride{}
			}

			settings.Compaction.ModelOverrides[provider+"/"+m.ID] = piCompactionOverride{
				ReserveTokens: max(int64(float64(m.ContextWindow)*piReserveShare), piReserveFloor),
			}
		}
	}

	return settings
}
