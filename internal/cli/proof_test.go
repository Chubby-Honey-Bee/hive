package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/runner"
)

// fakeProofModel is an OpenAI-compatible endpoint serving one model to the
// proof workflow. count-asserts runs its grep and answers the count. The
// synthesis answers its JSON without the shell call that writes the
// artifact, and the synthesis's repair runs that call and answers again.
type fakeProofModel struct {
	mu      sync.Mutex
	repairs int
	odd     []string
}

var (
	proofGrepCmd  = regexp.MustCompile(`grep -c 'assert' \S+`)
	proofWriteCmd = regexp.MustCompile(`mkdir -p .*> \S+`)
	proofArtifact = regexp.MustCompile(`"artifact":"([^"]+)"`)
)

func (f *fakeProofModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{map[string]any{"id": fakeLocalModel}}})
		return
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	var first, toolResult string
	called := false
	for _, m := range body.Messages {
		if m.Role == "user" && first == "" {
			first = m.Content
		}
		if m.Role == "tool" {
			called, toolResult = true, m.Content
		}
	}
	switch {
	case strings.Contains(first, "Your answer was sent back"):
		f.repairs++
		f.answerWithTool(w, called, proofWriteCmd.FindString(first), `{"verdict":"pass","artifact":"`+proofArtifact.FindStringSubmatch(first)[1]+`"}`)
	case strings.Contains(first, "reached the PASS branch"):
		writeFakeReply(w, `{"verdict":"pass","artifact":"`+proofArtifact.FindStringSubmatch(first)[1]+`"}`)
	case strings.Contains(first, "grep -c 'assert'"):
		f.answerWithTool(w, called, proofGrepCmd.FindString(first), `{"count":`+strings.TrimSpace(toolResult)+`}`)
	default:
		f.odd = append(f.odd, first)
		http.Error(w, "unexpected prompt", http.StatusBadRequest)
	}
}

// answerWithTool runs command with the shell tool first, then, once the
// request carries its result, answers.
func (f *fakeProofModel) answerWithTool(w http.ResponseWriter, called bool, command, answer string) {
	if !called {
		writeFakeToolCall(w, "shell", map[string]string{"command": command})
		return
	}
	writeFakeReply(w, answer)
}

// writeFakeReply answers a chat request with content as the final reply.
func writeFakeReply(w http.ResponseWriter, content string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": content},
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": fakeCompletionTokens},
	})
}

// The proof's artifact exists only if the shell tool wrote it. A synthesis
// that answers without writing it is sent back once to write it, so a model
// that skips the call still leaves the artifact chb proof checks for, and
// the run records the repair.
func TestProofWorkflow_AnArtifactNotWrittenIsRepaired(t *testing.T) {
	fake := &fakeProofModel{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	dir := t.TempDir()
	userTiers(t, "worker", fakeLocalModel)
	for k, v := range map[string]string{"OPENAI_BASE_URL": srv.URL, "OPENAI_API_KEY": "offline",
		"HIVE_BUDGET_MODE": "", "HIVE_DISABLE_RATE_LIMIT": "1", "HIVE_PROVIDER_ALLOWLIST": ""} {
		t.Setenv(k, v)
	}
	target := filepath.Join(dir, "asserts.go")
	if err := os.WriteFile(target, []byte(strings.Repeat("assert(x)\n", 81)), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "proof.db")
	s, err := db.NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	wf, err := filepath.Abs(filepath.Join("..", "..", "workflows", "proof.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(dir, "out", "result.txt")
	if _, err := runner.Run(context.Background(), s, runner.Config{
		WorkflowYAML: wf, ProjectName: "proof", ProjectDir: dir, DBPath: dbPath,
		Inputs:        map[string]any{"target_file": target, "result_path": result},
		MaxIterations: 25, Provider: "openai", Log: io.Discard,
	}); err != nil {
		t.Fatalf("the proof run failed: %v\nnodes: %s\nprompts it did not know: %q", err, proofNodeStates(t, s), fake.odd)
	}
	if len(fake.odd) > 0 {
		t.Fatalf("the fake was sent prompts it does not know: %q", fake.odd)
	}
	got, err := os.ReadFile(result)
	if err != nil || strings.TrimSpace(string(got)) != "PASS count=81" {
		t.Errorf("the artifact holds %q (%v), want PASS count=81", got, err)
	}
	if fake.repairs == 0 {
		t.Error("the synthesis that wrote no artifact was not sent back")
	}
}

// proofNodeStates is each node's status and error, for a failure message.
func proofNodeStates(t *testing.T, s *db.Store) string {
	t.Helper()
	rows, err := s.ReadDB.Query(`SELECT node_name, status, COALESCE(error, '') FROM workflow_node_states
		UNION ALL SELECT 'repair of ' || node_name, attempt, substr(failure_reason, 1, 300) || ' | ' || substr(COALESCE(repair_outputs_json, ''), 1, 200) FROM workflow_repairs`)
	if err != nil {
		return err.Error()
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, status, msg string
		if err := rows.Scan(&name, &status, &msg); err != nil {
			return err.Error()
		}
		out = append(out, name+" "+status+" "+msg)
	}
	return strings.Join(out, "; ")
}
