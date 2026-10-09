package mcp

// Spawning `chb`: how the server finds the chb binary beside itself, warns
// of version skew, finds the repo root, builds a child's environment and log
// path, starts a detached run and reads its run id back from that log.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// chbBin resolves the chb binary the MCP server shells out to for
// spawned tools (preflight, agent_run, chb ask, self_review, self_implement).
// Preference order:
//
//  1. chb next to the running chb-mcp — the install layout both goreleaser
//     archives and the container image use. The executable path is resolved
//     through symlinks first, so a chb-mcp reached via a symlink finds the
//     chb beside the real file rather than beside the link.
//  2. chb on $PATH, resolved by exec at use time.
//
// On Windows the sibling is chb.exe, so the side-by-side layout holds there
// too.
func (s *mcpServer) chbBin() string {
	s.binOnce.Do(func() {
		s.bin = resolveChbBin()
		warnOnVersionSkew(s.bin)
	})
	return s.bin
}

// chbExeName is the chb binary's file name on this platform.
func chbExeName() string {
	if runtime.GOOS == "windows" {
		return "chb.exe"
	}
	return "chb"
}

func resolveChbBin() string {
	exe, err := os.Executable()
	if err != nil {
		return chbExeName()
	}
	return resolveChbBinFrom(exe)
}

// resolveChbBinFrom is resolveChbBin for a given server executable path.
func resolveChbBinFrom(exe string) string {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	candidate := filepath.Join(filepath.Dir(exe), chbExeName())
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return chbExeName()
}

// warnOnVersionSkew reports a chb whose version differs from this server's.
// The two ship together, and a sibling or $PATH chb from another release
// fails in ways that look like bugs — unknown flags, a different schema — so
// say so once, up front, instead of leaving it to be diagnosed.
func warnOnVersionSkew(bin string) {
	out, err := exec.Command(bin, "--version").Output() //nolint:gosec
	if err != nil {
		return
	}
	got := strings.TrimSpace(string(out))
	if versionSkewed(version, got) {
		fmt.Fprintf(os.Stderr, "chb-mcp: WARNING: %s reports %q but this server is %s — install matching releases\n",
			bin, got, version)
	}
}

// versionSkewed reports whether chb's `--version` output ("chb version X")
// names a version other than server. It compares the X token for equality:
// a substring match let chb 1.2.10 pass for server 1.2.1. Two dev builds are
// not a mismatch; a dev build against a release is.
func versionSkewed(server, chbOutput string) bool {
	got := strings.TrimSpace(chbOutput)
	fields := strings.Fields(got)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "version" {
			got = fields[i+1]
			break
		}
	}
	return got != server
}

// repoRoot walks up from the DB path or the binary to find a directory
// containing both `workflows/` and `agents/`. Falls back to cwd.
func (s *mcpServer) repoRoot() string {
	for _, c := range s.repoRootCandidates() {
		if root, ok := climbToRepoRoot(c); ok {
			return root
		}
	}
	cwd, _ := os.Getwd()
	return cwd
}

// repoRootCandidates are where repoRoot starts to climb: the DB's
// grandparent, the binary's directory and the working directory.
func (s *mcpServer) repoRootCandidates() []string {
	candidates := []string{}
	if s.store != nil {
		candidates = append(candidates, filepath.Dir(filepath.Dir(s.store.Path)))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Dir(exe))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	return candidates
}

