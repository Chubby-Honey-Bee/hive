package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/comb"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// tokenSwarm is a swarm shape the run tokens read: four lens foragers, a
// dreamer after the Queen, and resonates pairs, one to a forager with no
// node. Its Queen joins settled and holds her verdict to the tally.
const tokenSwarm = `name: tokens
inputs: [question]
resonates:
  - [a, b]
  - [a, c]
  - [b, ghost]
  - [c, d]
nodes:
  forager-a: {type: agent, prompt: "a {question}"}
  forager-b: {type: agent, prompt: "b {question}"}
  forager-c: {type: agent, prompt: "c {question}"}
  forager-d: {type: agent, prompt: "d {question}"}
  dreamer-z: {type: agent, archetype: dreamer, forager_name: z, prompt: "ripen"}
  queen:
    type: agent
    join: settled
    prompt: "A: {verdict.forager:a}\nB: {verdict.forager:b}\nC: {verdict.forager:c}\nD: {verdict.forager:d}\nG: {verdict.forager:ghost}\nTALLY: {tally}\nNABLA: {nabla.fired}\nDIVERSITY: {diversity}\n"
    accept:
      - predicate: "tally_plurality == '' or outputs.verdict == tally_plurality or (outputs.verdict == 'abstain' and tally_abstentions >= tally_votes) or len(outputs.dissent_from_plurality) >= 20"
        reason: "The plurality was {tally_plurality}; you answered {outputs.verdict}. The tally: {tally_line}."
edges:
  - {from: forager-a, to: queen}
  - {from: forager-b, to: queen}
  - {from: forager-c, to: queen}
  - {from: forager-d, to: queen}
  - {from: queen, to: dreamer-z}
`

