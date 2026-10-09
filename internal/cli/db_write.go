package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/gate"
	"github.com/spf13/cobra"
)

// ── db-write ─────────────────────────────────────────────────

func newDBWriteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db-write <command> <json>",
		Short: "Write CDE-encoded data to the database",
	}

	cmd.AddCommand(
		newWriteDimensionCmd(),
		newWriteFindingCmd(),
		newWriteUpdateFindingCmd(),
		newWriteSourceCmd(),
		newWriteGapCmd(),
		newWriteResolveGapCmd(),
		newWriteConflictCmd(),
		newWriteConflictWinnerCmd(),
		newWriteResolveConflictCmd(),
		newWriteFollowupCmd(),
		newWriteAgentRunCmd(),
		newWriteCompleteRunCmd(),
		newWriteFailRunCmd(),
		newWriteEvaluationCmd(),
		newWriteGateWaveCmd(),
		newWriteSignalCmd(),
		newWriteCascadeRevertCmd(),
		newWritePromoteFindingCmd(),
	)

	return cmd
}

// dimensionKeys are the keys db-write dimension reads.
var dimensionKeys = writeKeys{
	accepted: []string{"name", "description", "values_json"},
	text:     []string{"name", "description"},
}

func newWriteDimensionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dimension <json>",
		Short: "Register a CDE dimension",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var d struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				ValuesJSON  json.RawMessage `json:"values_json"`
			}
			if err := decodeDBWritePayload("dimension", args[0], &d, dimensionKeys); err != nil {
				return err
			}
			// values_json may be a JSON array or a string holding one, as
			// depends_on_ids may on every surface.
			values := string(d.ValuesJSON)
			var asString string
			if err := json.Unmarshal(d.ValuesJSON, &asString); err == nil {
				values = asString
			}
			if err := store.Dimensions().AddDimension(d.Name, d.Description, values); err != nil {
				return err
			}
			fmt.Println("dimension written")
			return nil
		},
	}
}

func newWriteFindingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "finding <json>",
		Short: "Write a CDE-encoded finding with MSS label",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dbWriteFinding(args[0])
		},
	}
}

// dbWriteFinding writes a db-write finding payload as a new finding.
func dbWriteFinding(payload string) error {
	id, _, err := writeRecord("finding", payload)
	if err != nil {
		return err
	}
	fmt.Printf("Finding id=%d\n", id)
	return nil
}

func newWriteUpdateFindingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update_finding <json>",
		Short: "Update an existing finding with MSS validation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dbWriteUpdateFinding(args[0])
		},
	}
}

// dbWriteUpdateFinding applies a db-write update_finding payload.
func dbWriteUpdateFinding(payload string) error {
	findingID, updates, err := parseUpdateFindingPayload(payload)
	if err != nil {
		return err
	}
	if err := store.Findings().UpdateFinding(findingID, updates); err != nil {
		return err
	}
	fmt.Printf("Updated finding id=%d\n", findingID)
	return nil
}

// updateFindingKeys are the keys db-write update_finding reads.
var updateFindingKeys = writeKeys{
	accepted: []string{"finding_id", "mss_label", "finding", "evidence", "source_urls", "depends_on_ids"},
	integer:  []string{"finding_id"},
	text:     []string{"mss_label", "finding", "evidence"},
}

// parseUpdateFindingPayload reads an update_finding payload: the finding's
// id and the fields to change.
func parseUpdateFindingPayload(payload string) (int64, map[string]any, error) {
	var raw map[string]any
	if err := decodeDBWritePayload("update_finding", payload, &raw, updateFindingKeys); err != nil {
		return 0, nil, err
	}
	findingID, err := idFromMap(raw, "finding_id")
	if err != nil {
		return 0, nil, err
	}
	delete(raw, "finding_id")
	updates, err := updateFindingPayloadFields(raw)
	return findingID, updates, err
}

