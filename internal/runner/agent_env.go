package runner

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// agentEnv is the environment every agent subprocess runs with, the claude
// and gemini CLIs and the in-process bash tool the SDK backends expose
// (through modelEnv), and every command node's program. It is the parent's,
// with two additions.
//
// The directory of the running chb goes first on PATH, so an agent told to
// run `chb …` reaches the binary driving it even when that binary is not
// installed on PATH. Without it, a run started as ./bin/chb would have every
// agent command fail with "command not found", while each node could still
// pass its accept: predicate by returning some text.
//
// HIVE_DB_PATH names the run's database, so what an agent writes with
// `chb db-write` lands in the database the run reads, not in the default path
// under the agent's working directory.
//
// The bash tool also drops the provider credentials (withoutProviderCredentials).
func agentEnv(dbPath string) []string {
	env := os.Environ()
	if exe, err := os.Executable(); err == nil {
		env = withPathFirst(env, filepath.Dir(exe))
	}
	if dbPath != "" {
		env = setEnvVar(env, "HIVE_DB_PATH", dbPath)
	}
	return env
}

// AgentDBEnv names, in every process a model drives, the database of the run
// that model serves. A command that only the workflow's own steps may run
// against that database, such as chb hive next, refuses it there.
const AgentDBEnv = "HIVE_AGENT_DB"

// modelEnv is the environment of a process a model drives, the claude and
// gemini CLIs and the in-process bash tool: agentEnv, with AgentDBEnv naming
// the run's database when the run has one. A command node's program runs
// with agentEnv alone.
func modelEnv(dbPath string) []string {
	env := agentEnv(dbPath)
	if dbPath == "" {
		return env
	}
	return setEnvVar(env, AgentDBEnv, dbPath)
}

// providerCredentials are the variables the runner and its SDK clients read
// provider keys from.
var providerCredentials = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
	"OPENAI_API_KEY",
	"GEMINI_API_KEY", "GOOGLE_API_KEY",
}

// withoutProviderCredentials returns env less the provider credentials. The
// in-process bash tool runs with it: its output is stored in
// tool_invocations and sent back to the provider, so a command that prints
// the environment would write the key into both. The CLI backends keep the
// credentials, since each CLI may authenticate with its key.
func withoutProviderCredentials(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(providerCredentials, key) {
			out = append(out, kv)
		}
	}
	return out
}

// setEnvVar sets key=val, replacing any existing entry rather than appending a
// second one.
func setEnvVar(env []string, key, val string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return append(out, key+"="+val)
}

// withPathFirst puts dir at the front of PATH, dropping any later copy of it.
func withPathFirst(env []string, dir string) []string {
	parts := []string{dir}
	for _, p := range filepath.SplitList(agentEnvPath(env)) {
		if p != "" && p != dir {
			parts = append(parts, p)
		}
	}
	return setEnvVar(env, "PATH", strings.Join(parts, string(os.PathListSeparator)))
}

// agentEnvPath is the PATH env holds, its last when it holds several, ""
// when it holds none.
func agentEnvPath(env []string) string {
	current := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			current = v
		}
	}
	return current
}
