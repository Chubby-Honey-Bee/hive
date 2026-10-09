package runner

// gemini CLI subprocess backend.
//
// Wraps Google's `gemini` CLI (https://github.com/google-gemini/gemini-cli)
// as an LLMBackend so users can run autonomous workflows on whichever model
// `gemini` has authenticated, without setting GEMINI_API_KEY explicitly.
//
// Tool-use lives inside the CLI subprocess (gemini CLI has its own tool
// execution loop), so this backend doesn't drive tool_use turns from Go.
// That mirrors how the existing `claude` CLI backend operates.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// _geminiCLIDeterministicWarnOnce deduplicates the per-process warning that
// --deterministic / --seed have no effect on the gemini CLI backend.
var _geminiCLIDeterministicWarnOnce sync.Once

// _geminiCLIToolsWarnOnce deduplicates the per-process warning that a node's
// `tools:` allowlist does not reach the gemini CLI.
var _geminiCLIToolsWarnOnce sync.Once

// geminiCLIHelpTimeout bounds the `--help` probe (geminiCLITakesJSON).
var geminiCLIHelpTimeout = 30 * time.Second

// geminiCLIProbes holds, per CLI path, the latest `--help` probe.
var geminiCLIProbes sync.Map // CLI path → *geminiCLIProbeSlot

type geminiCLIProbeSlot struct {
	mu    sync.Mutex
	probe *geminiCLIProbe
}

// probeOf is the slot's probe of the CLI at path: the latest, or a new one
// it starts when there is none or the latest failed.
func (s *geminiCLIProbeSlot) probeOf(path string) *geminiCLIProbe {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.probe == nil || s.probe.failed() {
		s.probe = &geminiCLIProbe{done: make(chan struct{})}
		go s.probe.run(path)
	}
	return s.probe
}

// geminiCLIProbe is one `<path> --help`. Its fields are set before done
// closes.
type geminiCLIProbe struct {
	done chan struct{}
	// answered is true when --help printed its help, so the answer holds
	// for the process.
	answered bool
	json     bool
	// why says, when json is false, why the CLI's calls report no token
	// counts.
	why string
}

// failed reports whether p ended without an answer.
func (p *geminiCLIProbe) failed() bool {
	select {
	case <-p.done:
		return !p.answered
	default:
		return false
	}
}