// startTokenRun starts tokenSwarm and settles its foragers: each name in
// outputs completes with those outputs, each in rejected is rejected.
func startTokenRun(t *testing.T, store *db.Store, outputs map[string]map[string]any, rejected ...string) int64 {
	t.Helper()
	repo := store.Workflows()
	runID, err := InitWorkflow(repo, writeWorkflow(t, tokenSwarm), map[string]any{"question": "q"})
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range outputs {
		if err := CompleteNode(repo, runID, "forager-"+name, out); err != nil {
			t.Fatalf("complete forager-%s: %v", name, err)
		}
	}
	for _, name := range rejected {
		if err := repo.MarkNodeRejected(runID, "forager-"+name, "accept rejected", time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	return runID
}

// wantPlurality restates the tally's rule: abstain casts no vote, and the
// plurality is the one other verdict with strictly the most votes.
func wantPlurality(verdicts map[string]string) string {
	counts := map[string]int{}
	for _, v := range verdicts {
		if v != "" && v != "abstain" {
			counts[v]++
		}
	}
	best, plurality := 0, ""
	for v, n := range counts {
		if n > best {
			best, plurality = n, v
		}
	}
	for v, n := range counts {
		if n == best && v != plurality {
			return ""
		}
	}
	return plurality
}

// wantFired restates the ∇ rule: a pair fires when both verdicts are
// present, equal and not abstain, written a↔b (<verdict>) with a < b.
func wantFired(pairs [][2]string, verdicts map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range pairs {
		a, b := p[0], p[1]
		if b < a {
			a, b = b, a
		}
		va, vb := verdicts[a], verdicts[b]
		key := fmt.Sprintf("%s↔%s (%s)", a, b, va)
		if va == "" || va == "abstain" || va != vb || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// section returns the text between two labels of a resolved prompt.
func section(t *testing.T, prompt, label, next string) string {
	t.Helper()
	i := strings.Index(prompt, label)
	if i < 0 {
		t.Fatalf("no %q in %q", label, prompt)
	}
	s := prompt[i+len(label):]
	if next == "" {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(s[:strings.Index(s, next)])
}

// The Queen's tokens read this run's own node outputs when the engine hands
// her out: every field of each verdict, whatever another run wrote to the
// shared Comb vantage; a line saying why a forager has none; the tally with
// the rejected forager named; and the ∇ pairs the verdicts give.
func TestRunTokens_ReadThisRunsOutputs(t *testing.T) {
	outputs := map[string]map[string]any{
		"a": {"verdict": "support", "key_points": []any{"k1", "k2"}, "evidence": []any{"e1 <file.go:12>"}, "def_claims": []any{"d1"}, "falsification": "f1", "recommendation": "r-a"},
		"b": {"verdict": "support", "key_points": []any{"kb"}, "asm_claims": []any{"asm-b"}, "recommendation": "r-b"},
		"c": {"verdict": "abstain", "unk_claims": []any{"unk-c"}, "references": []any{map[string]any{"title": "T"}}, "recommendation": "r-c"},
	}
	store := newTestStore(t)
	runID := startTokenRun(t, store, outputs, "d")
	if err := comb.BuildForagerVantage(context.Background(), store, runID+1000, "a", `{"verdict":"oppose"}`,
		map[string]any{"verdict": "oppose", "recommendation": "someone else's"}); err != nil {
		t.Fatal(err)
	}

	next, err := GetNextNodes(store.Workflows(), runID)
	if err != nil || len(next) != 1 || next[0].Node != "queen" {
		t.Fatalf("next = %+v, %v; want queen, which joins past the rejected d", next, err)
	}
	got := next[0].ResolvedPrompt

	for name, lbl := range map[string][2]string{"a": {"A:", "B:"}, "b": {"B:", "C:"}, "c": {"C:", "D:"}} {
		sec := section(t, got, lbl[0], lbl[1])
		for k, v := range outputs[name] {
			for _, item := range itemsOf(v) {
				if !strings.Contains(sec, item) {
					t.Errorf("forager %s's verdict lacks %s item %q:\n%s", name, k, item, sec)
				}
			}
		}
	}
	if strings.Contains(got, "someone else's") || strings.Contains(section(t, got, "A:", "B:"), "oppose") {
		t.Errorf("a's token read the Comb vantage another run wrote:\n%s", got)
	}
	if d := section(t, got, "D:", "G:"); d != "(no verdict: forager-d is rejected in this run)" {
		t.Errorf("d's token %q, want the line saying it was rejected", d)
	}
	if g := section(t, got, "G:", "TALLY:"); g != "(no verdict: this run has no node for forager ghost)" {
		t.Errorf("ghost's token %q, want the line saying it has no node", g)
	}

	verdicts := map[string]string{}
	for name, out := range outputs {
		verdicts[name], _ = out["verdict"].(string)
	}
	tally := section(t, got, "TALLY:", "NABLA:")
	counts := map[string]int{}
	for _, v := range verdicts {
		counts[v]++
	}
	for v, n := range counts {
		if !strings.Contains(tally, fmt.Sprintf("%s %d (", v, n)) {
			t.Errorf("tally %q lacks %s %d", tally, v, n)
		}
	}
	if !strings.Contains(tally, "no verdict from d (rejected)") {
		t.Errorf("tally %q does not name the rejected forager", tally)
	}
	if p := wantPlurality(verdicts); !strings.HasSuffix(tally, "Plurality: "+p) {
		t.Errorf("tally %q, want plurality %q", tally, p)
	}
	fired := wantFired(ResonatesPairs(mustDefn(t, tokenSwarm)), verdicts)
	nabla := section(t, got, "NABLA:", "DIVERSITY:")
	if want := strings.Join(fired, "; "); (len(fired) == 0 && nabla != "none") || (len(fired) > 0 && nabla != want) {
		t.Errorf("∇ line %q, want %q", nabla, want)
	}
}

// itemsOf lists the strings a rendered field must carry: each list item, a
// string as it is, anything else as JSON.
func itemsOf(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, it := range x {
			out = append(out, itemsOf(it)...)
		}
		return out
	}
	b, _ := json.Marshal(v)
	return []string{string(b)}
}

func mustDefn(t *testing.T, src string) map[string]any {
	t.Helper()
	defn, err := LoadYAMLString(src)
	if err != nil {
		t.Fatal(err)
	}
	return defn
}

// statesFailRepo reads node states with an error; every other call goes to
// the real repo.
type statesFailRepo struct {
	*db.WorkflowsRepo
	err error
}

func (r statesFailRepo) GetWorkflowNodeStates(int64) ([]db.WorkflowNodeState, error) {
	return nil, r.err
}

// When this run's node states cannot be read, every run token says so,
// with the reason, and never a count; the plurality is empty, so the
// Queen's coherence rule holds her to nothing and her node still completes.
func TestRunTokens_ReadErrorIsUnavailable(t *testing.T) {
	store := newTestStore(t)
	runID := startTokenRun(t, store, map[string]map[string]any{
		"a": {"verdict": "support"}, "b": {"verdict": "support"}, "c": {"verdict": "oppose"}, "d": {"verdict": "support"},
	})
	readErr := errors.New("database is locked")
	repo := statesFailRepo{WorkflowsRepo: store.Workflows(), err: readErr}
	unavailable := fmt.Sprintf("unavailable (%v)", readErr)

	got, _ := resolveNodePrompt("{verdict.forager:a}|{tally}|{nabla.fired}|{diversity}", nil, repo, runID, mustDefn(t, tokenSwarm))
	if want := strings.Join([]string{unavailable, unavailable, unavailable, unavailable}, "|"); got != want {
		t.Errorf("resolved %q, want %q", got, want)
	}
	if p := computedState(repo, runID, "queen")[TallyPluralityKey]; p != "" {
		t.Errorf("computed plurality %v on a read error, want empty", p)
	}
	if err := CompleteNode(repo, runID, "queen", map[string]any{"verdict": "oppose", "dissent_from_plurality": ""}); err != nil {
		t.Errorf("queen with no readable tally was refused: %v", err)
	}
}

// The manual path gets what agent-run gets: `chb workflow next` hands the
// Queen out with her tokens filled, and `chb workflow complete` holds her
// verdict to the plurality the engine counts. That count goes to state
// under tally_plurality and never into her stored outputs, which stay what
// she returned, a plurality of her own included.
func TestManualPath_QueenTokensAndCoherence(t *testing.T) {
	verdicts := map[string]string{"a": "support", "b": "support", "c": "oppose", "d": "conditional"}
	outputs := map[string]map[string]any{}
	for f, v := range verdicts {
		outputs[f] = map[string]any{"verdict": v, "recommendation": "r"}
	}
	store := newTestStore(t)
	repo := store.Workflows()
	runID := startTokenRun(t, store, outputs)
	plurality := wantPlurality(verdicts)

	next, err := GetNextNodesManual(repo, runID)
	if err != nil || len(next) != 1 || next[0].Node != "queen" {
		t.Fatalf("next = %+v, %v; want queen", next, err)
	}
	if m := leftoverRunToken(next[0].ResolvedPrompt); m != "" {
		t.Errorf("the manual path handed out the literal %s", m)
	}

	departs := "oppose"
	silent := map[string]any{"verdict": departs, "dissent_from_plurality": "None", TallyPluralityKey: departs}
	var rej *AcceptRejection
	if err := CompleteNode(repo, runID, "queen", silent); !errors.As(err, &rej) {
		t.Fatalf("a departure from %s with a placeholder dissent completed (%v), want an accept rejection", plurality, err)
	}
	if want := fmt.Sprintf("The plurality was %s; you answered %s. The tally: %s.", plurality, departs,
		section(t, next[0].ResolvedPrompt, "TALLY:", "NABLA:")); rej.Reason != want {
		t.Errorf("the rejection's reason %q, want %q", rej.Reason, want)
	}
	reason := map[string]any{"verdict": departs, "dissent_from_plurality": "c's benchmark outweighs a and b.", TallyPluralityKey: departs}
	if err := CompleteNode(repo, runID, "queen", reason); err != nil {
		t.Fatalf("a departure with a reason was refused: %v", err)
	}
	var outs string
	_ = store.ReadDB.QueryRow(`SELECT outputs_json FROM workflow_node_states WHERE run_id=? AND node_name='queen'`, runID).Scan(&outs)
	var stored map[string]any
	_ = json.Unmarshal([]byte(outs), &stored)
	if stored[TallyPluralityKey] != departs {
		t.Errorf("stored outputs hold %s %v, want what the Queen returned, %q", TallyPluralityKey, stored[TallyPluralityKey], departs)
	}
	run, _ := repo.GetWorkflowRun(runID)
	var state map[string]any
	_ = json.Unmarshal([]byte(run.StateJSON), &state)
	if state[TallyPluralityKey] != plurality {
		t.Errorf("state %s = %v, want the engine's count %q", TallyPluralityKey, state[TallyPluralityKey], plurality)
	}
}

// leftoverRunToken returns the first run token left in a prompt, or "".
func leftoverRunToken(p string) string {
	for _, tok := range []string{"{verdict.", tallyToken, nablaToken, ledgerToken, diversityToken} {
		if strings.Contains(p, tok) {
			return tok
		}
	}
	return ""
}