// climbToRepoRoot climbs up to 6 levels from dir to a repo root
// (isRepoRoot), and reports whether it reached one.
func climbToRepoRoot(dir string) (string, bool) {
	for i := 0; i < 6; i++ {
		if isRepoRoot(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

// isRepoRoot reports whether dir holds both workflows/ and agents/.
func isRepoRoot(dir string) bool {
	return hasDir(filepath.Join(dir, "workflows")) && hasDir(filepath.Join(dir, "agents"))
}

func hasDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// runLogPath returns a log path under workspace/<label>/run-<ts>.log so the
// caller can tell the operator where to tail. Uses unix-nanos + a
// process-scoped serial counter to guarantee uniqueness even when two spawn
// requests arrive within the same wall-clock second: a seconds-resolution
// name would route two subprocesses' stdout to the same file.
func (s *mcpServer) runLogPath(label string) string {
	root := s.repoRoot()
	dir := filepath.Join(root, "workspace", label)
	_ = os.MkdirAll(dir, 0o755)
	n := atomic.AddInt64(&s.logCounter, 1)
	return filepath.Join(dir, fmt.Sprintf("run-%d-%d.log", time.Now().UTC().UnixNano(), n))
}

// scriptEnv builds the env map for a self-* script invocation. Maps
// optional `provider` arg → HIVE_PROVIDER, forwards the host's
// existing env (so API keys / paths flow through), and injects the
// session's active budget mode if one has been set via
// chb_set_budget_mode.
func (s *mcpServer) scriptEnv(args map[string]any) []string {
	env := os.Environ()
	if v := stringArg(args, "provider"); v != "" {
		env = append(env, "HIVE_PROVIDER="+v)
	}
	if mode := s.activeBudgetMode(); mode != "" {
		env = append(env, "HIVE_BUDGET_MODE="+mode)
	}
	return env
}

// activeBudgetMode returns the session's effective budget mode:
// the in-session override (set via chb_set_budget_mode) wins,
// else the host's HIVE_BUDGET_MODE env, else empty (which the
// runner treats as "standard").
func (s *mcpServer) activeBudgetMode() string {
	s.mu.Lock()
	override := s.budgetMode
	s.mu.Unlock()
	if override != "" {
		return override
	}
	return os.Getenv("HIVE_BUDGET_MODE")
}

// spawnDetachedRun launches a long-running `chb` subprocess,
// tees its stdout+stderr to logPath, and returns the run_id and pid.
// The caller's MCP tool result returns immediately.
func (s *mcpServer) spawnDetachedRun(cliArgs []string, logPath string) (int64, int, error) {
	bin := s.chbBin()
	// The session's budget mode reaches chb_agent_run through spawnEnv. "" —
	// inherit: for agent-run the cwd selects the project it edits and
	// commits to, which is the host's, not this server's.
	return s.spawnDetachedScriptWithEnv(bin, cliArgs, s.spawnEnv(), "", logPath)
}

// spawnEnv is the environment every spawned run inherits, with the session's
// budget mode, so `chb_set_budget_mode` reaches chb_agent_run as it reaches
// the self-* scripts (built through scriptEnv).
func (s *mcpServer) spawnEnv() []string {
	env := os.Environ()
	if mode := s.activeBudgetMode(); mode != "" {
		env = append(env, "HIVE_BUDGET_MODE="+mode)
	}
	return env
}

// spawnDetachedScriptWithEnv is the single launch primitive. The caller
// guarantees argv[0] is on PATH or absolute; we don't shell-interpret
// anything (so quoting attacks aren't a concern). The subprocess
// inherits stdin from /dev/null, stdout+stderr go to logPath.
//
// dir is the child's working directory, and each caller names it, because
// cwd means different things to different tools. For the self-* tools it is
// the HIVE repo whose artefacts the server reports; for chb_agent_run and
// chb_swarm it is the host's project, which is the thing under work — so
// those pass "" and inherit. Left implicit, the self-* children would
// resolve workspace/ and workflows/ wherever the MCP host started, while the
// server reports paths under its repo root; set in the primitive for
// everyone, it would silently move where chb_agent_run edits and commits.
func (s *mcpServer) spawnDetachedScriptWithEnv(bin string, args, env []string, dir, logPath string) (runID int64, pid int, err error) {
	if ok, why := s.limiter().allow(time.Now()); !ok {
		return 0, 0, errors.New(why)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, 0, fmt.Errorf("open log %s: %w", logPath, err)
	}
	cmd := detachedCommand(bin, args, env, dir, logFile)
	if err = cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, 0, fmt.Errorf("start: %w", err)
	}
	return watchSpawn(cmd, bin, logFile, logPath)
}

// detachedCommand is a spawned child's command: bin with args and env, in
// dir when one is named, its stdin /dev/null, and its stdout and stderr
// the log.
func detachedCommand(bin string, args, env []string, dir string, logFile *os.File) *exec.Cmd {
	cmd := exec.Command(bin, args...) //nolint:gosec
	if dir != "" {
		cmd.Dir = dir
		// A relative HIVE_DB_PATH resolves against the child's cwd, so
		// moving the cwd would silently point the child at a different
		// database from the server's. Pin it absolute first.
		env = absoluteDBPathEnv(env)
	}
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	return cmd
}

// watchSpawn watches a started child while it waits for the child's run id,
// and returns the run id and pid. A child that dies immediately (bad flag,
// missing workflow, no provider) is reported as the error it is, not as a
// healthy spawn with a run id of 0.
func watchSpawn(cmd *exec.Cmd, bin string, logFile *os.File, logPath string) (int64, int, error) {
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	pid := cmd.Process.Pid
	runID := readRunIDFromLog(logPath, 1500*time.Millisecond)
	select {
	case werr := <-waited:
		_ = logFile.Close()
		return exitedSpawn(werr, runID, pid, bin, logPath)
	default:
	}
	// Still running: record the eventual exit status in the log so a later
	// failure is visible to whoever reads it, rather than discarded.
	go logSpawnExit(waited, logFile)
	return runID, pid, nil
}

// exitedSpawn is what a child that exited while the server waited for its
// run id started: an error with the log's tail, unless it exited cleanly
// having started a run (a --dry-run, say).
func exitedSpawn(werr error, runID int64, pid int, bin, logPath string) (int64, int, error) {
	tail := logTail(logPath, 400)
	if werr != nil {
		return 0, 0, fmt.Errorf("%s exited immediately: %w\n%s", filepath.Base(bin), werr, tail)
	}
	if runID <= 0 {
		return 0, 0, fmt.Errorf("%s exited before starting a run\n%s", filepath.Base(bin), tail)
	}
	return runID, pid, nil
}

// logSpawnExit records a still-running child's exit status in its log
// once it exits, then closes the log.
func logSpawnExit(waited <-chan error, logFile *os.File) {
	if werr := <-waited; werr != nil {
		fmt.Fprintf(logFile, "\n[chb-mcp] run exited: %v\n", werr)
	} else {
		fmt.Fprintf(logFile, "\n[chb-mcp] run exited cleanly\n")
	}
	_ = logFile.Close()
}

// logTail returns the last n bytes of a log file, cut forward to a rune
// boundary, for surfacing why a spawned child died without making the caller
// open the file.
func logTail(path string, n int) string {
	b, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return ""
	}
	cut := max(len(b)-n, 0)
	for cut < len(b) && !utf8.RuneStart(b[cut]) {
		cut++
	}
	return strings.TrimSpace(string(b[cut:]))
}

// readRunIDFromLog polls a log file for the agent-run banner. Returns 0
// if no run id is observed within the timeout (the host can still read
// the log directly).
func readRunIDFromLog(path string, timeout time.Duration) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		if id := runIDInLog(path); id > 0 {
			return id
		}
		if !pausePoll(ctx, 75*time.Millisecond) {
			return 0
		}
	}
}

