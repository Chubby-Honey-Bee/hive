package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/hive"
)

// decodeObject parses a command's whole stdout as one JSON object, which is
// what a workflow command node requires of it.
func decodeObject(t *testing.T, cmd, out string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil || obj == nil {
		t.Fatalf("%s printed %q, not one JSON object: %v", cmd, out, err)
	}
	return obj
}

func TestHiveInit_JSON(t *testing.T) {
	s := useTestStore(t)
	for _, wantCreated := range []bool{true, false} {
		out, err := execute(t, newHiveCmd(), "init", "--project", "p", "--json")
		if err != nil {
			t.Fatal(err)
		}
		got := decodeObject(t, "hive init --json", out)
		var phase string
		var iteration int
		if err := s.ReadDB.QueryRow(`SELECT phase, iteration FROM hive_state WHERE project='p'`).Scan(&phase, &iteration); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"project": "p", "created": wantCreated, "phase": phase, "iteration": float64(iteration), "db": s.Path}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("init --json = %v, want %v", got, want)
		}
	}
}

// should_continue is the loop rule computed from the stored counter: another
// scan while the hive has run fewer iterations than the cap.
func TestHiveComplete_JSONComputesShouldContinue(t *testing.T) {
	s := useTestStore(t)
	const maxIterations = 2
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	for scan := 1; scan <= maxIterations+1; scan++ {
		if _, err := execute(t, newHiveCmd(), "next", "--project", "p"); err != nil {
			t.Fatal(err)
		}
		out, err := execute(t, newHiveCmd(), "complete", "--project", "p", "--action", "all",
			"--max-iterations", fmt.Sprint(maxIterations), "--json")
		if err != nil {
			t.Fatal(err)
		}
		got := decodeObject(t, "hive complete --json", out)
		var iteration int
		if err := s.ReadDB.QueryRow(`SELECT iteration FROM hive_state WHERE project='p'`).Scan(&iteration); err != nil {
			t.Fatal(err)
		}
		if got["iteration"] != float64(iteration) || got["should_continue"] != (iteration < maxIterations) || got["phase"] != "scanning" {
			t.Fatalf("scan %d: complete --json = %v, want iteration %d, should_continue %v, phase scanning",
				scan, got, iteration, iteration < maxIterations)
		}
	}

	// Without a cap there is no rule to compute, so no should_continue.
	out, err := execute(t, newHiveCmd(), "complete", "--project", "p", "--action", "all", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if _, has := decodeObject(t, "hive complete --json", out)["should_continue"]; has {
		t.Fatalf("complete --json without --max-iterations reported should_continue: %s", out)
	}
	if _, err := execute(t, newHiveCmd(), "complete", "--project", "p", "--action", "all", "--max-iterations", "0", "--json"); err == nil {
		t.Fatal("--max-iterations 0 was accepted")
	}
}

// What a pass wrote, read from the database, beside what its agent
// reported: findings_added and gaps_resolved count the pass's changes, and
// changed says whether its end differs from its start. Each pass here adds
// a number of findings and closes a number of gaps, and the expected counts
// are those numbers. A pass no scan recorded has no start, so none of the
// three is printed.
func TestHiveComplete_JSONReportsWhatThePassWrote(t *testing.T) {
	s := useTestStore(t)
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	d := 0
	for i := 0; i < 3; i++ {
		if err := s.Gaps().AddGap(1, "t", fmt.Sprintf("question %d", i), "minor", &d, &d, &d, &d); err != nil {
			t.Fatal(err)
		}
	}
	var gapIDs []int64
	rows, err := s.ReadDB.Query(`SELECT id FROM gaps ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		gapIDs = append(gapIDs, id)
	}
	rows.Close()

	for i, pass := range []struct{ findings, resolves int }{{2, 1}, {0, 0}, {1, 2}} {
		if _, err := execute(t, newHiveCmd(), "next", "--project", "p"); err != nil {
			t.Fatal(err)
		}
		var last int64
		for f := 0; f < pass.findings; f++ {
			if last, err = s.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "t", MSSLabel: "assumption",
				Finding: fmt.Sprintf("pass %d finding %d", i, f), D1: &d, D2: &d, D3: &d, D4: &d}); err != nil {
				t.Fatal(err)
			}
		}
		for r := 0; r < pass.resolves; r++ {
			if last == 0 {
				if last, err = s.Findings().AddFinding(&db.Finding{Wave: 1, Agent: "t", MSSLabel: "assumption",
					Finding: "an answer", D1: &d, D2: &d, D3: &d, D4: &d}); err != nil {
					t.Fatal(err)
				}
				pass.findings++
			}
			if err := s.Gaps().ResolveGap(gapIDs[0], 1, "t", last); err != nil {
				t.Fatal(err)
			}
			gapIDs = gapIDs[1:]
		}
		out, err := execute(t, newHiveCmd(), "complete", "--project", "p", "--action", "all", "--json")
		if err != nil {
			t.Fatal(err)
		}
		got := decodeObject(t, "hive complete --json", out)
		changed := pass.findings > 0 || pass.resolves > 0
		if got["findings_added"] != float64(pass.findings) || got["gaps_resolved"] != float64(pass.resolves) || got["changed"] != changed {
			t.Fatalf("pass %d (%+v): complete --json = %v, want findings_added %d, gaps_resolved %d, changed %v",
				i+1, pass, got, pass.findings, pass.resolves, changed)
		}
	}

	// A pass no scan recorded: completed again with no scan before it.
	if _, err := s.WriteDB.Exec(`DELETE FROM hive_iterations`); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newHiveCmd(), "complete", "--project", "p", "--action", "all", "--json")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeObject(t, "hive complete --json", out)
	for _, k := range []string{"findings_added", "gaps_resolved", "changed"} {
		if _, has := got[k]; has {
			t.Fatalf("complete --json for a pass no scan recorded printed %s: %v", k, got)
		}
	}
}

func TestHiveStatus_JSONIsOneObject(t *testing.T) {
	s := useTestStore(t)
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	d := 0
	if err := s.Gaps().AddGap(1, "t", "open question", "critical", &d, &d, &d, &d); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newHiveCmd(), "status", "--project", "p", "--json")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeObject(t, "hive status --json", out)
	state, err := hive.ScanState(s, "p")
	if err != nil {
		t.Fatal(err)
	}
	terminal, reason := hive.CheckTermination(state)
	want := map[string]any{
		"phase":           state.Hive.Phase,
		"iteration":       float64(state.Hive.Iteration),
		"unresolved_gaps": float64(len(state.UnresolvedGaps)),
		"critical_gaps":   float64(len(state.CriticalGaps)),
		"is_terminal":     terminal,
		"reason":          reason,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("status --json %s = %v, want %v", k, got[k], v)
		}
	}
}

// `hive report --wave W` lists that wave's findings and counts them, and
// lists the sources recorded at the wave with their validation, which the
// hive's gate evaluator reads. Without --wave it lists every finding and no
// sources. The expected lists come from what was seeded at each wave.
func TestHiveReport_Wave(t *testing.T) {
	s := useTestStore(t)
	if _, err := execute(t, newHiveCmd(), "init", "--project", "p"); err != nil {
		t.Fatal(err)
	}
	perWave := map[int]int{1: 2, 2: 3}
	for wave, n := range perWave {
		for i := 0; i < n; i++ {
			if _, err := s.WriteDB.Exec(
				`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding) VALUES (?, 'a', 0, 0, 0, ?, 'assumption', ?)`,
				wave, i, fmt.Sprintf("wave %d finding %d", wave, i),
			); err != nil {
				t.Fatal(err)
			}
		}
	}
	sources := []struct {
		url    string
		wave   int
		status string // "" leaves the default
	}{
		{"https://example.com/live", 1, "live"},
		{"https://example.com/new", 1, ""},
		{"https://example.com/other", 2, "dead"},
	}
	for _, src := range sources {
		if _, err := s.WriteDB.Exec(`INSERT INTO sources (url, wave) VALUES (?, ?)`, src.url, src.wave); err != nil {
			t.Fatal(err)
		}
		if src.status != "" {
			if _, err := s.WriteDB.Exec(`UPDATE sources SET validation_status=? WHERE url=?`, src.status, src.url); err != nil {
				t.Fatal(err)
			}
		}
	}
	var unchecked string
	if err := s.ReadDB.QueryRow(`SELECT validation_status FROM sources WHERE url='https://example.com/new'`).Scan(&unchecked); err != nil {
		t.Fatal(err)
	}

	const wave = 1
	out, err := execute(t, newHiveCmd(), "report", "--project", "p", "--wave", fmt.Sprint(wave))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Total    int `json:"findings_total"`
		Findings []struct {
			Wave int `json:"wave"`
		} `json:"findings"`
		Wave    *int `json:"wave"`
		Sources []struct {
			URL    string `json:"url"`
			Status string `json:"validation_status"`
		} `json:"wave_sources"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != perWave[wave] || len(got.Findings) != perWave[wave] || got.Wave == nil || *got.Wave != wave {
		t.Fatalf("report --wave %d: %d findings of %d, wave %v; want %d", wave, len(got.Findings), got.Total, got.Wave, perWave[wave])
	}
	for _, f := range got.Findings {
		if f.Wave != wave {
			t.Fatalf("report --wave %d listed a finding of wave %d", wave, f.Wave)
		}
	}
	var want []string
	for _, src := range sources {
		if src.wave == wave {
			status := src.status
			if status == "" {
				status = unchecked
			}
			want = append(want, src.url+" "+status)
		}
	}
	var listed []string
	for _, src := range got.Sources {
		listed = append(listed, src.URL+" "+src.Status)
	}
	if fmt.Sprint(listed) != fmt.Sprint(want) {
		t.Fatalf("wave_sources = %v, want %v", listed, want)
	}

	out, err = execute(t, newHiveCmd(), "report", "--project", "p")
	if err != nil {
		t.Fatal(err)
	}
	all := decodeObject(t, "hive report", out)
	if _, has := all["wave_sources"]; has || all["findings_total"] != float64(perWave[1]+perWave[2]) {
		t.Fatalf("report without --wave = findings_total %v, wave_sources present %v; want every finding and no sources",
			all["findings_total"], has)
	}
}

func seedFindings(t *testing.T, s *db.Store, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if _, err := s.WriteDB.Exec(
			`INSERT INTO findings (wave, agent, d1, d2, d3, d4, mss_label, finding) VALUES (1, ?, 0, 0, 0, 0, 'definition', ?)`,
			"agent-"+text, text,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSwarmMergeAndDetectConflicts_JSON(t *testing.T) {
	s := useTestStore(t)
	seedFindings(t, s, "the cache is enabled by default", "the cache is not enabled by default")
	var findings int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM findings`).Scan(&findings); err != nil {
		t.Fatal(err)
	}

	out, err := execute(t, newSwarmMergeCmd(), "--json")
	if err != nil {
		t.Fatal(err)
	}
	merge := decodeObject(t, "swarm-merge --json", out)
	if merge["total_findings"] != float64(findings) {
		t.Fatalf("swarm-merge --json total_findings = %v, want %d", merge["total_findings"], findings)
	}
	for _, k := range []string{"coordinate_groups", "near_duplicates", "convergence_updates", "conflicts_detected"} {
		if _, ok := merge[k]; !ok {
			t.Fatalf("swarm-merge --json lacks %s: %s", k, out)
		}
	}

	var before int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM conflicts`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	out, err = execute(t, newDetectConflictsCmd(), "--json")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeObject(t, "detect-conflicts --json", out)
	var after int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM conflicts`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	details, _ := got["details"].([]any)
	if got["new"] != float64(after-before) || got["dry_run"] != false || got["total"] != float64(len(details)) {
		t.Fatalf("detect-conflicts --json = %s, want new %d (rows written) and total = len(details)", out, after-before)
	}
}

// guard --eval-stdin derives the verdict from the gaps and the scores, and
// records the gaps as important gaps. --json prints one object, and the exit
// status still says whether the gate opened.
func TestGuard_EvalStdinDerivesTheVerdict(t *testing.T) {
	s := useTestStore(t)
	seedFindings(t, s, "the gate reads wave one")

	run := func(stdin string, args ...string) (map[string]any, error) {
		t.Helper()
		cmd := newGuardCmd()
		cmd.SetIn(strings.NewReader(stdin))
		out, err := execute(t, cmd, args...)
		if out == "" {
			return nil, err
		}
		return decodeObject(t, "guard --json", out), err
	}
	countGaps := func() int {
		t.Helper()
		var n int
		if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM gaps WHERE agent='evaluator' AND priority='important' AND wave=1`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	lastVerdict := func() string {
		t.Helper()
		var v string
		if err := s.ReadDB.QueryRow(`SELECT verdict FROM evaluations WHERE wave=1 ORDER BY id DESC LIMIT 1`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	// A gap the wave already holds open, word for word, is not written
	// again, so the third evaluation writes one gap of its two.
	cases := []struct {
		scores int
		gaps   []string
	}{
		{4, nil},
		{5, []string{"which regions?", "what does it cost?"}},
		{5, []string{"which regions?", "who runs it?", "who runs it?"}},
		{3, nil},
	}
	open := map[string]bool{}
	for _, c := range cases {
		gaps := c.gaps
		if gaps == nil {
			gaps = []string{}
		}
		in, _ := json.Marshal(map[string]any{"coverage": c.scores, "depth": c.scores, "sources": c.scores, "actionability": c.scores, "gaps": gaps})
		wantVerdict := "NEEDS_MORE_WORK"
		if c.scores >= 4 && len(gaps) == 0 {
			wantVerdict = "COMPLETE"
		}
		wantWritten := 0
		for _, g := range gaps {
			if !open[g] {
				open[g] = true
				wantWritten++
			}
		}
		gapsBefore := countGaps()
		got, err := run(string(in), "--wave", "1", "--eval-stdin", "--json", "--force")
		if got == nil {
			t.Fatalf("%s: no JSON printed (err %v)", in, err)
		}
		if got["verdict"] != wantVerdict || lastVerdict() != wantVerdict {
			t.Fatalf("%s: verdict printed %v, recorded %s, want %s", in, got["verdict"], lastVerdict(), wantVerdict)
		}
		if got["gaps_written"] != float64(wantWritten) || countGaps()-gapsBefore != wantWritten {
			t.Fatalf("%s: gaps_written %v, rows %d, want %d", in, got["gaps_written"], countGaps()-gapsBefore, wantWritten)
		}
		// Every other gate check passes on this wave (it has a finding, no
		// conflict and a clean audit), so the verdict alone decides.
		opened, _ := got["opened"].(bool)
		if opened != (err == nil) || opened != (wantVerdict == "COMPLETE") {
			t.Fatalf("%s: opened %v with exit error %v, want opened iff the verdict is COMPLETE", in, got["opened"], err)
		}
	}

	// A stated verdict, or both evaluation sources, are refused before
	// anything is written.
	gapsBefore := countGaps()
	var evals int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM evaluations`).Scan(&evals); err != nil {
		t.Fatal(err)
	}
	if _, err := run(`{"coverage":5,"depth":5,"sources":5,"actionability":5,"gaps":["x"],"verdict":"COMPLETE"}`,
		"--wave", "1", "--eval-stdin", "--json"); err == nil {
		t.Fatal("an evaluation stating its own verdict was accepted")
	}
	if _, err := run(`{}`, "--wave", "1", "--eval-stdin", "--eval", `{"verdict":"COMPLETE"}`); err == nil {
		t.Fatal("--eval with --eval-stdin was accepted")
	}
	var evalsAfter int
	if err := s.ReadDB.QueryRow(`SELECT COUNT(*) FROM evaluations`).Scan(&evalsAfter); err != nil {
		t.Fatal(err)
	}
	if countGaps() != gapsBefore || evalsAfter != evals {
		t.Fatalf("a refused evaluation wrote %d gap(s) and %d evaluation(s)", countGaps()-gapsBefore, evalsAfter-evals)
	}
}
