package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
)

func newResearchStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.NewStore(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func countRuns(t *testing.T, s *mcpServer) (inFlight int) {
	t.Helper()
	s.runs.Range(func(_, _ any) bool { inFlight++; return true })
	return inFlight
}

// chb_research starts a paid run in-process, so it answers to the spawn
// limit like every other spawning tool.
func TestResearch_ConsultsTheSpawnLimit(t *testing.T) {
	t.Chdir(t.TempDir())
	const max = 1
	var buf bytes.Buffer
	s := newTestServer(&buf)
	s.store = newResearchStore(t)
	s.spawnLimit = &spawnLimiter{max: max, window: time.Minute, starts: []time.Time{time.Now()}}

	text, isErr, rpcErr := callTool(t, s, &buf, "chb_research", map[string]any{"topic": "a new topic"})
	s.runsWG.Wait()
	if rpcErr != nil {
		t.Fatalf("protocol error: %v", rpcErr)
	}
	want := fmt.Sprintf("rate limit: %d runs already started", max)
	if !isErr || !strings.Contains(text, want) {
		t.Fatalf("isError=%v text=%q; want the refusal %q", isErr, text, want)
	}
	if n := countRuns(t, s); n != 0 {
		t.Errorf("%d research runs in flight after the refusal; want 0", n)
	}
}

// The workflow argument names a file under workflows/, nothing else, so
// "../../outside/escaped" is refused and starts no run.
func TestResearch_RefusesAWorkflowOutsideWorkflows(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(filepath.Join(cwd, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Not a valid workflow, so nothing could reach a model even if the file
	// were accepted.
	if err := os.WriteFile(filepath.Join(root, "outside", "escaped.yaml"), []byte("nodes: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	for _, name := range []string{"../outside/escaped", "../../outside/escaped", "sub/escaped", ".hidden"} {
		var buf bytes.Buffer
		s := newTestServer(&buf)
		s.store = newResearchStore(t)
		s.spawnLimit = &spawnLimiter{max: 1, window: time.Minute}

		text, isErr, rpcErr := callTool(t, s, &buf, "chb_research", map[string]any{"topic": "t", "workflow": name})
		s.runsWG.Wait()
		if rpcErr != nil {
			t.Fatalf("%s: protocol error: %v", name, rpcErr)
		}
		if !isErr || !strings.Contains(text, "plain file name") {
			t.Errorf("%s: isError=%v text=%q; want the plain-file-name refusal", name, isErr, text)
		}
		var runs int
		if err := s.store.ReadDB.QueryRow(`SELECT COUNT(*) FROM workflow_runs`).Scan(&runs); err != nil {
			t.Fatal(err)
		}
		if runs != 0 {
			t.Errorf("%s: workflow_runs=%d; want 0", name, runs)
		}
		// A refused name spends no slot of the spawn limit.
		if len(s.spawnLimit.starts) != 0 {
			t.Errorf("%s: the refusal used %d spawn slots; want 0", name, len(s.spawnLimit.starts))
		}
	}
}

// chb_research runs its workflow in-process under the session's budget
// mode, as the runs the other tools spawn do through their environment: a
// tier node is sent the slot of the mode chb_set_budget_mode set. The run
// here goes to a stub of the OpenAI API, which records the model each call
// names.
func TestResearch_RunsUnderTheSessionBudgetMode(t *testing.T) {
	// One model per slot of one tier, so the model a call names shows the
	// mode it was resolved under. A tier's slots are named for the modes.
	want := map[string]string{"premium": "m-premium", "standard": "m-standard", "cheap": "m-cheap", "free": "m-free"}

	dir := t.TempDir()
	override := "tiers:\n  probe:\n"
	for _, mode := range []string{"premium", "standard", "cheap", "free"} {
		override += "    " + mode + ": " + want[mode] + "\n"
	}
	modelsPath := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(modelsPath, []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	wf := "name: probe\nnodes:\n  answer:\n    type: agent\n    tier: probe\n    prompt: answer\n"
	if err := os.WriteFile(filepath.Join(dir, "workflows", "probe.yaml"), []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	var mu sync.Mutex
	var sent []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The run first lists the models the endpoint serves, as a local
		// server does: every slot of the tier.
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			data := []map[string]string{}
			for _, m := range want {
				data = append(data, map[string]string{"id": m})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, body.Model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer stub.Close()
	t.Setenv("HIVE_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", stub.URL)
	t.Setenv("HIVE_MODELS_PATH", modelsPath)
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")
	t.Setenv("HIVE_BUDGET_MODE", "")

	for mode, model := range want {
		t.Run(mode, func(t *testing.T) {
			mu.Lock()
			sent = nil
			mu.Unlock()
			var buf bytes.Buffer
			s := newTestServer(&buf)
			s.store = newResearchStore(t)
			s.spawnLimit = &spawnLimiter{max: 1, window: time.Minute}
			s.handleSetBudgetMode(rpcRequest{}, map[string]any{"mode": mode})

			text, isErr, rpcErr := callTool(t, s, &buf, "chb_research", map[string]any{"topic": "budget " + mode, "workflow": "probe"})
			s.runsWG.Wait()
			if rpcErr != nil || isErr {
				t.Fatalf("chb_research: isError=%v rpc=%v text=%q", isErr, rpcErr, text)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(sent) == 0 {
				t.Fatal("the run made no model call")
			}
			for _, got := range sent {
				if got != model {
					t.Errorf("budget mode %s: the run called %q; want %q", mode, got, model)
				}
			}
		})
	}
}

// A mode that is no mode is refused, as `chb agent-run` refuses it, rather
// than run on the standard slot. It spends no slot of the spawn limit.
func TestResearch_RefusesAnUnknownBudgetMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Not a valid workflow, so nothing could reach a model even if the call
	// were accepted.
	if err := os.WriteFile(filepath.Join(dir, "workflows", "broken.yaml"), []byte("nodes: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	const typo = "premuim"
	t.Setenv("HIVE_BUDGET_MODE", typo)

	var buf bytes.Buffer
	s := newTestServer(&buf)
	s.store = newResearchStore(t)
	s.spawnLimit = &spawnLimiter{max: 1, window: time.Minute}
	text, isErr, rpcErr := callTool(t, s, &buf, "chb_research", map[string]any{"topic": "t", "workflow": "broken"})
	s.runsWG.Wait()
	if rpcErr != nil {
		t.Fatalf("protocol error: %v", rpcErr)
	}
	if !isErr || !strings.Contains(text, typo) {
		t.Errorf("isError=%v text=%q; want a refusal naming %q", isErr, text, typo)
	}
	if len(s.spawnLimit.starts) != 0 {
		t.Errorf("the refusal used %d spawn slots; want 0", len(s.spawnLimit.starts))
	}
}

// A chb_research request taken before the shutdown began, whose handler
// reaches the run's start after it, starts no run: the shutdown has already
// cancelled the runs it knows of and would close the store under this one.
// The run's backend cannot be built here, so a run that did start would
// stop before calling a model.
func TestResearch_StartsNoRunOnceShutdownBegins(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HIVE_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "")
	var buf bytes.Buffer
	s := newTestServer(&buf)
	s.shutdown()
	s.store = newResearchStore(t)
	s.spawnLimit = &spawnLimiter{max: 1, window: time.Minute}
	s.handleResearch(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("44"), Method: "tools/call"}, map[string]any{"topic": "late"})
	s.runsWG.Wait()
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if !resp.Result.IsError || !strings.Contains(buf.String(), "shutting down") {
		t.Errorf("chb_research during the shutdown answered %s; want a refusal that the server is shutting down", buf.String())
	}
}

// A research run that panics fails, not the server: the panic is logged, the
// run is marked failed, and its cleanup runs, so the project is free for
// another run and the server serves on.
func TestResearch_APanickingRunIsMarkedFailed(t *testing.T) {
	t.Chdir(t.TempDir())
	var buf bytes.Buffer
	s := newTestServer(&buf)
	s.store = newResearchStore(t)
	s.spawnLimit = &spawnLimiter{max: 2, window: time.Minute}
	prev := researchRun
	t.Cleanup(func() { researchRun = prev })
	var runID int64
	researchRun = func(_ context.Context, store *db.Store, cfg runner.Config) (*runner.Result, error) {
		id, err := store.Workflows().CreateWorkflowRun("panics", 1, "name: panics\nnodes: {}\n", "{}", nil)
		if err != nil {
			return nil, err
		}
		runID = id
		cfg.RunStarted(id)
		panic("research bug")
	}

	text, isErr, rpcErr := callTool(t, s, &buf, "chb_research", map[string]any{"topic": "panics"})
	s.runsWG.Wait()
	if rpcErr != nil || isErr {
		t.Fatalf("chb_research: isError=%v rpc=%v text=%q", isErr, rpcErr, text)
	}
	run, err := s.store.Workflows().GetWorkflowRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" {
		t.Errorf("the panicked run is %q, want failed", run.Status)
	}
	if n := countRuns(t, s); n != 0 {
		t.Errorf("%d research runs still registered after the panic; want 0", n)
	}
	if _, _, rpcErr := callTool(t, s, &buf, "chb_summary", map[string]any{}); rpcErr != nil {
		t.Errorf("the server stopped answering after the panic: %v", rpcErr)
	}
}
