package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// verdictItems lists every string a rendered verdict must carry for these
// outputs: each list item and each scalar, but the forager's own name.
func verdictItems(outputs map[string]any) []string {
	var out []string
	for k, v := range outputs {
		if k == "forager" || k == "final_text" {
			continue
		}
		switch x := v.(type) {
		case []any:
			for _, it := range x {
				if s, ok := it.(string); ok {
					out = append(out, strings.TrimSpace(s))
				} else {
					var b strings.Builder
					enc := json.NewEncoder(&b)
					enc.SetEscapeHTML(false)
					_ = enc.Encode(it)
					out = append(out, strings.TrimSpace(b.String()))
				}
			}
		case string:
			out = append(out, strings.TrimSpace(x))
		}
	}
	return out
}

// tallyOf counts verdicts and names the plurality: abstain casts no vote,
// and the plurality is the one other verdict with strictly the most votes,
// else "".
func tallyOf(verdicts map[string]string) (map[string]int, string) {
	counts := map[string]int{}
	for _, v := range verdicts {
		if v != "" {
			counts[v]++
		}
	}
	best, plurality := 0, ""
	for v, n := range counts {
		if v != "abstain" && n > best {
			best, plurality = n, v
		}
	}
	for v, n := range counts {
		if v != "abstain" && n == best && v != plurality {
			return counts, ""
		}
	}
	return counts, plurality
}

// firedPairs is the ∇ rule restated: a resonates pair fires when both
// verdicts are present, equal and not abstain. Each pair is written a↔b
// with a < b.
func firedPairs(pairs [][2]string, verdicts map[string]string) []string {
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

// seedSwarmRun starts a generated swarm run and completes its forager nodes
// with the given replies, parsed as the accept path parses them.
func seedSwarmRun(t *testing.T, store *db.Store, swarm []foragers.Forager, replies map[string]string) (int64, map[string]any, map[string]map[string]any) {
	t.Helper()
	return seedSwarmRunProfile(t, store, swarm, replies, foragers.ProfileFull)
}

// seedSwarmRunProfile is seedSwarmRun with the swarm generated under a
// persona profile.
func seedSwarmRunProfile(t *testing.T, store *db.Store, swarm []foragers.Forager, replies map[string]string, profile string) (int64, map[string]any, map[string]map[string]any) {
	t.Helper()
	yamlText, err := foragers.GenerateWorkflow(swarm, foragers.WorkflowOptions{Name: "tokens", Model: "m", SynthesizerModel: "m", PersonaProfile: profile})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "swarm.yaml")
	if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
		t.Fatal(err)
	}
	runID, err := workflow.InitWorkflow(store.Workflows(), path, map[string]any{"question": "q", "context": ""})
	if err != nil {
		t.Fatal(err)
	}
	defn, _ := workflow.LoadYAMLString(yamlText)
	parsed := map[string]map[string]any{}
	for name, reply := range replies {
		out := workflow.ExtractJSONOutput(reply)
		parsed[name] = out
		if err := workflow.CompleteNode(store.Workflows(), runID, "forager-"+name, out); err != nil {
			t.Fatalf("complete forager-%s: %v", name, err)
		}
	}
	return runID, defn, parsed
}

// queenReply is a Queen reply with the given verdict and dissent. It also
// writes a tally_plurality of its own, equal to its verdict, which the
// engine's count must outweigh.
func queenReply(verdict string, dissent any) string {
	js, _ := json.Marshal(map[string]any{"report": "## Swarm Verdict", "verdict": verdict, "convergence": "high",
		"recommendation": "r", "coverage": 3, "gaps": []any{}, "dissent_from_plurality": dissent,
		workflow.TallyPluralityKey: verdict})
	return string(js)
}

// Queen's accept: holds her verdict to the tally's plurality unless
// dissent_from_plurality gives a reason of at least MinDissentChars. A
// plurality the model writes itself changes nothing, and stays in her
// stored outputs as she wrote it. One repair is allowed. The rule holds
// under either persona profile: the lean Queen reads the ledger, and the
// tally still.
func TestQueen_PluralityCoherence(t *testing.T) {
	for _, profile := range []string{foragers.ProfileFull, foragers.ProfileLean} {
		t.Run(profile, func(t *testing.T) { queenPluralityCoherence(t, profile) })
	}
}

