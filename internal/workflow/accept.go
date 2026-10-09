package workflow

import (
	"fmt"
	"maps"
	"strings"
)

// MinToolCallsPredicate is the Predicate of an AcceptRejection the runner
// raises for a node whose calls made fewer tool calls than its
// min_tool_calls. EvaluatedValue is the count made.
const MinToolCallsPredicate = "min_tool_calls"

// AcceptRejection is returned by CompleteNode when one of the node's
// accept: predicates fails. It carries enough information for the
// runner to record a precise rationale on the node row and to feed the
// on_reject: repair prompt.
type AcceptRejection struct {
	NodeName       string
	Predicate      string
	EvaluatedValue string
	EvalError      string
	Outputs        map[string]any
	// Reason is the accept: item's reason with its placeholders filled,
	// when the item gives one and its predicate evaluated to false. Error()
	// leads with it, and a repair prompt's {accept_failure} is it alone.
	Reason string
}

// Error says which check rejected the node's outputs and how, led by the
// item's reason when it gives one. An output_schema rejection names the
// violations alone.
func (a *AcceptRejection) Error() string {
	if a.Predicate == OutputSchemaPredicate && a.EvalError == "" {
		return fmt.Sprintf("output_schema on node %q: %s", a.NodeName, a.EvaluatedValue)
	}
	s := a.predicateText()
	if a.Reason != "" {
		return a.Reason + " (" + s + ")"
	}
	return s
}

// predicateText says what the rejecting predicate did: failed to evaluate,
// counted too few tool calls, or evaluated to a false value.
func (a *AcceptRejection) predicateText() string {
	switch {
	case a.EvalError != "":
		return fmt.Sprintf("accept: predicate %q on node %q failed to evaluate (%s)", a.Predicate, a.NodeName, a.EvalError)
	case a.Predicate == MinToolCallsPredicate:
		return fmt.Sprintf("min_tool_calls on node %q: %s tool call(s) made", a.NodeName, a.EvaluatedValue)
	}
	return fmt.Sprintf("accept: predicate %q on node %q evaluated to %s", a.Predicate, a.NodeName, a.EvaluatedValue)
}

// evaluateAccept reads accept: from the node definition and evaluates
// every predicate. Returns nil when accept: is absent or all pass; an
// *AcceptRejection on the first failure.
//
// During eval the state map is augmented with two synthetic keys:
//
//	"outputs" → the agent's outputs map (so `outputs.count` works)
//
// State already contains the merged outputs, so unprefixed `count` also works.
func evaluateAccept(defn map[string]any, nodeName string, state, outputs map[string]any) *AcceptRejection {
	nodes, _ := defn["nodes"].(map[string]any)
	node, _ := nodes[nodeName].(map[string]any)
	items := AcceptItems(node)
	if len(items) == 0 {
		return nil
	}
	// Augment a copy of state with `outputs` so authors can use either form.
	evalState := make(map[string]any, len(state)+1)
	maps.Copy(evalState, state)
	evalState["outputs"] = outputs
	for _, it := range items {
		if rejection := checkAcceptItem(it, nodeName, evalState, outputs); rejection != nil {
			return rejection
		}
	}
	return nil
}

// checkAcceptItem evaluates one item's predicate: a rejection when it fails
// to evaluate or is false, nil when it holds.
func checkAcceptItem(it AcceptItem, nodeName string, evalState, outputs map[string]any) *AcceptRejection {
	v, err := SafeEval(it.Predicate, evalState)
	if err != nil {
		return &AcceptRejection{
			NodeName:       nodeName,
			Predicate:      it.Predicate,
			EvaluatedValue: "<error>",
			EvalError:      err.Error(),
			Outputs:        outputs,
		}
	}
	if toBool(v) {
		return nil
	}
	return &AcceptRejection{
		NodeName:       nodeName,
		Predicate:      it.Predicate,
		EvaluatedValue: fmt.Sprintf("%v", v),
		Outputs:        outputs,
		Reason:         fillAcceptReason(it.Reason, evalState, outputs),
	}
}

// AcceptItem is one accept: item: a predicate, and the reason a rejection
// by it gives, "" when the item gives none.
type AcceptItem struct{ Predicate, Reason string }

// AcceptItems reads a node's accept: list. An item is a predicate string,
// or a map with the predicate under `predicate` and a reason under
// `reason`. An empty predicate is skipped, and so is an item of any other
// shape, which CheckNodeFields refuses before a run starts. Tolerant of a
// missing or non-list accept: (no accept: → no rejection).
func AcceptItems(node map[string]any) []AcceptItem {
	list, _ := node["accept"].([]any)
	out := make([]AcceptItem, 0, len(list))
	for _, x := range list {
		if it := acceptItem(x); it.Predicate != "" {
			out = append(out, it)
		}
	}
	return out
}

// acceptItem reads one accept: item; an item of another shape has no
// predicate.
func acceptItem(x any) AcceptItem {
	switch v := x.(type) {
	case string:
		return AcceptItem{Predicate: v}
	case map[string]any:
		return AcceptItem{Predicate: stringField(v, "predicate"), Reason: stringField(v, "reason")}
	}
	return AcceptItem{}
}

// fillAcceptReason fills a reason's placeholders in one pass:
// {outputs.<key>} from the node's outputs, any other from state. A
// placeholder no value fills stays as written.
func fillAcceptReason(reason string, state, outputs map[string]any) string {
	if reason == "" {
		return ""
	}
	fromState, fromOutputs := stateLookup(state), stateLookup(outputs)
	out, _ := fillPlaceholders(reason, func(key string) (string, bool) {
		if k, ok := strings.CutPrefix(key, "outputs."); ok {
			return fromOutputs(k)
		}
		return fromState(key)
	})
	return out
}

// nodeOnRejectBlock returns the on_reject: object (or nil if absent or malformed).
// Used by the runner's repair loop.
func nodeOnRejectBlock(defn map[string]any, nodeName string) map[string]any {
	nodes, _ := defn["nodes"].(map[string]any)
	if nodes == nil {
		return nil
	}
	raw, ok := nodes[nodeName].(map[string]any)
	if !ok {
		return nil
	}
	or, _ := raw["on_reject"].(map[string]any)
	return or
}

// NodeOnRejectBlock is the exported wrapper for runner-package callers.
func NodeOnRejectBlock(defn map[string]any, nodeName string) map[string]any {
	return nodeOnRejectBlock(defn, nodeName)
}
