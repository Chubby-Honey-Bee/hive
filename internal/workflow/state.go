package workflow

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// loadNodeStateMap pulls workflow_node_states via the typed repo into the
// map[string]map[string]any shape that resolveDecisionNodes and
// findReadyNodes use.
func loadNodeStateMap(repo Store, runID int64) map[string]map[string]any {
	nodes, err := repo.GetWorkflowNodeStates(runID)
	if err != nil {
		return nil
	}
	states := make(map[string]map[string]any, len(nodes))
	for _, n := range nodes {
		states[n.NodeName] = map[string]any{
			"node_type":    n.NodeType,
			"status":       n.Status,
			"attempt":      n.Attempt,
			"error":        nullableText(n.Error),
			"completed_at": nullableText(n.CompletedAt),
		}
	}
	return states
}

// statusOf is a node state's status, "" when it has none.
func statusOf(nodeState map[string]any) string {
	s, _ := nodeState["status"].(string)
	return s
}

// nullableText is a nullable column's text, or a nil *string when it is
// NULL.
func nullableText(s db.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

// decodeState is a run's state_json as a map, empty when it holds no JSON
// object.
func decodeState(stateJSON string) map[string]any {
	var state map[string]any
	json.Unmarshal([]byte(stateJSON), &state)
	if state == nil {
		state = map[string]any{}
	}
	return state
}

// applyStateUpdatesEngine applies a node's state_updates block to the run
// state. It is the only implementation.
func applyStateUpdatesEngine(defn map[string]any, nodeName string, state map[string]any) {
	nodes, _ := defn["nodes"].(map[string]any)
	node, _ := nodes[nodeName].(map[string]any)
	updates, _ := node["state_updates"].(map[string]any)
	for key, value := range updates {
		state[key] = stateUpdate(value, state)
	}
}

// stateUpdate is the value a state_updates entry writes: a string holding a
// placeholder filled from state (ResolveTemplate), any other value as it is.
func stateUpdate(value any, state map[string]any) any {
	if s, ok := value.(string); ok && strings.Contains(s, "{") {
		return ResolveTemplate(s, state)
	}
	return value
}

// timestamp is the current time as the store records it: UTC, RFC 3339.
func timestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