func (p *geminiCLIProbe) run(path string) {
	defer close(p.done)
	ctx, cancel := context.WithTimeout(context.Background(), geminiCLIHelpTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--help")
	// A wrapper that runs the CLI as its child leaves the output pipe open
	// when the timeout kills the wrapper; WaitDelay closes it.
	cmd.WaitDelay = time.Second
	help, err := cmd.CombinedOutput()
	switch {
	case bytes.Contains(help, []byte("--output-format")):
		p.answered, p.json = true, true
	case err != nil:
		p.why = fmt.Sprintf("%s --help failed, so chb cannot tell whether it takes --output-format json: %v", path, err)
	default:
		p.answered = true
		p.why = fmt.Sprintf("%s --help lists no --output-format flag, which gemini-cli has from 0.6.0", path)
	}
}

// geminiCLITakesJSON reports whether the gemini CLI at path has
// --output-format, asked by `<path> --help`. The CLI answers --help while it
// parses its flags, before it signs in or calls a model, so the probe costs
// nothing. gemini-cli has the flag from 0.6.0. When the CLI has none, why
// says so. Calls at once share one probe. An answer holds for the process;
// a probe that failed is run again by the next call. A call whose ctx ends
// first stops waiting and leaves the probe running for the others.
func geminiCLITakesJSON(ctx context.Context, path string) (ok bool, why string) {
	v, _ := geminiCLIProbes.LoadOrStore(path, &geminiCLIProbeSlot{})
	p := v.(*geminiCLIProbeSlot).probeOf(path)
	select {
	case <-p.done:
		return p.json, p.why
	case <-ctx.Done():
		return false, fmt.Sprintf("the call ended before %s --help answered: %v", path, ctx.Err())
	}
}

// GeminiCLIBackend shells out to the `gemini` CLI.
type GeminiCLIBackend struct {
	CLIPath    string
	ProjectDir string
	// DBPath is the run's database, forwarded to the agent as
	// HIVE_DB_PATH (agentEnv).
	DBPath string
}

// newGeminiCLIBackend returns a configured backend or an error if the
// `gemini` binary isn't on PATH.
func newGeminiCLIBackend(cfg Config) (*GeminiCLIBackend, error) {
	cli := os.Getenv("GEMINI_CLI_PATH")
	if cli == "" {
		cli = "gemini"
	}
	if _, err := exec.LookPath(cli); err != nil {
		return nil, fmt.Errorf("gemini CLI not found (set GEMINI_CLI_PATH or install gemini-cli): %w", err)
	}
	return &GeminiCLIBackend{CLIPath: cli, ProjectDir: cfg.ProjectDir, DBPath: firstNonEmptyString(cfg.DBPath, os.Getenv("HIVE_DB_PATH"))}, nil
}

// Run dispatches a single prompt to the `gemini` CLI. Stderr is preserved on
// error so the operator sees auth failures or rate limits in the run log.
//
// `gemini -p <prompt>` is the canonical headless invocation; we honor a
// model override via `-m <model>` when the workflow YAML pins one. A CLI
// that takes --output-format json is asked for it, and its reply gives the
// final text and each model's tokens; an older one's stdout is the final
// text, and its call is unmetered. A call whose context ends, cancelled or
// past its bound, fails with the context's error, as the claude CLI's does;
// one whose CLI exits non-zero is read for the reply it printed
// (geminiCLIFailed).
func (b *GeminiCLIBackend) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	warnCLISampling(&_geminiCLIDeterministicWarnOnce, req)
	warnGeminiCLIUnsupported(req)
	takesJSON, why := geminiCLITakesJSON(ctx, b.CLIPath)
	var stdout, stderr bytes.Buffer
	runErr := b.command(ctx, geminiCLIArgs(takesJSON, req.Model), geminiCLIInput(req), &stdout, &stderr).Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("gemini CLI timed out or cancelled: %w", ctx.Err())
	}
	if runErr != nil {
		return geminiCLIFailed(takesJSON, stdout.Bytes(), stderr.Bytes(), runErr)
	}
	if takesJSON {
		return geminiCLIJSONReply(stdout.Bytes(), req.MaxTokens)
	}
	return geminiCLITextReply(stdout.String(), why, req.MaxTokens)
}

// geminiCLIFailed is the result of a call whose CLI exited with runErr,
// having printed stdout and stderr. A CLI that takes --output-format json is
// read as the claude CLI is: the reply it printed, on stdout, else on
// stderr, where gemini-cli prints an error it throws, comes back with the
// error when it reports tokens, so they are charged, and the error it
// carries joins the call's error, with the HTTP status its code gives. A
// reply that reports no tokens, or none at all, gives no result: no call
// answered.
func geminiCLIFailed(takesJSON bool, stdout, stderr []byte, runErr error) (*RunResult, error) {
	err := fmt.Errorf("gemini CLI: %w (stderr: %s)", runErr, truncate(string(stderr), 600))
	r := geminiCLIFailedReply(takesJSON, stdout, stderr)
	if r == nil {
		return nil, err
	}
	res, replyErr := geminiCLIReplyResult(r)
	if replyErr != nil {
		err = fmt.Errorf("%w: %w", err, replyErr)
	}
	return failedCLIResult(res, err)
}

// geminiCLIFailedReply is the reply a CLI that exited non-zero printed: on
// stdout, else on stderr; nil when it takes no --output-format json, or
// printed none.
func geminiCLIFailedReply(takesJSON bool, stdout, stderr []byte) *geminiCLIReply {
	if !takesJSON {
		return nil
	}
	if r := geminiCLIReplyIn(stdout); r != nil {
		return r
	}
	return geminiCLIReplyIn(stderr)
}

// warnGeminiCLIUnsupported warns once each that the CLI is passed no output
// cap and no tool allowlist, when req sets them.
func warnGeminiCLIUnsupported(req RunRequest) {
	if req.MaxTokens > 0 {
		warnCLIMaxTokensOnce()
	}
	if req.Registry != nil && req.Registry.Restricted() {
		// The gemini CLI runs its own tool set and has no flag that narrows
		// it per call, so the allowlist cannot reach it.
		_geminiCLIToolsWarnOnce.Do(func() {
			fmt.Fprintln(os.Stderr, "[agent-run] a node's tools: allowlist has no effect on the gemini CLI backend (the CLI offers its own tools)")
		})
	}
}

