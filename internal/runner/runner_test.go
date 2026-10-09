package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// newTempStore creates an on-disk SQLite DB with the full schema applied.
func newTempStore(t *testing.T) *db.Store {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "runner.db")
	store, err := db.NewStore(tmp)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Init(); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestResolvePath_Sandbox(t *testing.T) {
	root := t.TempDir()
	if _, err := resolvePath(root, "inside.txt"); err != nil {
		t.Fatalf("inside should resolve: %v", err)
	}
	if _, err := resolvePath(root, "../escape.txt"); err == nil {
		t.Fatalf("path traversal must be rejected")
	}
	if _, err := resolvePath(root, "/etc/passwd"); err == nil {
		t.Fatalf("absolute escape must be rejected")
	}
}

func TestReadWriteEditTools(t *testing.T) {
	root := t.TempDir()
	fs := NewFSRecorder()
	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	reg.registerWrite(root, fs)
	reg.registerRead(root, fs)
	reg.registerEdit(root, fs)

	ctx := context.Background()
	if _, err := reg.Handlers["write_file"](ctx, map[string]any{"path": "a.txt", "content": "hello\nworld\n"}); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	out, err := reg.Handlers["read_file"](ctx, map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if !strings.Contains(out, "1: hello") || !strings.Contains(out, "2: world") {
		t.Fatalf("read output missing expected line numbers: %q", out)
	}
	if _, err := reg.Handlers["edit_file"](ctx, map[string]any{
		"path": "a.txt", "old_string": "world", "new_string": "claude",
	}); err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if !strings.Contains(string(b), "claude") {
		t.Fatalf("edit did not apply: %s", string(b))
	}
	if len(fs.TouchedPaths()) != 1 {
		t.Fatalf("expected 1 touched path, got %d", len(fs.TouchedPaths()))
	}
}

func TestBashTool_DenyList(t *testing.T) {
	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	reg.registerShell(t.TempDir(), NewFSRecorder(), hostShell())
	if _, err := reg.Handlers["shell"](context.Background(), map[string]any{"command": "rm -rf /"}); err == nil {
		t.Fatal("deny-list must block rm -rf /")
	}
}

func TestDBWriteTool_Finding(t *testing.T) {
	store := newTempStore(t)
	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	reg.registerDBWrite(store)
	handler := reg.Handlers["chb_db_write"]
	if handler == nil {
		t.Fatal("handler not registered")
	}
	out, err := handler(context.Background(), map[string]any{
		"kind": "finding",
		"fields": map[string]any{
			"wave":      1,
			"agent":     "test",
			"mss_label": "assumption",
			"finding":   "test finding",
		},
	})
	if err != nil {
		t.Fatalf("dbWriteFinding: %v", err)
	}
	if !strings.HasPrefix(out, "finding id=") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestDBWriteTool_Gap_PriorityNormalization(t *testing.T) {
	store := newTempStore(t)
	reg := &ToolRegistry{Handlers: map[string]ToolHandler{}}
	reg.registerDBWrite(store)
	// "high" should map to "important" (allowed enum).
	_, err := reg.Handlers["chb_db_write"](context.Background(), map[string]any{
		"kind": "gap",
		"fields": map[string]any{
			"wave":        1,
			"agent":       "test",
			"description": "x",
			"priority":    "high",
		},
	})
	if err != nil {
		t.Fatalf("gap write: %v", err)
	}
}

func TestResolveModelAlias(t *testing.T) {
	if got := ResolveModelAlias("sonnet"); got != "claude-sonnet-4-6" {
		t.Errorf("sonnet alias: %q", got)
	}
	if got := ResolveModelAlias("opus"); got != "claude-opus-4-8" {
		t.Errorf("opus alias: %q", got)
	}
	if got := ResolveModelAlias("opus-4-6"); got != "claude-opus-4-6" {
		t.Errorf("opus-4-6 alias: %q", got)
	}
	if got := ResolveModelAlias("haiku"); got != "claude-haiku-4-5" {
		t.Errorf("haiku alias: %q", got)
	}
	if got := ResolveModelAlias("claude-sonnet-4-5-20250929"); string(got) != "claude-sonnet-4-5-20250929" {
		t.Errorf("pin-through: %q", got)
	}
}

func TestCommitMessageFor(t *testing.T) {
	m := CommitMessageFor(2, "w2-fan-claims", "verified 3 claims")
	if !strings.HasPrefix(m, "validate(wave-2): w2-fan-claims") {
		t.Errorf("unexpected msg: %q", m)
	}
	m2 := CommitMessageFor(0, "evaluate", "")
	if !strings.HasPrefix(m2, "validate: evaluate") {
		t.Errorf("unexpected msg (wave 0): %q", m2)
	}
}

func TestDetectWave(t *testing.T) {
	cases := map[string]int{
		"w1-fix":       1,
		"w12-foo":      12,
		"seed-sources": 0,
		"evaluate":     0,
		"wave-3":       0, // only "wN-" prefix is detected
	}
	for k, want := range cases {
		if got := detectWave(k); got != want {
			t.Errorf("detectWave(%q)=%d want %d", k, got, want)
		}
	}
}

func TestPrintRunSummary(t *testing.T) {
	cases := []struct {
		name string
		res  *Result
		// keys we expect present in the JSON output
		wantKeys []string
		// optional value checks: key → expected string representation
		wantVals map[string]any
	}{
		{
			name: "happy path — fully populated result",
			res: &Result{
				RunID:        42,
				Iterations:   3,
				NodesRun:     7,
				Commits:      []string{"abc123", "def456"},
				PRURL:        "https://github.com/org/repo/pull/99",
				InputTokens:  1000,
				OutputTokens: 500,
			},
			wantKeys: []string{"run_id", "iterations", "nodes_run", "commits", "pr_url", "input_tokens", "output_tokens"},
			wantVals: map[string]any{
				"run_id":        float64(42),
				"iterations":    float64(3),
				"nodes_run":     float64(7),
				"pr_url":        "https://github.com/org/repo/pull/99",
				"input_tokens":  float64(1000),
				"output_tokens": float64(500),
			},
		},
		{
			name:     "zero-value result",
			res:      &Result{},
			wantKeys: []string{"run_id", "iterations", "nodes_run", "commits", "pr_url", "input_tokens", "output_tokens"},
			wantVals: map[string]any{
				"run_id":        float64(0),
				"iterations":    float64(0),
				"nodes_run":     float64(0),
				"pr_url":        "",
				"input_tokens":  float64(0),
				"output_tokens": float64(0),
			},
		},
		{
			name: "nil commits slice",
			res: &Result{
				RunID:   1,
				Commits: nil,
			},
			wantKeys: []string{"commits"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			PrintRunSummary(&buf, tc.res)
			out := buf.String()

			// Must be valid JSON.
			var m map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
				t.Fatalf("output is not valid JSON: %v\noutput: %q", err, out)
			}

			// All expected keys must be present.
			for _, k := range tc.wantKeys {
				if _, ok := m[k]; !ok {
					t.Errorf("missing key %q in output", k)
				}
			}

			// Value assertions.
			for k, want := range tc.wantVals {
				got, ok := m[k]
				if !ok {
					t.Errorf("key %q absent", k)
					continue
				}
				// json.Unmarshal decodes numbers as float64 and strings as string.
				switch w := want.(type) {
				case float64:
					if gf, ok2 := got.(float64); !ok2 || gf != w {
						t.Errorf("key %q: got %v (%T), want %v", k, got, got, w)
					}
				case string:
					if gs, ok2 := got.(string); !ok2 || gs != w {
						t.Errorf("key %q: got %v (%T), want %q", k, got, got, w)
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Run — top-level integration tests covering branches not exercised by the
// parallel-dispatch suite.
// ---------------------------------------------------------------------------

// TestRun_ResolveLLMBackendError covers the error branch inside Run where
// resolveLLMBackend returns an error because no backend can be constructed.
// cfg.Backend is nil (forces real resolution), provider is pinned to "gemini",
// and both GEMINI_API_KEY and GOOGLE_API_KEY are cleared — NewGeminiBackend
// returns an error which Run must propagate.
func TestRun_ResolveLLMBackendError(t *testing.T) {
	const wfYAML = `
name: test-backend-err
nodes:
  w1-step:
    type: agent
    model: haiku
    prompt: "do something"
    outputs: [result]
`
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "backend-err.yaml")
	if err := os.WriteFile(wfFile, []byte(wfYAML), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	// Clear every API key so all SDK backends fail, then pin provider to
	// "gemini" which requires GEMINI_API_KEY or GOOGLE_API_KEY.
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	// Prevent HIVE_PROVIDER from interfering.
	t.Setenv("HIVE_PROVIDER", "")

	store := newTempStore(t)
	var logBuf bytes.Buffer
	cfg := Config{
		WorkflowYAML: wfFile,
		ProjectDir:   tmpDir,
		Provider:     "gemini", // pin to gemini — no key → NewGeminiBackend errors
		// Backend intentionally nil so resolveLLMBackend runs the real path
		Log: &logBuf,
	}
	_, err := Run(context.Background(), store, cfg)
	if err == nil {
		t.Fatal("expected error when gemini backend cannot be resolved, got nil")
	}
	if !strings.Contains(err.Error(), "init backend") {
		t.Errorf("expected 'init backend' in error, got: %v", err)
	}
}

// TestRun_BootstrapWorkflowError covers the error branch inside Run where
// bootstrapWorkflow returns an error because the YAML file does not exist.
func TestRun_BootstrapWorkflowError(t *testing.T) {
	store := newTempStore(t)
	var logBuf bytes.Buffer
	cfg := Config{
		WorkflowYAML: filepath.Join(t.TempDir(), "nonexistent.yaml"),
		Backend:      &stubBackend{}, // bypass backend resolution entirely
		Log:          &logBuf,
	}
	_, err := Run(context.Background(), store, cfg)
	if err == nil {
		t.Fatal("expected error for missing workflow YAML, got nil")
	}
	if !strings.Contains(err.Error(), "init workflow") {
		t.Errorf("error should mention 'init workflow', got: %v", err)
	}
}

// TestRun_SingleNodeStub exercises the full Run happy-path with a trivial
// one-node workflow and an injected stub backend (no real LLM call). Covers
// parsedDefn construction, runtimeContext setup, dispatchLoop success-path,
// and the emit/log/cleanup sequence that follows.
func TestRun_SingleNodeStub(t *testing.T) {
	const wfYAML = `
name: test-run-singlenode
nodes:
  w1-step:
    type: agent
    model: haiku
    prompt: "do something"
    outputs: [result]
`
	tmpDir := t.TempDir()
	wfFile := filepath.Join(tmpDir, "single.yaml")
	if err := os.WriteFile(wfFile, []byte(wfYAML), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	store := newTempStore(t)
	var logBuf bytes.Buffer
	cfg := Config{
		WorkflowYAML: wfFile,
		ProjectDir:   tmpDir, // not a git repo → auto-commit disabled
		Backend:      &stubBackend{},
		Log:          &logBuf,
	}
	res, err := Run(context.Background(), store, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.RunID == 0 {
		t.Error("RunID should be non-zero")
	}
	if res.NodesRun != 1 {
		t.Errorf("NodesRun=%d, want 1", res.NodesRun)
	}
	if res.Iterations < 1 {
		t.Errorf("Iterations=%d, want ≥1", res.Iterations)
	}
}

// ---------------------------------------------------------------------------
// dispatchLoop — targeted branch coverage
// ---------------------------------------------------------------------------

// TestDispatchLoop_MaxIterationsZero verifies that when MaxIterations=0 the
// loop body never executes and the cap is reported as an error: the run is
// unfinished, so agent-run exits non-zero.
func TestDispatchLoop_MaxIterationsZero(t *testing.T) {
	store := newTempStore(t)
	var logBuf bytes.Buffer
	rc := &runtimeContext{
		ctx:   context.Background(),
		cfg:   Config{MaxIterations: 0},
		store: store,
		res:   &Result{},
		logf:  makeLogf(&logBuf),
		gc:    &GitCommitter{},
	}
	err := rc.dispatchLoop()
	if err == nil || !strings.Contains(err.Error(), "MaxIterations=0 reached") {
		t.Fatalf("expected the MaxIterations=0 cap reported, got %v", err)
	}
}

// TestDispatchLoop_CostCapAlreadyHit verifies the cost-cap branch: when
// CostUSDx10000 already equals MaxCostUSDx10000 at the start of the first
// iteration, dispatchLoop returns nil and logs "max-cost reached" without
// calling GetNextNodes.
func TestDispatchLoop_CostCapAlreadyHit(t *testing.T) {
	store := newTempStore(t)
	var logBuf bytes.Buffer
	rc := &runtimeContext{
		ctx:   context.Background(),
		cfg:   Config{MaxIterations: 10, MaxCostUSDx10000: 500},
		store: store,
		res:   &Result{CostUSDx10000: 500}, // already at the cap
		logf:  makeLogf(&logBuf),
		gc:    &GitCommitter{},
	}
	err := rc.dispatchLoop()
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !strings.Contains(logBuf.String(), "max-cost reached") {
		t.Errorf("expected 'max-cost reached' in log, got: %q", logBuf.String())
	}
}

// TestDispatchLoop_GetNextNodesError verifies that an error returned by
// workflow.GetNextNodes (here triggered by a non-existent runID) is surfaced
// as a "get next nodes: …" error from dispatchLoop.
func TestDispatchLoop_GetNextNodesError(t *testing.T) {
	store := newTempStore(t)
	var logBuf bytes.Buffer
	rc := &runtimeContext{
		ctx:   context.Background(),
		cfg:   Config{MaxIterations: 5, MaxCostUSDx10000: 0},
		store: store,
		runID: 99999, // non-existent run → GetNextNodes returns an error
		res:   &Result{},
		logf:  makeLogf(&logBuf),
		gc:    &GitCommitter{},
	}
	err := rc.dispatchLoop()
	if err == nil {
		t.Fatal("expected error for non-existent runID, got nil")
	}
	if !strings.Contains(err.Error(), "get next nodes") {
		t.Errorf("expected 'get next nodes' in error, got: %v", err)
	}
}

func TestFirstLine(t *testing.T) {
	// firstLine: TrimSpace the whole string, then return up to the first '\n'
	// (when '\n' index > 0). TrimSpace removes leading '\n', so the i>0
	// guard never fires on a leading newline after trim.
	cases := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"hello\nworld", "hello"},
		{"hello\nworld\nmore", "hello"},
		{"  hello  \nworld", "hello  "}, // TrimSpace → "hello  \nworld"; split before \n
		{"\nhello", "hello"},            // TrimSpace removes leading \n → "hello"
		{"", ""},
		{"   ", ""},
		{"only one line", "only one line"},
		{"line1\nline2\nline3", "line1"},
	}
	for _, tc := range cases {
		got := firstLine(tc.input)
		if got != tc.want {
			t.Errorf("firstLine(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
