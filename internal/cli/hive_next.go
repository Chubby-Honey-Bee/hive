package cli

import (
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/hive"
	"github.com/spf13/cobra"
)

func newHiveNextCmd() *cobra.Command {
	var apply bool
	var maxIterations int
	cmd := &cobra.Command{
		Use:   "next --project <name>",
		Short: "Scan state, evaluate signals, generate dispatch plan",
		Long: `Scans the project state, evaluates the bee-colony signals, and prints the
plan: which agents to dispatch, which directions to damp, whether to gate.

The plan is advisory by default — an agent reads it and acts. --apply
additionally executes the plan's four deterministic kinds of action.
` + "`fix_mss`" + `: the settle pass demotes each guarantee whose dependencies
reach an unknown to an assumption (mss_fixed lists them). It repairs
laundering only.
` + "`cascade_revert`" + `: what rests on a conflict's contradicted finding is
reverted to unknown, and the conflict is closed (cascades lists each).
` + "`cap_finding`" + `: a finding the quorum signal says several agents converged
on is capped. The cap is recorded once, so quorum does not act on the
finding again, and the finding stays an assumption: agreement is not a
derivation, and only premises named in depends_on_ids make a guarantee.
` + "`adjust_params`" + `: the batch size or model tier the signals call for is
clamped and stored. Under --apply the output also lists open_actions: the
plan less those four kinds, what is left for an agent. research_actions
are its dispatch_agent actions, without the model field nothing reads, and
gate_requested and gate_wave say whether it holds a run_gate and for which
wave.

A scan records everything in one transaction, --apply's writes included:
a scan that errors changes nothing and counts no iteration.

With --max-iterations N, a hive that is not terminal and has run N passes
is not scanned: the output has phase capped and nothing is recorded.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, _ := cmd.Flags().GetString("project")
			if err := checkHiveIterationFlag(cmd, "max-iterations", maxIterations); err != nil {
				return err
			}
			return runHiveNext(hiveNextPass{project: project, apply: apply, maxIterations: maxIterations})
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false,
		"execute the plan's fix_mss (settle demotes laundering guarantees), cascade_revert (revert what rests on a conflict's loser, then close the conflict), cap_finding (quorum caps a converged finding, which stays an assumption) and adjust_params actions, in the scan's transaction, and list open_actions, research_actions, gate_requested and gate_wave")
	cmd.Flags().IntVar(&maxIterations, "max-iterations", 0,
		"the hive's iteration cap: a hive that is not terminal and has run this many passes is not scanned, and the output has phase capped")
	return cmd
}

// addNothingOpen sets --apply's keys for a scan that planned nothing: a
// terminal or capped one.
func addNothingOpen(out map[string]any) {
	out["open_actions"] = []any{}
	out["research_actions"] = []any{}
	out["gate_requested"] = false
	out["gate_wave"] = 0
}

// hiveNextPass is one chb hive next: the project, the flags, and the
// database the scan uses once resolved.
type hiveNextPass struct {
	project       string
	apply         bool
	maxIterations int
	dbFile        string
}

// runHiveNext scans the project, records a terminal scan or plans the next
// pass, and prints the outcome as one JSON object.
func runHiveNext(p hiveNextPass) error {
	r, err := hiveLoopStore(p.project, "next")
	if err != nil {
		return err
	}
	p.dbFile = r.Path

	state, err := hive.ScanState(store, p.project)
	if err != nil {
		return err
	}

	if isTerminal, reason := hive.CheckTermination(state); isTerminal {
		return p.recordTerminal(state, reason)
	}
	return p.planPass(state)
}

// recordTerminal records a terminal scan and prints it.
func (p hiveNextPass) recordTerminal(state *hive.State, reason string) error {
	rec, err := hive.RecordScan(store, hive.Scan{
		Project: p.project, State: state, Terminal: true, TerminalReason: reason,
	})
	if err != nil {
		return err
	}
	return p.printIdle(map[string]any{
		"phase":         "terminal",
		"reason":        reason,
		"iteration":     rec.Iteration,
		"actions":       []any{},
		"signals_fired": []any{},
		"summary": map[string]any{
			"total_findings": state.TotalFindings,
			"labels":         state.Labels,
			"mss_integrity":  state.MSSIntegrity,
			"iteration":      state.Hive.Iteration,
		},
		"db": p.dbFile,
	})
}

// printIdle prints the output of a scan that planned nothing, a terminal or
// a capped one, with --apply's keys empty under --apply.
func (p hiveNextPass) printIdle(out map[string]any) error {
	if p.apply {
		addNothingOpen(out)
	}
	return printJSON(out)
}

// planPass evaluates the signals, plans the pass and records the scan, then
// prints the plan, or the cap when the hive is at it.
func (p hiveNextPass) planPass(state *hive.State) error {
	// Evaluate signals
	newSignals := hive.EvaluateSignals(store, state)

	// Generate plan, before recording its signals or advancing the
	// iteration, so a plan it refuses leaves neither behind.
	plan, err := hive.GeneratePlan(state, newSignals)
	if err != nil {
		return err
	}

	// The metrics, the signals (recorded already acted on: this plan
	// is the one acting on them), the research state as the pass
	// begins, what --apply runs, and the iteration bump with the
	// watermark of the pending signals this plan read are one
	// transaction.
	rec, err := hive.RecordScan(store, hive.Scan{
		Project: p.project, State: state, Fired: newSignals,
		Apply: p.apply, Plan: plan, MaxIterations: p.maxIterations,
	})
	if err != nil {
		return err
	}
	if rec.Capped {
		return p.printIdle(p.cappedOutput(rec))
	}
	return printJSON(p.dispatchOutput(state, newSignals, plan, rec))
}

// cappedOutput is the output of a scan that found the hive at its cap.
func (p hiveNextPass) cappedOutput(rec hive.ScanResult) map[string]any {
	return map[string]any{
		"phase":          "capped",
		"reason":         fmt.Sprintf("iteration %d reached max_iterations %d", rec.Iteration, p.maxIterations),
		"iteration":      rec.Iteration,
		"max_iterations": p.maxIterations,
		"actions":        []any{},
		"signals_fired":  []any{},
		"db":             p.dbFile,
	}
}

// dispatchOutput is the output of a scan that planned a pass.
func (p hiveNextPass) dispatchOutput(state *hive.State, newSignals []hive.Signal, plan []hive.Action, rec hive.ScanResult) map[string]any {
	out := map[string]any{
		"phase":         "dispatching",
		"iteration":     rec.Iteration,
		"signals_fired": hiveFiredSignals(newSignals, rec.SignalIDs),
		"actions":       plan,
		"summary": map[string]any{
			"total_findings":        state.TotalFindings,
			"labels":                state.Labels,
			"mss_integrity":         state.MSSIntegrity,
			"unresolved_gaps":       len(state.UnresolvedGaps),
			"unresolved_conflicts":  len(state.UnresolvedConflicts),
			"batch_size":            state.Hive.BatchSize,
			"model_tier":            state.Hive.ModelTier,
			"convergence_threshold": state.Hive.ConvergenceThreshold,
		},
		"db": p.dbFile,
	}
	if p.maxIterations > 0 {
		out["max_iterations"] = p.maxIterations
	}
	// Only present when --apply ran: the default plan output shape is
	// a stable contract (fixtures/cli-behavior.jsonl replays it).
	if p.apply {
		addHiveAppliedPlan(out, plan, rec)
	}
	return out
}

// hiveFiredSignals is each fired signal's type and the id it was recorded
// under.
func hiveFiredSignals(fired []hive.Signal, ids []int64) []map[string]any {
	summary := make([]map[string]any, len(fired))
	for i, s := range fired {
		summary[i] = map[string]any{"type": s.SignalType, "id": ids[i]}
	}
	return summary
}

// addHiveAppliedPlan sets --apply's keys: what it ran and what it left open.
func addHiveAppliedPlan(out map[string]any, plan []hive.Action, rec hive.ScanResult) {
	out["mss_fixed"] = rec.MSSFixed
	out["cascades"] = rec.Cascades
	out["caps"] = rec.Caps
	out["params_applied"] = rec.ParamsApplied
	o := openHivePlan(plan)
	out["open_actions"] = o.open
	out["research_actions"] = o.research
	out["gate_requested"] = o.gateRequested
	out["gate_wave"] = o.gateWave
}

// hiveOpenPlan is what --apply leaves of a plan for an agent: the actions
// it did not run, the research among them, and the gate it asks for.
type hiveOpenPlan struct {
	open, research []hive.Action
	gateRequested  bool
	gateWave       int
}

// openHivePlan is the plan less the four kinds of action --apply runs.
func openHivePlan(plan []hive.Action) hiveOpenPlan {
	o := hiveOpenPlan{open: []hive.Action{}, research: []hive.Action{}}
	for _, a := range plan {
		o.add(a)
	}
	return o
}

// add files one action of the plan: a dispatch_agent is research too,
// without the model field nothing reads, and a run_gate requests its wave's
// gate.
func (o *hiveOpenPlan) add(a hive.Action) {
	switch a.Type {
	case "fix_mss", "cascade_revert", "cap_finding", "adjust_params":
		return
	case "dispatch_agent":
		r := a
		r.Model = ""
		o.research = append(o.research, r)
	case "run_gate":
		o.gateRequested, o.gateWave = true, a.Wave
	}
	o.open = append(o.open, a)
}