func queenPluralityCoherence(t *testing.T, profile string) {
	swarm := []foragers.Forager{
		{Name: "a", Description: "a", Body: "a"},
		{Name: "b", Description: "b", Body: "b"},
		{Name: "c", Description: "c", Body: "c"},
	}
	cases := []struct {
		name     string
		replies  []string // the dispatch, then the repair
		accepted bool
		repairs  int
	}{
		{"agrees", []string{queenReply("support", "")}, true, 0},
		{"departs with a reason", []string{queenReply("oppose", "The two supports rest on one untested assumption.")}, true, 0},
		{"departs silently, then repaired", []string{queenReply("oppose", ""), queenReply("oppose", "b's evidence outweighs a and c.")}, true, 1},
		{"departs with a placeholder twice", []string{queenReply("oppose", "N/A"), queenReply("conditional", "None")}, false, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newTempStore(t)
			runID, defn, parsed := seedSwarmRunProfile(t, store, swarm, map[string]string{
				"a": `{"verdict":"support","recommendation":"x"}`,
				"b": `{"verdict":"oppose","recommendation":"y"}`,
				"c": `{"verdict":"support","recommendation":"z"}`,
			}, profile)
			verdicts := map[string]string{}
			for n, o := range parsed {
				verdicts[n], _ = o["verdict"].(string)
			}
			_, plurality := tallyOf(verdicts)

			next, err := workflow.GetNextNodes(store.Workflows(), runID)
			if err != nil || len(next) != 1 || next[0].Node != "queen" {
				t.Fatalf("next = %v, %v; want queen", next, err)
			}
			if lean := profile == foragers.ProfileLean; lean != strings.Contains(next[0].ResolvedPrompt, "Lenses (verdict") {
				t.Fatalf("under %s the Queen reads the ledger = %v", profile, !lean)
			}
			var fns []func(RunRequest) (*RunResult, error)
			for _, r := range c.replies {
				fns = append(fns, successResult(r))
			}
			backend := &callableBackend{fns: fns}
			rc := buildAgentNodeRC(t, store, runID, defn, backend, Config{})
			rc.executeAgentNode(next[0])

			if backend.calls != len(c.replies) {
				t.Errorf("backend calls = %d, want %d", backend.calls, len(c.replies))
			}
			var status, outs string
			_ = store.ReadDB.QueryRow(`SELECT status, COALESCE(outputs_json,'') FROM workflow_node_states WHERE run_id=? AND node_name='queen'`, runID).Scan(&status, &outs)
			if (status == "completed") != c.accepted {
				t.Fatalf("queen is %s, want accepted=%v", status, c.accepted)
			}
			var n int
			_ = store.ReadDB.QueryRow(`SELECT COUNT(*) FROM workflow_repairs WHERE run_id=? AND node_name='queen'`, runID).Scan(&n)
			if n != c.repairs {
				t.Errorf("repairs = %d, want %d", n, c.repairs)
			}
			if !c.accepted {
				return
			}
			var o map[string]any
			_ = json.Unmarshal([]byte(outs), &o)
			if o[workflow.TallyPluralityKey] != o["verdict"] {
				t.Errorf("stored outputs hold %s %v, want the Queen's own %v", workflow.TallyPluralityKey, o[workflow.TallyPluralityKey], o["verdict"])
			}
			run, _ := store.Workflows().GetWorkflowRun(runID)
			var state map[string]any
			_ = json.Unmarshal([]byte(run.StateJSON), &state)
			if state[workflow.TallyPluralityKey] != plurality {
				t.Errorf("state %s = %v, want the engine's count %q", workflow.TallyPluralityKey, state[workflow.TallyPluralityKey], plurality)
			}
		})
	}
}

// queenAccepts restates Queen's tally rule. A tie or a tally of abstains
// leaves no plurality to depart from, so any verdict passes. Otherwise the
// verdict matches the plurality; or it is abstain and at least as many
// lenses abstained as voted, a lens with no verdict counting as neither;
// or the dissent is a string of at least MinDissentChars. A placeholder is
// no reason. Queen's output_schema requires the dissent to be a string, so
// a null one is refused before her accept: runs, whatever the verdict.
func queenAccepts(lenses map[string]string, verdict string, dissent any) bool {
	d, isString := dissent.(string)
	if !isString {
		return false
	}
	counts, plurality := tallyOf(lenses)
	votes := 0
	for v, n := range counts {
		if v != "abstain" {
			votes += n
		}
	}
	switch {
	case plurality == "", verdict == plurality:
		return true
	case verdict == "abstain" && counts["abstain"] >= votes:
		return true
	}
	return len(d) >= foragers.MinDissentChars
}