// geminiCLIArgs are the CLI's arguments: --output-format json when it takes
// it, and model, resolved, when one is set.
//
// One message, one channel: stdin, which is what docs/specs/runner.md names
// for this backend and what the claude backend does. The prompt goes once,
// the system prompt ahead of it, and a long prompt never meets the
// platform's argument-length limit.
//
// From gemini-cli 0.12.0, -p takes exactly one value (nargs: 1), and a
// bare -p, or one followed by another flag, fails the parse. So -p gets
// an empty value; the CLI puts stdin ahead of it.
func geminiCLIArgs(takesJSON bool, model string) []string {
	var args []string
	if takesJSON {
		args = append(args, "--output-format", "json")
	}
	args = append(args, "-p", "")
	if model != "" {
		args = append(args, "-m", ResolveGeminiModel(model))
	}
	return args
}

// geminiCLIInput is what the CLI reads on stdin: the prompt, after the
// system prompt and a blank line when req has one.
func geminiCLIInput(req RunRequest) string {
	if req.System != "" {
		return req.System + "\n\n" + req.Prompt
	}
	return req.Prompt
}

// command is the gemini CLI run with args, input on stdin and its output in
// stdout and stderr, in the project directory, with the agent environment.
func (b *GeminiCLIBackend) command(ctx context.Context, args []string, input string, stdout, stderr *bytes.Buffer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, b.CLIPath, args...)
	cmd.Env = modelEnv(b.DBPath)
	if b.ProjectDir != "" {
		cmd.Dir = b.ProjectDir
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = strings.NewReader(input)
	// As a command node's program: a cancelled call kills the CLI's whole
	// group, and a child that keeps stdout open holds Wait for at most 5 s
	// after the CLI ends.
	killProcessGroupOnCancel(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// geminiCLIJSONReply is the result of the CLI's --output-format json reply,
// stdout, from a call sent maxTokens as its cap.
func geminiCLIJSONReply(stdout []byte, maxTokens int64) (*RunResult, error) {
	res, err := geminiCLIJSONResult(stdout)
	if err != nil {
		return res, fmt.Errorf("gemini CLI: %w", err)
	}
	// The CLI reports no stop reason. Past its error and warnings, only
	// an empty answer is caught.
	return res, incompleteReply("gemini CLI", res.StopReason, "", res.FinalText, maxTokens)
}

// geminiCLITextReply is the result of a CLI without --output-format json,
// whose stdout, out, is the final text; why says why its usage is
// unreported. The CLI's plain-text output carries no token usage, so the
// call's tokens read 0 and its cost is unknown: it is counted unmetered,
// never as $0 on a priced model.
func geminiCLITextReply(out, why string, maxTokens int64) (*RunResult, error) {
	res := &RunResult{
		FinalText:    out,
		Turns:        1,
		StopReason:   "end_turn",
		InputTokens:  0,
		OutputTokens: 0,
		// The CLI runs its own tool loop and reports none of its calls.
		ToolCallsUnreported: true,
		UsageUnreported:     true,
		UsageUnreportedWhy:  why,
	}
	// The CLI reports no stop reason, so only an empty answer is caught.
	return res, incompleteReply("gemini CLI", res.StopReason, "", out, maxTokens)
}

// geminiCLIReply is what the CLI prints for --output-format json, the
// fields chb reads: https://google-gemini.github.io/gemini-cli/docs/cli/headless.html
// (last modified 2025-10-07). A model's prompt tokens include its cached
// ones, and its total is prompt + candidates + thoughts + tool.
type geminiCLIReply struct {
	Response *string `json:"response"`
	Stats    *struct {
		Models map[string]geminiCLIModelStats `json:"models"`
	} `json:"stats"`
	Error *geminiCLIError `json:"error"`
	// Warnings is what gemini-cli 0.42.0 and later reports when it stopped
	// or held back the agent: a loop detected, the session turn cap, a hook
	// that stopped or blocked it.
	Warnings []string `json:"warnings"`
}

// geminiCLIError is the error a reply carries. Code is a number or a name,
// as the CLI gives it; a number from 400 to 599 is read as the HTTP status
// the failed call got (status).
type geminiCLIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    any    `json:"code"`
}

// status is the HTTP error status e's code gives: the code when it is a
// whole number from 400 to 599; 0 when it is not one, such as a name or an
// exit code, which is under 256.
func (e *geminiCLIError) status() int {
	n, _ := e.Code.(float64)
	if code := int(n); float64(code) == n && code >= 400 && code <= 599 {
		return code
	}
	return 0
}

// isReply reports whether r holds what a reply holds: a response, stats or
// an error.
func (r *geminiCLIReply) isReply() bool {
	return r.Response != nil || r.Stats != nil || r.Error != nil
}

// geminiCLIModelStats is one model's entry in the reply's stats.models.
type geminiCLIModelStats struct {
	API struct {
		TotalRequests int `json:"totalRequests"`
		TotalErrors   int `json:"totalErrors"`
	} `json:"api"`
	Tokens struct {
		Prompt     int64 `json:"prompt"`
		Candidates int64 `json:"candidates"`
		Cached     int64 `json:"cached"`
		Thoughts   int64 `json:"thoughts"`
		Tool       int64 `json:"tool"`
	} `json:"tokens"`
}

// geminiCLITruncated opens the message gemini-cli 0.54.0 and later give an
// INVALID_STREAM error when the model's stream ended at MAX_TOKENS with no
// text (MAX_TOKENS_EXCEEDED_SUGGESTION, packages/core/src/utils/constants.ts).
const geminiCLITruncated = "Model response was truncated because it exceeded the token limit."

// geminiCLIJSONResult reads raw, the CLI's stdout, for its --output-format
// json reply (geminiCLIReplyResult). A reply that is not that JSON returns
// an error with an unmetered result, so the answered call is still counted.
func geminiCLIJSONResult(raw []byte) (*RunResult, error) {
	if r := geminiCLIReplyIn(raw); r != nil {
		return geminiCLIReplyResult(r)
	}
	res := &RunResult{
		StopReason: "end_turn",
		Turns:      1,
		// The CLI runs its own tool loop and reports none of its calls.
		ToolCallsUnreported: true,
		UsageUnreported:     true,
		UsageUnreportedWhy:  "its reply was not the JSON --output-format json prints",
	}
	return res, fmt.Errorf("the reply is not the JSON --output-format json prints: %s", truncate(string(raw), 600))
}

// geminiCLIReplyResult is the result of r, the CLI's --output-format json
// reply. The final text is its response. Each model in stats.models is one
// ModelUsage: input is its prompt tokens, cached ones included, plus its
// tool-use prompt tokens; cached is its cached tokens; output is its
// candidates plus its thoughts. A model that answered no request and spent
// no token is left out. Its calls are the requests the models answered. The
// reply is unmetered when its stats.models names no model that spent
// tokens, or a model that answered a request reports none. A reply that
// carries an error or warnings returns an error with the result, so the
// answered call is still counted. An INVALID_STREAM error that says the
// response was truncated at the token limit counts one call cut off at the
// cap. That is a lower bound: gemini-cli makes up to 4 attempts at a stream
// that ends that way (MID_STREAM_RETRY_OPTIONS,
// packages/core/src/core/geminiChat.ts), and the reply does not say how
// many of them ended at the cap.
func geminiCLIReplyResult(r *geminiCLIReply) (*RunResult, error) {
	res := &RunResult{
		StopReason: "end_turn",
		// The CLI runs its own tool loop and reports none of its calls.
		ToolCallsUnreported: true,
	}
	if r.Response != nil {
		res.FinalText = *r.Response
	}
	if unreported := geminiCLIUsage(r, res); unreported != "" {
		// The cost is unknown, never $0: the call is unmetered.
		res.Turns = max(res.Turns, 1)
		res.UsageUnreported = true
		res.UsageUnreportedWhy = unreported
	}
	return res, geminiCLIReplyError(r, res)
}

// geminiCLIReplyIn is the reply in raw, the CLI's stdout: the first line
// that opens a JSON object holding a reply (isReply). A line the CLI prints
// ahead of it is skipped. nil when no line does.
func geminiCLIReplyIn(raw []byte) *geminiCLIReply {
	for rest := raw; len(rest) > 0; {
		if r := geminiCLIReplyAt(rest); r != nil {
			return r
		}
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			return nil
		}
		rest = rest[i+1:]
	}
	return nil
}

