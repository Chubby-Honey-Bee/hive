package runner

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// _cliDeterministicWarnOnce deduplicates the per-process warning that
// --deterministic / --seed have no effect on CLI backends.
var _cliDeterministicWarnOnce sync.Once

// _cliMaxTokensWarnOnce deduplicates the per-process warning that
// --max-output-tokens / per-model max_output_tokens have no effect on
// CLI backends (the claude CLI has no --max-tokens flag).
var _cliMaxTokensWarnOnce sync.Once

// warnCLIMaxTokensOnce is shared by the claude and gemini CLI backends, so a
// process warns once whichever CLI it drives.
func warnCLIMaxTokensOnce() {
	_cliMaxTokensWarnOnce.Do(func() {
		fmt.Fprintln(os.Stderr, "[agent-run] --max-output-tokens / per-model max_output_tokens have no effect on CLI backends (the CLI is not passed an output cap)")
	})
}

// CLIBackend shells out to the `claude` CLI (Claude Code headless mode)
// instead of hitting the Anthropic API directly. Uses the user's existing
// Claude Code auth — no ANTHROPIC_API_KEY required.
//
// HIVE-native custom tools (chb_db_write) are exposed to the CLI
// via an MCP sidecar (internal/mcp). Standard tools (Read/Write/Edit/
// Glob/Grep/Bash/WebFetch) are Claude Code built-ins.
type CLIBackend struct {
	CLIPath       string
	MCPServerPath string
	ScratchDir    string
	AllowedTools  []string
	// ProjectDir is passed through to the MCP sidecar via env so it can
	// open the same SQLite DB the SDK path would have used.
	ProjectDir string
	// DBPath, if set, is forwarded to the MCP sidecar as HIVE_DB_PATH.
	DBPath string
}

// errNoProvider is the refusal when nothing names a provider: no key, no
// CLI on PATH, no --provider, HIVE_PROVIDER or profile. The runner
// returns it as is, without the backend's name, since no backend was chosen.
var errNoProvider = errors.New("no provider configured")

// NewCLIBackend returns a backend for the claude CLI at CLAUDE_CODE_CLI_PATH,
// else `claude` on PATH, or an error when it is not there: errNoProvider
// when nothing else names a provider either.
func NewCLIBackend(cfg Config) (*CLIBackend, error) {
	cliPath := cmp.Or(os.Getenv("CLAUDE_CODE_CLI_PATH"), "claude")
	if _, err := exec.LookPath(cliPath); err != nil {
		return nil, claudeCLIMissing(cfg, cliPath, err)
	}
	return &CLIBackend{
		CLIPath:       cliPath,
		MCPServerPath: mcpSidecarPath(),
		ScratchDir:    os.TempDir(),
		AllowedTools:  []string{"Read", "Write", "Edit", "Glob", "Grep", "Bash", "WebFetch"},
		ProjectDir:    cfg.ProjectDir,
		DBPath:        firstNonEmptyString(cfg.DBPath, os.Getenv("HIVE_DB_PATH")),
	}, nil
}

// claudeCLIMissing is the error for a claude CLI that is not at cliPath, the
// lookup's error err. It is errNoProvider when nothing else names a provider
// either: the CLI was then only the fallback.
func claudeCLIMissing(cfg Config, cliPath string, err error) error {
	if noProviderConfigured(cfg) {
		return fmt.Errorf("%w: no API key is set and neither the claude nor the gemini CLI is on PATH. "+
			"Set ANTHROPIC_API_KEY, GEMINI_API_KEY or OPENAI_API_KEY, install the claude or gemini CLI, "+
			"or name a provider with --provider or HIVE_PROVIDER. A server on this machine such as Ollama is \"local\", "+
			"at HIVE_LOCAL_BASE_URL, and `chb ask --profile local-fast` routes every role there. "+
			"(The claude CLI, the fallback, was looked for as %q.)", errNoProvider, cliPath)
	}
	return fmt.Errorf("claude CLI not found at %q (set CLAUDE_CODE_CLI_PATH to its path): %w", cliPath, err)
}

