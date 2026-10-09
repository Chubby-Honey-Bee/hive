package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// verifyTestsClaim closes the trust hole where an agent self-reports
// `tests_pass=true` without running any tests. When node.VerifyTests is
// set (typically by the implement-loop workflow generator), the runner
// re-runs `go test` against the packages the agent edited and overrides
// outputs.tests_pass based on the actual exit code.
//
// Behavior:
//   - If node.VerifyTests is false, no-op.
//   - If outputs lack a positive tests_pass=true claim, no-op (the
//     verifier never invents a positive claim — it only contradicts
//     unsupported ones).
//   - If `git diff --name-only --relative` returns no .go files, no-op
//     (nothing to verify; the agent didn't touch Go code).
//   - Otherwise: run `go test ./pkg` for each affected package. On
//     non-zero exit, mutate runResult.FinalText to encode an outputs map
//     with tests_pass=false and a verifier_override field carrying the
//     first 500 bytes of the test output. completeOrRepair re-extracts
//     the corrected outputs and the on_reject repair loop picks up the
//     real failure instead of the agent's lie.
func (rc *runtimeContext) verifyTestsClaim(ctx context.Context, node workflow.DispatchNode, runResult *RunResult) {
	if !node.VerifyTests || runResult == nil {
		return
	}
	outputs := workflow.ExtractJSONOutput(runResult.FinalText)
	claim, ok := outputs["tests_pass"].(bool)
	if !ok || !claim {
		return
	}
	pkgs := derivedAffectedPackages(rc.cfg.ProjectDir)
	if len(pkgs) == 0 {
		return
	}

	args := append([]string{"test", "-count=1"}, pkgs...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = rc.cfg.ProjectDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}

	rc.logf("verify_tests: agent claimed tests_pass=true but `go %s` failed: %v",
		strings.Join(args, " "), err)
	outputs["tests_pass"] = false
	snippet := string(out)
	if len(snippet) > 500 {
		snippet = snippet[:runeStart(snippet, 500)]
	}
	outputs["verifier_override"] = snippet

	if buf, marshalErr := json.Marshal(outputs); marshalErr == nil {
		runResult.FinalText = string(buf)
	}
}

// derivedAffectedPackages reads `git diff --name-only --relative` and returns
// one package pattern per directory containing a modified .go file: `./<dir>`,
// or `.` for a file at the project root. --relative gives paths relative to
// repoDir and limits the diff to it, so a project in a subdirectory of its
// repository gets patterns that resolve from repoDir. A directory that no
// longer exists (the agent deleted the package) has nothing to test and is
// skipped. Subpackages are not included: they are tested only when their own
// files changed. Returns nil on git error or no matches — both cases are
// treated as "nothing to verify" by the caller.
func derivedAffectedPackages(repoDir string) []string {
	cmd := exec.Command("git", "diff", "--name-only", "--relative")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	pkgSet := map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, ".go") {
			continue
		}
		dir := filepath.Dir(line)
		if dir == "" || dir == "." {
			pkgSet["."] = struct{}{}
			continue
		}
		if fi, err := os.Stat(filepath.Join(repoDir, dir)); err != nil || !fi.IsDir() {
			continue
		}
		pkgSet["./"+filepath.ToSlash(dir)] = struct{}{}
	}
	if len(pkgSet) == 0 {
		return nil
	}
	pkgs := make([]string, 0, len(pkgSet))
	for k := range pkgSet {
		pkgs = append(pkgs, k)
	}
	sort.Strings(pkgs)
	return pkgs
}
