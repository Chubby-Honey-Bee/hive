package cli

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
	"gopkg.in/yaml.v3"
)

func (r *preflightReport) checkYAML(path string) {
	if _, err := os.Stat(path); err != nil {
		r.fail("yaml exists", fmt.Sprintf("%s: %v", path, err))
		return
	}
	errs, _, err := workflow.Validate(path)
	if err != nil {
		r.fail("yaml validates", err.Error())
		return
	}
	if len(errs) > 0 {
		r.fail("yaml validates", strings.Join(errs, "; "))
		return
	}
	r.pass("yaml validates", path)
}

func (r *preflightReport) loadDefinition(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var defn map[string]any
	_ = yaml.Unmarshal(data, &defn)
	return defn
}

// checkFanSources refuses a parallel_fan that would not fan out: one whose
// fan_source nothing upstream declares runs once on its unsubstituted prompt,
// and one whose prompt lacks its placeholder sends every item the same prompt.
// With no nodes, or a graph that does not build, it checks nothing and warns.
func (r *preflightReport) checkFanSources(defn map[string]any) {
	if nodes, _ := defn["nodes"].(map[string]any); nodes == nil {
		r.warn("parallel_fan wiring", "no nodes — skipping")
		return
	}
	problems, err := workflow.FanSourceProblems(defn)
	if err != nil {
		r.warn("parallel_fan wiring", fmt.Sprintf("graph does not build (%v) — skipping", err))
		return
	}
	if len(problems) > 0 {
		r.fail("parallel_fan wiring", strings.Join(problems, "; "))
		return
	}
	r.pass("parallel_fan wiring", "every fan_source is declared upstream and every fan prompt holds its placeholder")
}

func (r *preflightReport) checkAcceptPresence(defn map[string]any) {
	nodes, _ := defn["nodes"].(map[string]any)
	if nodes == nil {
		r.warn("accept: coverage", "no nodes — skipping")
		return
	}
	if missing := nodesMissingAccept(nodes); len(missing) > 0 {
		r.fail("accept: coverage", "nodes missing accept: → "+strings.Join(missing, ", "))
		return
	}
	r.pass("accept: coverage", "every agent / parallel_fan node has accept:")
}

// nodesMissingAccept names, sorted, the nodes accept: has to judge that
// have none.
func nodesMissingAccept(nodes map[string]any) []string {
	var missing []string
	for _, n := range sortedNodeNames(nodes) {
		if nodeMissingAccept(nodes[n]) {
			missing = append(missing, n)
		}
	}
	return missing
}

// nodeMissingAccept reports whether a node accept: has to judge has no
// accept:.
func nodeMissingAccept(raw any) bool {
	node, _ := raw.(map[string]any)
	if !acceptJudgesNode(node) {
		return false
	}
	acc, ok := node["accept"].([]any)
	return !ok || len(acc) == 0
}

// acceptJudgesNode reports whether accept: has a model output to judge on
// the node: an agent or parallel_fan node, unless it is a dreamer. A
// dreamer-archetype node runs the deterministic ripening loop, not an LLM,
// so there is no model output for accept: to judge, and a swarm generated
// with a dreamer (--foragers default or all) passes its own preflight.
func acceptJudgesNode(node map[string]any) bool {
	t, _ := node["type"].(string)
	a, _ := node["archetype"].(string)
	return modelNodeType(t) && a != "dreamer"
}

// modelNodeType reports whether nodes of type t prompt a model: agent and
// parallel_fan nodes.
func modelNodeType(t string) bool {
	return t == "agent" || t == "parallel_fan"
}

// sortedNodeNames are the names of a workflow's nodes, sorted.
func sortedNodeNames(nodes map[string]any) []string {
	names := make([]string, 0, len(nodes))
	for n := range nodes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// nodePrompt is the node's prompt, else its prompt_template.
func nodePrompt(node map[string]any) string {
	prompt, _ := node["prompt"].(string)
	if prompt == "" {
		prompt, _ = node["prompt_template"].(string)
	}
	return prompt
}

// acceptOutputRE pulls the output keys an accept: predicate reads.
var acceptOutputRE = regexp.MustCompile(`outputs\.(\w+)`)

// checkAcceptAsksForItsOutputs refuses a node whose accept: judges an output
// its prompt never asks the agent to return. A node declaring one output is
// safe — the engine aliases a prose answer onto it — but with several
// declared outputs there is nothing to alias, so the predicate reads a key
// that is not there and the node is rejected on its first run.
func (r *preflightReport) checkAcceptAsksForItsOutputs(defn map[string]any) {
	nodes, _ := defn["nodes"].(map[string]any)
	if nodes == nil {
		r.warn("accept: reads outputs the prompt asks for", "no nodes — skipping")
		return
	}
	var bad []string
	for _, n := range sortedNodeNames(nodes) {
		bad = append(bad, unaskedAcceptOutputs(n, nodes[n])...)
	}
	if len(bad) == 0 {
		r.pass("accept: reads outputs the prompt asks for", "every judged output is named in its node's prompt")
		return
	}
	r.fail("accept: reads outputs the prompt asks for",
		"the prompt never asks for these, so the predicate reads a missing key → "+strings.Join(bad, ", "))
}

// unaskedAcceptOutputs names, as node.key, each output the node's accept:
// reads that its prompt never asks for.
func unaskedAcceptOutputs(name string, raw any) []string {
	node, _ := raw.(map[string]any)
	if !acceptReadsNamedOutputs(node) {
		return nil
	}
	prompt := nodePrompt(node)
	var bad []string
	for _, key := range acceptOutputKeys(node) {
		if !strings.Contains(prompt, key) {
			bad = append(bad, name+"."+key)
		}
	}
	return bad
}

// acceptReadsNamedOutputs reports whether the node's accept: can read an
// output its prompt never asks for. A node declaring one output is safe —
// the engine aliases a prose answer onto it. A command node has no prompt:
// its outputs are keys of the JSON its program prints, and a missing one
// rejects the node.
func acceptReadsNamedOutputs(node map[string]any) bool {
	t, _ := node["type"].(string)
	outs, _ := node["outputs"].([]any)
	return t != "command" && len(outs) >= 2
}

// acceptOutputKeys lists the output keys the node's accept: predicates read,
// once each, in the order they first appear.
func acceptOutputKeys(node map[string]any) []string {
	seen := map[string]bool{}
	var keys []string
	for _, a := range workflow.AcceptItems(node) {
		for _, m := range acceptOutputRE.FindAllStringSubmatch(a.Predicate, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				keys = append(keys, m[1])
			}
		}
	}
	return keys
}