// updateFindingPayloadFields is the fields an update_finding payload sets.
func updateFindingPayloadFields(raw map[string]any) (map[string]any, error) {
	updates := presentWriteKeys(raw, "mss_label", "finding", "evidence", "source_urls")
	v, ok := raw["depends_on_ids"]
	if !ok {
		return updates, nil
	}
	// The same normaliser as db-write finding, so a JSON-string list is
	// stored as the list it holds, not double-encoded.
	deps, err := updateFindingDependsOn(v)
	if err != nil {
		return nil, err
	}
	updates["depends_on_ids"] = deps
	return updates, nil
}

// presentWriteKeys copies the named keys a db-write payload holds into a new
// map.
func presentWriteKeys(raw map[string]any, keys ...string) map[string]any {
	out := make(map[string]any)
	for _, k := range keys {
		if v, ok := raw[k]; ok {
			out[k] = v
		}
	}
	return out
}

// updateFindingDependsOn is the depends_on_ids an update stores: the
// normalised list, or NULL for an empty one.
func updateFindingDependsOn(v any) (any, error) {
	deps, err := db.NormalizeDependsOnIDs(v)
	if err != nil || deps == "" {
		return nil, err
	}
	return deps, nil
}

func newWriteSourceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "source <json>",
		Short: "Register a source URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, err := writeRecord("source", args[0])
			return err
		},
	}
}

func newWriteGapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gap <json>",
		Short: "Record a knowledge gap",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _, err := writeRecord("gap", args[0])
			return err
		},
	}
}

func newWriteResolveGapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resolve_gap <json>",
		Short: "Mark a gap answered by a finding",
		Long: `Records that a finding answers a gap: {"gap_id":N,"wave":W,"agent":"name","finding_id":F}.

Refuses an unknown gap, a gap already resolved, and a finding that does not
exist. This is the only way a gap closes, and the hive's termination check
waits on it: a project terminates only once no critical or important gap is
open.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return printRecord("resolve_gap", args[0])
		},
	}
}

// conflictKeys are the keys db-write conflict reads.
var conflictKeys = writeKeys{
	accepted: []string{"wave", "finding_a_id", "finding_b_id", "description"},
	integer:  []string{"wave", "finding_a_id", "finding_b_id"},
	text:     []string{"description"},
}

func newWriteConflictCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "conflict <json>",
		Short: "Record a finding conflict",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw map[string]any
			if err := decodeDBWritePayload("conflict", args[0], &raw, conflictKeys); err != nil {
				return err
			}
			a, b := toInt64CLI(raw["finding_a_id"]), toInt64CLI(raw["finding_b_id"])
			if !positiveWriteIDs(a, b) {
				return fmt.Errorf("conflict needs positive finding_a_id and finding_b_id")
			}
			return store.Conflicts().AddConflict(intFromMap(raw, "wave"), a, b, stringFromMap(raw, "description"))
		},
	}
}

// conflictWinnerKeys are the keys db-write conflict_winner reads.
var conflictWinnerKeys = writeKeys{
	accepted: []string{"conflict_id", "winner_finding_id"},
	integer:  []string{"conflict_id", "winner_finding_id"},
}

// newWriteConflictWinnerCmd adjudicates a conflict. Naming a winner while
// leaving the resolution NULL is what arms the alarm pheromone: the next
// `chb hive next` sees the conflict, emits an alarm signal, and the cascade
// reverts the loser. The adjudication also writes one refuted outcome on the
// loser with source downstream_run (calibration.Adjudicate), which runs no
// cascade of its own.
func newWriteConflictWinnerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "conflict_winner <json>",
		Short: "Adjudicate a conflict — names the surviving finding, arming the alarm cascade, and records the loser refuted",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw map[string]any
			if err := decodeDBWritePayload("conflict_winner", args[0], &raw, conflictWinnerKeys); err != nil {
				return err
			}
			conflictID, winnerID := toInt64CLI(raw["conflict_id"]), toInt64CLI(raw["winner_finding_id"])
			if !positiveWriteIDs(conflictID, winnerID) {
				return fmt.Errorf("conflict_winner needs positive conflict_id and winner_finding_id")
			}
			adj, err := calibration.Adjudicate(store, conflictID, winnerID)
			if err != nil {
				return err
			}
			fmt.Printf("Conflict %d adjudicated: finding %d survives\n", conflictID, winnerID)
			fmt.Printf("outcome #%d: finding %d refuted (downstream_run); no cascade (the adjudication arms it)\n", adj.OutcomeID, adj.LoserID)
			return nil
		},
	}
}

func newWriteResolveConflictCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resolve_conflict <json>",
		Short: "Close a conflict, recording how it was settled",
		Long: `Records how a conflict was settled: {"conflict_id":N,"wave":W,"resolution":"..."}.

