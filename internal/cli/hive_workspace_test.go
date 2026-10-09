package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/hive"
)

// A workspace layout is a directory with the CLI's default database at
// workspace/hive.db, as a run from a repository root has it. Each chb
// command the tests run starts there, as each process does, and the globals
// go back to it afterwards.
type workspaceLayout struct {
	root  string
	named string // relative, as main.go's default is
}

func useWorkspaceLayout(t *testing.T) *workspaceLayout {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	if err := os.MkdirAll("workspace", 0o755); err != nil {
		t.Fatal(err)
	}
	prevStore, prevPath, prevPinned := store, dbPath, dbPinned
	store = nil
	t.Cleanup(func() {
		if store != nil {
			store.Close()
		}
		store, dbPath, dbPinned = prevStore, prevPath, prevPinned
	})
	return &workspaceLayout{root: root, named: filepath.Join("workspace", "hive.db")}
}

// chb runs one hive command as a fresh process would: it opens the named
// database, runs, and closes whichever database the command ended on.
func (l *workspaceLayout) chb(t *testing.T, pinned bool, args ...string) (string, error) {
	t.Helper()
	if store != nil {
		store.Close()
	}
	s, err := db.NewStore(l.named)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	store, dbPath, dbPinned = s, l.named, pinned
	return execute(t, newHiveCmd(), args...)
}

