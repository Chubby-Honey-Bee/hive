package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

type hiveRow struct {
	iteration      int
	phase          string
	signals        int
	pendingSignals int
}

func readHiveRow(t *testing.T, s *db.Store) hiveRow {
	t.Helper()
	var r hiveRow
	if err := s.ReadDB.QueryRow(`SELECT iteration, phase FROM hive_state WHERE project='p'`).Scan(&r.iteration, &r.phase); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(acted_on = 0), 0) FROM signals`).Scan(&r.signals, &r.pendingSignals); err != nil {
		t.Fatal(err)
	}
	return r
}

// A hive at its cap is not scanned: `hive next --max-iterations` prints
// phase capped with the stored count and records nothing, so a new run on a
// capped hive does no pass and the count never passes the cap.
func TestHiveNext_CappedRecordsNothing(t *testing.T) {
	s := useTestStore(t)
	const limit = 2
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDB.Exec(`UPDATE hive_state SET iteration=? WHERE project='p'`, limit); err != nil {
		t.Fatal(err)
	}
	d := 0
	if err := s.Gaps().AddGap(1, "t", "open question", "critical", &d, &d, &d, &d); err != nil {
		t.Fatal(err)
	}
	before := readHiveRow(t, s)
	out, err := execute(t, newHiveCmd(), "next", "--project", "p", "--apply", "--max-iterations", fmt.Sprint(limit))
	if err != nil {
		t.Fatal(err)
	}
	got := decodeObject(t, "hive next --max-iterations", out)
	if got["phase"] != "capped" || got["iteration"] != float64(limit) || got["max_iterations"] != float64(limit) {
		t.Fatalf("next = %s, want phase capped at iteration %d", out, limit)
	}
	for _, k := range []string{"open_actions", "research_actions"} {
		if open, ok := got[k].([]any); !ok || len(open) != 0 {
			t.Fatalf("%s = %v, want [] so a command node reading it finds the key", k, got[k])
		}
	}
	if got["gate_requested"] != false || got["gate_wave"] != 0.0 {
		t.Fatalf("gate_requested %v, gate_wave %v; want false and 0", got["gate_requested"], got["gate_wave"])
	}
	if after := readHiveRow(t, s); after != before {
		t.Fatalf("a capped scan changed the hive: %+v, want %+v", after, before)
	}

	// Below the cap the same hive is scanned and counted.
	out, err = execute(t, newHiveCmd(), "next", "--project", "p", "--max-iterations", fmt.Sprint(limit+1))
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeObject(t, "hive next", out); got["phase"] != "dispatching" || got["iteration"] != float64(limit+1) {
		t.Fatalf("next below the cap = %s, want dispatching at iteration %d", out, limit+1)
	}
}

// Under --apply, open_actions is the plan less the four kinds --apply ran:
// what is left for an agent. research_actions are its dispatch_agent
// actions, less the model field nothing reads, and gate_requested and
// gate_wave its run_gate. A plan of only applied kinds needs no model call.
func TestHiveNext_ApplyListsOpenActions(t *testing.T) {
	s := useTestStore(t)
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	// More open gaps than the stored baseline plus batch_size fires a
	// tremble dance, whose adjust_params --apply runs.
	var batch int
	if err := s.ReadDB.QueryRow(`SELECT batch_size FROM hive_state WHERE project='p'`).Scan(&batch); err != nil {
		t.Fatal(err)
	}
	d := 0
	for i := 0; i <= batch; i++ {
		if err := s.Gaps().AddGap(1, "t", fmt.Sprintf("question %d", i), "important", &d, &d, &d, &d); err != nil {
			t.Fatal(err)
		}
	}
	out, err := execute(t, newHiveCmd(), "next", "--project", "p", "--apply")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Actions         []map[string]any `json:"actions"`
		OpenActions     []map[string]any `json:"open_actions"`
		ResearchActions []map[string]any `json:"research_actions"`
		GateRequested   bool             `json:"gate_requested"`
		GateWave        float64          `json:"gate_wave"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	var want, research []map[string]any
	applied, gate, gateWave := 0, false, 0.0
	for _, a := range got.Actions {
		switch a["type"] {
		case "fix_mss", "cascade_revert", "cap_finding", "adjust_params":
			applied++
			continue
		case "dispatch_agent":
			r := map[string]any{}
			for k, v := range a {
				if k != "model" {
					r[k] = v
				}
			}
			research = append(research, r)
		case "run_gate":
			// wave is left out of the action when it is 0.
			gate = true
			gateWave, _ = a["wave"].(float64)
		}
		want = append(want, a)
	}
	if applied == 0 || len(research) == 0 || !gate {
		t.Fatalf("setup: the plan needs an action --apply runs, a dispatch and a gate: %s", out)
	}
	if fmt.Sprint(got.OpenActions) != fmt.Sprint(want) {
		t.Fatalf("open_actions = %v, want the plan less what --apply ran: %v", got.OpenActions, want)
	}
	if fmt.Sprint(got.ResearchActions) != fmt.Sprint(research) {
		t.Fatalf("research_actions = %v, want the dispatch_agent actions without model: %v", got.ResearchActions, research)
	}
	if got.GateRequested != gate || got.GateWave != gateWave {
		t.Fatalf("gate_requested %v, gate_wave %v; want %v and %v", got.GateRequested, got.GateWave, gate, gateWave)
	}
}

