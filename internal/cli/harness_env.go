package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// harnessPlaceholderKey is the value the self-test harnesses export as
// ANTHROPIC_API_KEY when the machine has no provider at all. It is not a
// key and is never sent anywhere: the harnesses only run preflight (which
// checks that *some* provider is configured) and --dry-run dispatches.
const harnessPlaceholderKey = "harness-placeholder-not-a-key"

// ensureHarnessProviderEnv makes `chb validate`, `chb replay-behavior`,
// `chb gen-behavior` and `chb mcp-smoke` deterministic on a machine with no
// provider configured (CI runners, a fresh laptop). preflight's provider
// check fails honestly when nothing resolves; the harnesses are asserting
// everything *else* about preflight, so they pin that one variable and say
// so. Child processes inherit the setting through os.Environ().
func ensureHarnessProviderEnv(stderr io.Writer) {
	if harnessProviderEnvSet() || harnessProviderCLIOnPath() {
		return
	}
	_ = os.Setenv("ANTHROPIC_API_KEY", harnessPlaceholderKey)
	fmt.Fprintln(stderr, "  [harness] no provider configured on this machine; exporting a placeholder ANTHROPIC_API_KEY so preflight's provider check is deterministic (harness only, never sent)")
}

// harnessProviderEnvSet: a variable that configures a provider is set.
func harnessProviderEnvSet() bool {
	for _, k := range []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENAI_API_KEY", "HIVE_PROVIDER"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// harnessProviderCLIOnPath: a provider's CLI, claude or gemini, is on PATH.
func harnessProviderCLIOnPath() bool {
	for _, cli := range []string{"claude", "gemini"} {
		if _, err := exec.LookPath(cli); err == nil {
			return true
		}
	}
	return false
}

// harnessChildEnv is the environment the self-test harnesses give the chb
// processes they run: this process's, without OPENAI_BASE_URL. With it set,
// preflight asks that endpoint which models it serves, so a harness's
// expected preflight result would hang on whether a local server is up and
// what it holds. The harnesses check the workflow files, not the server.
func harnessChildEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "OPENAI_BASE_URL=") {
			env = append(env, kv)
		}
	}
	return env
}
