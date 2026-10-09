package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/runner"
)

func (r *preflightReport) checkProviderAuth(want string, defn map[string]any) {
	// agent-run refuses an unknown provider: value, or one the allowlist
	// bars, before the run starts. It is checked first: it holds whatever
	// credentials the machine has, and the credential check below may stop
	// the report early.
	if err := runner.PreflightWorkflowProviders(defn); err != nil {
		r.fail("per-node provider", err.Error())
	}
	if !r.checkBackendAuth(runner.ResolveBackendKind(runner.Config{Provider: want})) {
		return
	}
	r.checkNodeProviderAuth(defn)
}

// checkBackendAuth checks the machine has what the resolved backend runs
// on, and reports whether it has.
func (r *preflightReport) checkBackendAuth(kind runner.BackendKind) bool {
	msg, ok := backendAuth(kind)
	if !ok {
		r.fail("provider auth", msg)
		return false
	}
	r.pass("provider auth", msg)
	return true
}

// backendAuth checks the machine has what the resolved backend runs on: an
// SDK backend its API key, a local endpoint nothing, any other backend its
// CLI. It returns the line to report and whether the check passed.
func backendAuth(kind runner.BackendKind) (string, bool) {
	switch kind {
	case runner.BackendAnthropic, runner.BackendGemini, runner.BackendOpenAI:
		return sdkBackendAuth(kind)
	case runner.BackendLocal:
		return "local endpoint " + runner.LocalBaseURL() + " (no API key required; HIVE_LOCAL_BASE_URL)", true
	}
	return cliBackendAuth(kind)
}

// sdkBackendAuth checks an SDK backend's API key is set.
func sdkBackendAuth(kind runner.BackendKind) (string, bool) {
	switch kind {
	case runner.BackendAnthropic:
		return authByEnv("anthropic SDK (ANTHROPIC_API_KEY set)", "resolved provider anthropic but ANTHROPIC_API_KEY is unset", "ANTHROPIC_API_KEY")
	case runner.BackendGemini:
		return authByEnv("gemini SDK (API key set)", "resolved provider gemini but neither GEMINI_API_KEY nor GOOGLE_API_KEY is set", "GEMINI_API_KEY", "GOOGLE_API_KEY")
	}
	return authByEnv("openai SDK (OPENAI_API_KEY set)", "resolved provider openai but OPENAI_API_KEY is unset", "OPENAI_API_KEY")
}

// cliBackendAuth checks a CLI backend's binary is on PATH: gemini for
// gemini-cli, else claude. This is also the "nothing resolvable" path: the
// resolver falls back to claude-cli, and a missing `claude` fails.
func cliBackendAuth(kind runner.BackendKind) (string, bool) {
	if kind == runner.BackendGeminiCLI {
		return authByBinary("gemini CLI on PATH (no API key required)", "resolved provider gemini-cli but 'gemini' is not on PATH", "gemini")
	}
	return authByBinary("claude CLI on PATH (no API key required)", "no provider resolvable: no SDK key set and 'claude' CLI not on PATH — set a key or install a CLI", "claude")
}

// authByEnv passes, with the line pass, when any of keys is set in the
// environment, and fails with the line fail otherwise.
func authByEnv(pass, fail string, keys ...string) (string, bool) {
	if anyEnvSet(keys) {
		return pass, true
	}
	return fail, false
}

// authByBinary passes, with the line pass, when bin is on PATH, and fails
// with the line fail otherwise.
func authByBinary(pass, fail, bin string) (string, bool) {
	if binaryOnPath(bin) {
		return pass, true
	}
	return fail, false
}

// providerAuthNeed is what a node's provider: override runs on: its API key
// or its CLI, either of which will do, and the failure when this machine
// has neither.
type providerAuthNeed struct {
	key  string
	cli  string
	fail string
}

// nodeProviderAuthNeeds holds the need of each provider a node may name.
var nodeProviderAuthNeeds = map[string]providerAuthNeed{
	"anthropic":  {key: "ANTHROPIC_API_KEY", fail: "node %q wants provider:anthropic but ANTHROPIC_API_KEY is unset"},
	"gemini":     {key: "GEMINI_API_KEY", cli: "gemini", fail: "node %q wants provider:gemini but neither GEMINI_API_KEY nor 'gemini' CLI is available"},
	"openai":     {key: "OPENAI_API_KEY", fail: "node %q wants provider:openai but OPENAI_API_KEY is unset"},
	"claude-cli": {cli: "claude", fail: "node %q wants provider:claude-cli but 'claude' CLI is missing"},
	"gemini-cli": {cli: "gemini", fail: "node %q wants provider:gemini-cli but 'gemini' CLI is missing"},
}

// met reports whether this machine has the API key or the CLI.
func (n providerAuthNeed) met() bool {
	return (n.key != "" && os.Getenv(n.key) != "") || (n.cli != "" && binaryOnPath(n.cli))
}

// checkNodeProviderAuth fails each node whose provider: override this
// machine cannot run. Per-node provider: overrides are honored only if
// their corresponding env / CLI is also available.
func (r *preflightReport) checkNodeProviderAuth(defn map[string]any) {
	nodes, _ := defn["nodes"].(map[string]any)
	for name, raw := range nodes {
		node, _ := raw.(map[string]any)
		prov, _ := node["provider"].(string)
		need, ok := nodeProviderAuthNeeds[strings.ToLower(prov)]
		if ok && !need.met() {
			r.fail("per-node provider auth", fmt.Sprintf(need.fail, name))
		}
	}
}

