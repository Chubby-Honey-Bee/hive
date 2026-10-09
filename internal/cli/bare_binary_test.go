package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	hive "github.com/Chubby-Honey-Bee/hive"
	"github.com/Chubby-Honey-Bee/hive/internal/foragers"
)

// A chb alone in a directory with no foragers/, agents/ or workflows/ runs
// on the copies it carries: `chb list` lists the carried foragers and says
// so once, and workflows/hive.yaml validates, passes preflight and dry-runs
// from the carried copy. A workflows/ beside the binary is read before it.
func TestBareBinaryRunsOnTheCarriedCopies(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	chb := filepath.Join(binDir, "chb")
	build := exec.Command("go", "build", "-o", chb, "./cmd/chb")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/chb: %v\n%s", err, out)
	}
	// Beside the binary: a workflow under a name no carried copy has.
	beside, err := fs.ReadFile(hive.Workflows, "proof.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(binDir, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "workflows", "beside.yaml"), beside, 0o644); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	run := func(args ...string) (string, string, error) {
		t.Helper()
		cmd := exec.Command(chb, args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"HIVE_FORAGERS_DIR=",
			"HIVE_DB_PATH="+filepath.Join(work, "hive.db"),
			"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "HIVE_PROVIDER=")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}

	carried, err := foragers.LoadFS(hive.Foragers)
	if err != nil {
		t.Fatal(err)
	}
	lenses := 0
	for _, w := range carried {
		if w.Archetype != foragers.ArchetypeSynthesizer {
			lenses++
		}
	}
	stdout, stderr, err := run("list", "--color", "never")
	if err != nil {
		t.Fatalf("chb list: %v\n%s", err, stderr)
	}
	if want := fmt.Sprintf("HIVE — %d foragers available", lenses); !strings.HasPrefix(stdout, want) {
		t.Errorf("chb list does not start %q:\n%s", want, stdout)
	}
	for _, w := range carried {
		if !strings.Contains(stdout, " "+w.Name+" ") {
			t.Errorf("chb list lacks the carried forager %s", w.Name)
		}
	}
	const note = "reading the copy the binary carries"
	if n := strings.Count(stderr, note); n != 1 {
		t.Errorf("chb list said %q %d times, want once:\n%s", note, n, stderr)
	}

	hiveYAML := filepath.Join("workflows", "hive.yaml")
	stdout, stderr, err = run("workflow", "validate", hiveYAML)
	if err != nil || !strings.HasPrefix(stdout, "VALID") {
		t.Errorf("chb workflow validate %s: %v\n%s%s", hiveYAML, err, stdout, stderr)
	}

	_, stderr, err = run("preflight", hiveYAML, "--skip-provider-check")
	if err != nil || !strings.Contains(stderr, "checking the copy the binary carries") {
		t.Errorf("chb preflight %s: %v\n%s", hiveYAML, err, stderr)
	}

	_, stderr, err = run("agent-run", hiveYAML, "--dry-run", "--branch", "",
		"--inputs", `{"project":"bare","max_iterations":1}`)
	if err != nil || !strings.Contains(stderr, "running the copy the binary carries") || !strings.Contains(stderr, "[dry-run] would run init") {
		t.Errorf("chb agent-run %s --dry-run: %v\n%s", hiveYAML, err, stderr)
	}

	stdout, stderr, err = run("workflow", "validate", "beside.yaml")
	if err != nil || !strings.HasPrefix(stdout, "VALID") {
		t.Errorf("chb workflow validate beside.yaml (beside the binary): %v\n%s%s", err, stdout, stderr)
	}
}