// counts is a row count per table, for asserting a database untouched.
func counts(t *testing.T, path string) map[string]int {
	t.Helper()
	s, err := db.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out := map[string]int{}
	for _, table := range []string{"hive_state", "hive_iterations", "hive_tier_log", "signals", "findings", "gaps", "capped_findings", "workflow_runs", "tool_invocations", "sources", "evaluations"} {
		var n int
		if err := s.ReadDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

func hiveStateOf(t *testing.T, path string) map[string]int {
	t.Helper()
	s, err := db.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.ReadDB.Query("SELECT project, iteration FROM hive_state")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var p string
		var it int
		if err := rows.Scan(&p, &it); err != nil {
			t.Fatal(err)
		}
		out[p] = it
	}
	return out
}

func keysOf(m map[string]int) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Two projects initialised from one directory land in two databases: the
// first in the default database, the second in workspace/B/hive.db,
// which the rule computes from the default database's layout. The second
// init says so. Every hive command for B then reads and writes B's database
// alone, and names it.
func TestHive_ASecondProjectGetsItsOwnWorkspaceDatabase(t *testing.T) {
	l := useWorkspaceLayout(t)
	// By the rule: the named database is <root>/workspace/hive.db, so
	// its workspace root is <root> and B's database is
	// <root>/workspace/B/hive.db. The literal path is the definition;
	// hive.WorkspacePath must agree with it.
	wantB := filepath.Join("workspace", "B", "hive.db")
	if got := hive.WorkspacePath(l.named, "B"); got != wantB {
		t.Fatalf("hive.WorkspacePath(%s, B) = %s, want %s", l.named, got, wantB)
	}

	out, err := l.chb(t, false, "init", "--project", "A")
	if err != nil || !strings.Contains(out, "Hive initialized for project 'A'") || !strings.Contains(out, "Database: "+l.named+"\n") {
		t.Fatalf("init A: out=%q err=%v, want the named database", out, err)
	}
	out, err = l.chb(t, false, "init", "--project", "B")
	if err != nil {
		t.Fatalf("init B: %v", err)
	}
	wantLine := fmt.Sprintf("Database: %s (%s hosts hive project 'A')", wantB, l.named)
	if !strings.Contains(out, "Hive initialized for project 'B'") || !strings.Contains(out, wantLine) {
		t.Fatalf("init B printed %q, want %q", out, wantLine)
	}
	if _, err := os.Stat(filepath.Join(l.root, wantB)); err != nil {
		t.Fatalf("B's database: %v", err)
	}
	if got := hiveStateOf(t, l.named); len(got) != 1 || got["A"] != 0 {
		t.Fatalf("the default database hosts %v, want A alone", got)
	}
	if got := hiveStateOf(t, wantB); len(got) != 1 || got["B"] != 0 {
		t.Fatalf("B's database hosts %v, want B alone", got)
	}
	out, err = l.chb(t, false, "init", "--project", "B", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeObject(t, "init --json", out); got["db"] != wantB || got["created"] != false {
		t.Fatalf("a second init B --json = %v, want db %s and created false", got, wantB)
	}

	// Something for B's scan to plan on, in B's database only.
	bs, err := db.NewStore(wantB)
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := bs.Gaps().AddGap(1, "seed", "B's open question", "critical", &zero, &zero, &zero, &zero); err != nil {
		t.Fatal(err)
	}
	bs.Close()
	before := counts(t, l.named)

	jsonDB := func(args ...string) {
		t.Helper()
		out, err := l.chb(t, false, args...)
		if err != nil {
			t.Fatalf("hive %v: %v", args, err)
		}
		if got := decodeObject(t, strings.Join(args, " "), out); got["db"] != wantB {
			t.Fatalf("hive %v: db = %v, want %s", args, got["db"], wantB)
		}
	}
	jsonDB("next", "--project", "B")
	if got := hiveStateOf(t, wantB); got["B"] != 1 {
		t.Fatalf("after next, B's iteration = %d in %s, want 1", got["B"], wantB)
	}
	jsonDB("complete", "--project", "B", "--action", "all", "--json")
	if got := counts(t, wantB); got["hive_iterations"] != 1 {
		t.Fatalf("B's database recorded %d pass(es), want the one next and complete ran", got["hive_iterations"])
	}
	jsonDB("status", "--project", "B", "--json")
	jsonDB("report", "--project", "B")
	out, err = l.chb(t, false, "status", "--project", "B")
	if err != nil || !strings.Contains(out, "Database:   "+wantB+"\n") {
		t.Fatalf("status B: out=%q err=%v, want its database named", out, err)
	}
	out, err = l.chb(t, false, "signals", "--project", "B")
	if err != nil || !strings.HasPrefix(out, wantLine+"\n") {
		t.Fatalf("signals B: out=%q err=%v, want %q first", out, err, wantLine)
	}
	out, err = l.chb(t, false, "reset", "--project", "B")
	if err != nil || !strings.Contains(out, wantLine+"\n") {
		t.Fatalf("reset B: out=%q err=%v, want %q", out, err, wantLine)
	}
	if got := hiveStateOf(t, wantB); got["B"] != 0 {
		t.Fatalf("after reset, B's iteration = %d, want 0", got["B"])
	}
	if after := counts(t, l.named); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("B's commands changed the default database: %v → %v", before, after)
	}
	if got := hiveStateOf(t, l.named); len(got) != 1 || got["A"] != 0 {
		t.Fatalf("the default database hosts %v after B's commands, want A at iteration 0", got)
	}
}

// A command other than init does not create the workspace database: before
// `hive init --project B`, every other hive verb for B is refused, naming the
// database B would use and the project the default database hosts, and
// creates nothing.
func TestHive_ACommandBeforeInitIsRefusedNamingThePath(t *testing.T) {
	l := useWorkspaceLayout(t)
	if _, err := l.chb(t, false, "init", "--project", "A"); err != nil {
		t.Fatal(err)
	}
	wantB := filepath.Join("workspace", "B", "hive.db")
	for _, args := range [][]string{
		{"next", "--project", "B"},
		{"status", "--project", "B"},
		{"complete", "--project", "B", "--action", "all"},
		{"signals", "--project", "B"},
		{"report", "--project", "B"},
		{"reset", "--project", "B"},
	} {
		out, err := l.chb(t, false, args...)
		if err == nil || !strings.Contains(err.Error(), wantB) || !strings.Contains(err.Error(), `"A"`) || !strings.Contains(err.Error(), "hive init --project B") {
			t.Errorf("hive %v before init: out=%q err=%v; want a refusal naming %s, A and the init command", args, out, err, wantB)
		}
	}
	if _, err := os.Stat(filepath.Join(l.root, "workspace", "B")); !os.IsNotExist(err) {
		t.Fatalf("a refused command created workspace/B (%v)", err)
	}
	if got := hiveStateOf(t, l.named); len(got) != 1 || got["A"] != 0 {
		t.Fatalf("the default database hosts %v, want A at iteration 0", got)
	}
}

// --db pins the database for one command. Pinned to a database that hosts
// another project's hive, a hive command is refused rather than sent on,
// naming both projects and the path the project uses; nothing is created
// and the pinned database is unchanged. Pinned to a database that hosts no
// other hive, it is used.
func TestHive_APinnedDatabaseHostingAnotherHiveIsRefused(t *testing.T) {
	l := useWorkspaceLayout(t)
	if _, err := l.chb(t, true, "init", "--project", "A"); err != nil {
		t.Fatal(err)
	}
	wantB := filepath.Join("workspace", "B", "hive.db")
	for _, args := range [][]string{
		{"init", "--project", "B"},
		{"next", "--project", "B"},
		{"status", "--project", "B", "--json"},
	} {
		_, err := l.chb(t, true, args...)
		if err == nil {
			t.Fatalf("hive %v --db %s succeeded on a database hosting A", args, l.named)
		}
		for _, want := range []string{`"A"`, `"B"`, l.named, wantB, "--db " + wantB} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("hive %v pinned: err = %v, want it to name %s", args, err, want)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(l.root, "workspace", "B")); !os.IsNotExist(err) {
		t.Fatalf("a pinned refusal created workspace/B (%v)", err)
	}
	if got := hiveStateOf(t, l.named); len(got) != 1 || got["A"] != 0 {
		t.Fatalf("the pinned database hosts %v, want A at iteration 0", got)
	}

	// The same command unpinned goes to B's workspace database.
	out, err := l.chb(t, false, "init", "--project", "B", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeObject(t, "init B", out); got["db"] != wantB || got["created"] != true {
		t.Fatalf("init B unpinned = %v, want created in %s", got, wantB)
	}
	// Pinned to its own database, B is served there.
	l.named = wantB
	out, err = l.chb(t, true, "status", "--project", "B", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeObject(t, "status B", out); got["db"] != wantB {
		t.Fatalf("status B --db %s: db = %v", wantB, got["db"])
	}
}

// The hive workflow run for two projects in turn from one directory: the
// first on the default database, the second with its own seeded workspace
// database, as `chb init` lays it out. agent-run points the second run at
// that database, so its own rows, its init's report and its hive all land
// there, and the first project's database is untouched by it.
func TestAgentRun_HiveWorkflowForTwoProjectsFromOneDirectory(t *testing.T) {
	h := newOfflineHive(t, wellFormedHiveReplies)
	wf := hiveWorkflowPath(t)
	t.Setenv("HIVE_PROFILE", "")
	t.Chdir(h.dir)
	prevStore, prevPath, prevPinned := store, dbPath, dbPinned
	store, dbPath, dbPinned = h.store, h.dbPath, false
	t.Cleanup(func() {
		if store != h.store {
			store.Close()
		}
		store, dbPath, dbPinned = prevStore, prevPath, prevPinned
	})
	run := func(project string) {
		t.Helper()
		inputs, _ := json.Marshal(map[string]any{"project": project, "max_iterations": 1})
		if _, err := execute(t, newAgentRunCmd(), wf, "--dir", h.dir, "--inputs", string(inputs),
			"--provider", "openai", "--branch", ""); err != nil {
			t.Fatalf("agent-run for %s: %v", project, err)
		}
	}

	run("alpha")
	if got := hiveStateOf(t, h.dbPath); len(got) != 1 || got["alpha"] != 1 {
		t.Fatalf("after alpha's run, %s hosts %v, want alpha at iteration 1", h.dbPath, got)
	}
	alphaBefore := counts(t, h.dbPath)

	// beta's workspace database, seeded, where `chb init beta` puts it: the
	// run's database is a file in no workspace layout, so the root is the
	// current directory.
	betaPath := filepath.Join(h.dir, "workspace", "beta", "hive.db")
	if got, _ := filepath.Abs(hive.WorkspacePath(h.dbPath, "beta")); got != betaPath {
		t.Fatalf("hive.WorkspacePath(%s, beta) = %s, want %s", h.dbPath, got, betaPath)
	}
	if err := os.MkdirAll(filepath.Dir(betaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	bs, err := db.NewStore(betaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := bs.Init(); err != nil {
		t.Fatal(err)
	}
	const betaQuestion = "How many workers does beta's fan run at once?"
	zero := 0
	if err := bs.Gaps().AddGap(1, "seed", betaQuestion, "critical", &zero, &zero, &zero, &zero); err != nil {
		t.Fatal(err)
	}
	bs.Close()
	h.fake.mu.Lock()
	h.fake.calls = map[string][]fakeHiveCall{}
	h.fake.mu.Unlock()

	run("beta")
	if store.Path != filepath.Join("workspace", "beta", "hive.db") && store.Path != betaPath {
		t.Fatalf("agent-run left the CLI on %s, want beta's database", store.Path)
	}
	if got := hiveStateOf(t, betaPath); len(got) != 1 || got["beta"] != 1 {
		t.Fatalf("beta's database hosts %v, want beta at iteration 1", got)
	}
	if got := hiveStateOf(t, h.dbPath); len(got) != 1 || got["alpha"] != 1 {
		t.Fatalf("after beta's run, %s hosts %v, want alpha at iteration 1", h.dbPath, got)
	}
	if after := counts(t, h.dbPath); fmt.Sprint(after) != fmt.Sprint(alphaBefore) {
		t.Fatalf("beta's run changed alpha's database: %v → %v", alphaBefore, after)
	}
	beta := counts(t, betaPath)
	if beta["workflow_runs"] != 1 || beta["hive_iterations"] != 1 {
		t.Fatalf("beta's database holds %v, want the run's own row and one pass (tables %v)", beta, keysOf(beta))
	}
	betaStore, err := db.NewStore(betaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer betaStore.Close()
	var runID int64
	if err := betaStore.ReadDB.QueryRow(`SELECT id FROM workflow_runs`).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	inits := commandInvocations(t, betaStore, runID, "init")
	if len(inits) != 1 {
		t.Fatalf("init ran %d time(s) in beta's run", len(inits))
	}
	var initOut struct {
		DB string `json:"db"`
	}
	if err := json.Unmarshal([]byte(inits[0].stdout), &initOut); err != nil || initOut.DB != betaPath {
		t.Fatalf("beta's init reported db %q (%v), want %s", initOut.DB, err, betaPath)
	}
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	if calls := h.fake.calls["dispatch"]; len(calls) == 0 || !strings.Contains(calls[0].prompt, betaQuestion) {
		t.Fatalf("beta's dispatch was planned from %d call(s), want beta's gap in the plan", len(calls))
	}
	if _, err := os.Stat(filepath.Join(h.dir, "workspace", "beta", "final-synthesis.md")); err != nil {
		t.Fatalf("beta's synthesis: %v", err)
	}
}
