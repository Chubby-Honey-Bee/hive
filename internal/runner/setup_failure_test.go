package runner

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// A setup step that fails stops the run before its row exists and before the
// auto-commit branch is checked out, for each kind of failure.
func TestRun_SetupFailureWritesNoRunAndLeavesCheckoutAlone(t *testing.T) {
	const plain = `
name: plain
nodes:
  w1-a:
    type: agent
    model: haiku
    prompt: go
`
	const pinned = `
name: pinned
nodes:
  w1-a:
    type: agent
    model: haiku
    provider: openai
    prompt: go
`
	for _, tc := range []struct {
		name, workflow, provider, allowlist, wantErr string
		injected                                     bool
	}{
		{name: "a backend that cannot be built", workflow: plain, provider: "openai", wantErr: "init backend openai"},
		{name: "the allowlist refuses the run default", workflow: plain, provider: "openai", allowlist: "anthropic", wantErr: "HIVE_PROVIDER_ALLOWLIST"},
		{name: "the allowlist refuses a node's provider", workflow: pinned, injected: true, allowlist: "claude-cli", wantErr: "workflow provider preflight failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "")
			t.Setenv("HIVE_PROVIDER_ALLOWLIST", tc.allowlist)
			project := initCleanRepo(t)
			_, wfPath := writeWorkflow(t, tc.workflow)
			repo := &GitCommitter{ProjectDir: project}
			before, err := repo.run("rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				t.Fatalf("read HEAD: %v %s", err, before)
			}

			cfg := Config{
				WorkflowYAML: wfPath, ProjectDir: project, Branch: "x",
				Provider: tc.provider, Log: &bytes.Buffer{},
			}
			backend := &stubBackend{}
			if tc.injected {
				cfg.Backend = backend
			}
			store := newTempStore(t)
			_, err = Run(context.Background(), store, cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v; want one containing %q", err, tc.wantErr)
			}

			var runs int
			if err := store.WriteDB.QueryRow(`SELECT COUNT(*) FROM workflow_runs`).Scan(&runs); err != nil {
				t.Fatal(err)
			}
			if runs != 0 {
				t.Errorf("workflow_runs=%d after the failure; want 0", runs)
			}
			if len(backend.submitted) != 0 {
				t.Errorf("backend called %d times", len(backend.submitted))
			}
			if after, _ := repo.run("rev-parse", "--abbrev-ref", "HEAD"); after != before {
				t.Errorf("HEAD moved from %q to %q", strings.TrimSpace(before), strings.TrimSpace(after))
			}
			if out, _ := repo.run("branch", "--list", "x"); strings.TrimSpace(out) != "" {
				t.Errorf("branch x was created: %q", out)
			}
		})
	}
}
