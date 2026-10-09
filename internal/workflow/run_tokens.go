package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// The run tokens a swarm's evaluator and Queen read. Each resolves from this
// run's own forager nodes: their status and the outputs stored when they
// completed, which the accept path parsed with ExtractJSONOutput. None reads
// the Comb's forager vantages, which hold one row per forager for the whole
// database, so another run can overwrite them. The engine resolves them when
// it hands a node out, so agent-run and the manual path (chb workflow next)
// get the same prompt.
const (
	tallyToken     = "{tally}"
	nablaToken     = "{nabla.fired}"
	ledgerToken    = "{swarm.ledger}"
	diversityToken = "{diversity}"
)

// ResonatesPairs reads a workflow's machine-readable `resonates:` pairs, a
// top-level list of two-element [a, b] lists. It returns nil for a workflow
// without the key (a research workflow, say).
func ResonatesPairs(defn map[string]any) [][2]string {
	raw, _ := defn["resonates"].([]any)
	var out [][2]string
	for _, item := range raw {
		if pair, ok := resonatesPair(item); ok {
			out = append(out, pair)
		}
	}
	return out
}

// resonatesPair reads one resonates entry: a list of two non-empty names.
func resonatesPair(item any) ([2]string, bool) {
	pair, _ := item.([]any)
	if len(pair) != 2 {
		return [2]string{}, false
	}
	a, _ := pair[0].(string)
	b, _ := pair[1].(string)
	return [2]string{a, b}, a != "" && b != ""
}

// foragerOutcome is one lens forager's node in this run.
type foragerOutcome struct {
	node    string
	status  string
	model   string         // the node's resolved_model; "" when none is recorded
	outputs map[string]any // set when the node completed with outputs that parse
}

func (o foragerOutcome) verdict() string {
	v, _ := o.outputs["verdict"].(string)
	return v
}

// lensNameOf names the lens a node runs, or "" when the node is not a lens
// forager. A forager is named by foragerName, the rule GetNextNodes applies.
// Dreamer nodes are left out: they run after the Queen and take no position
// on the question.
func lensNameOf(nodeName string, def map[string]any) string {
	if a, _ := def["archetype"].(string); strings.EqualFold(strings.TrimSpace(a), "dreamer") {
		return ""
	}
	return foragerName(nodeName, def)
}

