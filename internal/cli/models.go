package cli

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
	"github.com/spf13/cobra"
)

func pathDir(p string) string { return filepath.Dir(p) }

func osCommandWithStdio(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c
}

// newModelsCmd returns the `chb models` parent command. The
// subcommands let an operator inspect the unified models config — what
// models are known, what each tier resolves to under each budget mode,
// and where the override file lives.
func newModelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Inspect the models / pricing / tiers config",
		Long: `Inspect the unified models config that drives the cost meter, tier
resolution, and alias canonicalisation.

The config ships embedded in the binary (internal/models/default-models.yaml)
and can be overridden by writing ~/.config/hive/models.yaml (or
$XDG_CONFIG_HOME/hive/models.yaml, or $HIVE_MODELS_PATH). Override
entries merge on top of the embedded defaults — to add one model you only
need to ship that one model entry.

Subcommands:
  list        — every known model with pricing + family + Copilot multiplier
  tiers       — the four-position cost dial: each role × each budget mode
  show <model>— pricing + tier membership for one model
  path        — print the override file path (whether it exists or not)
  edit        — open the override file in $EDITOR (creates it if missing)
`,
	}
	cmd.AddCommand(
		newModelsListCmd(),
		newModelsTiersCmd(),
		newModelsShowCmd(),
		newModelsPathCmd(),
		newModelsEditCmd(),
	)
	return cmd
}

func newModelsListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every model with pricing + family + Copilot multiplier",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := models.Load()
			ids := make([]string, 0, len(cfg.Models))
			for k := range cfg.Models {
				ids = append(ids, k)
			}
			sort.Strings(ids)

			if jsonOut {
				out := map[string]any{
					"last_updated": cfg.LastUpdated,
					"models":       cfg.Models,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Last updated: %s\n\n", cfg.LastUpdated)
			fmt.Fprintf(cmd.OutOrStdout(), "%-32s %-10s %12s %12s %8s\n",
				"MODEL", "FAMILY", "$/1M IN", "$/1M OUT", "COPILOT")
			fmt.Fprintln(cmd.OutOrStdout(), strings.Repeat("─", 80))
			for _, id := range ids {
				m := cfg.Models[id]
				fmt.Fprintf(cmd.OutOrStdout(), "%-32s %-10s %12.2f %12.2f %8.2fx\n",
					id, m.Family, m.InputPerMTokUSD, m.OutputPerMTokUSD, m.CopilotMultiplier)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a table")
	return cmd
}

func newModelsTiersCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "tiers",
		Short: "Show the four-position cost dial: each role × each budget mode",
		RunE: func(cmd *cobra.Command, args []string) error {
			return showModelTiers(cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a table")
	return cmd
}

// showModelTiers prints what each role's tier resolves to under each budget
// mode, as a table or, with jsonOut, as JSON.
func showModelTiers(out io.Writer, jsonOut bool) error {
	cfg := models.Load()
	roles := make([]string, 0, len(cfg.Tiers))
	for k := range cfg.Tiers {
		roles = append(roles, k)
	}
	sort.Strings(roles)

	modes := []string{"premium", "standard", "cheap", "free"}

	if jsonOut {
		return writeTiersJSON(out, cfg, roles, modes)
	}
	printTiersTable(out, cfg, roles, modes)
	return nil
}

// writeTiersJSON writes the role × mode table as indented JSON.
func writeTiersJSON(w io.Writer, cfg *models.Config, roles, modes []string) error {
	out := map[string]map[string]string{}
	for _, role := range roles {
		out[role] = map[string]string{}
		for _, mode := range modes {
			out[role][mode] = cfg.ResolveTier(role, mode)
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// printTiersTable prints the role × mode table and where the user's models
// config, the override of the tiers, lives.
func printTiersTable(out io.Writer, cfg *models.Config, roles, modes []string) {
	fmt.Fprintf(out, "%-12s %-22s %-22s %-22s %-22s\n",
		"ROLE", "PREMIUM", "STANDARD (default)", "CHEAP", "FREE")
	fmt.Fprintln(out, strings.Repeat("─", 102))
	for _, role := range roles {
		fmt.Fprintf(out, "%-12s ", role)
		for _, mode := range modes {
			fmt.Fprintf(out, "%-22s ", cfg.ResolveTier(role, mode))
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Override at: %s\n", models.UserConfigPath())
}

func newModelsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <model-or-alias>",
		Short: "Show pricing + tier membership for one model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return showModel(cmd.OutOrStdout(), args[0])
		},
	}
}

// showModel prints the pricing of the model name resolves to, and the tiers
// that select it.
func showModel(out io.Writer, name string) error {
	cfg := models.Load()
	id := cfg.Resolve(name)
	m, ok := cfg.Models[id]
	if !ok {
		return fmt.Errorf("model %q not in config (resolved to %q)", name, id)
	}
	fmt.Fprintf(out, "model: %s\n", id)
	if id != name {
		fmt.Fprintf(out, "  (resolved from alias %q)\n", name)
	}
	printModelPricing(out, m)
	printModelTiers(out, cfg, id)
	return nil
}

// printModelPricing prints the model's family and prices.
func printModelPricing(out io.Writer, m models.Model) {
	fmt.Fprintf(out, "  family:             %s\n", m.Family)
	fmt.Fprintf(out, "  input_per_mtok_usd: $%.2f\n", m.InputPerMTokUSD)
	if m.CachedInputPerMTokUSD != nil {
		fmt.Fprintf(out, "  cached_input_per_mtok_usd: $%g\n", *m.CachedInputPerMTokUSD)
	} else {
		fmt.Fprintln(out, "  cached_input_per_mtok_usd: none (a cached token costs the input price)")
	}
	if m.CacheWritePerMTokUSD != nil {
		fmt.Fprintf(out, "  cache_write_per_mtok_usd: $%g\n", *m.CacheWritePerMTokUSD)
	} else {
		fmt.Fprintln(out, "  cache_write_per_mtok_usd: none (a token written to the cache costs the input price)")
	}
	if m.CacheWrite1hPerMTokUSD != nil {
		fmt.Fprintf(out, "  cache_write_1h_per_mtok_usd: $%g\n", *m.CacheWrite1hPerMTokUSD)
	} else {
		fmt.Fprintln(out, "  cache_write_1h_per_mtok_usd: none (a 1-hour cache write costs the 5-minute write price)")
	}
	fmt.Fprintf(out, "  output_per_mtok_usd: $%.2f\n", m.OutputPerMTokUSD)
	fmt.Fprintf(out, "  copilot_multiplier: %.2fx\n", m.CopilotMultiplier)
}

// printModelTiers prints the reverse lookup: which tiers/modes select this
// model?
func printModelTiers(out io.Writer, cfg *models.Config, id string) {
	fmt.Fprintln(out, "  selected by:")
	selectors := tiersSelecting(cfg, id)
	for _, t := range selectors {
		fmt.Fprintf(out, "    tier=%s mode=%s\n", t.role, t.mode)
	}
	if len(selectors) == 0 {
		fmt.Fprintln(out, "    (no tier × mode combination resolves to this model)")
	}
}

// tierMode is one role's tier under one budget mode.
type tierMode struct{ role, mode string }

// tiersSelecting lists each tier and budget mode that resolves to the
// model.
func tiersSelecting(cfg *models.Config, id string) []tierMode {
	var selectors []tierMode
	for _, role := range []string{"planner", "synthesist", "worker", "verifier"} {
		for _, mode := range []string{"premium", "standard", "cheap", "free"} {
			if cfg.ResolveTier(role, mode) == id {
				selectors = append(selectors, tierMode{role, mode})
			}
		}
	}
	return selectors
}

func newModelsPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the override config file path (created or not)",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := models.UserConfigPath()
			if path == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "(no override path resolvable — neither HOME nor XDG_CONFIG_HOME is set)")
				return nil
			}
			info, err := os.Stat(path)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s (does not exist — `chb models edit` will create it)\n", path)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%d bytes)\n", path, info.Size())
			return nil
		},
	}
}

func newModelsEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open the override config in $EDITOR (creates it if missing)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return editModelsOverride(cmd.OutOrStdout())
		},
	}
}

// editModelsOverride opens the override file in $EDITOR, vi by default.
func editModelsOverride(out io.Writer) error {
	path := models.UserConfigPath()
	if path == "" {
		return fmt.Errorf("no override path resolvable — set HOME or XDG_CONFIG_HOME")
	}
	editor := cmp.Or(os.Getenv("EDITOR"), "vi")
	if err := seedModelsOverride(path); err != nil {
		return err
	}
	fmt.Fprintf(out, "Opening %s in %s\n", path, editor)
	c := osCommandWithStdio(editor, path)
	return c.Run()
}

// seedModelsOverride seeds the file with a friendly comment if absent so
// the user has a starting point.
func seedModelsOverride(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if mkdirErr := os.MkdirAll(pathDir(path), 0o755); mkdirErr != nil {
		return fmt.Errorf("create config dir: %w", mkdirErr)
	}
	if err := os.WriteFile(path, []byte(modelsOverrideSeed), 0o644); err != nil {
		return fmt.Errorf("seed override file: %w", err)
	}
	return nil
}

// modelsOverrideSeed is the commented example a new override file starts
// with.
const modelsOverrideSeed = `# Override file for chb's models / pricing / tiers config.
# Defaults ship embedded in the binary; entries here merge on top.
# Schema: same as internal/models/default-models.yaml in the repo.
# Run: chb models tiers   to see the resolved table.

# Example: add a custom model + use it for the planner tier
# models:
#   my-private-model:
#     family: openai
#     input_per_mtok_usd: 5.00
#     cached_input_per_mtok_usd: 0.50   # optional; a cached token costs the input price without it
#     output_per_mtok_usd: 20.00
#     copilot_multiplier: 1.0
# tiers:
#   planner:
#     premium: my-private-model
`
