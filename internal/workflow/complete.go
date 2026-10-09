package workflow

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// CompleteNode marks a workflow node as completed with outputs. The runner
// and `chb workflow complete` complete nodes through it, so both merge
// outputs and apply state_updates alike.
//
// If the node has an `accept:` block, every
// predicate is evaluated against state (with `outputs` exposed as a
// map literal so authors can write either `count >= 50` or
// `outputs.count >= 50`). The first failing predicate yields
// ErrAcceptRejected; the caller (typically the runner's executeNode)
// is expected to recognize that error and either repair via on_reject:
// or mark the node terminally rejected.
func CompleteNode(repo Store, runID int64, nodeName string, outputs map[string]any) error {
	computed := computedState(repo, runID, nodeName)
	return repo.FinishNodeInTx(runID, nodeName, "completed", func(run *db.WorkflowRun) (string, string, error) {
		return mergeNodeOutputs(run, nodeName, outputs, computed, false)
	})
}

// CompleteFanItems is CompleteNode for a parallel_fan whose items the caller
// ran one by one and checked against the node's output_schema, as the runner
// does. The joined outputs are not one reply, so they are not checked again.
// A caller that did not check each item uses CompleteNode, which checks a
// fan's outputs as one reply.
func CompleteFanItems(repo Store, runID int64, nodeName string, outputs map[string]any) error {
	computed := computedState(repo, runID, nodeName)
	return repo.FinishNodeInTx(runID, nodeName, "completed", func(run *db.WorkflowRun) (string, string, error) {
		return mergeNodeOutputs(run, nodeName, outputs, computed, true)
	})
}

// CompleteEmptyFan completes a parallel_fan whose fan_source held no
// items. Like CompleteNode it applies the node's state_updates, and writes
// an empty fan_overflow list when the node has fan_limit, in one write
// transaction. It records empty outputs and evaluates no accept:, because
// there is no output to judge.
func CompleteEmptyFan(repo Store, runID int64, nodeName string) error {
	return repo.FinishNodeInTx(runID, nodeName, "completed", func(run *db.WorkflowRun) (string, string, error) {
		defn, err := runDefinition(run, "state_updates")
		if err != nil {
			return "", "", err
		}
		state := decodeState(run.StateJSON)
		if key, rest, ok := fanOverflowFor(defn, nodeName, state); ok {
			state[key] = rest
		}
		applyStateUpdatesEngine(defn, nodeName, state)
		st, _ := json.Marshal(state)
		return "{}", string(st), nil
	})
}

// mergeNodeOutputs is the pure half of node completion: alias the final text
// onto the declared output, fold outputs into the run state, apply the
// node's state_updates, write the overflow of every fan whose source that
// set, write the engine's computed values, and evaluate its accept:
// predicates. It returns the serialised outputs and new state, or the
// AcceptRejection. Computed values go to state only, so the stored outputs
// are what the node returned. itemsChecked skips the output_schema check
// for a fan whose caller checked each item's reply (CompleteFanItems).
func mergeNodeOutputs(run *db.WorkflowRun, nodeName string, outputs, computed map[string]any, itemsChecked bool) (outputsJSON, stateJSON string, err error) {
	// A definition that will not parse is corruption: it was valid YAML when
	// InitWorkflow stored it. Tolerated, the error would skip the node's
	// accept: predicates and its state_updates in silence — the gate would
	// not gate — so it is refused.
	defn, err := runDefinition(run, "accept: and state_updates")
	if err != nil {
		return "", "", err
	}
	// The shape comes before the predicates, and before prose could be
	// aliased onto a declared output: a schema'd node answers with a JSON
	// object or is rejected.
	if err = shapeRejection(run, nodeName, outputs, itemsChecked); err != nil {
		return "", "", err
	}
	outputs = AliasFinalTextToDeclaredOutput(defn, nodeName, outputs)
	state := mergedState(defn, nodeName, decodeState(run.StateJSON), outputs, computed)
	// Evaluate accept: predicates against state + outputs map.
	if rejection := evaluateAccept(defn, nodeName, state, outputs); rejection != nil {
		return "", "", rejection
	}
	o, _ := json.Marshal(outputs)
	st, _ := json.Marshal(state)
	return string(o), string(st), nil
}