// queenRun is one Queen dispatch over a seeded swarm: her node's status,
// the prompts her backend was sent, the run and its definition.
type queenRun struct {
	status  string
	prompts []string
	store   *db.Store
	runID   int64
	defn    map[string]any
}

// runQueen seeds a swarm whose lenses return the given verdicts, a lens
// whose verdict is "" being rejected, and runs the Queen, her backend
// answering with the replies in turn: the dispatch, then the repair. With
// one reply the repair gets it again.
func runQueen(t *testing.T, lenses map[string]string, replies ...string) queenRun {
	t.Helper()
	var swarm []foragers.Forager
	lensReplies := map[string]string{}
	var rejected []string
	for name, v := range lenses {
		swarm = append(swarm, foragers.Forager{Name: name, Description: name, Body: name})
		if v == "" {
			rejected = append(rejected, name)
			continue
		}
		lensReplies[name] = fmt.Sprintf(`{"verdict":%q,"recommendation":"x"}`, v)
	}
	r := queenRun{store: newTempStore(t)}
	var defn map[string]any
	r.runID, defn, _ = seedSwarmRun(t, r.store, swarm, lensReplies)
	r.defn = defn
	for _, name := range rejected {
		if err := r.store.Workflows().MarkNodeRejected(r.runID, "forager-"+name, "accept rejected", "2026-09-30T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	next, err := workflow.GetNextNodes(r.store.Workflows(), r.runID)
	if err != nil || len(next) != 1 || next[0].Node != "queen" {
		t.Fatalf("next = %v, %v; want queen", next, err)
	}
	var fns []func(RunRequest) (*RunResult, error)
	for _, reply := range replies {
		answer := successResult(reply)
		fns = append(fns, func(req RunRequest) (*RunResult, error) {
			r.prompts = append(r.prompts, req.Prompt)
			return answer(req)
		})
	}
	rc := buildAgentNodeRC(t, r.store, r.runID, defn, &callableBackend{fns: fns}, Config{})
	rc.executeAgentNode(next[0])
	_ = r.store.ReadDB.QueryRow(`SELECT status FROM workflow_node_states WHERE run_id=? AND node_name='queen'`, r.runID).Scan(&r.status)
	return r
}

// Queen's accept: gives the outcome the restated rule gives, on ties,
// abstains, placeholders and null dissents.
func TestQueen_PluralityCoherence_TieAbstainAndPlaceholders(t *testing.T) {
	cases := []struct {
		name    string
		lenses  map[string]string
		verdict string
		dissent any
	}{
		{"tie, no dissent", map[string]string{"a": "support", "b": "oppose"}, "conditional", ""},
		{"abstains outnumber a support, Queen follows it", map[string]string{"a": "abstain", "b": "abstain", "c": "support"}, "support", ""},
		{"abstains outnumber a support, Queen abstains silently", map[string]string{"a": "abstain", "b": "abstain", "c": "support"}, "abstain", ""},
		{"abstains tie a support but not the votes, Queen abstains silently", map[string]string{"a": "abstain", "b": "abstain", "c": "support", "d": "support", "e": "oppose"}, "abstain", ""},
		{"abstains tie the votes, Queen abstains silently", map[string]string{"a": "abstain", "b": "abstain", "c": "abstain", "d": "support", "e": "support", "f": "oppose"}, "abstain", ""},
		{"abstains tie the votes, two lenses rejected, Queen abstains silently", map[string]string{"a": "abstain", "b": "abstain", "c": "support", "d": "support", "e": "", "f": ""}, "abstain", ""},
		{"abstains fall short of a support, Queen abstains silently", map[string]string{"a": "abstain", "b": "support", "c": "support"}, "abstain", ""},
		{"every lens abstains", map[string]string{"a": "abstain", "b": "abstain"}, "abstain", ""},
		{"departs, null dissent", map[string]string{"a": "support", "b": "support", "c": "oppose"}, "oppose", nil},
		{"departs, N/A", map[string]string{"a": "support", "b": "support", "c": "oppose"}, "oppose", "N/A"},
		{"departs, Not applicable", map[string]string{"a": "support", "b": "support", "c": "oppose"}, "oppose", "Not applicable"},
		{"departs with a sentence", map[string]string{"a": "support", "b": "support", "c": "oppose"}, "oppose", "c's measured failure outweighs a and b."},
		{"follows, null dissent", map[string]string{"a": "support", "b": "support", "c": "oppose"}, "support", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := queenAccepts(c.lenses, c.verdict, c.dissent)
			_, plurality := tallyOf(c.lenses)
			if r := runQueen(t, c.lenses, queenReply(c.verdict, c.dissent)); (r.status == "completed") != want {
				t.Errorf("queen is %s with plurality %q, verdict %q, dissent %v; want accepted=%v", r.status, plurality, c.verdict, c.dissent, want)
			}
		})
	}
}

// benchQueenTallies are the five swarm runs the 2026-09-29 quick bench
// screen lost at the Queen: each lens's verdict as the run's
// workflow_node_states holds it, and the verdict the Queen gave with an
// empty dissent, on her dispatch and again on her repair.
var benchQueenTallies = []struct {
	run     string
	lenses  map[string]string
	verdict string
}{
	{"C3 AB-s1-b", map[string]string{"architect": "abstain", "empiricist": "abstain", "pragmatist": "abstain", "scholar": "abstain",
		"skeptic": "oppose", "steward": "abstain", "timekeeper": "abstain"}, "abstain"},
	{"C5 AB-s1-b", map[string]string{"architect": "abstain", "empiricist": "abstain", "pragmatist": "abstain", "scholar": "abstain",
		"skeptic": "oppose", "steward": "abstain", "timekeeper": "abstain"}, "abstain"},
	{"Q4 AB-s1-a", map[string]string{"architect": "abstain", "empiricist": "oppose", "pragmatist": "abstain", "scholar": "abstain",
		"skeptic": "oppose", "steward": "abstain", "timekeeper": "abstain"}, "abstain"},
	{"C2 F6-s1-b", map[string]string{"architect": "support", "empiricist": "support", "pragmatist": "abstain", "scholar": "abstain",
		"skeptic": "oppose", "steward": "support", "timekeeper": "oppose"}, "conditional"},
	{"C1 AB-s1-b", map[string]string{"architect": "abstain", "empiricist": "abstain", "pragmatist": "abstain", "scholar": "abstain",
		"skeptic": "oppose", "steward": "abstain", "timekeeper": "abstain"}, "abstain"},
}

// On the five tallies the bench lost, the Queen's reply is accepted or
// rejected as the restated rule says, with no repair when it is accepted.
func TestQueen_BenchTallies(t *testing.T) {
	for _, c := range benchQueenTallies {
		t.Run(c.run, func(t *testing.T) {
			want := queenAccepts(c.lenses, c.verdict, "")
			r := runQueen(t, c.lenses, queenReply(c.verdict, ""))
			if (r.status == "completed") != want {
				counts, plurality := tallyOf(c.lenses)
				t.Fatalf("queen is %s on %v (plurality %q) answering %s; want accepted=%v", r.status, counts, plurality, c.verdict, want)
			}
			wantCalls := 2 // the dispatch and the repair
			if want {
				wantCalls = 1
			}
			if len(r.prompts) != wantCalls {
				t.Errorf("backend calls = %d, want %d", len(r.prompts), wantCalls)
			}
		})
	}
}

// A Queen who departs from a plurality some lens voted for, with no
// dissent, is rejected however many lenses abstained: a committed verdict
// against it, or an abstain when more lenses voted than abstained, a
// committed majority that splits and a lens with no verdict included.
func TestQueen_CommittedOverrideStillRejected(t *testing.T) {
	c3, c2 := benchQueenTallies[0].lenses, benchQueenTallies[3].lenses
	cases := []struct {
		name    string
		lenses  map[string]string
		verdict string
		dissent string
	}{
		{"abstains outnumber an oppose, Queen supports", c3, "support", ""},
		{"abstains outnumber an oppose, Queen conditional", c3, "conditional", "N/A"},
		{"a support plurality, Queen conditional", c2, "conditional", ""},
		{"a support plurality, Queen abstains with fewer abstains", c2, "abstain", ""},
		{"abstains tie an oppose plurality under a split majority, Queen abstains",
			map[string]string{"a": "oppose", "b": "oppose", "c": "oppose", "d": "conditional", "e": "abstain", "f": "abstain", "g": "abstain"}, "abstain", ""},
		{"two lenses rejected are no abstains, Queen abstains",
			map[string]string{"a": "support", "b": "support", "c": "abstain", "d": "", "e": ""}, "abstain", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if queenAccepts(c.lenses, c.verdict, c.dissent) {
				t.Fatalf("the rule accepts %s on %v; the case is no override", c.verdict, c.lenses)
			}
			if r := runQueen(t, c.lenses, queenReply(c.verdict, c.dissent)); r.status != "rejected" {
				_, plurality := tallyOf(c.lenses)
				t.Errorf("queen is %s answering %s against plurality %q with dissent %q; want rejected", r.status, c.verdict, plurality, c.dissent)
			}
		})
	}
}

