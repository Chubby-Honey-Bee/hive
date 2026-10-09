package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/Chubby-Honey-Bee/hive/internal/harness"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
)

// newAgentHarnessCmd is chb agent-harness: its flags build the harness's
// options, and the harness package runs them.
func newAgentHarnessCmd() *cobra.Command {
	var o harness.Options
	cmd := &cobra.Command{
		Use:   "agent-harness",
		Short: "Run the shipped prompts through a provider (the local claude CLI by default) and assert their contracts",
		Long: `Drives foragers, agent templates and the proof workflow
through a provider — by default the claude CLI on this machine (no API key;
uses your Claude Code auth), or any other with --provider — and checks what
each prompt promises:

  swarm     chb ask on a fixed question + preset. Every forager's verdict
            JSON must satisfy its own frontmatter contract (required keys,
            verdict enum, length caps, forbidden phrases); the run must
            complete with no failed or rejected node; converging
            resonates-bonded pairs must have a fired forager_bonds row;
            the canonical artifact must verify; the MSS audit must pass.
  template  an agents/<name>.md prompt + a fixed micro-task via claude -p.
            Marker templates are ingested with chb ingest; db-write
            templates must have written their findings. Labels and
            dependency rules are enforced by the store.
  proof     chb proof (the end-to-end run of the unattended runner).
  hive      the hive workflow over a database seeded with one critical gap.
            The gap must be resolved by a finding the run wrote, the
            finding must answer it, the next plan must not dispatch for it
            again, and the MSS audit must pass — all read from the database.
  bench     graded twin items (docs/specs/bench.md): questions the
            forager roster answers, each paired with an edit that flips the
            answer, asked of a swarm and of one call with no persona. Each
            run is graded and written to results.jsonl for chb bench decide.
  design    the executability benchmark (docs/specs/bench-design.md):
            HIVE researches a small Go task and writes a design and a plan,
            one model call writes the same from the same inputs, a plain
            executor carries each plan out without seeing the task, and
            hidden tests grade the result. One row per task and arm goes
            to results.jsonl for chb design report.

Swarm and bench runs work in a private copy of foragers/ and agents/, and
fail if that copy changed. Template cases drive claude -p, so they run only
on the claude-cli provider and are skipped on any other.

Model text varies between runs; the contracts do not. The report pins the
inputs (suite sha256, preset, provider, tier) and records each artifact's
sha256 as a drift detector — not as a promise of byte-equal output.

Run from a terminal or from inside a Claude Code session; both work.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			o.Self, o.Stdout = exe, os.Stdout
			o.Foragers, o.LoadForagers = foragersSource, loadForagers
			o.SeedSet, o.ProviderSet = cmd.Flags().Changed("seed"), cmd.Flags().Changed("provider")
			return harness.Run(cmd.Context(), o)
		},
	}
	cmd.Flags().StringVar(&o.Suite, "suite", "fixtures/agent-harness/suite.yaml", "suite file")
	cmd.Flags().StringVar(&o.Workspace, "workspace", "workspace/agent-harness", "workspace root (one dir per case; wiped per run)")
	cmd.Flags().StringVar(&o.BudgetMode, "budget-mode", "cheap", "tier dial for swarm cases (cheap = the personas' declared haiku floor)")
	cmd.Flags().StringVar(&o.Model, "model", "haiku", "claude model alias for template cases")
	cmd.Flags().StringVar(&o.Only, "only", "", "run only cases whose name contains this substring")
	cmd.Flags().BoolVar(&o.Slow, "slow", false, "include cases marked slow")
	cmd.Flags().BoolVar(&o.KeepGoing, "keep-going", true, "run every case even after a failure")
	cmd.Flags().BoolVar(&o.Strict, "strict", false, "fail a case on adherence warnings too (forbidden phrases, length caps, bare JSON) — for persona tuning and release gates")
	cmd.Flags().StringVar(&o.Provider, "provider", string(runner.BackendClaudeCLI), "provider for every case: claude-cli, anthropic, openai, gemini, gemini-cli or local (default under --profile: the profile's)")
	cmd.Flags().StringVar(&o.Profile, "profile", "", "routing profile every swarm, bench, proof and hive case runs under, exported as HIVE_PROFILE: the lenses, the bench solo control and Queen run as its roles say, so a model or Ollama update that breaks one shows up; not with --lens-model, --queen-model or the reasoning flags (default: HIVE_PROFILE)")
	cmd.Flags().StringVar(&o.LensModel, "lens-model", "", "pin a model for every forager and the bench solo control (chb ask --model); a comma-separated list mixes models across the lenses, rotated by each bench item's seed and repetition, and the solo control runs on the first; default: the tier system")
	cmd.Flags().StringVar(&o.QueenModel, "queen-model", "", "pin a model for Queen (chb ask --synthesizer-model); default: the tier system")
	cmd.Flags().StringVar(&o.LensReasoning, "lens-reasoning", "", "reasoning level for every forager and the bench solo control (chb ask --forager-reasoning): none, low, medium or high; default: none sent, so the server's default applies")
	cmd.Flags().StringVar(&o.QueenReasoning, "queen-reasoning", "", "reasoning level for Queen, scope and the evaluator (chb ask --synthesizer-reasoning): none, low, medium or high; default: none sent")
	cmd.Flags().Int64Var(&o.Seed, "seed", 0, "sampling seed for swarm and bench runs (implies temperature 0); a bench item's repetition r runs with seed+r-1")
	cmd.Flags().IntVar(&o.Reps, "reps", 1, "runs of each bench item on each arm")
	cmd.Flags().StringVar(&o.Config, "config", "", "name of this configuration in bench results, which chb bench decide groups by (default: provider/lens/queen/budget-mode, then the reasoning levels when either is set, or under --profile the profile, then the persona profile and sections when either is given)")
	cmd.Flags().StringVar(&o.PersonaProfile, "persona-profile", "", "persona profile for swarm and bench runs (chb ask --persona-profile full|lean); default: ask's, full")
	cmd.Flags().StringVar(&o.PersonaSections, "persona-sections", "", "persona body sections for swarm and bench runs (chb ask --persona-sections), for the lean-versus-full ablation")
	cmd.Flags().BoolVar(&o.DirectVoice, "direct-voice", false, "pass --direct-voice to every swarm and bench chb ask: the model's own answer as one more vote; the solo control is unchanged")
	cmd.Flags().BoolVar(&o.ContextSplit, "context-split", false, "pass --context-split to every swarm and bench chb ask: each lens reads its own part of the context")
	return cmd
}