// runIDInLog is the run id the log at path names so far, 0 while it names
// none or cannot be read.
func runIDInLog(path string) int64 {
	b, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return 0
	}
	return scanRunID(string(b))
}

// pausePoll waits d between two polls, and reports false when ctx ends
// first.
func pausePoll(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// scanRunID looks for "workflow run ID: <N>" in a log buffer.
func scanRunID(text string) int64 {
	const marker = "workflow run ID:"
	i := strings.Index(text, marker)
	if i < 0 {
		return 0
	}
	rest := strings.TrimSpace(text[i+len(marker):])
	end := strings.IndexAny(rest, " \n\r\t")
	if end > 0 {
		rest = rest[:end]
	}
	n, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// absoluteDBPathEnv rewrites a relative HIVE_DB_PATH to an absolute one,
// resolved against this process's working directory.
func absoluteDBPathEnv(env []string) []string {
	out := make([]string, len(env))
	for i, kv := range env {
		out[i] = absoluteDBPathVar(kv)
	}
	return out
}

// absoluteDBPathVar is kv with a relative HIVE_DB_PATH made absolute,
// and any other variable as it is.
func absoluteDBPathVar(kv string) string {
	v, ok := strings.CutPrefix(kv, "HIVE_DB_PATH=")
	if !ok || v == "" {
		return kv
	}
	return "HIVE_DB_PATH=" + absolutePath(v)
}

// absolutePath is path resolved against this process's working directory,
// or path as it is when it is absolute already or cannot be resolved.
func absolutePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
