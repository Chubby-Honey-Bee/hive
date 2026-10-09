package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/spf13/cobra"
)

// askFakeModel answers every chat completion of a two-lens swarm: the
// constraint probe with its constants, a lens prompt (it holds the lens
// persona's marker) with that lens's verdict, and the Queen with hers.
type askFakeModel struct {
	mu    sync.Mutex
	calls []string
}

const askFakeModelID = "ask-fake"

func (f *askFakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{map[string]any{"id": askFakeModelID}}})
		return
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var prompt string
	for _, m := range body.Messages {
		if m.Role == "user" {
			prompt = m.Content
			break
		}
	}
	lens := func(name string) string {
		return fmt.Sprintf(`{"forager":%q,"key_points":["k"],"evidence":["e"],"uncertainties":["u"],"verdict":"support","recommendation":"go"}`, name)
	}
	var content, node string
	switch {
	case strings.HasPrefix(prompt, "Describe today's weather"):
		node, content = "probe", `{"probe":"hive-constraint-ok","order":"second"}`
	case strings.Contains(prompt, "You are ALPHA-LENS"):
		node, content = "alpha", lens("alpha")
	case strings.Contains(prompt, "You are BETA-LENS"):
		node, content = "beta", lens("beta")
	case strings.Contains(prompt, "You are **the Queen**"):
		node, content = "queen", `{"report":"# Swarm Verdict\n\nShip.","convergence":"high","coverage":4,"gaps":[],"dissent_from_plurality":"","verdict":"support","recommendation":"ship it"}`
	default:
		http.Error(w, "unexpected prompt: "+prompt, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, node)
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": content},
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
	})
}

// askSwarmRoot is a root with ask and agent-run, as chb's has, over a fresh
// database, two test lenses bonded by resonates, the shipped Queen and a
// fake OpenAI-compatible endpoint. It returns the root and the database.
func askSwarmRoot(t *testing.T) (*cobra.Command, string) {
	t.Helper()
	fake := &askFakeModel{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	lenses := t.TempDir()
	queen, err := os.ReadFile(filepath.Join("..", "..", "foragers", "queen.md"))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"alpha.md": "---\nname: alpha\ntitle: Alpha\ndescription: The first test lens.\narchetype: lens\nbonds:\n  - to: beta\n    kind: resonates\n---\nYou are ALPHA-LENS. Answer with the lens JSON.\n",
		"beta.md":  "---\nname: beta\ntitle: Beta\ndescription: The second test lens.\narchetype: lens\n---\nYou are BETA-LENS. Answer with the lens JSON.\n",
		"queen.md": string(queen),
	} {
		if err := os.WriteFile(filepath.Join(lenses, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HIVE_FORAGERS_DIR", lenses)
	t.Setenv("HIVE_PROVIDER", "openai")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "offline")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	t.Setenv("HIVE_PROFILE", "")
	t.Setenv("HIVE_BUDGET_MODE", "")
	t.Setenv("HIVE_MAX_OUTPUT_TOKENS", "")
	t.Setenv("HIVE_EMBED_PROVIDER", "stub")
	t.Setenv("HIVE_DISABLE_RATE_LIMIT", "1")

	dir := t.TempDir()
	path := filepath.Join(dir, "ask.db")
	s, err := db.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	prevStore, prevDB := store, dbPath
	store = s
	t.Cleanup(func() { store, dbPath = prevStore, prevDB; s.Close() })
	t.Chdir(dir)

	root := &cobra.Command{Use: "chb", SilenceUsage: true}
	root.PersistentFlags().StringVar(&dbPath, "db", "", "")
	root.AddCommand(newSwarmAskCmd(), newAgentRunCmd())
	return root, path
}

// runAsk runs the root with args, capturing what reached os.Stdout (the
// verdict goes through the command's writer, which defaults to it) and
// the command's stderr.
func runAsk(t *testing.T, root *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	saved := os.Stdout
	os.Stdout = w
	printed := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		printed <- string(b)
	}()
	var errBuf bytes.Buffer
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	w.Close()
	os.Stdout = saved
	return <-printed, errBuf.String(), err
}

func askArgs(path string, extra ...string) []string {
	return append([]string{"--db", path, "ask", "Should we ship?", "--foragers", "alpha,beta", "--no-eval",
		"--model", askFakeModelID, "--synthesizer-model", askFakeModelID, "--out", filepath.Join(filepath.Dir(path), "swarm.yaml")}, extra...)
}

// stdout holds the verdict alone: the three calibration lines, computed from
// the run's rows. agent-run's JSON trailer goes to stderr with the rest of
// the run's narration.
func TestAsk_StdoutIsTheVerdictAndTheTrailerIsOnStderr(t *testing.T) {
	root, path := askSwarmRoot(t)
	stdout, stderr, err := runAsk(t, root, askArgs(path)...)
	if err != nil {
		t.Fatalf("ask: %v\nstderr:\n%s", err, stderr)
	}
	want := "Quorum: convergence high; tally support 2; plurality support, margin 2; no dissent written\n" +
		"∇ fired: alpha↔beta (support)\n" +
		"Verdict: support — ship it\n"
	if stdout != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
	}
	if !strings.Contains(stderr, `"run_id":`) || !strings.Contains(stderr, `"nodes_run":`) {
		t.Errorf("agent-run's trailer is not on stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Swarm assembled: 2 foragers") {
		t.Errorf("the preamble is not on stderr:\n%s", stderr)
	}
}

// --json prints one object and nothing else on stdout: the verdict, the
// Queen's recommendation and report, the calibration the artifact records,
// and the trailer's fields under run. The trailer itself is printed nowhere.
func TestAsk_JSONIsOneObjectAndNothingElse(t *testing.T) {
	root, path := askSwarmRoot(t)
	stdout, stderr, err := runAsk(t, root, askArgs(path, "--json")...)
	if err != nil {
		t.Fatalf("ask --json: %v\nstderr:\n%s", err, stderr)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var got struct {
		RunID          int64  `json:"run_id"`
		Question       string `json:"question"`
		Verdict        string `json:"verdict"`
		Recommendation string `json:"recommendation"`
		Report         string `json:"report"`
		Calibration    struct {
			QueenStatus string   `json:"queen_status"`
			Convergence string   `json:"convergence"`
			Plurality   string   `json:"plurality"`
			Margin      int      `json:"margin"`
			Votes       int      `json:"votes"`
			NablaFired  []string `json:"nabla_fired"`
		} `json:"calibration"`
		Run map[string]any `json:"run"`
	}
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, stdout)
	}
	if rest, _ := io.ReadAll(dec.Buffered()); strings.TrimSpace(string(rest)) != "" || dec.More() {
		t.Errorf("stdout holds more than one object:\n%s", stdout)
	}
	if got.RunID <= 0 || got.Question != "Should we ship?" || got.Verdict != "support" || got.Recommendation != "ship it" || !strings.HasPrefix(got.Report, "# Swarm Verdict") {
		t.Errorf("object: %+v", got)
	}
	c := got.Calibration
	if c.QueenStatus != "completed" || c.Convergence != "high" || c.Plurality != "support" || c.Margin != 2 || c.Votes != 2 || len(c.NablaFired) != 1 || c.NablaFired[0] != "alpha↔beta (support)" {
		t.Errorf("calibration: %+v", c)
	}
	if id, _ := got.Run["run_id"].(float64); int64(id) != got.RunID || got.Run["nodes_run"] == nil {
		t.Errorf("run: %v, want the trailer's fields for run %d", got.Run, got.RunID)
	}
	if strings.Contains(stderr, `"nodes_run":`) {
		t.Errorf("the trailer was printed beside the object:\n%s", stderr)
	}
}