// LensNames lists the lens foragers of a workflow definition, sorted: the
// swarm whose track records `{calibration.lenses}` renders.
func LensNames(defn map[string]any) []string {
	nodes, _ := defn["nodes"].(map[string]any)
	var out []string
	for nodeName, raw := range nodes {
		def, _ := raw.(map[string]any)
		if name := lensNameOf(nodeName, def); name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// runForagers maps each lens forager of this run to its node, by
// lensNameOf.
func runForagers(repo Store, runID int64, defn map[string]any) (map[string]foragerOutcome, error) {
	states, err := repo.GetWorkflowNodeStates(runID)
	if err != nil {
		return nil, err
	}
	nodes, _ := defn["nodes"].(map[string]any)
	out := map[string]foragerOutcome{}
	for _, s := range states {
		def, _ := nodes[s.NodeName].(map[string]any)
		if name := lensNameOf(s.NodeName, def); name != "" {
			out[name] = outcomeOf(s)
		}
	}
	return out, nil
}

// outcomeOf is a lens forager's node row as an outcome, with its outputs
// when it completed with outputs that parse.
func outcomeOf(s db.WorkflowNodeState) foragerOutcome {
	o := foragerOutcome{node: s.NodeName, status: s.Status, model: s.Model}
	var m map[string]any
	if s.Status == "completed" && s.OutputsJSON.Valid && json.Unmarshal([]byte(s.OutputsJSON.String), &m) == nil {
		o.outputs = m
	}
	return o
}

// readRunForagers reads one run's definition and its lens foragers' nodes.
func readRunForagers(repo Store, runID int64) (map[string]any, map[string]foragerOutcome, error) {
	run, err := repo.GetWorkflowRun(runID)
	if err != nil {
		return nil, nil, err
	}
	defn, err := LoadYAMLString(run.DefinitionYAML)
	if err != nil {
		return nil, nil, err
	}
	outcomes, err := runForagers(repo, runID, defn)
	if err != nil {
		return nil, nil, err
	}
	return defn, outcomes, nil
}

// runTokenLookup resolves one run token by its key, the text between its
// braces, and reports false for any other key. It reads this run's forager
// nodes once, at the first token. A read error resolves every token to
// `unavailable (<reason>)`, never to a count, and the node still dispatches.
func runTokenLookup(repo Store, runID int64, defn map[string]any) func(key string) (string, bool) {
	r := &runTokenReader{repo: repo, runID: runID, defn: defn}
	return r.lookup
}

// runTokenText renders each run token but {verdict.…} from the workflow's
// definition and this run's foragers.
var runTokenText = map[string]func(defn map[string]any, outcomes map[string]foragerOutcome) string{
	tallyToken: func(_ map[string]any, outcomes map[string]foragerOutcome) string {
		return tallyVerdicts(outcomes).String()
	},
	nablaToken: func(defn map[string]any, outcomes map[string]foragerOutcome) string {
		return nablaLine(ResonatesPairs(defn), outcomes)
	},
	ledgerToken: func(defn map[string]any, outcomes map[string]foragerOutcome) string {
		return swarmLedger(ResonatesPairs(defn), outcomes)
	},
	diversityToken: func(_ map[string]any, outcomes map[string]foragerOutcome) string {
		return DiversityOf(lensAnswers(outcomes)).String()
	},
}

// runTokenReader is runTokenLookup's state: the run, and its forager nodes
// once read.
type runTokenReader struct {
	repo     Store
	runID    int64
	defn     map[string]any
	outcomes map[string]foragerOutcome
	readErr  error
	read     bool
}

func (r *runTokenReader) lookup(key string) (string, bool) {
	render, ok := r.renderer(key)
	if !ok {
		return "", false
	}
	if err := r.readForagers(); err != nil {
		return fmt.Sprintf("unavailable (%v)", err), true
	}
	return render(r.outcomes), true
}

// renderer is the function that renders the run token key names, if it
// names one.
func (r *runTokenReader) renderer(key string) (func(map[string]foragerOutcome) string, bool) {
	if verdictKey, ok := strings.CutPrefix(key, "verdict."); ok {
		return func(outcomes map[string]foragerOutcome) string { return verdictTokenText(outcomes, verdictKey) }, true
	}
	text, ok := runTokenText["{"+key+"}"]
	if !ok {
		return nil, false
	}
	return func(outcomes map[string]foragerOutcome) string { return text(r.defn, outcomes) }, true
}

// readForagers reads this run's forager nodes the first time it is called,
// and returns the read's error every time.
func (r *runTokenReader) readForagers() error {
	if !r.read {
		r.outcomes, r.readErr = runForagers(r.repo, r.runID, r.defn)
		r.read = true
	}
	return r.readErr
}

// verdictTokenText resolves one {verdict.<key>} token: a forager's full
// verdict when its node completed in this run, else a line that says why
// there is none. A key that is not forager:<name> resolves to empty with a
// stderr warning.
func verdictTokenText(outcomes map[string]foragerOutcome, key string) string {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "forager:") {
		fmt.Fprintf(os.Stderr, "[workflow] verdict token warning: %q is not forager:<name>\n", key)
		return ""
	}
	name := strings.TrimPrefix(key, "forager:")
	o, ok := outcomes[name]
	switch {
	case !ok:
		return fmt.Sprintf("(no verdict: this run has no node for forager %s)", name)
	case o.outputs == nil:
		return fmt.Sprintf("(no verdict: %s is %s in this run)", o.node, o.status)
	}
	return renderVerdict(o.outputs)
}

// nablaLine renders the run's fired ∇ pairs (firedPairs); `none` when pairs
// exist and none fired; or a line saying the swarm declares no pairs.
func nablaLine(pairs [][2]string, outcomes map[string]foragerOutcome) string {
	if len(pairs) == 0 {
		return "none — this swarm declares no resonates pairs"
	}
	fired := firedPairs(pairs, outcomes)
	if len(fired) == 0 {
		return "none"
	}
	return strings.Join(fired, "; ")
}

// firedPairs is the resonates pairs whose two foragers returned identical
// verdict strings, neither abstain (comb.ConvergedPairs), each written
// `a↔b (<verdict>)`; empty, not nil, when none fired.
func firedPairs(pairs [][2]string, outcomes map[string]foragerOutcome) []string {
	verdicts := make(map[string]string, len(outcomes))
	for name, o := range outcomes {
		verdicts[name] = o.verdict()
	}
	fired := []string{}
	for _, p := range comb.ConvergedPairs(pairs, verdicts) {
		fired = append(fired, fmt.Sprintf("%s↔%s (%s)", p[0], p[1], verdicts[p[0]]))
	}
	return fired
}
