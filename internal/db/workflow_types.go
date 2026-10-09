package db

import (
	"database/sql"
	"encoding/json"
)

// Workflow domain types. The engine reads typed fields rather than
// map[string]any rows, so type assertions concentrate at the JSON edges
// (definition_yaml, inputs_json, state_json, outputs_json) instead of
// recurring at every query.
//
// The json tags give a row written as JSON its columns' snake_case names.

// NullString is a sql.NullString that marshals as a JSON string or null.
// The embedded type keeps Scan, Value, .String and .Valid working unchanged;
// only the JSON shape differs — a bare sql.NullString marshals as
// {"String": "...", "Valid": true}, which leaked the driver type onto the wire.
type NullString struct{ sql.NullString }

// MarshalJSON renders the value, or null when it is not set.
func (n NullString) MarshalJSON() ([]byte, error) {
	if !n.Valid {
		return []byte("null"), nil
	}
	return json.Marshal(n.String)
}

// WorkflowRun mirrors a row from the workflow_runs table.
type WorkflowRun struct {
	ID             int64      `json:"id"`
	Name           string     `json:"workflow_name"`
	Version        int        `json:"workflow_version"`
	DefinitionYAML string     `json:"definition_yaml"`
	InputsJSON     string     `json:"inputs_json"`
	StateJSON      string     `json:"state_json"`
	Status         string     `json:"status"` // running | completed | failed | paused
	StartedAt      NullString `json:"started_at"`
	CompletedAt    NullString `json:"completed_at"`
}

// WorkflowNodeState mirrors a row from the workflow_node_states table.
type WorkflowNodeState struct {
	ID          int64      `json:"id"`
	RunID       int64      `json:"run_id"`
	NodeName    string     `json:"node_name"`
	NodeType    string     `json:"node_type"`
	Status      string     `json:"status"` // pending | ready | running | completed | failed | skipped | waiting_human
	OutputsJSON NullString `json:"outputs_json"`
	Error       NullString `json:"error"`
	Attempt     int        `json:"attempt"`
	StartedAt   NullString `json:"started_at"`
	CompletedAt NullString `json:"completed_at"`
	// Model is the resolved_model column — the canonical SDK model ID the
	// node's latest dispatch resolved. Empty until a dispatch pins it (the
	// repair loop falls back to node.Model then).
	Model string `json:"resolved_model"`
	// SchemaEnforcement is how the node's output_schema held its accepted
	// answer: "enforced at decode" or "post-hoc only". Empty for a node
	// without a schema, or one that completed with no call.
	SchemaEnforcement string `json:"schema_enforcement,omitempty"`
}