// A run that fails prints its error once, and ask leaves the root's
// SilenceErrors as it found it.
func TestAsk_AFailedRunPrintsItsErrorOnce(t *testing.T) {
	root, path := askSwarmRoot(t)
	t.Setenv("HIVE_PROVIDER", "claude-cli")
	t.Setenv("CLAUDE_CODE_CLI_PATH", filepath.Join(t.TempDir(), "no-such-claude"))
	stdout, stderr, err := runAsk(t, root, askArgs(path)...)
	if err == nil {
		t.Fatal("expected the run to fail: no claude CLI")
	}
	if n := strings.Count(stderr, "Error:"); n != 1 {
		t.Errorf("the error was printed %d times, want once:\n%s", n, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty when the run fails:\n%s", stdout)
	}
	if root.SilenceErrors {
		t.Error("ask left the root's SilenceErrors set")
	}
}

// A dispatch runs on the store ask's command opened: ask opens one store,
// and none is open when it returns.
func TestAsk_DispatchOpensOneStoreAndClosesIt(t *testing.T) {
	root, path := askSwarmRoot(t)
	var opened []*db.Store
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		err := openCommandStore(cmd, args)
		opened = append(opened, store)
		return err
	}
	root.PersistentPostRun = closeCommandStore
	if _, stderr, err := runAsk(t, root, askArgs(path)...); err != nil {
		t.Fatalf("ask: %v\nstderr:\n%s", err, stderr)
	}
	if len(opened) != 1 {
		t.Errorf("ask opened %d stores, want 1", len(opened))
	}
	for i, s := range opened {
		if s.ReadDB.Ping() == nil || s.WriteDB.Ping() == nil {
			t.Errorf("store %d of %d is open after ask returned", i+1, len(opened))
		}
	}
}

// --json needs a run to report.
func TestAsk_JSONRefusesNoDispatch(t *testing.T) {
	root, path := askSwarmRoot(t)
	_, _, err := runAsk(t, root, askArgs(path, "--json", "--no-dispatch")...)
	if err == nil || !strings.Contains(err.Error(), "--json") || !strings.Contains(err.Error(), "--no-dispatch") {
		t.Errorf("err = %v, want a refusal naming both flags", err)
	}
}