// runDefinition parses a run's stored definition. One that will not parse
// is corruption, and the error names what cannot be applied without it.
func runDefinition(run *db.WorkflowRun, needs string) (map[string]any, error) {
	defn, err := LoadYAMLString(run.DefinitionYAML)
	if err != nil {
		return nil, fmt.Errorf("run %d definition_yaml does not parse, so %s cannot be applied: %w", run.ID, needs, err)
	}
	return defn, nil
}

// shapeRejection is the AcceptRejection for outputs that break the node's
// output_schema, the reason its schemas could not be read, or nil.
func shapeRejection(run *db.WorkflowRun, nodeName string, outputs map[string]any, itemsChecked bool) error {
	rejection, err := schemaRejection(run.DefinitionYAML, nodeName, outputs, itemsChecked)
	if err != nil {
		return fmt.Errorf("run %d: %w", run.ID, err)
	}
	if rejection != nil {
		return rejection
	}
	return nil
}

// mergedState folds a completing node's outputs into the run state: the
// outputs, the node's own fan overflow (read from its source before the
// outputs merged), its state_updates, the overflow of every fan whose source
// it set, then the engine's computed values.
func mergedState(defn map[string]any, nodeName string, state, outputs, computed map[string]any) map[string]any {
	overflowKey, overflow, hasOverflow := fanOverflowFor(defn, nodeName, state)
	maps.Copy(state, outputs)
	if hasOverflow {
		state[overflowKey] = overflow
	}
	applyStateUpdatesEngine(defn, nodeName, state)
	writeSourceOverflows(defn, nodeName, outputs, state)
	maps.Copy(state, computed)
	return state
}

// ExtractJSONOutput tries to pull a single JSON object from the model's final
// text (the workflow engine expects outputs as a JSON map). Falls back to
// { "final_text": <text> } if no JSON block is found.
//
// It tries, in turn, the text whole once a ```json fence is stripped, its
// widest braced span (widestBraceSpan), and its last balanced object
// (lastJSONObject).
func ExtractJSONOutput(text string) map[string]any {
	text = strings.TrimSpace(text)
	if text == "" {
		return map[string]any{}
	}
	text = stripCodeFence(text)
	for _, span := range []func(string) string{strings.TrimSpace, widestBraceSpan, lastJSONObject} {
		if out := jsonObject(span(text)); out != nil {
			return out
		}
	}
	return map[string]any{"final_text": text}
}

// stripCodeFence strips a ``` fence, ```json or bare, from around text,
// which is trimmed.
func stripCodeFence(text string) string {
	if !strings.HasPrefix(text, "```") {
		return text
	}
	if idx := strings.Index(text, "\n"); idx > 0 {
		text = text[idx+1:]
	}
	if idx := strings.LastIndex(text, "```"); idx > 0 {
		text = text[:idx]
	}
	return text
}

// jsonObject is s decoded as a JSON object; nil when it is not one.
func jsonObject(s string) map[string]any {
	var out map[string]any
	if json.Unmarshal([]byte(s), &out) != nil {
		return nil
	}
	return out
}

// widestBraceSpan is text from its first { to its last }, "" when it has no
// such span. Decoded, it is the object when that is the only braced thing in
// the text.
func widestBraceSpan(text string) string {
	i := strings.Index(text, "{")
	j := strings.LastIndex(text, "}")
	if i < 0 || j <= i {
		return ""
	}
	return text[i : j+1]
}