var negativeEvidenceRE = regexp.MustCompile(`(?i)\b(verify|confirm|negative|does not|cannot|fails|reject|do NOT)\b`)

func (r *preflightReport) checkPromptLanguage(defn map[string]any) {
	nodes, _ := defn["nodes"].(map[string]any)
	if nodes == nil {
		return
	}
	weak := weakPromptNodes(nodes)
	if len(weak) == 0 {
		r.pass("prompt negative-evidence language", "every agent prompt nudges toward verification")
		return
	}
	sort.Strings(weak)
	r.warn("prompt negative-evidence language", "nodes with weak prompts (advisory): "+strings.Join(weak, ", "))
}

// weakPromptNodes names the nodes whose prompt lacks negative-evidence
// language.
func weakPromptNodes(nodes map[string]any) []string {
	var weak []string
	for name, raw := range nodes {
		if promptLacksNegativeEvidence(raw) {
			weak = append(weak, name)
		}
	}
	return weak
}

// promptLacksNegativeEvidence reports whether a node prompts a model with a
// prompt that never nudges toward verification.
func promptLacksNegativeEvidence(raw any) bool {
	node, _ := raw.(map[string]any)
	if t, _ := node["type"].(string); !modelNodeType(t) {
		return false
	}
	prompt := nodePrompt(node)
	return prompt != "" && !negativeEvidenceRE.MatchString(prompt)
}

// listCommandNodes names every command node and the argv it will run. A
// command node executes a program with no model in the loop, so the operator
// sees each one before launch. A workflow without one prints nothing.
func (r *preflightReport) listCommandNodes(defn map[string]any) {
	r.passListing("command nodes (run with no model)", preflightNodeLines(defn, "command", commandNodeLine))
}

// commandNodeLine names a command node and the argv it will run.
func commandNodeLine(name string, node map[string]any) string {
	argv, _ := node["argv"].([]any)
	parts := make([]string, 0, len(argv))
	for _, a := range argv {
		parts = append(parts, fmt.Sprint(a))
	}
	return name + ": " + strings.Join(parts, " ")
}

// listCalibrateNodes names every calibrate node with its rebuild and scope.
// A calibrate node runs the calibration recompute with no model in the
// loop, so the operator sees each one before launch. A workflow without one
// prints nothing.
func (r *preflightReport) listCalibrateNodes(defn map[string]any) {
	r.passListing("calibrate nodes (run with no model)", preflightNodeLines(defn, "calibrate", calibrateNodeLine))
}

// calibrateNodeLine names a calibrate node with its rebuild and scope.
func calibrateNodeLine(name string, node map[string]any) string {
	rebuild, _ := node["rebuild"].(bool)
	return fmt.Sprintf("%s: rebuild=%v scope=%s", name, rebuild, calibrateNodeScope(node))
}

// calibrateNodeScope is the scope a calibrate node recomputes: every scope
// when it names none, global when it names the empty one.
func calibrateNodeScope(node map[string]any) string {
	v, has := node["scope"]
	if !has {
		return "every scope"
	}
	if s, _ := v.(string); s != "" {
		return s
	}
	return "global"
}

// preflightNodeLines describes, one line each, the workflow's nodes of type
// nodeType.
func preflightNodeLines(defn map[string]any, nodeType string, describe func(name string, node map[string]any) string) []string {
	nodes, _ := defn["nodes"].(map[string]any)
	var lines []string
	for name, raw := range nodes {
		node, _ := raw.(map[string]any)
		if t, _ := node["type"].(string); t == nodeType {
			lines = append(lines, describe(name, node))
		}
	}
	return lines
}