// geminiCLIReplyAt is the reply s opens with, nil when s, which is not
// empty, does not open with one.
func geminiCLIReplyAt(s []byte) *geminiCLIReply {
	if s[0] != '{' {
		return nil
	}
	var r geminiCLIReply
	if json.NewDecoder(bytes.NewReader(s)).Decode(&r) != nil || !r.isReply() {
		return nil
	}
	return &r
}

// geminiCLIUsage sets res's calls and tokens from r's stats.models and says
// why the reply's usage is unreported: it has no stats.models, a model that
// answered requests reports no tokens, or no model spent any. "" when the
// usage is reported.
func geminiCLIUsage(r *geminiCLIReply, res *RunResult) string {
	if r.Stats == nil || r.Stats.Models == nil {
		return "its --output-format json reply has no stats.models"
	}
	unreported := addGeminiCLIModels(res, r.Stats.Models)
	if len(res.ByModel) == 0 {
		return cmp.Or(unreported, "its --output-format json reply's stats.models names no model that spent tokens")
	}
	return unreported
}

// addGeminiCLIModels adds each model's stats to res, in name order, and says
// why the usage is unreported for the first model that answered requests and
// reports no tokens; "" when none did.
func addGeminiCLIModels(res *RunResult, stats map[string]geminiCLIModelStats) string {
	var unreported string
	for _, name := range slices.Sorted(maps.Keys(stats)) {
		unreported = cmp.Or(unreported, addGeminiCLIModel(res, name, stats[name]))
	}
	return unreported
}