Refuses an unknown conflict, a conflict already resolved, and an empty
resolution. Run it after conflict_winner and after cascade_revert of the losing
finding: a resolved conflict does not arm the alarm cascade. The hive's
termination check waits on it: a project terminates only once no conflict is
unresolved.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return printRecord("resolve_conflict", args[0])
		},
	}
}

// followupKeys are the keys db-write followup reads, and a FOLLOWUP
// marker's that chb ingest holds to their types.
var followupKeys = writeKeys{
	accepted: []string{"wave", "agent", "question", "priority", "d1", "d2", "d3", "d4"},
	integer:  []string{"wave", "d1", "d2", "d3", "d4"},
	text:     []string{"agent", "question", "priority"},
}

func newWriteFollowupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "followup <json>",
		Short: "Record a follow-up question",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw map[string]any
			if err := decodeDBWritePayload("followup", args[0], &raw, followupKeys); err != nil {
				return err
			}
			return store.Followups().AddFollowup(
				intFromMap(raw, "wave"), stringFromMap(raw, "agent"),
				stringFromMap(raw, "question"), stringFromMapDefault(raw, "priority", "important"),
				intPtrFromMap(raw, "d1"), intPtrFromMap(raw, "d2"),
				intPtrFromMap(raw, "d3"), intPtrFromMap(raw, "d4"),
			)
		},
	}
}

// agentRunKeys are the keys db-write agent_run reads.
var agentRunKeys = writeKeys{
	accepted: []string{"wave", "agent_name", "agent_type", "model", "prompt_summary", "target_d1", "target_d2", "target_d3", "target_d4"},
	integer:  []string{"wave", "target_d1", "target_d2", "target_d3", "target_d4"},
	text:     []string{"agent_name", "agent_type", "model", "prompt_summary"},
}

func newWriteAgentRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agent_run <json>",
		Short: "Record the start of an agent run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw map[string]any
			if err := decodeDBWritePayload("agent_run", args[0], &raw, agentRunKeys); err != nil {
				return err
			}
			id, err := store.AgentRuns().AddAgentRun(
				intFromMap(raw, "wave"),
				stringFromMap(raw, "agent_name"),
				stringFromMap(raw, "agent_type"),
				stringFromMap(raw, "model"),
				stringFromMap(raw, "prompt_summary"),
				intPtrFromMap(raw, "target_d1"), intPtrFromMap(raw, "target_d2"),
				intPtrFromMap(raw, "target_d3"), intPtrFromMap(raw, "target_d4"),
			)
			if err != nil {
				return err
			}
			fmt.Printf("Run id=%d\n", id)
			return nil
		},
	}
}

// completeRunKeys are the keys db-write complete_run reads.
var completeRunKeys = writeKeys{
	accepted: []string{"run_id", "summary", "tool_uses", "duration_ms", "total_tokens"},
	integer:  []string{"run_id", "tool_uses", "duration_ms", "total_tokens"},
	text:     []string{"summary"},
}

func newWriteCompleteRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "complete_run <json>",
		Short: "Mark an agent run as completed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw map[string]any
			if err := decodeDBWritePayload("complete_run", args[0], &raw, completeRunKeys); err != nil {
				return err
			}
			runID, err := idFromMap(raw, "run_id")
			if err != nil {
				return err
			}
			summary := stringFromMap(raw, "summary")
			return store.AgentRuns().CompleteAgentRun(runID, summary,
				intPtrFromMap(raw, "tool_uses"),
				intPtrFromMap(raw, "duration_ms"),
				intPtrFromMap(raw, "total_tokens"),
			)
		},
	}
}

// failRunKeys are the keys db-write fail_run reads.
var failRunKeys = writeKeys{
	accepted: []string{"run_id", "error_summary"},
	integer:  []string{"run_id"},
	text:     []string{"error_summary"},
}

func newWriteFailRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fail_run <json>",
		Short: "Mark an agent run as failed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw map[string]any
			if err := decodeDBWritePayload("fail_run", args[0], &raw, failRunKeys); err != nil {
				return err
			}
			runID, err := idFromMap(raw, "run_id")
			if err != nil {
				return err
			}
			return store.AgentRuns().FailAgentRun(runID, stringFromMap(raw, "error_summary"))
		},
	}
}

// evaluationKeys are the keys db-write evaluation reads.
var evaluationKeys = writeKeys{
	accepted: []string{"wave", "coverage", "depth", "sources", "actionability", "mss_integrity", "verdict", "laundering", "untraceable", "redundant", "notes"},
	integer:  []string{"wave", "coverage", "depth", "sources", "actionability", "mss_integrity", "laundering", "untraceable", "redundant"},
	text:     []string{"verdict", "notes"},
}

func newWriteEvaluationCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "evaluation <json>",
		Short: "Record a wave evaluation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw map[string]any
			if err := decodeDBWritePayload("evaluation", args[0], &raw, evaluationKeys); err != nil {
				return err
			}
			notes := stringPtrFromMap(raw, "notes")
			return store.Evaluations().AddEvaluation(
				intFromMap(raw, "wave"),
				intFromMap(raw, "coverage"), intFromMap(raw, "depth"),
				intFromMap(raw, "sources"), intFromMap(raw, "actionability"),
				intFromMap(raw, "mss_integrity"), stringFromMap(raw, "verdict"),
				intFromMapDefault(raw, "laundering", 0),
				intFromMapDefault(raw, "untraceable", 0),
				intFromMapDefault(raw, "redundant", 0),
				notes,
			)
		},
	}
}

func newWriteGateWaveCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "gate_wave <wave>",
		Short: "Open the wave gate for synthesis",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dbWriteGateWave(args[0], force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "force gate open despite failures")
	return cmd
}

// dbWriteGateWave runs the gate pipeline for the wave arg names and reports
// whether the gate opened.
func dbWriteGateWave(arg string, force bool) error {
	wave, err := parseGateWaveArg(arg)
	if err != nil {
		return err
	}
	// One gate, one policy: gate_wave runs the pipeline `chb guard` runs,
	// using the latest recorded evaluation; --force waives warnings, never
	// an error.
	result, err := gate.RunGatePipeline(store, wave, nil, false, force, false)
	if err != nil {
		return err
	}
	printGateWaveIssues(result)
	if !result.Opened {
		return fmt.Errorf("wave %d gate BLOCKED", wave)
	}
	// The wave tick is recorded by RunGatePipeline when it opens the gate,
	// for guard and gate_wave alike.
	fmt.Printf("Wave %d gate OPENED.\n", wave)
	return nil
}

// parseGateWaveArg reads gate_wave's argument, which must be a positive wave
// number.
func parseGateWaveArg(arg string) (int, error) {
	wave, err := strconv.Atoi(arg)
	if err != nil || wave < 1 {
		return 0, fmt.Errorf("gate_wave expects a positive wave number, got %q", arg)
	}
	return wave, nil
}