// anyEnvSet reports whether any of keys is set in the environment.
func anyEnvSet(keys []string) bool {
	for _, k := range keys {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// binaryOnPath reports whether bin is on PATH.
func binaryOnPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func (r *preflightReport) checkPathBinaries() {
	r.checkChbOnPath()
	// `git` is needed by agent-run's auto-commit path. Soft check: only
	// runs that auto-commit need it, so missing git is a ⚠ not a ✗. The
	// runtime image ships git.
	r.checkSoftBinary("git", "git not on PATH — required only by --branch / --auto-pr (the runner's auto-commit path)")
}

// checkChbOnPath checks for `chb` itself, which is required — agent-run
// shells out to its own binary for spawned subprocesses (review,
// implement, chb ask in MCP context). It resolves the running executable's
// path when LookPath misses, since preflight can run from a binary that is
// not on PATH (./bin/chb, say).
func (r *preflightReport) checkChbOnPath() {
	if binaryOnPath("chb") {
		r.pass("PATH binary", "chb")
		return
	}
	// Fall back to checking the running executable: if we got
	// here, *some* chb binary is running, so flag a softer
	// warning rather than a hard fail.
	if exe, err := os.Executable(); err == nil && exe != "" {
		r.warn("PATH binary (soft)", "chb not on PATH (running binary at "+exe+")")
		return
	}
	r.fail("PATH binary", "chb not on PATH")
}

// checkSoftBinary passes bin on PATH, and warns with missing when it is not.
func (r *preflightReport) checkSoftBinary(bin, missing string) {
	if !binaryOnPath(bin) {
		r.warn("PATH binary (soft)", missing)
		return
	}
	r.pass("PATH binary", bin)
}

func (r *preflightReport) checkTargetDir(target string, initTarget bool) {
	if target == "" {
		return // optional
	}
	abs, _ := filepath.Abs(target)
	info, err := os.Stat(abs)
	if os.IsNotExist(err) {
		r.createTargetDir(abs, initTarget)
		return
	}
	if err != nil {
		r.fail("target dir", abs+": "+err.Error())
		return
	}
	r.checkExistingTargetDir(abs, info, initTarget)
}

// createTargetDir creates and git-inits a missing target dir, when
// --init-target allows it.
func (r *preflightReport) createTargetDir(abs string, initTarget bool) {
	if !initTarget {
		r.fail("target dir", abs+" does not exist (pass --init-target to create)")
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		r.fail("target dir init", err.Error())
		return
	}
	if err := gitInitTargetDir(abs); err != nil {
		r.fail("target dir init", "git init: "+err.Error())
		return
	}
	r.pass("target dir", abs+" (created + git init)")
}

// checkExistingTargetDir checks an existing target dir is a directory that
// holds nothing but .git and is a git repo.
func (r *preflightReport) checkExistingTargetDir(abs string, info os.FileInfo, initTarget bool) {
	if !info.IsDir() {
		r.fail("target dir", abs+" is not a directory")
		return
	}
	if !r.targetDirEmpty(abs) || !r.targetDirIsRepo(abs, initTarget) {
		return
	}
	r.pass("target dir", abs+" (empty + git-initialized)")
}

// targetDirEmpty checks the target dir holds nothing but .git.
func (r *preflightReport) targetDirEmpty(abs string) bool {
	entries, err := os.ReadDir(abs)
	if err != nil {
		r.fail("target dir read", err.Error())
		return false
	}
	if nonGit := countNonGitEntries(entries); nonGit > 0 {
		r.fail("target dir empty", fmt.Sprintf("%s contains %d non-.git entries — refusing to launch", abs, nonGit))
		return false
	}
	return true
}

// countNonGitEntries counts the entries other than .git.
func countNonGitEntries(entries []os.DirEntry) int {
	n := 0
	for _, e := range entries {
		if e.Name() != ".git" {
			n++
		}
	}
	return n
}

// targetDirIsRepo checks the target dir is a git repo, git-initialising it
// when --init-target allows it.
func (r *preflightReport) targetDirIsRepo(abs string, initTarget bool) bool {
	if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
		return true
	}
	if !initTarget {
		r.fail("target dir git-init", abs+" is not a git repo (pass --init-target)")
		return false
	}
	if err := gitInitTargetDir(abs); err != nil {
		r.fail("target dir init", "git init: "+err.Error())
		return false
	}
	return true
}

// gitInitTargetDir runs git init in the target dir.
func gitInitTargetDir(abs string) error {
	return exec.Command("git", "-C", abs, "init", "-q").Run()
}

// checkBehaviorFixture checks, in a HIVE source checkout (inHiveCheckout),
// that it holds the behavior fixture chb replay-behavior replays. It says
// nothing anywhere else: the fixture is for developing HIVE.
func (r *preflightReport) checkBehaviorFixture() {
	if !inHiveCheckout() {
		return
	}
	if fixtureFileUsable(behaviorFixture) {
		r.pass("behavior fixture", behaviorFixture)
		return
	}
	r.warn("behavior fixture", behaviorFixture+" missing — chb gen-behavior writes it (advisory)")
}

// fixtureFileUsable reports whether path is a file, not a directory, with
// something in it.
func fixtureFileUsable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func (r *preflightReport) checkPricingFreshness() {
	t, err := time.Parse("2006-01-02", runner.PricingLastUpdated())
	if err != nil {
		r.warn("pricing freshness", "unparseable PricingLastUpdated — defaulting to OK")
		return
	}
	age := time.Since(t)
	days := int(age.Hours() / 24)
	if days > 60 {
		r.warn("pricing freshness", fmt.Sprintf("pricing tables are %d days old (last updated %s) — verify provider rates", days, runner.PricingLastUpdated()))
		return
	}
	r.pass("pricing freshness", fmt.Sprintf("pricing tables %d days old (last updated %s)", days, runner.PricingLastUpdated()))
}