// addGeminiCLIModel adds model name's stats m to res: the requests it
// answered as calls, and its tokens as one ModelUsage when it spent any.
// Input is its prompt tokens, cached ones included, plus its tool-use prompt
// tokens; output is its candidates plus its thoughts. It says why the usage
// is unreported when the model answered requests and reports no tokens, ""
// otherwise.
func addGeminiCLIModel(res *RunResult, name string, m geminiCLIModelStats) string {
	u := ModelUsage{
		Model:             name,
		InputTokens:       m.Tokens.Prompt + m.Tokens.Tool,
		CachedInputTokens: m.Tokens.Cached,
		OutputTokens:      m.Tokens.Candidates + m.Tokens.Thoughts,
	}
	answered := m.API.TotalRequests - m.API.TotalErrors
	res.Turns += answered
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return geminiCLINoTokens(name, answered)
	}
	res.ByModel = append(res.ByModel, u)
	res.InputTokens += u.InputTokens
	res.CachedInputTokens += u.CachedInputTokens
	res.OutputTokens += u.OutputTokens
	return ""
}

// geminiCLINoTokens says why a model that reports no tokens leaves the
// usage unreported: it answered requests. "" when it answered none.
func geminiCLINoTokens(name string, answered int) string {
	if answered <= 0 {
		return ""
	}
	return fmt.Sprintf("its --output-format json reply's stats.models reports no tokens for %s, which answered %d request(s)", name, answered)
}

// geminiCLIReplyError is the error r carries, nil when it carries none: its
// error (geminiCLIReportedError), else its warnings, which may leave the
// answer cut short.
func geminiCLIReplyError(r *geminiCLIReply, res *RunResult) error {
	if r.Error != nil {
		return geminiCLIReportedError(r.Error, res)
	}
	if len(r.Warnings) > 0 {
		return &incompleteReplyError{"the reply carries warnings, so its answer may be cut short: " + strings.Join(r.Warnings, "; ")}
	}
	return nil
}

// geminiCLIReportedError is the error for a reply whose error is e. An
// INVALID_STREAM error is an incomplete reply: the model's stream ended with
// no usable reply, empty, cut off at the output cap, or blocked. One whose
// message says the response was truncated at the token limit counts one
// call cut off at the cap in res. Any other carries the HTTP status its code
// gives (withStatus), so an API error the CLI reports is classed by its
// status, as the REST backends' are.
func geminiCLIReportedError(e *geminiCLIError, res *RunResult) error {
	err := fmt.Errorf("%s: %s", e.Type, e.Message)
	if e.Type != "INVALID_STREAM" {
		return withStatus(err, e.status())
	}
	if strings.HasPrefix(e.Message, geminiCLITruncated) {
		res.CutOffCalls = 1
	}
	return &incompleteReplyError{err.Error()}
}
