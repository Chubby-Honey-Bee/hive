package mcp

// The calibration tools: the outcomes ledger's MCP surface. In-process, on
// the server's store, through the same shape and validation as `chb
// outcome-record` (calibration.Input), and the same rows as `chb db-read
// calibration`.

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Chubby-Honey-Bee/hive/internal/calibration"
)

func outcomeRecordSpec() map[string]any {
	integer := func(desc string) map[string]any {
		return map[string]any{"type": "integer", "description": desc}
	}
	return map[string]any{
		"name":        "chb_outcome_record",
		"title":       "Record an outcome",
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false},
		"description": "Append one outcome to the ledger in the workspace database (hive.db) with source human: whether a finding, a lens verdict or a synthesis verdict turned out confirmed, refuted or partial. The same body as `chb outcome-record`: a finding outcome names finding_id and copies the finding's label and coordinates; a lens_verdict names lens and run_id; a synthesis_verdict names run_id. A key outside the shape is refused. A refuted finding runs the alarm cascade over what depends on it and keeps its own label. Returns the outcome id and the findings the cascade reverted.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"subject_kind":      map[string]any{"type": "string", "enum": []string{"finding", "lens_verdict", "synthesis_verdict"}, "description": "What the outcome resolves"},
				"finding_id":        integer("The finding, for subject_kind finding"),
				"lens":              map[string]any{"type": "string", "description": "The forager, for subject_kind lens_verdict"},
				"run_id":            integer("The workflow run, for lens_verdict and synthesis_verdict"),
				"belief_tick_id":    integer("The Time Wheel tick the belief was held at (optional)"),
				"d1":                integer("Coordinate (optional; a finding's are copied from it)"),
				"d2":                integer("Coordinate (optional)"),
				"d3":                integer("Coordinate (optional)"),
				"d4":                integer("Coordinate (optional)"),
				"resolution":        map[string]any{"type": "string", "enum": []string{"confirmed", "refuted", "partial"}, "description": "How the claim turned out"},
				"stated_confidence": integer("The confidence stated at belief time, 0 to 100 (optional; feeds the Brier score)"),
				"predicted_value":   map[string]any{"type": "number", "description": "The value predicted, for a numeric claim (optional)"},
				"actual_value":      map[string]any{"type": "number", "description": "The value observed (optional)"},
				"rationale":         map[string]any{"type": "string", "description": "Why it resolved this way (optional)"},
				"evidence_urls":     map[string]any{"type": "string", "description": "Where the evidence is (optional)"},
				"resolved_tick_id":  integer("The Time Wheel tick the resolution is anchored to (optional)"),
			},
			"required": []string{"subject_kind", "resolution"},
		},
	}
}

func (s *mcpServer) handleOutcomeRecord(req rpcRequest, args map[string]any) {
	if s.store == nil {
		s.writeToolResult(req.ID, "", "HIVE_DB_PATH is not set", true)
		return
	}
	inputs, err := decodeOutcomeInputs(args)
	if err != nil {
		s.writeOutcomeError(req.ID, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := calibration.Record(s.store, inputs[0].Outcome(calibration.SourceHuman))
	if err != nil {
		s.writeOutcomeError(req.ID, err)
		return
	}
	s.writeJSONToolResult(req.ID, outcomeRecordResult(rec))
}

// writeOutcomeError answers a chb_outcome_record call that failed. An
// outcome refused for what it says (calibration.ErrInvalid) is a tool error
// the caller can fix; any other failure is the store's, protocol error
// -32603, so the caller can tell the two apart.
func (s *mcpServer) writeOutcomeError(id json.RawMessage, err error) {
	if errors.Is(err, calibration.ErrInvalid) {
		s.writeToolResult(id, "", err.Error(), true)
		return
	}
	s.writeErr(id, -32603, "internal error: "+err.Error())
}

// decodeOutcomeInputs decodes a chb_outcome_record call's arguments as the
// ledger's input shape (calibration.DecodeInputs). Arguments outside the
// shape are the caller's to fix, so the error is calibration.ErrInvalid.
func decodeOutcomeInputs(args map[string]any) ([]calibration.Input, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	inputs, err := calibration.DecodeInputs(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", calibration.ErrInvalid, err)
	}
	return inputs, nil
}

// outcomeRecordResult is chb_outcome_record's result: the outcome's id and
// the findings the cascade reverted, none as an empty list.
func outcomeRecordResult(rec calibration.Recorded) map[string]any {
	reverted := rec.Reverted
	if reverted == nil {
		reverted = []int64{}
	}
	return map[string]any{
		"id":       rec.ID,
		"source":   calibration.SourceHuman,
		"reverted": reverted,
		"note":     fmt.Sprintf("outcome %d recorded; no finding label changed", rec.ID),
	}
}

func calibrationReadSpec() map[string]any {
	return map[string]any{
		"name":        "chb_calibration_read",
		"title":       "Read calibration scores",
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		"description": "List the calibration scores in the workspace database (hive.db): one row per predictor (a lens, an MSS label, the ∇ signal as convergence/nabla, the Queen as synthesizer/queen) and scope (the global scope '' or a d1=x / d1=x;d2=y prefix), with its counts, hit rate, Brier score, weight and whether it is calibrated (10 or more outcomes; an uncalibrated weight is treated as 1 everywhere). Read-only, the same rows as the CLI's calibration listing. The scores are correlational, not proof of skill.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":  map[string]any{"type": "string", "enum": []string{"lens", "label", "convergence", "synthesizer"}, "description": "One predictor kind (optional; every kind when absent)"},
				"scope": map[string]any{"type": "string", "description": "One scope: '' for the global scope, or a prefix such as d1=2 (optional; every scope when absent)"},
			},
		},
	}
}

func (s *mcpServer) handleCalibrationRead(req rpcRequest, args map[string]any) {
	if s.store == nil {
		s.writeToolResult(req.ID, "", "HIVE_DB_PATH is not set", true)
		return
	}
	kind, _ := args["kind"].(string)
	var scope *string
	if v, ok := args["scope"].(string); ok {
		scope = &v
	}
	rows, err := s.store.Calibration().ListScores(kind, scope)
	if err != nil {
		s.writeToolResult(req.ID, "", err.Error(), true)
		return
	}
	s.writeJSONToolResult(req.ID, map[string]any{"scores": calibration.Views(rows), "note": calibration.CorrelationalNote})
}
