package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/hive"
	"github.com/spf13/cobra"
)

// useTestStore points the package's store at a fresh database for one test.
func useTestStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.NewStore(filepath.Join(t.TempDir(), "hive.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	prev := store
	store = s
	t.Cleanup(func() { store = prev })
	return s
}

// execute runs a command and returns what it printed to stdout.
func execute(t *testing.T, root *cobra.Command, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	printed := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		printed <- string(b)
	}()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	runErr := root.Execute()
	w.Close()
	os.Stdout = stdout
	return <-printed, runErr
}

// A failed insert is an error, not "already initialized". Initializing a
// project already on record says so. A second project is
// hive_workspace_test.go's.
func TestHiveInit_InsertFailureIsAnError(t *testing.T) {
	s := useTestStore(t)
	if _, err := s.WriteDB.Exec(`CREATE TRIGGER refuse BEFORE INSERT ON hive_state BEGIN SELECT RAISE(FAIL, 'simulated write failure'); END`); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newHiveCmd(), "init", "--project", "A")
	if err == nil || !strings.Contains(err.Error(), "simulated write failure") {
		t.Errorf("init err = %v, want the insert failure", err)
	}
	if strings.Contains(out, "already initialized") {
		t.Errorf("init printed %q for an insert that failed", out)
	}

	if _, err := s.WriteDB.Exec(`DROP TRIGGER refuse`); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, newHiveCmd(), "init", "--project", "A"); err != nil {
		t.Fatal(err)
	}
	out, err = execute(t, newHiveCmd(), "init", "--project", "A")
	if err != nil || !strings.Contains(out, "already initialized") {
		t.Errorf("second init: out=%q err=%v, want already initialized", out, err)
	}
}