// printGateWaveIssues lists the pipeline's errors, then its warnings.
func printGateWaveIssues(result *gate.GateResult) {
	for _, e := range result.Errors {
		fmt.Printf("  - %s\n", e)
	}
	for _, w := range result.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

// signalKeys are the keys db-write signal reads; payload is any JSON value.
var signalKeys = writeKeys{
	accepted: []string{"signal_type", "source_type", "source_id", "payload", "wave", "target_d1", "target_d2", "target_d3", "target_d4"},
	integer:  []string{"source_id", "wave", "target_d1", "target_d2", "target_d3", "target_d4"},
	text:     []string{"signal_type", "source_type"},
}

func newWriteSignalCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "signal <json>",
		Short: "Emit a bee colony signal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var raw map[string]any
			if err := decodeDBWritePayload("signal", args[0], &raw, signalKeys); err != nil {
				return err
			}
			var payload any
			if p, ok := raw["payload"]; ok {
				payload = p
			}
			id, err := store.Signals().EmitSignal(
				stringFromMap(raw, "signal_type"),
				stringPtrFromMap(raw, "source_type"),
				int64PtrFromMap(raw, "source_id"),
				intPtrFromMap(raw, "target_d1"), intPtrFromMap(raw, "target_d2"),
				intPtrFromMap(raw, "target_d3"), intPtrFromMap(raw, "target_d4"),
				payload, intPtrFromMap(raw, "wave"),
			)
			if err != nil {
				return err
			}
			fmt.Printf("Signal id=%d\n", id)
			return nil
		},
	}
}

func newWriteCascadeRevertCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cascade_revert <finding_id>",
		Short: "Alarm pheromone cascade revert",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fid, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || fid <= 0 {
				return fmt.Errorf("cascade_revert expects a positive integer finding_id, got %q", args[0])
			}
			reverted, err := store.CascadeRevert(fid)
			if err != nil {
				return err
			}
			fmt.Printf("Cascade reverted: %v\n", reverted)
			return nil
		},
	}
}

func newWritePromoteFindingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "promote_finding <json>",
		Short: "Relabel an assumption a guarantee that rests on the findings named in depends_on_ids",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dbWritePromoteFinding(args[0])
		},
	}
}

// promoteFindingKeys are the keys db-write promote_finding reads.
var promoteFindingKeys = writeKeys{
	accepted: []string{"finding_id", "depends_on_ids"},
	integer:  []string{"finding_id"},
}

// dbWritePromoteFinding relabels the finding a promote_finding payload names
// a guarantee resting on the findings it lists.
func dbWritePromoteFinding(payload string) error {
	var raw map[string]any
	if err := decodeDBWritePayload("promote_finding", payload, &raw, promoteFindingKeys); err != nil {
		return err
	}
	findingID, err := idFromMap(raw, "finding_id")
	if err != nil {
		return err
	}
	deps, err := promoteFindingDeps(raw)
	if err != nil {
		return err
	}
	return store.Findings().PromoteFinding(findingID, deps)
}

// promoteFindingDeps is the non-empty list of findings a promotion rests
// on, normalised. The payload's depends_on_ids may be a JSON array or a
// JSON string holding one, as on every other surface.
func promoteFindingDeps(raw map[string]any) ([]int64, error) {
	depsJSON, err := db.NormalizeDependsOnIDs(raw["depends_on_ids"])
	if err != nil {
		return nil, err
	}
	if depsJSON == "" {
		return nil, fmt.Errorf("promote_finding requires non-empty \"depends_on_ids\"")
	}
	var deps []int64
	_ = json.Unmarshal([]byte(depsJSON), &deps) // the normaliser's own JSON array
	return deps, nil
}

// ── db-write payload helpers ─────────────────────────────────