// mcpSidecarPath is the chb-mcp sidecar, HIVE_MCP_PATH else `chb-mcp`,
// when it is on PATH, else "". The sidecar is optional: without it
// chb_db_write is not available inside the CLI subprocess, which is fine
// for workflows that do not use it.
func mcpSidecarPath() string {
	mcpPath := cmp.Or(os.Getenv("HIVE_MCP_PATH"), "chb-mcp")
	if _, err := exec.LookPath(mcpPath); err != nil {
		return ""
	}
	return mcpPath
}

// cliResultEnvelope mirrors the JSON shape emitted by
// `claude -p --output-format json`. We only pull the fields we need.
type cliResultEnvelope struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	IsError  bool   `json:"is_error"`
	NumTurns int    `json:"num_turns"`
	Result   string `json:"result"`
	// Errors is what an error subtype, such as error_max_turns, reports in
	// place of a result.
	Errors     []string `json:"errors"`
	StopReason string   `json:"stop_reason"`
	// Usage is the main loop's tokens, summed over its turns, as the
	// Messages API counts them: input_tokens is the uncached remainder, and
	// the prompt is it plus the cache reads plus the cache writes.
	// cache_creation splits the writes by lifetime.
	Usage struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheCreation            struct {
			Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
	} `json:"usage"`
	// ModelUsage holds every model the run called, its own inner calls
	// included, keyed by the model it sent, which on Bedrock or Vertex is
	// the provider's id. It does not split cache writes by lifetime.
	ModelUsage map[string]cliModelUsage `json:"modelUsage"`
}