// `hive complete --expect-iteration N` refuses, before any write, a hive
// whose count moved during the pass: an agent that ran `hive next` itself
// would otherwise count two iterations for one pass, silently.
func TestHiveComplete_ExpectIterationRefusesAMovedCount(t *testing.T) {
	s := useTestStore(t)
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, newHiveCmd(), "next", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	scanned := readHiveRow(t, s).iteration
	if _, err := s.WriteDB.Exec(`INSERT INTO signals (signal_type, acted_on) VALUES ('stop_signal', 0)`); err != nil {
		t.Fatal(err)
	}
	// Something else scans during the pass.
	if _, err := execute(t, newHiveCmd(), "next", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	before := readHiveRow(t, s)
	_, err := execute(t, newHiveCmd(), "complete", "--project", "p", "--action", "all",
		"--expect-iteration", fmt.Sprint(scanned), "--max-iterations", "5", "--json")
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("recorded %d", scanned)) {
		t.Fatalf("complete = %v, want a refusal naming the scanned iteration %d", err, scanned)
	}
	if after := readHiveRow(t, s); after != before {
		t.Fatalf("a refused complete changed the hive: %+v, want %+v", after, before)
	}

	// With the count the scan recorded, it completes.
	if _, err := execute(t, newHiveCmd(), "complete", "--project", "p", "--action", "all",
		"--expect-iteration", fmt.Sprint(before.iteration)); err != nil {
		t.Fatal(err)
	}
}

// Two passes in a row that change nothing stop the loop below the cap, with
// the reason. A pass that adds a finding keeps it going.
func TestHiveComplete_StallStopsTheLoop(t *testing.T) {
	s := useTestStore(t)
	const limit = 10
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	pass := func(change bool) map[string]any {
		t.Helper()
		if _, err := execute(t, newHiveCmd(), "next", "--project", "p"); err != nil {
			t.Fatal(err)
		}
		if change {
			seedFindings(t, s, fmt.Sprintf("finding %d", readHiveRow(t, s).iteration))
		}
		out, err := execute(t, newHiveCmd(), "complete", "--project", "p", "--action", "all",
			"--max-iterations", fmt.Sprint(limit), "--json")
		if err != nil {
			t.Fatal(err)
		}
		return decodeObject(t, "hive complete --json", out)
	}
	for i, change := range []bool{true, false, true, false} {
		if got := pass(change); got["should_continue"] != true || got["stop_reason"] != "" {
			t.Fatalf("pass %d (change %v): %v, want should_continue with no stop_reason", i+1, change, got)
		}
	}
	got := pass(false)
	why, _ := got["stop_reason"].(string)
	if got["should_continue"] != false || !strings.Contains(why, "stalled") {
		t.Fatalf("after two unchanged passes: %v, want should_continue false, stalled", got)
	}
}