// A Queen rejected after her repair leaves both replies for the operator:
// her first in the repair's prompt, her repair's in its row.
func TestQueen_RejectedRepliesAreKept(t *testing.T) {
	c := benchQueenTallies[3]
	first, repair := queenReply(c.verdict, ""), queenReply("oppose", "N/A")
	r := runQueen(t, c.lenses, first, repair)
	if r.status != "rejected" {
		t.Fatalf("queen is %s, want rejected", r.status)
	}
	firstJSON, _ := json.Marshal(workflow.ExtractJSONOutput(first))
	var prompt, repairJSON string
	_ = r.store.ReadDB.QueryRow(`SELECT repair_prompt, COALESCE(repair_outputs_json,'') FROM workflow_repairs WHERE run_id=? AND node_name='queen' AND attempt=1`, r.runID).Scan(&prompt, &repairJSON)
	if !strings.Contains(prompt, "Outputs that failed:\n"+string(firstJSON)) {
		t.Errorf("the repair prompt does not hold the first reply %s", firstJSON)
	}
	var kept, want map[string]any
	_ = json.Unmarshal([]byte(repairJSON), &kept)
	want = workflow.ExtractJSONOutput(repair)
	if kept["verdict"] != want["verdict"] || kept["dissent_from_plurality"] != want["dissent_from_plurality"] {
		t.Errorf("repair_outputs_json %s, want the repair's reply %v", repairJSON, want)
	}
}