// cliModelUsage is one model's entry in the reply's modelUsage.
// CanonicalModel is the id Claude Code prices the entry by, for a key that
// is a provider's id or an alias.
type cliModelUsage struct {
	InputTokens              int64  `json:"inputTokens"`
	OutputTokens             int64  `json:"outputTokens"`
	CacheReadInputTokens     int64  `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64  `json:"cacheCreationInputTokens"`
	CanonicalModel           string `json:"canonicalModel"`
}

// cliUsage sets res's tokens from a claude CLI reply. A model's input is its
// uncached input plus its cache reads plus its cache writes; the reads are
// its cached tokens. When modelUsage names a model that spent tokens, each
// such model is one ByModel entry, priced at its own prices, and the
// 1-hour writes usage reports go to the models that wrote the most first,
// each up to its writes. Otherwise usage gives the tokens, priced at the
// node's model. An entry is priced by its key when the models config lists
// that, else by its canonicalModel when it has one: a Bedrock or Vertex id
// is not in the config, and a model newer than the CLI may be given an
// older model's canonical id. A model id's "[1m]" suffix, Claude Code's
// mark for its 1M context, is dropped: the price is the model's.
func cliUsage(env *cliResultEnvelope, res *RunResult) {
	res.ByModel = append(res.ByModel, cliSpentModels(env.ModelUsage)...)
	if len(res.ByModel) == 0 {
		cliTotalUsage(env, res)
		return
	}
	cliSplitCacheWrite1h(res.ByModel, env.Usage.CacheCreation.Ephemeral1hInputTokens)
	slices.SortFunc(res.ByModel, func(a, b ModelUsage) int { return strings.Compare(a.Model, b.Model) })
	for _, mu := range res.ByModel {
		res.InputTokens += mu.InputTokens
		res.CachedInputTokens += mu.CachedInputTokens
		res.CacheWriteTokens += mu.CacheWriteTokens
		res.CacheWrite1hTokens += mu.CacheWrite1hTokens
		res.OutputTokens += mu.OutputTokens
	}
}

// cliSpentModels is a reply's modelUsage as one ModelUsage per model that
// spent tokens, each entry under the model cliPricedModel prices it as, in
// no order.
func cliSpentModels(usage map[string]cliModelUsage) []ModelUsage {
	byName := map[string]ModelUsage{}
	for id, m := range usage {
		name := cliPricedModel(id, m.CanonicalModel)
		mu := byName[name]
		mu.Model = name
		mu.InputTokens += m.InputTokens + m.CacheReadInputTokens + m.CacheCreationInputTokens
		mu.CachedInputTokens += m.CacheReadInputTokens
		mu.CacheWriteTokens += m.CacheCreationInputTokens
		mu.OutputTokens += m.OutputTokens
		byName[name] = mu
	}
	var spent []ModelUsage
	for _, mu := range byName {
		if mu.spentTokens() {
			spent = append(spent, mu)
		}
	}
	return spent
}

// cliPricedModel is the model a modelUsage entry keyed id is priced as: id
// when the models config lists it, else canonical, its canonicalModel, when
// it has one; "[1m]" dropped from either.
func cliPricedModel(id, canonical string) string {
	name := strip1MContext(id)
	if !isMetered(name) && canonical != "" {
		return strip1MContext(canonical)
	}
	return name
}

// cliTotalUsage sets res's tokens from the reply's usage, for a reply whose
// modelUsage names no model that spent tokens: they are priced at the node's
// model.
func cliTotalUsage(env *cliResultEnvelope, res *RunResult) {
	u := env.Usage
	res.InputTokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	res.CachedInputTokens = u.CacheReadInputTokens
	res.CacheWriteTokens = u.CacheCreationInputTokens
	res.CacheWrite1hTokens = min(u.CacheCreation.Ephemeral1hInputTokens, u.CacheCreationInputTokens)
	res.OutputTokens = u.OutputTokens
}

// cliSplitCacheWrite1h hands left, the reply's 1-hour cache writes, to the
// models that wrote the most first, each up to its writes. It leaves
// byModel sorted that way.
func cliSplitCacheWrite1h(byModel []ModelUsage, left int64) {
	slices.SortFunc(byModel, func(a, b ModelUsage) int {
		return cmp.Or(cmp.Compare(b.CacheWriteTokens, a.CacheWriteTokens), strings.Compare(a.Model, b.Model))
	})
	for i := range byModel {
		take := max(min(left, byModel[i].CacheWriteTokens), 0)
		byModel[i].CacheWrite1hTokens = take
		left -= take
	}
}

// strip1MContext drops a model id's "[1m]" suffix.
func strip1MContext(id string) string {
	if len(id) > 4 && strings.EqualFold(id[len(id)-4:], "[1m]") {
		return id[:len(id)-4]
	}
	return id
}

// Run drives a single `claude -p` invocation, maps the JSON envelope to a
// RunResult, and returns. Tool-use loops happen inside the CLI subprocess
// (Claude Code handles them internally); chb does not see individual
// tool_use turns here. ToolInvocations stays empty for the CLI path.
func (b *CLIBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	warnCLISampling(&_cliDeterministicWarnOnce, req)
	args, cleanup, err := b.cliArgs(req)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var stdout, stderr bytes.Buffer
	runErr := b.command(ctx, args, req.Prompt, &stdout, &stderr).Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("claude CLI timed out or cancelled: %w", ctx.Err())
	}
	return cliResult(stdout.Bytes(), stderr.String(), runErr)
}

// warnCLISampling warns once, through once, that sampling options have no
// effect on a CLI backend, when req sets any.
func warnCLISampling(once *sync.Once, req RunRequest) {
	if req.Temperature != nil || req.TopP != nil || req.Seed != nil {
		once.Do(func() {
			fmt.Fprintln(os.Stderr, "[agent-run] --deterministic / --seed / --temperature / --top-p have no effect on CLI backends")
		})
	}
}

// cliArgs are the claude CLI's arguments for req, and the cleanup of the MCP
// config file they name; a cleanup that does nothing when they name none.
func (b *CLIBackend) cliArgs(req RunRequest) ([]string, func(), error) {
	tools := b.toolSet(req.Registry)
	mcpConfig, cleanup, err := b.sidecarConfig(tools)
	if err != nil {
		return nil, nil, err
	}
	args := cliPromptArgs(req)
	args = append(args, tools.restrictArgs()...)
	args = append(args, tools.allowArgs(mcpConfig)...)
	args = append(args, cliTurnArgs(req)...)
	return args, cleanup, nil
}

// cliPromptArgs are the flags every call sends: one JSON reply, no
// permission prompts, and the request's system prompt and model when it
// sets them.
func cliPromptArgs(req RunRequest) []string {
	args := []string{
		"-p",
		"--output-format", "json",
		"--permission-mode", "bypassPermissions",
	}
	if req.System != "" {
		args = append(args, "--system-prompt", req.System)
	}
	if req.Model != "" {
		// Normalize known aliases to Claude Code's CLI model names.
		args = append(args, "--model", canonicalCLIModel(req.Model))
	}
	return args
}

// cliTurnArgs caps the call's turns when req sets a cap: the CLI takes
// --max-turns (verified against its parser). It has no --max-tokens flag
// (verified via `claude --help` 2026-06): the per-turn output cap is
// enforced server-side by Claude's defaults, so a request's cap only warns.
// Operators who need the cap should use the SDK backend instead.
func cliTurnArgs(req RunRequest) []string {
	if req.MaxTokens > 0 {
		warnCLIMaxTokensOnce()
	}
	if req.MaxTurns > 0 {
		return []string{"--max-turns", fmt.Sprint(req.MaxTurns)}
	}
	return nil
}

// cliToolSet is what a call offers the claude CLI of its built-in tools.
type cliToolSet struct {
	// allowed are the built-ins offered, and denied the ones a node's
	// `tools:` allowlist removed.
	allowed, denied []string
	// restricted is set when an allowlist narrowed the registry.
	restricted bool
	// dbWrite is set when the tools keep chb_db_write, which the chb
	// sidecar serves.
	dbWrite bool
}

// toolSet is the CLI's tools for a call whose registry is reg. A node's
// `tools:` allowlist narrowed the registry, and the CLI runs its own tools,
// so the allowlist reaches it as flags (restrictArgs). The chb sidecar comes
// along only when the allowlist names chb_db_write.
func (b *CLIBackend) toolSet(reg *ToolRegistry) cliToolSet {
	if reg == nil || !reg.Restricted() {
		return cliToolSet{allowed: append([]string{}, b.AllowedTools...), dbWrite: true}
	}
	return b.restrictedToolSet(reg.Names())
}

// restrictedToolSet is the CLI's tools under an allowlist that kept names,
// in registry names: the built-ins that stand for one of them
// (cliToolRegistryName), with the rest denied.
func (b *CLIBackend) restrictedToolSet(names []string) cliToolSet {
	s := cliToolSet{restricted: true, dbWrite: slices.Contains(names, "chb_db_write")}
	for _, t := range b.AllowedTools {
		if slices.Contains(names, cliToolRegistryName[t]) {
			s.allowed = append(s.allowed, t)
		} else {
			s.denied = append(s.denied, t)
		}
	}
	return s
}

// restrictArgs narrow the CLI's tools to an allowlist: --tools offers only
// the built-ins it names ("" offers none), --disallowed-tools denies the
// rest, and --strict-mcp-config keeps the user's own MCP servers out. None
// when no allowlist narrowed the tools.
func (s cliToolSet) restrictArgs() []string {
	if !s.restricted {
		return nil
	}
	args := []string{"--tools", joinTools(s.allowed)}
	if len(s.denied) > 0 {
		args = append(args, "--disallowed-tools", joinTools(s.denied))
	}
	return append(args, "--strict-mcp-config")
}

// allowArgs name the sidecar's config file, mcpConfig, when it comes along
// ("" when not), and the tools the CLI may run without asking. An
// allowlist that kept no tool allows none.
//
// The sidecar's DB-write tool is allowed by the name Claude Code's MCP
// convention gives it, mcp__<server>__<tool>, so it mirrors both the server
// key written in writeMCPConfig ("chb") and the tool's registered name
// ("chb_db_write"). It is one of the sidecar's tools, not every one;
// widening the list means naming a server-wide form, not hand-copying the
// sidecar's tool names into a second list that would drift.
func (s cliToolSet) allowArgs(mcpConfig string) []string {
	var args []string
	allowed := s.allowed
	if mcpConfig != "" {
		args = append(args, "--mcp-config", mcpConfig)
		allowed = append(allowed, "mcp__chb__chb_db_write")
	}
	if !s.restricted || len(allowed) > 0 {
		args = append(args, "--allowed-tools", joinTools(allowed))
	}
	return args
}

// sidecarConfig writes the MCP config that brings the chb sidecar along,
// when the sidecar was found and tools keep chb_db_write, and returns its
// path and cleanup. The path is "" when the sidecar stays out, and the
// cleanup then does nothing.
func (b *CLIBackend) sidecarConfig(tools cliToolSet) (path string, cleanup func(), err error) {
	if b.MCPServerPath == "" || !tools.dbWrite {
		return "", func() {}, nil
	}
	path, cleanup, err = b.writeMCPConfig()
	if err != nil {
		return "", nil, fmt.Errorf("write mcp config: %w", err)
	}
	return path, cleanup, nil
}

// command is the claude CLI run with args, prompt on stdin and its output
// in stdout and stderr, in the project directory, with the agent
// environment.
func (b *CLIBackend) command(ctx context.Context, args []string, prompt string, stdout, stderr *bytes.Buffer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, b.CLIPath, args...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = modelEnv(b.DBPath)
	if b.ProjectDir != "" {
		cmd.Dir = b.ProjectDir
	}
	// As a command node's program: a cancelled call kills the CLI's whole
	// group, and a child that keeps stdout open holds Wait for at most 5 s
	// after the CLI ends.
	killProcessGroupOnCancel(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// cliResult reads a CLI run that ended with runErr: its reply on stdout, and
// on a failure what it said on stderr. Claude Code prints its result and
// then exits 1 when that result is marked is_error, so a run that exits
// non-zero is read from the result it printed, when it printed one.
func cliResult(stdout []byte, stderr string, runErr error) (*RunResult, error) {
	env, decodeErr := decodeCLIReply(stdout)
	if runErr != nil {
		runErr = fmt.Errorf("claude CLI failed: %w\nstderr: %s", runErr, truncate(stderr, 2000))
	}
	if err := cliUnreadable(env, decodeErr, runErr); err != nil {
		return nil, err
	}
	return cliReplyResult(&env, runErr)
}

// decodeCLIReply decodes the CLI's stdout into its result envelope.
func decodeCLIReply(stdout []byte) (cliResultEnvelope, error) {
	var env cliResultEnvelope
	raw := bytes.TrimSpace(stdout)
	if len(raw) == 0 {
		return env, fmt.Errorf("claude CLI returned empty stdout")
	}
	if err := unmarshalCLIEnvelope(raw, &env); err != nil {
		return env, fmt.Errorf("decode claude JSON: %w (raw=%s)", err, truncate(string(raw), 1000))
	}
	return env, nil
}

// unmarshalCLIEnvelope decodes raw into env. claude -p --output-format json
// emits a single JSON object on the last line; earlier lines may carry
// status. So when raw does not decode whole, its last line is decoded, and
// the error is the whole's when that fails too.
func unmarshalCLIEnvelope(raw []byte, env *cliResultEnvelope) error {
	err := json.Unmarshal(raw, env)
	if err == nil {
		return nil
	}
	if i := bytes.LastIndex(raw, []byte{'\n'}); i >= 0 && json.Unmarshal(raw[i+1:], env) == nil {
		return nil
	}
	return err
}

// cliUnreadable is the error for a run with no reply to read: runErr when
// the run failed and printed no result, else decodeErr; nil when env holds
// the reply.
func cliUnreadable(env cliResultEnvelope, decodeErr, runErr error) error {
	if runErr != nil && (decodeErr != nil || env.Type != "result") {
		return runErr
	}
	return decodeErr
}

// cliReplyResult is the result of the reply env, from a run that ended with
// runErr. A reply marked is_error, or from a run that failed, is an error,
// not an answer.
func cliReplyResult(env *cliResultEnvelope, runErr error) (*RunResult, error) {
	res := &RunResult{
		FinalText:   strings.TrimSpace(env.Result),
		Turns:       env.NumTurns,
		ToolUses:    0, // CLI hides per-tool audit from us
		Invocations: nil,
		StopReason:  env.StopReason,
		// The CLI runs its own tool loop and reports none of its calls.
		ToolCallsUnreported: true,
	}
	cliUsage(env, res)
	err := runErr
	if env.IsError {
		err = cliReportedError(env)
	}
	if err != nil {
		return failedCLIResult(res, err)
	}
	// The CLI reports the stop reason of its final reply only, so a turn it
	// cut off inside its own loop is not counted.
	if env.StopReason == "max_tokens" {
		res.CutOffCalls = 1
	}
	// The CLI is never sent req.MaxTokens, so the cap it stopped at is its own.
	return res, incompleteReply("claude CLI", env.StopReason, "max_tokens", res.FinalText, 0)
}

// cliReportedError is the error for a reply marked is_error: its subtype,
// when that is not success, and what its result and errors say.
func cliReportedError(env *cliResultEnvelope) error {
	what := "is_error=true"
	if env.Subtype != "" && env.Subtype != "success" {
		what += " (" + env.Subtype + ")"
	}
	why := env.Errors
	if env.Result != "" {
		why = append([]string{env.Result}, why...)
	}
	return fmt.Errorf("claude CLI reported %s: %s", what, truncate(strings.Join(why, "; "), 500))
}

// failedCLIResult returns err for res, a reply that is an error, not an
// answer. The tokens it reports were spent, so they come back with the error
// to be charged; a reply that reports none made no call that answered.
func failedCLIResult(res *RunResult, err error) (*RunResult, error) {
	res.FinalText = ""
	if res.InputTokens == 0 && res.OutputTokens == 0 {
		return nil, err
	}
	return res, err
}

// writeMCPConfig writes a one-shot MCP config JSON pointing at the
// chb-mcp sidecar. Returns the file path + a cleanup func.
func (b *CLIBackend) writeMCPConfig() (string, func(), error) {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"chb": map[string]any{
				"command": b.MCPServerPath,
				"args":    []string{},
				"env": map[string]string{
					"HIVE_DB_PATH": b.DBPath,
				},
			},
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", func() {}, err
	}
	f, err := os.CreateTemp(b.ScratchDir, "chb-mcp-*.json")
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return "", func() {}, err
	}
	f.Close()
	return path, func() { os.Remove(path) }, nil
}

// canonicalCLIModel normalizes HIVE's model aliases to names the
// Claude Code CLI understands. The CLI accepts both aliases ("sonnet",
// "opus", "haiku") and full IDs.
func canonicalCLIModel(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "sonnet", "claude-sonnet", "sonnet-4-6":
		return "sonnet"
	case "opus", "claude-opus", "opus-4-6":
		return "opus"
	case "haiku", "claude-haiku", "haiku-4-5":
		return "haiku"
	}
	return name
}

// cliToolRegistryName maps each Claude Code built-in the CLI backend allows
// to the in-process registry tool it stands for, so a node's `tools:`
// allowlist, written in registry names, selects the built-ins.
var cliToolRegistryName = map[string]string{
	"Read":     "read_file",
	"Write":    "write_file",
	"Edit":     "edit_file",
	"Glob":     "glob",
	"Grep":     "grep",
	"Bash":     "shell",
	"WebFetch": "web_fetch",
}

// joinTools returns a comma-separated string for --allowed-tools.
func joinTools(tools []string) string { return strings.Join(tools, ",") }

// firstNonEmptyString returns the first argument that is not empty.
func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