// lastJSONObject returns the final balanced {...} span in text, or "" when
// there is none. Brace counting skips over string literals so a `{` or `}`
// inside a JSON string value doesn't throw off the balance.
//
// It serves prose, then a JSON tail — what a model returns when it writes a
// preamble or a report before the object it was asked for. The widest span
// fails there as soon as the prose contains a brace of its own, and the
// node's accept: predicate then sees no `verdict` at all and rejects a good
// answer.
func lastJSONObject(text string) string {
	end := strings.LastIndex(text, "}")
	if end < 0 {
		return ""
	}
	var scan jsonBraceScan
	for i := end; i >= 0; i-- {
		if scan.step(text, i) {
			return text[i : end+1]
		}
	}
	return ""
}

// jsonBraceScan is lastJSONObject's state as it reads text backwards from
// its last }: the depth of braces open, and whether it is inside a string.
type jsonBraceScan struct {
	depth    int
	inString bool
}

// step reads text[i], the byte before the last one read, and reports
// whether it is the { that balances the } the scan started at.
func (s *jsonBraceScan) step(text string, i int) bool {
	if s.inString {
		// A quote ends the string unless it is escaped.
		s.inString = text[i] != '"' || quoteEscaped(text, i)
		return false
	}
	return s.outsideString(text[i])
}

// outsideString reads c, a byte outside any string, and reports whether it
// balances the scan's braces.
func (s *jsonBraceScan) outsideString(c byte) bool {
	switch c {
	case '"':
		s.inString = true
	case '}':
		s.depth++
	case '{':
		s.depth--
		return s.depth == 0
	}
	return false
}

// quoteEscaped reports whether the quote at text[i] is escaped: an odd
// number of backslashes comes before it.
func quoteEscaped(text string, i int) bool {
	bs := 0
	for k := i - 1; k >= 0 && text[k] == '\\'; k-- {
		bs++
	}
	return bs%2 == 1
}

// AliasFinalTextToDeclaredOutput handles the common case where an
// agent emits prose (not structured JSON) and ExtractJSONOutput falls
// back to {"final_text": "<text>"}. If the node declares `outputs:`,
// the first declared key gets final_text copied into it so downstream
// nodes (parallel_fan with fan_source, decision conditions reading
// state, prompt templates) can resolve their references.
//
// Only fires when:
//   - outputs has exactly one entry, and that entry's key is final_text
//   - the node declares outputs: [<key>, …] with at least one entry
//   - that first declared key is not already present in outputs
//
// All other shapes pass through unchanged. workflow.CompleteNode routes
// through this, so the runner and the manual path agree.
func AliasFinalTextToDeclaredOutput(defn map[string]any, nodeName string, outputs map[string]any) map[string]any {
	finalText, ok := proseOnly(outputs)
	if !ok {
		return outputs
	}
	first := firstDeclaredOutput(defn, nodeName)
	if first == "" || first == "final_text" {
		return outputs
	}
	return map[string]any{first: finalText, "final_text": finalText}
}

// proseOnly returns the text of outputs that hold final_text alone.
func proseOnly(outputs map[string]any) (string, bool) {
	text, ok := outputs["final_text"].(string)
	return text, ok && len(outputs) == 1
}

// firstDeclaredOutput is the first key of the node's `outputs:`, "" when it
// declares none.
func firstDeclaredOutput(defn map[string]any, nodeName string) string {
	declared := declaredOutputs(defn, nodeName)
	if len(declared) == 0 {
		return ""
	}
	return declared[0]
}

// declaredOutputs returns the node's `outputs:` list as a slice of
// strings. Returns nil for nodes without outputs or with non-list
// shapes.
func declaredOutputs(defn map[string]any, nodeName string) []string {
	nodes, _ := defn["nodes"].(map[string]any)
	node, _ := nodes[nodeName].(map[string]any)
	rawOutputs, ok := node["outputs"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(rawOutputs))
	for _, v := range rawOutputs {
		if s, _ := v.(string); s != "" {
			out = append(out, s)
		}
	}
	return out
}