// The Queen's repair prompt says in plain words what the plurality was,
// what she answered and how to fix it, with the tally, and says it again
// after her failed reply. It leaves out the predicate, which names state
// she never saw. The repair's failure_reason records the same words for the
// operator, and the predicate after them.
func TestQueen_RepairSaysThePluralityInPlainWords(t *testing.T) {
	c := benchQueenTallies[3]
	counts, plurality := tallyOf(c.lenses)
	first := queenReply(c.verdict, "")
	r := runQueen(t, c.lenses, first)
	if len(r.prompts) != 2 {
		t.Fatalf("backend calls = %d, want the dispatch and one repair", len(r.prompts))
	}
	var failure string
	_ = r.store.ReadDB.QueryRow(`SELECT failure_reason FROM workflow_repairs WHERE run_id=? AND node_name='queen' AND attempt=1`, r.runID).Scan(&failure)
	queen, _ := r.defn["nodes"].(map[string]any)["queen"].(map[string]any)
	var predicate string
	for _, it := range workflow.AcceptItems(queen) {
		if it.Reason != "" {
			predicate = it.Predicate
		}
	}
	if predicate == "" {
		t.Fatal("queen's accept: has no item with a reason")
	}
	if strings.Contains(r.prompts[1], predicate) {
		t.Errorf("the repair prompt shows the predicate %q", predicate)
	}
	if !strings.Contains(failure, predicate) {
		t.Errorf("the repair's failure_reason lacks the predicate: %s", failure)
	}
	firstJSON, _ := json.Marshal(workflow.ExtractJSONOutput(first))
	i := strings.Index(r.prompts[1], string(firstJSON))
	if i < 0 {
		t.Fatalf("the repair prompt does not hold the failed reply %s", firstJSON)
	}
	after := r.prompts[1][i+len(firstJSON):]
	for _, w := range []string{fmt.Sprintf("plurality was %s", plurality), fmt.Sprintf("you answered %s", c.verdict)} {
		if !strings.Contains(after, w) {
			t.Errorf("after the failed reply the repair prompt lacks %q: %q", w, after)
		}
	}
	want := []string{
		fmt.Sprintf("plurality was %s", plurality),
		fmt.Sprintf("you answered %s", c.verdict),
		fmt.Sprintf("Either answer %s", plurality),
		"dissent_from_plurality",
	}
	for v, n := range counts {
		var names []string
		for name, lv := range c.lenses {
			if lv == v {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		want = append(want, fmt.Sprintf("%s %d (%s)", v, n, strings.Join(names, ", ")))
	}
	for _, w := range want {
		if !strings.Contains(r.prompts[1], w) {
			t.Errorf("the repair prompt lacks %q:\n%.600s", w, r.prompts[1])
		}
		if !strings.Contains(failure, w) {
			t.Errorf("the repair's failure_reason lacks %q: %s", w, failure)
		}
	}
}