// A malformed --outputs, or a gain parameter that is not a number, fails the
// command before anything changes: the signals the plan read stay pending,
// the phase stays, and batch_size keeps its value.
func TestHiveComplete_BadOutputsChangeNothing(t *testing.T) {
	s := useTestStore(t)
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	res, err := s.WriteDB.Exec(`INSERT INTO signals (signal_type, source_type, payload_json, wave) VALUES ('stop_signal','system','{}',1)`)
	if err != nil {
		t.Fatal(err)
	}
	sigID, _ := res.LastInsertId()
	if _, err := execute(t, newHiveCmd(), "next", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	type snapshot struct {
		phase string
		batch int
		acted int
	}
	read := func() snapshot {
		t.Helper()
		var v snapshot
		if err := s.ReadDB.QueryRow(`SELECT phase, batch_size FROM hive_state WHERE project='p'`).Scan(&v.phase, &v.batch); err != nil {
			t.Fatal(err)
		}
		if err := s.ReadDB.QueryRow(`SELECT acted_on FROM signals WHERE id=?`, sigID).Scan(&v.acted); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := read()
	for _, outputs := range []string{
		fmt.Sprintf(`{"params":{"batch_size":%d}`, before.batch+1),
		`{"params":{"batch_size":"7"}}`,
	} {
		if _, err := execute(t, newHiveCmd(), "complete", "--project", "p", "--action", "x", "--outputs", outputs); err == nil {
			t.Errorf("complete --outputs %s succeeded, want an error", outputs)
		}
		if after := read(); after != before {
			t.Errorf("complete --outputs %s changed %+v to %+v", outputs, before, after)
		}
	}
}

// `db-write resolve_conflict` closes a conflict once, recording the
// resolution and the wave.
func TestDBWriteResolveConflict(t *testing.T) {
	s := useTestStore(t)
	var ids [2]int64
	for i := range ids {
		res, err := s.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, mss_label, finding) VALUES (1,'t',0,'definition',?)`, fmt.Sprint("claim ", i))
		if err != nil {
			t.Fatal(err)
		}
		ids[i], _ = res.LastInsertId()
	}
	if err := s.Conflicts().AddConflict(1, ids[0], ids[1], "[negation] claims"); err != nil {
		t.Fatal(err)
	}
	var conflictID int64
	if err := s.ReadDB.QueryRow(`SELECT id FROM conflicts`).Scan(&conflictID); err != nil {
		t.Fatal(err)
	}
	arg := fmt.Sprintf(`{"conflict_id":%d,"wave":3,"resolution":"finding %d survives"}`, conflictID, ids[0])
	if _, err := execute(t, newDBWriteCmd(), "resolve_conflict", arg); err != nil {
		t.Fatalf("resolve_conflict: %v", err)
	}
	var resolution string
	var wave int
	if err := s.ReadDB.QueryRow(`SELECT resolution, resolved_by_wave FROM conflicts WHERE id=?`, conflictID).Scan(&resolution, &wave); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("finding %d survives", ids[0]); resolution != want || wave != 3 {
		t.Errorf("resolution=%q wave=%d, want %q and 3", resolution, wave, want)
	}
	if _, err := execute(t, newDBWriteCmd(), "resolve_conflict", arg); err == nil || !strings.Contains(err.Error(), "already resolved") {
		t.Errorf("second resolve_conflict: err = %v, want already resolved", err)
	}
}

// A plan with no actions prints "actions": [], not null. Here the only
// candidate action is the gap's dispatch, and a pending stop_signal at the
// gap's coordinates suppresses it; the critical gap withholds run_gate.
func TestHiveNext_EmptyPlanIsAnArray(t *testing.T) {
	s := useTestStore(t)
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	d := [4]int{0, 1, 0, 0}
	if err := s.Gaps().AddGap(1, "t", "a critical gap", "critical", &d[0], &d[1], &d[2], &d[3]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDB.Exec(`INSERT INTO signals (signal_type, source_type, target_d1, target_d2, target_d3, target_d4, payload_json, wave)
		VALUES ('stop_signal','system',?,?,?,?,'{}',1)`, d[0], d[1], d[2], d[3]); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newHiveCmd(), "next", "--project", "p")
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Actions json.RawMessage `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("hive next printed %q: %v", out, err)
	}
	if got := strings.Join(strings.Fields(string(plan.Actions)), ""); got != "[]" {
		t.Errorf(`"actions" = %s, want []`, plan.Actions)
	}
}

// The plan dispatches to the latest wave plus one. A latest wave that cannot
// be incremented is refused before the scan records its signals or advances
// the iteration, rather than wrap to a negative dispatch wave.
func TestHiveNext_RefusesAWaveThatCannotBeIncremented(t *testing.T) {
	s := useTestStore(t)
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	// Five unknowns in the latest wave raise a shaking signal.
	d := 0
	for i := 0; i < 5; i++ {
		if _, err := s.Findings().AddFinding(&db.Finding{Wave: math.MaxInt, Agent: "a", MSSLabel: "unknown", Finding: "x", D1: &d}); err != nil {
			t.Fatal(err)
		}
	}
	state, err := hive.ScanState(s, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(hive.EvaluateSignals(s, state)) == 0 {
		t.Fatal("setup: no signal fires, so a recorded signal could not be seen")
	}

	_, err = execute(t, newHiveCmd(), "next", "--project", "p")
	if err == nil || !strings.Contains(err.Error(), "cannot be incremented") {
		t.Fatalf("hive next err = %v, want the wave refused", err)
	}
	var signals, iteration int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM signals`).Scan(&signals); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT iteration FROM hive_state WHERE project = 'p'`).Scan(&iteration); err != nil {
		t.Fatal(err)
	}
	if signals != 0 || iteration != state.Hive.Iteration {
		t.Fatalf("after the refusal: %d signal(s) recorded, iteration %d; want 0 and %d", signals, iteration, state.Hive.Iteration)
	}
}

// `chb hive next` executes cap_finding and adjust_params only with --apply.
// The seed raises both: a converged assumption for quorum, and a latest wave
// that unknowns dominate for a tier upgrade. Without --apply the output has
// no caps, nothing is capped and model_tier stays. With it the candidate is
// capped and stays an assumption, and model_tier moves up one tier. A second
// --apply has nothing to cap and prints "caps": [].
func TestHiveNext_ApplyCapsAndAdjustsOnlyWithApply(t *testing.T) {
	s := useTestStore(t)
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	var threshold int
	var tier string
	if err := s.ReadDB.QueryRow(`SELECT convergence_threshold, model_tier FROM hive_state WHERE project='p'`).Scan(&threshold, &tier); err != nil {
		t.Fatal(err)
	}
	res, err := s.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding, convergence_level, convergence_count)
		VALUES (1,'a',0,0,0,0,'assumption','the site is good','high',?)`, threshold)
	if err != nil {
		t.Fatal(err)
	}
	candidate, _ := res.LastInsertId()
	for i := 0; i < 5; i++ {
		if _, err := s.WriteDB.Exec(`INSERT INTO findings (wave, agent, d1, d2, mss_label, finding) VALUES (1,'a',1,?,'unknown','no answer')`, i); err != nil {
			t.Fatal(err)
		}
	}
	nextTier := tier
	tiers := hive.ModelTiers()
	for i, name := range tiers {
		if name == tier && i+1 < len(tiers) {
			nextTier = tiers[i+1]
		}
	}
	if nextTier == tier {
		t.Fatalf("setup: model_tier %s has no tier above it", tier)
	}

	type stored struct {
		label string
		caps  int
		tier  string
	}
	read := func() stored {
		t.Helper()
		var v stored
		if err := s.ReadDB.QueryRow(`SELECT mss_label, (SELECT COUNT(*) FROM capped_findings WHERE finding_id = f.id) FROM findings f WHERE id=?`, candidate).Scan(&v.label, &v.caps); err != nil {
			t.Fatal(err)
		}
		if err := s.ReadDB.QueryRow(`SELECT model_tier FROM hive_state WHERE project='p'`).Scan(&v.tier); err != nil {
			t.Fatal(err)
		}
		return v
	}
	next := func(args ...string) (map[string]json.RawMessage, []hive.Action) {
		t.Helper()
		out, err := execute(t, newHiveCmd(), append([]string{"next", "--project", "p"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]json.RawMessage
		var plan []hive.Action
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("hive next printed %q: %v", out, err)
		}
		if err := json.Unmarshal(v["actions"], &plan); err != nil {
			t.Fatalf("actions %s: %v", v["actions"], err)
		}
		return v, plan
	}

	out, plan := next()
	planned := map[string]bool{}
	for _, a := range plan {
		if a.Type == "cap_finding" && a.FindingID == candidate {
			planned["cap"] = true
		}
		if a.Type == "adjust_params" && a.Params["model_tier"] == nextTier {
			planned["tier"] = true
		}
	}
	if !planned["cap"] || !planned["tier"] {
		t.Fatalf("setup: the plan %+v must cap finding %d and move model_tier to %s", plan, candidate, nextTier)
	}
	if _, ok := out["caps"]; ok {
		t.Errorf("without --apply the output has caps: %s", out["caps"])
	}
	if got, want := read(), (stored{"assumption", 0, tier}); got != want {
		t.Fatalf("without --apply: %+v, want %+v", got, want)
	}

	out, _ = next("--apply")
	var caps []hive.CapResult
	if err := json.Unmarshal(out["caps"], &caps); err != nil {
		t.Fatalf("caps %s: %v", out["caps"], err)
	}
	if len(caps) != 1 || caps[0].FindingID != candidate || !caps[0].Capped {
		t.Fatalf("caps %+v, want finding %d capped", caps, candidate)
	}
	if got, want := read(), (stored{"assumption", 1, nextTier}); got != want {
		t.Fatalf("with --apply: %+v, want %+v", got, want)
	}

	out, _ = next("--apply")
	if got := strings.Join(strings.Fields(string(out["caps"])), ""); got != "[]" {
		t.Errorf(`with nothing to cap, "caps" = %s, want []`, out["caps"])
	}
}

// chb hive next, complete and reset move the hive's loop, which the hive
// workflow's own command nodes run. In a process a model drives, where
// HIVE_AGENT_DB names its run's database, each refuses that database,
// naming the variable, and the iteration and phase stay as they were. A
// database the model's run does not use, as a replay's scratch one, is
// still the verbs' to move.
func TestHiveLoopVerbs_RefuseTheirModelsRun(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(t.TempDir(), "absent-models.yaml")
	for _, args := range [][]string{{"hive", "init", "--project", "demo"}, {"hive", "next", "--project", "demo"}} {
		if out, err := runChb(t, dir, models, nil, args...); err != nil {
			t.Fatalf("chb %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	state := func() (int, string) {
		t.Helper()
		s, err := db.NewStore(filepath.Join(dir, "workspace", "hive.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var n int
		var phase string
		if err := s.ReadDB.QueryRow(`SELECT iteration, phase FROM hive_state WHERE project='demo'`).Scan(&n, &phase); err != nil {
			t.Fatal(err)
		}
		return n, phase
	}
	n, phase := state()
	runDB := "HIVE_AGENT_DB=" + filepath.Join(dir, "workspace", "hive.db")
	for _, args := range [][]string{
		{"hive", "next", "--project", "demo"},
		{"hive", "complete", "--project", "demo", "--action", "all"},
		{"hive", "reset", "--project", "demo"},
	} {
		out, err := runChb(t, dir, models, []string{runDB}, args...)
		if err == nil || !strings.Contains(out, "HIVE_AGENT_DB") {
			t.Errorf("chb %s in its run's model's shell: err %v, output %q; want a refusal naming HIVE_AGENT_DB", strings.Join(args, " "), err, out)
		}
	}
	if n2, phase2 := state(); n2 != n || phase2 != phase {
		t.Errorf("after the refusals the hive is at iteration %d, phase %s; want %d, %s as before", n2, phase2, n, phase)
	}
	other := "HIVE_AGENT_DB=" + filepath.Join(t.TempDir(), "another-run.db")
	if out, err := runChb(t, dir, models, []string{other}, "hive", "next", "--project", "demo"); err != nil {
		t.Fatalf("chb hive next under another run's model: %v\n%s", err, out)
	}
	if n2, _ := state(); n2 != n+1 {
		t.Errorf("under another run's model chb hive next left the iteration at %d, want %d", n2, n+1)
	}
}

// hiveLoopRow is what a reset writes: the project's count and phase, the
// pending signals and the project's recorded passes.
type hiveLoopRow struct {
	iteration int
	phase     string
	pending   int
	passes    int
}

// readHiveLoop reads project's hiveLoopRow from s.
func readHiveLoop(t *testing.T, s *db.Store, project string) hiveLoopRow {
	t.Helper()
	var h hiveLoopRow
	if err := s.ReadDB.QueryRow(`SELECT iteration, phase FROM hive_state WHERE project = ?`, project).Scan(&h.iteration, &h.phase); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM signals WHERE acted_on = 0`).Scan(&h.pending); err != nil {
		t.Fatal(err)
	}
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM hive_iterations WHERE project = ?`, project).Scan(&h.passes); err != nil {
		t.Fatal(err)
	}
	return h
}

// A reset that fails partway changes nothing: with the pass history's delete
// refused after the count and the pending signals are written, the hive
// keeps its count, phase, pending signal and pass history. Once the delete
// is allowed, the same reset clears all four.
func TestHiveReset_AFailurePartwayLeavesTheHiveAsItWas(t *testing.T) {
	s := useTestStore(t)
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, newHiveCmd(), "next", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteDB.Exec(`INSERT INTO signals (signal_type, source_type, payload_json, wave) VALUES ('stop_signal','system','{}',1)`); err != nil {
		t.Fatal(err)
	}
	before := readHiveLoop(t, s, "p")
	if before.iteration != 1 || before.phase == "scanning" || before.pending == 0 || before.passes != 1 {
		t.Fatalf("fixture: %+v; want one recorded pass past scanning and a pending signal", before)
	}
	if _, err := s.WriteDB.Exec(`CREATE TRIGGER refuse BEFORE DELETE ON hive_iterations BEGIN SELECT RAISE(FAIL, 'simulated write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, newHiveCmd(), "reset", "--project", "p"); err == nil || !strings.Contains(err.Error(), "simulated write failure") {
		t.Fatalf("reset err = %v, want the delete's failure", err)
	}
	if after := readHiveLoop(t, s, "p"); after != before {
		t.Fatalf("after a failed reset the hive is %+v; want %+v", after, before)
	}

	if _, err := s.WriteDB.Exec(`DROP TRIGGER refuse`); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, newHiveCmd(), "reset", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	if got := readHiveLoop(t, s, "p"); got != (hiveLoopRow{phase: "scanning"}) {
		t.Fatalf("after the reset the hive is %+v; want count 0, scanning, nothing pending, no recorded pass", got)
	}
}