// writeRecord writes a db-write payload as a record of kind through the
// store's one write path, which chb_db_write also takes (db.Store.WriteRecord),
// and returns the row's id and the line that reports it.
func writeRecord(kind, payload string) (int64, string, error) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(payload), &fields); err != nil {
		return 0, "", fmt.Errorf("parse json: %w", err)
	}
	return store.WriteRecord(kind, fields)
}

// printRecord writes a db-write payload as a record of kind and prints the
// line that reports it.
func printRecord(kind, payload string) error {
	_, line, err := writeRecord(kind, payload)
	if err != nil {
		return err
	}
	fmt.Println(line)
	return nil
}

// checkWriteKeys refuses a db-write payload that holds a key its kind does
// not read, naming the key and the kind's keys, so {"agent_id":1} cannot
// write a finding with no agent and report success.
func checkWriteKeys(kind, payload string, accepted ...string) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return fmt.Errorf("parse json: %w", err)
	}
	unknown := unknownWriteKeys(raw, accepted)
	if len(unknown) == 0 {
		return nil
	}
	noun := "key"
	if len(unknown) > 1 {
		noun = "keys"
	}
	return fmt.Errorf("db-write %s: unknown %s %s; accepted keys: %s",
		kind, noun, strings.Join(unknown, ", "), strings.Join(accepted, ", "))
}

// unknownWriteKeys is the payload's keys outside accepted, quoted and
// sorted.
func unknownWriteKeys(raw map[string]json.RawMessage, accepted []string) []string {
	var unknown []string
	for k := range raw {
		if !slices.Contains(accepted, k) {
			unknown = append(unknown, strconv.Quote(k))
		}
	}
	slices.Sort(unknown)
	return unknown
}

// writeKeys are the keys a db-write kind reads, in the order its refusal
// lists them, and which of them hold an integer and which text. Any other
// holds a list, an object or either of two shapes, and is checked where it
// is read.
type writeKeys struct {
	accepted, integer, text []string
}

// decodeDBWritePayload refuses a db-write payload holding a key its kind
// does not read (checkWriteKeys), or an integer or text key holding
// anything else (checkWriteTypes), then decodes it into v.
func decodeDBWritePayload(kind, payload string, v any, keys writeKeys) error {
	if err := checkWriteKeys(kind, payload, keys.accepted...); err != nil {
		return err
	}
	if err := checkWriteTypes(payload, keys); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(payload), v); err != nil {
		return fmt.Errorf("parse json: %w", err)
	}
	return nil
}

// checkWriteTypes refuses a db-write payload whose integer keys hold no
// whole number or whose text keys hold no string, with the error
// db.Store.WriteRecord gives the same fields (db.CheckFieldTypes).
func checkWriteTypes(payload string, keys writeKeys) error {
	var raw map[string]any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return fmt.Errorf("parse json: %w", err)
	}
	return db.CheckFieldTypes(raw, keys.integer, keys.text)
}

// positiveWriteIDs reports whether every id a db-write payload gave is above
// zero.
func positiveWriteIDs(ids ...int64) bool {
	for _, id := range ids {
		if id <= 0 {
			return false
		}
	}
	return true
}

// idFromMap reads a required row id from a db-write payload. JSON numbers
// arrive as float64; a missing id, a string or a fraction is refused by name.
// complete_run, fail_run and update_finding asserted the value to float64
// unchecked and panicked on a payload without it.
func idFromMap(m map[string]any, key string) (int64, error) {
	v := m[key]
	if v == nil {
		return 0, fmt.Errorf("%s is required", key)
	}
	f, ok := v.(float64)
	if !ok || !isWholeRowID(f) {
		return 0, fmt.Errorf("%s must be a positive integer, got %v", key, v)
	}
	return int64(f), nil
}

// isWholeRowID reports whether f is a whole number a row id can hold: from
// 1 to below 2^63.
func isWholeRowID(f float64) bool {
	return f == math.Trunc(f) && f >= 1 && f < 1<<63
}
