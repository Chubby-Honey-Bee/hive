package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/runner"
)

// captureBackend records the system prompt and the database each dispatch
// was given, and answers with a fixed verdict.
type captureBackend struct {
	mu      sync.Mutex
	systems []string
	dbPaths []string
}

func (b *captureBackend) Run(_ context.Context, req runner.RunRequest) (*runner.RunResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.systems = append(b.systems, req.System)
	db := ""
	if req.Registry != nil {
		for _, kv := range req.Registry.Env {
			if strings.HasPrefix(kv, "HIVE_DB_PATH=") {
				db = strings.TrimPrefix(kv, "HIVE_DB_PATH=")
			}
		}
	}
	b.dbPaths = append(b.dbPaths, db)
	return &runner.RunResult{FinalText: `{"verdict":"support"}`, Turns: 1}, nil
}

// A replica runs the caller's personas, as agent-run does, while its agents
// write to the replica's own database.
func TestRunOneReplica_UsesTheCallersPersonas(t *testing.T) {
	project := t.TempDir()
	persona := "ANALYST PERSONA from " + project
	if err := os.MkdirAll(filepath.Join(project, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "agents", "analyst.md"), []byte(persona), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv("HIVE_DB_PATH", filepath.Join(project, "callers.db"))

	const wf = `name: replica-persona
nodes:
  forager-a:
    type: agent
    agent: analyst
    model: haiku
    prompt: "Answer: {question}"
    outputs: [verdict]
  queen:
    type: agent
    agent: analyst
    model: haiku
    prompt: "Synthesize: {question}"
    outputs: [verdict]
edges:
  - {from: forager-a, to: queen}
`
	backend := &captureBackend{}
	if _, _, err := runOneReplica(context.Background(), "q", 0, wf, "stochastic", nil, 0, "", "", backend); err != nil {
		t.Fatalf("runOneReplica: %v", err)
	}

	const nodes = 2
	if len(backend.systems) != nodes {
		t.Fatalf("dispatches = %d, want %d", len(backend.systems), nodes)
	}
	for i, s := range backend.systems {
		if s != persona {
			t.Errorf("dispatch %d system prompt = %q, want the caller's agents/analyst.md %q", i, s, persona)
		}
		db := backend.dbPaths[i]
		if filepath.Base(db) != "hive.db" || !strings.HasPrefix(filepath.Base(filepath.Dir(db)), "swarm-replicate-0-") {
			t.Errorf("dispatch %d HIVE_DB_PATH = %q, want the replica's own hive.db", i, db)
		}
	}
}
