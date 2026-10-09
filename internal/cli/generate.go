package cli

import (
	"fmt"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/spf13/cobra"
)

// newSwarmGenerateCmd produces the workflow YAML for a swarm run
// without dispatching it. Useful for previewing the prompt the foragers
// will see, or for committing the YAML so a CI run can re-execute the
// same swarm.
func newSwarmGenerateCmd() *cobra.Command {
	var o swarmGenerateOptions
	cmd := &cobra.Command{
		Use:   "generate <question>",
		Short: "Render the forager-swarm workflow YAML for a question without dispatching",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSwarmGenerate(cmd, &o, strings.Join(args, " "))
		},
	}
	cmd.Flags().StringVar(&o.outPath, "out", "", "workflow YAML output path (default: stdout)")
	// `generate` exists to preview what `ask` will dispatch, so it defaults
	// to the same preset.
	cmd.Flags().StringSliceVar(&o.foragerList, "foragers", []string{"balanced"}, "forager list ('balanced' = the 9 foragers chb ask dispatches, 'minimal' = 7 axis-owners, 'default' = default:true foragers, 'all' = every deliberation-eligible forager, or comma-separated names)")
	cmd.Flags().StringVar(&o.model, "model", "", "pin a literal model for each forager agent; a comma-separated list gives the lenses, in name order, the models in turn, and the follow-up lens the first (default: empty → tier system kicks in)")
	cmd.Flags().StringVar(&o.foragerTier, "forager-tier", "", "tier role for each forager (default: 'synthesist' when --model is unset)")
	cmd.Flags().StringVar(&o.repairModel, "synthesizer-model", "", "pin a literal model for Queen (default: empty → tier system kicks in)")
	cmd.Flags().StringVar(&o.synthTier, "synthesizer-tier", "", "tier role for Queen (default: 'planner' when --synthesizer-model is unset)")
	cmd.Flags().StringVar(&o.name, "name", "", "workflow name (default: swarm-of-foragers-<date>)")
	cmd.Flags().StringVar(&o.lensTools, "lens-tools", "none", lensToolsUsage)
	cmd.Flags().StringVar(&o.profile, "persona-profile", foragers.ProfileFull, personaProfileUsage)
	cmd.Flags().StringVar(&o.sections, "persona-sections", "", personaSectionsUsage)
	cmd.Flags().StringVar(&o.foragerReasoning, "forager-reasoning", "", foragerReasoningUsage)
	cmd.Flags().StringVar(&o.synthReasoning, "synthesizer-reasoning", "", synthReasoningUsage)
	return cmd
}

// swarmGenerateOptions holds chb generate's flag values.
type swarmGenerateOptions struct {
	outPath          string
	foragerList      []string
	model            string
	repairModel      string
	foragerTier      string
	synthTier        string
	name             string
	lensTools        string
	profile          string
	sections         string
	foragerReasoning string
	synthReasoning   string
}

// runSwarmGenerate renders the swarm workflow for the question and prints
// it, or writes it to --out.
func runSwarmGenerate(cmd *cobra.Command, o *swarmGenerateOptions, question string) error {
	swarm, synth, err := selectSwarmForagers(o.foragerList)
	if err != nil {
		return err
	}
	secs, err := personaSectionsFlag(o.sections)
	if err != nil {
		return err
	}
	yamlText, err := foragers.GenerateWorkflow(swarm, o.workflowOptions(cmd, synth, secs))
	if err != nil {
		return err
	}
	return o.emit(cmd, yamlText, len(swarm), question)
}

// workflowOptions are the generator options generate's flags set.
func (o *swarmGenerateOptions) workflowOptions(cmd *cobra.Command, synth foragers.Forager, secs []int) foragers.WorkflowOptions {
	return foragers.WorkflowOptions{
		Name:             o.name,
		Model:            o.model,
		ForagerTier:      o.foragerTier,
		SynthesizerModel: o.repairModel,
		SynthesizerTier:  o.synthTier,
		Synthesizer:      synth,
		LensTools:        o.lensTools,
		PersonaProfile:   o.profile,
		PersonaSections:  secs,
		Warn:             warnTo(cmd.ErrOrStderr()),

		ForagerReasoning:     o.foragerReasoning,
		SynthesizerReasoning: o.synthReasoning,
	}
}

// emit prints the workflow, or writes it to --out and says so on stderr.
func (o *swarmGenerateOptions) emit(cmd *cobra.Command, yamlText string, foragerCount int, question string) error {
	if o.outPath == "" {
		fmt.Fprint(cmd.OutOrStdout(), yamlText)
		return nil
	}
	if err := writeGeneratedSwarmYAML(o.outPath, yamlText); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"Wrote %d-forager swarm → %s for question: %s\n",
		foragerCount, o.outPath, truncateForLog(question, 60),
	)
	return nil
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return clip(s, n) + "…"
}
