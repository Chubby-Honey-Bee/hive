package runner

// Tests for pricing a gemini CLI call from the token stats its
// --output-format json reply carries, and for the older CLI without that
// flag. Each CLI is a shell script and the Gemini API an httptest fake; no
// model is called.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// cliModel is one model's entry in a fake CLI's stats.models.
type cliModel struct {
	Requests, Errors                           int
	Prompt, Candidates, Cached, Thoughts, Tool int64
}

// jsonHelp is the --help branch of a CLI that has --output-format, with the
// flag's line as gemini-cli prints it.
const jsonHelp = `case " $* " in *" --help "*) echo '  -o, --output-format  The format of the CLI output.  [string] [choices: "text", "json", "stream-json"]'; exit 0;; esac
`

// jsonGeminiCLI is the body of a gemini CLI that takes --output-format json:
// its --help lists the flag, a call without the flag fails, and a call
// prints preamble, then response with stats.models from ms in the shape the
// headless docs give.
func jsonGeminiCLI(t *testing.T, response string, ms map[string]cliModel, preamble string) string {
	t.Helper()
	return jsonReplyCLI(preamble + printJSON(t, map[string]any{"response": response, "stats": cliStats(ms)}))
}

// cliStats is the stats of a reply whose stats.models holds ms, in the
// shape the headless docs give.
func cliStats(ms map[string]cliModel) map[string]any {
	models := map[string]any{}
	for name, m := range ms {
		models[name] = map[string]any{
			"api": map[string]any{"totalRequests": m.Requests, "totalErrors": m.Errors, "totalLatencyMs": 1000},
			"tokens": map[string]any{
				"prompt": m.Prompt, "candidates": m.Candidates, "cached": m.Cached, "thoughts": m.Thoughts, "tool": m.Tool,
				"total": m.Prompt + m.Candidates + m.Thoughts + m.Tool,
			},
		}
	}
	return map[string]any{
		"models": models,
		"tools":  map[string]any{"totalCalls": 0, "totalSuccess": 0, "totalFail": 0, "totalDurationMs": 0},
		"files":  map[string]any{"totalLinesAdded": 0, "totalLinesRemoved": 0},
	}
}

// printJSON is a shell command that prints reply as indented JSON.
func printJSON(t *testing.T, reply map[string]any) string {
	t.Helper()
	js, err := json.MarshalIndent(reply, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return "cat <<'GEMINI_JSON'\n" + string(js) + "\nGEMINI_JSON\n"
}

// jsonReplyCLI is the body of a gemini CLI that takes --output-format json
// and answers a call by running reply, with the prompt it read from stdin
// in $input. Like gemini-cli 0.12.0 and later, it fails a -p that has no
// value or is followed by another flag.
func jsonReplyCLI(reply string) string {
	return jsonHelp + `case " $* " in *" --output-format json "*) ;; *) echo 'Unknown argument: the call lacks --output-format json' >&2; exit 42;; esac
prev=
for a in "$@"; do
  if [ "$prev" = "-p" ]; then case "$a" in -*) echo 'Not enough arguments following: p' >&2; exit 1;; esac; fi
  prev=$a
done
if [ "$prev" = "-p" ]; then echo 'Not enough arguments following: p' >&2; exit 1; fi
input=$(cat)
` + reply
}

// oldGeminiCLI is the body of a gemini CLI from before --output-format: its
// --help lists no such flag, it refuses the flag, and it prints answer.
func oldGeminiCLI(answer string) string {
	return `case " $* " in *" --help "*) echo '  -p, --prompt  Prompt. Appended to input on stdin (if any).  [string]'; echo '  -m, --model  Model  [string]'; exit 0;; esac
case " $* " in *" --output-format"*) echo 'Unknown argument: output-format' >&2; exit 1;; esac
cat >/dev/null
printf '%s\n' '` + answer + `'
`
}

// ratesX10000 are a model's prices in 1/10000 USD per 1M tokens, read from
// its models-config entry: a cached token costs the cached price when the
// entry gives one, else the input price.
func ratesX10000(t *testing.T, model string) (in, cached, out int64) {
	t.Helper()
	m, ok := models.Load().Models[model]
	if !ok {
		t.Fatalf("precondition: %q has no models-config entry", model)
	}
	c := m.InputPerMTokUSD
	if m.CachedInputPerMTokUSD != nil {
		c = *m.CachedInputPerMTokUSD
	}
	return int64(m.InputPerMTokUSD * 10_000), int64(c * 10_000), int64(m.OutputPerMTokUSD * 10_000)
}

// cachedCost is a call's cost on model, in 1/10000 USD, for in input tokens
// of which cached were read from the cache, and out output tokens.
func cachedCost(t *testing.T, model string, in, cached, out int64) int64 {
	t.Helper()
	pin, pcached, pout := ratesX10000(t, model)
	return ((in-cached)*pin + cached*pcached + out*pout) / 1_000_000
}

// cliCall is what one call of a fake CLI with stats ms should record: its
// usage per model, sorted, leaving out a model that spent no token; its
// answered requests; its cost times 1,000,000 over the priced models; and
// whether its usage is unreported: a model that answered a request spent no
// token, or no model spent any. An unreported call whose models answered no
// request counts one call.
func cliCall(t *testing.T, ms map[string]cliModel) (usage []ModelUsage, calls int, costX1e6 int64, unreported bool) {
	t.Helper()
	names := make([]string, 0, len(ms))
	for name := range ms {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		m := ms[name]
		calls += m.Requests - m.Errors
		if m.Prompt+m.Tool+m.Candidates+m.Thoughts == 0 {
			unreported = unreported || m.Requests > m.Errors
			continue
		}
		u := ModelUsage{Model: name, InputTokens: m.Prompt + m.Tool, CachedInputTokens: m.Cached, OutputTokens: m.Candidates + m.Thoughts}
		usage = append(usage, u)
		if !isMetered(name) {
			continue
		}
		pin, pcached, pout := ratesX10000(t, name)
		costX1e6 += (u.InputTokens-u.CachedInputTokens)*pin + u.CachedInputTokens*pcached + u.OutputTokens*pout
	}
	if unreported || len(usage) == 0 {
		return usage, max(calls, 1), costX1e6, true
	}
	return usage, calls, costX1e6, false
}

// docStats are the stats of the example reply in the gemini-cli headless
// docs, plus a model whose one request failed and spent no token.
var docStats = map[string]cliModel{
	"gemini-2.5-pro":   {Requests: 2, Prompt: 24939, Candidates: 20, Cached: 21263, Thoughts: 154},
	"gemini-2.5-flash": {Requests: 1, Prompt: 8965, Candidates: 10, Thoughts: 30, Tool: 28},
	"gemini-9-preview": {Requests: 1, Errors: 1},
}

// TestGeminiCLIBackend_JSONStats: a CLI that has --output-format is sent
// --output-format json, and its reply gives the final text and each model's
// tokens: input is prompt plus tool, cached apart, output is candidates plus
// thoughts. A model that spent no token is left out, and the call counts the
// requests the models answered. A line printed ahead of the JSON is skipped.
func TestGeminiCLIBackend_JSONStats(t *testing.T) {
	const response = "{\"answer\":\"ok\"}\nwith a second line"
	wantUsage, wantCalls, _, unreported := cliCall(t, docStats)
	if unreported {
		t.Fatal("precondition: the docs' stats leave the call's usage unreported")
	}
	for _, preamble := range []string{"", "echo 'Loaded cached credentials.'\n"} {
		t.Run(fmt.Sprintf("preamble %q", preamble), func(t *testing.T) {
			b := &GeminiCLIBackend{CLIPath: fakeGeminiScript(t, jsonGeminiCLI(t, response, docStats, preamble))}
			res, err := b.Run(context.Background(), RunRequest{Prompt: "p", Model: "gemini-2.5-pro"})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.FinalText != response {
				t.Errorf("FinalText = %q, want %q", res.FinalText, response)
			}
			if !slices.Equal(res.ByModel, wantUsage) {
				t.Errorf("ByModel = %+v, want %+v", res.ByModel, wantUsage)
			}
			var in, cached, out int64
			for _, u := range wantUsage {
				in, cached, out = in+u.InputTokens, cached+u.CachedInputTokens, out+u.OutputTokens
			}
			if res.InputTokens != in || res.CachedInputTokens != cached || res.OutputTokens != out {
				t.Errorf("tokens in/cached/out = %d/%d/%d, want %d/%d/%d", res.InputTokens, res.CachedInputTokens, res.OutputTokens, in, cached, out)
			}
			if res.Turns != wantCalls {
				t.Errorf("Turns = %d, want %d answered requests", res.Turns, wantCalls)
			}
			if res.UsageUnreported {
				t.Errorf("usage reported unreported: %s", res.UsageUnreportedWhy)
			}
		})
	}
}

// TestGeminiCLIBackend_JSONReplyFaults: a reply that is not the JSON, or
// that carries an error or warnings, fails the call but still returns it,
// so its calls are counted. An INVALID_STREAM error and warnings, which
// gemini-cli 0.42.0 and later print with the reply's stats, are incomplete
// replies, never read as rate limits. A reply whose stats.models is missing
// or empty, or reports no tokens for a model that answered a request, is
// unmetered and says why: its cost is unknown, not $0. An error the CLI
// prints on stderr as it exits non-zero, as gemini-cli does for an error it
// throws, returns no result, so no call is counted.
func TestGeminiCLIBackend_JSONReplyFaults(t *testing.T) {
	answeredNoTokens := map[string]cliModel{"gemini-2.5-flash": {Requests: 2}}
	onlyFailed := map[string]cliModel{"gemini-2.5-flash": {Requests: 1, Errors: 1}}
	warnings := []string{"Loop detected, stopping execution", "Maximum session turns exceeded"}
	cases := []struct {
		name  string
		reply string
		// stats is the reply's stats.models, nil when it has none.
		stats      map[string]cliModel
		wantErr    string
		incomplete bool
		wantText   string
		// why is part of UsageUnreportedWhy, empty when usage is reported.
		why string
		// cutOff is the calls the reply says were cut off at the cap.
		cutOff int
	}{
		{name: "not JSON", reply: "echo 'plain words'\n", wantErr: "not the JSON --output-format json prints", why: "not the JSON"},
		{name: "no stats", reply: printJSON(t, map[string]any{"response": "only text"}), wantText: "only text", why: "has no stats.models"},
		{name: "empty stats.models", reply: printJSON(t, map[string]any{"response": "only text", "stats": cliStats(nil)}), stats: map[string]cliModel{}, wantText: "only text", why: "names no model that spent tokens"},
		{name: "answered with no tokens", reply: printJSON(t, map[string]any{"response": "only text", "stats": cliStats(answeredNoTokens)}), stats: answeredNoTokens, wantText: "only text", why: "reports no tokens for gemini-2.5-flash"},
		{name: "only failed requests", reply: printJSON(t, map[string]any{"response": "only text", "stats": cliStats(onlyFailed)}), stats: onlyFailed, wantText: "only text", why: "names no model that spent tokens"},
		{
			name: "invalid stream",
			reply: printJSON(t, map[string]any{"response": "", "stats": cliStats(docStats), "error": map[string]any{
				"type": "INVALID_STREAM", "message": "Invalid stream: The model returned an empty response or malformed tool call.",
			}}),
			stats: docStats, wantErr: "INVALID_STREAM: Invalid stream", incomplete: true,
		},
		{
			// gemini-cli 0.54.0 and later: the stream ended at MAX_TOKENS with
			// no text (MAX_TOKENS_EXCEEDED_SUGGESTION in core's constants.ts).
			name: "truncated at the token limit",
			reply: printJSON(t, map[string]any{"response": "", "stats": cliStats(docStats), "error": map[string]any{
				"type": "INVALID_STREAM", "message": "Model response was truncated because it exceeded the token limit. Try using /compress to free up context space.",
			}}),
			stats: docStats, wantErr: "INVALID_STREAM: Model response was truncated", incomplete: true, cutOff: 1,
		},
		{
			name:  "warnings",
			reply: printJSON(t, map[string]any{"response": "partial", "stats": cliStats(docStats), "warnings": warnings}),
			stats: docStats, wantErr: strings.Join(warnings, "; "), incomplete: true, wantText: "partial",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &GeminiCLIBackend{CLIPath: fakeGeminiScript(t, jsonReplyCLI(c.reply))}
			res, err := b.Run(context.Background(), RunRequest{Prompt: "p"})
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("Run: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
			}
			var incomplete *incompleteReplyError
			if got := errors.As(err, &incomplete); got != c.incomplete || (c.incomplete && isRateLimitError(err)) {
				t.Errorf("err %v: incomplete reply %v, rate limit %v; want incomplete %v and no rate limit", err, got, isRateLimitError(err), c.incomplete)
			}
			if res == nil {
				t.Fatal("no result, so the answered call is not counted")
			}
			wantCalls, unreported := 1, true
			if c.stats != nil {
				_, wantCalls, _, unreported = cliCall(t, c.stats)
			}
			if unreported != (c.why != "") {
				t.Fatalf("precondition: the stats leave usage unreported %v, but the case expects a reason %q", unreported, c.why)
			}
			if callsMade(res) != wantCalls {
				t.Errorf("callsMade = %d, want %d", callsMade(res), wantCalls)
			}
			if res.FinalText != c.wantText {
				t.Errorf("FinalText = %q, want %q", res.FinalText, c.wantText)
			}
			if res.CutOffCalls != c.cutOff {
				t.Errorf("CutOffCalls = %d, want %d", res.CutOffCalls, c.cutOff)
			}
			if res.UsageUnreported != unreported || !strings.Contains(res.UsageUnreportedWhy, c.why) {
				t.Errorf("UsageUnreported = %v (%q), want %v saying %q", res.UsageUnreported, res.UsageUnreportedWhy, unreported, c.why)
			}
		})
	}
	t.Run("error on stderr", func(t *testing.T) {
		b := &GeminiCLIBackend{CLIPath: fakeGeminiScript(t, jsonReplyCLI(`echo '{"error":{"type":"ApiError","message":"quota exhausted","code":429}}' >&2
exit 1
`))}
		res, err := b.Run(context.Background(), RunRequest{Prompt: "p"})
		if err == nil || !strings.Contains(err.Error(), "quota exhausted") {
			t.Errorf("err = %v, want one carrying the CLI's stderr", err)
		}
		if res != nil {
			t.Errorf("result %+v, want none: the CLI reports no usage for a run it failed", res)
		}
	})
}

// TestRun_GeminiCLITruncatedReplyCountsCutOff: a node whose gemini CLI reply
// says the model's response was truncated at the token limit fails, not as
// a rate limit, and its row counts one call cut off at the cap, with the
// tokens the stats report. A reply with warnings fails too, and counts none.
func TestRun_GeminiCLITruncatedReplyCountsCutOff(t *testing.T) {
	usage, _, _, unreported := cliCall(t, docStats)
	if unreported {
		t.Fatal("precondition: the docs' stats leave the call's usage unreported")
	}
	var in int64
	for _, u := range usage {
		in += u.InputTokens
	}
	yaml := `name: cli
nodes:
  cli:
    type: agent
    model: gemini-2.5-flash
    prompt: "answer"
    outputs: [answer]
`
	for _, c := range []struct {
		name   string
		reply  map[string]any
		want   string
		cutOff int
	}{
		{"truncated", map[string]any{"response": "", "stats": cliStats(docStats), "error": map[string]any{
			"type": "INVALID_STREAM", "message": "Model response was truncated because it exceeded the token limit. Try using /compress to free up context space.",
		}}, "Model response was truncated", 1},
		{"warnings", map[string]any{"response": "partial", "stats": cliStats(docStats), "warnings": []string{"Loop detected, stopping execution"}}, "Loop detected", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, store, log, err := geminiCLIRun(t, jsonReplyCLI(printJSON(t, c.reply)), yaml, Config{})
			if err == nil {
				t.Fatal("the run succeeded, want it failed on the node")
			}
			var status, errText string
			var tin int64
			if err := store.ReadDB.QueryRow(`SELECT status, COALESCE(error, ''), tokens_in FROM workflow_node_states WHERE node_name='cli'`).Scan(&status, &errText, &tin); err != nil {
				t.Fatal(err)
			}
			if status != "failed" || !strings.Contains(errText, c.want) {
				t.Errorf("node %s with error %q, want failed naming %q", status, errText, c.want)
			}
			if strings.Contains(log, "RATE_LIMITED") {
				t.Errorf("the node was read as rate limited:\n%s", log)
			}
			if got := readCutoffCalls(t, store, "cli"); got != c.cutOff {
				t.Errorf("cutoff_calls = %d, want %d", got, c.cutOff)
			}
			if tin != in {
				t.Errorf("tokens_in = %d, want the stats' %d", tin, in)
			}
		})
	}
}

// TestGeminiCLIBackend_WithoutJSONFlag: a CLI whose --help lists no
// --output-format, or whose --help fails, is never sent the flag (the old
// CLI refuses it), and its stdout is the answer, unmetered, with the reason.
func TestGeminiCLIBackend_WithoutJSONFlag(t *testing.T) {
	const answer = `{"answer":"ok"}`
	cases := []struct {
		name, body, why string
	}{
		{"old CLI", oldGeminiCLI(answer), "lists no --output-format flag"},
		{"help fails", `case " $* " in *" --help "*) exit 3;; *" --output-format"*) exit 1;; esac
cat >/dev/null
echo '` + answer + `'
`, "--help failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := fakeGeminiScript(t, c.body)
			b := &GeminiCLIBackend{CLIPath: path}
			res, err := b.Run(context.Background(), RunRequest{Prompt: "p", Model: "gemini-2.5-flash"})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if strings.TrimSpace(res.FinalText) != answer {
				t.Errorf("FinalText = %q, want %q", res.FinalText, answer)
			}
			if !res.UsageUnreported || res.InputTokens != 0 || res.OutputTokens != 0 || len(res.ByModel) != 0 {
				t.Errorf("usage = unreported %v, %d/%d tokens, %d models; want unreported and none", res.UsageUnreported, res.InputTokens, res.OutputTokens, len(res.ByModel))
			}
			if !strings.Contains(res.UsageUnreportedWhy, path) || !strings.Contains(res.UsageUnreportedWhy, c.why) {
				t.Errorf("UsageUnreportedWhy = %q, want it to name %s and say %q", res.UsageUnreportedWhy, path, c.why)
			}
		})
	}
}

// TestGeminiCLITakesJSON_AsksOnce: the CLI's --help is run once per CLI path
// in the process, however many calls and backends use it at once.
func TestGeminiCLITakesJSON_AsksOnce(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "help-count")
	body := `case " $* " in *" --help "*) echo x >> '` + counter + `';; esac
` + jsonGeminiCLI(t, "ok", docStats, "")
	path := fakeGeminiScript(t, body)
	const calls = 6
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := &GeminiCLIBackend{CLIPath: path}
			res, err := b.Run(context.Background(), RunRequest{Prompt: fmt.Sprint("p", i)})
			if err == nil && res.UsageUnreported {
				err = fmt.Errorf("call %d unmetered: %s", i, res.UsageUnreportedWhy)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "x\n"); n != 1 {
		t.Errorf("--help ran %d times for %d calls, want once", n, calls)
	}
}

// helpCount is how many times the fake CLI that logs each --help to counter
// was asked.
func helpCount(t *testing.T, counter string) int {
	t.Helper()
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "x\n")
}

// TestGeminiCLITakesJSON_RetriesAFailedProbe: a --help that fails is not
// kept. Its call goes without the flag and is unmetered, saying why, and the
// next call asks again. A --help that answers is kept.
func TestGeminiCLITakesJSON_RetriesAFailedProbe(t *testing.T) {
	dir := t.TempDir()
	counter, failedOnce := filepath.Join(dir, "help-count"), filepath.Join(dir, "failed-once")
	body := `case " $* " in *" --help "*) echo x >> '` + counter + `'; if [ ! -e '` + failedOnce + `' ]; then : > '` + failedOnce + `'; exit 3; fi;; esac
case " $* " in *" --help "*|*" --output-format json "*) ;; *) cat >/dev/null; echo plain; exit 0;; esac
` + jsonGeminiCLI(t, "ok", docStats, "")
	b := &GeminiCLIBackend{CLIPath: fakeGeminiScript(t, body)}
	res, err := b.Run(context.Background(), RunRequest{Prompt: "p"})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !res.UsageUnreported || !strings.Contains(res.UsageUnreportedWhy, "--help failed") {
		t.Errorf("first call: UsageUnreported = %v (%q), want unmetered because --help failed", res.UsageUnreported, res.UsageUnreportedWhy)
	}
	const later = 2
	for i := range later {
		res, err := b.Run(context.Background(), RunRequest{Prompt: "p"})
		if err != nil {
			t.Fatalf("call %d: %v", i+2, err)
		}
		if res.UsageUnreported {
			t.Errorf("call %d unmetered: %s", i+2, res.UsageUnreportedWhy)
		}
	}
	if n := helpCount(t, counter); n != 2 {
		t.Errorf("--help ran %d times for %d calls, want 2: once failed, once answered and kept", n, 1+later)
	}
}

// TestGeminiCLITakesJSON_EndedCallStopsWaiting: a call whose context has
// ended does not wait for a --help still running. The probe goes on, and a
// later call uses its answer without asking again.
func TestGeminiCLITakesJSON_EndedCallStopsWaiting(t *testing.T) {
	old := geminiCLIHelpTimeout
	geminiCLIHelpTimeout = time.Hour
	t.Cleanup(func() { geminiCLIHelpTimeout = old })
	dir := t.TempDir()
	counter, release := filepath.Join(dir, "help-count"), filepath.Join(dir, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o644) })
	body := `case " $* " in *" --help "*) echo x >> '` + counter + `'; while [ ! -e '` + release + `' ]; do sleep 0.05; done;; esac
` + jsonGeminiCLI(t, "ok", docStats, "")
	path := fakeGeminiScript(t, body)
	b := &GeminiCLIBackend{CLIPath: path}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if res, err := b.Run(ended, RunRequest{Prompt: "p"}); err == nil || res != nil {
		t.Fatalf("Run on an ended context = %+v, %v; want no result and an error", res, err)
	}
	v, ok := geminiCLIProbes.Load(path)
	if !ok {
		t.Fatal("no probe was started")
	}
	s := v.(*geminiCLIProbeSlot)
	s.mu.Lock()
	p := s.probe
	s.mu.Unlock()
	select {
	case <-p.done:
		t.Fatal("the probe ended before --help was released, so the call waited for it")
	default:
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(time.Minute):
		t.Fatal("the probe did not end a minute after --help was released")
	}
	res, err := b.Run(context.Background(), RunRequest{Prompt: "p"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.UsageUnreported {
		t.Errorf("unmetered: %s", res.UsageUnreportedWhy)
	}
	if n := helpCount(t, counter); n != 1 {
		t.Errorf("--help ran %d times, want once", n)
	}
}

// TestGeminiCLITakesJSON_HelpTimeoutHolds: a --help past its timeout counts
// as failed, even when a child it started holds its output open, as a
// wrapper that runs the CLI as its child does.
func TestGeminiCLITakesJSON_HelpTimeoutHolds(t *testing.T) {
	old := geminiCLIHelpTimeout
	geminiCLIHelpTimeout = 100 * time.Millisecond
	t.Cleanup(func() { geminiCLIHelpTimeout = old })
	release := filepath.Join(t.TempDir(), "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o644) })
	body := `case " $* " in *" --help "*) (while [ ! -e '` + release + `' ]; do sleep 0.05; done) & wait; exit 0;; esac
cat >/dev/null
echo plain
`
	b := &GeminiCLIBackend{CLIPath: fakeGeminiScript(t, body)}
	done := make(chan *RunResult, 1)
	go func() {
		res, _ := b.Run(context.Background(), RunRequest{Prompt: "p"})
		done <- res
	}()
	select {
	case res := <-done:
		if res == nil || !res.UsageUnreported || !strings.Contains(res.UsageUnreportedWhy, "--help failed") {
			t.Errorf("result %+v, want the call unmetered because --help failed", res)
		}
	case <-time.After(time.Minute):
		t.Fatal("the probe outlived its timeout, waiting on the output its child held open")
	}
}

// TestUsageCost: cached tokens cost the model's cached price, and the input
// price on a model whose entry gives none; each model is priced at its own
// prices; a model with no entry makes the cost unknown and is named.
func TestUsageCost(t *testing.T) {
	const withCached, withoutCached = "gemini-2.5-pro", "gpt-5.4"
	cfg := models.Load()
	if cfg.Models[withCached].CachedInputPerMTokUSD == nil {
		t.Fatalf("precondition: %q gives no cached price", withCached)
	}
	if cfg.Models[withoutCached].CachedInputPerMTokUSD != nil {
		t.Fatalf("precondition: %q gives a cached price", withoutCached)
	}
	const unpricedModel = "gemini-9-preview"
	if isMetered(unpricedModel) {
		t.Fatalf("precondition: %q has a models-config entry", unpricedModel)
	}
	a := ModelUsage{Model: withCached, InputTokens: 900_000, CachedInputTokens: 700_000, OutputTokens: 50_000}
	b := ModelUsage{Model: withoutCached, InputTokens: 400_000, CachedInputTokens: 300_000, OutputTokens: 20_000}
	price := func(u ModelUsage) int64 {
		in, cached, out := ratesX10000(t, u.Model)
		return (u.InputTokens-u.CachedInputTokens)*in + u.CachedInputTokens*cached + u.OutputTokens*out
	}
	if _, bCached, _ := ratesX10000(t, withoutCached); bCached != cfg.PriceInPer1MTokensX10000(withoutCached) {
		t.Fatalf("precondition: the test's rates price %q's cached tokens at %d, not its input price", withoutCached, bCached)
	}
	cases := []struct {
		name         string
		usage        []ModelUsage
		wantCost     int64
		wantPriced   bool
		wantUnpriced string
	}{
		{"cached price", []ModelUsage{a}, price(a) / 1_000_000, true, ""},
		{"no cached price", []ModelUsage{b}, price(b) / 1_000_000, true, ""},
		{"two models", []ModelUsage{a, b}, (price(a) + price(b)) / 1_000_000, true, ""},
		{"an unpriced model", []ModelUsage{a, {Model: unpricedModel, InputTokens: 10, OutputTokens: 10}}, price(a) / 1_000_000, false, unpricedModel},
		{"no model named", []ModelUsage{{InputTokens: 10, OutputTokens: 10}}, 0, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cost, priced, unpriced := usageCostUSDx10000(c.usage)
			if cost != c.wantCost || priced != c.wantPriced || unpriced != c.wantUnpriced {
				t.Errorf("usageCostUSDx10000 = %d, %v, %q; want %d, %v, %q", cost, priced, unpriced, c.wantCost, c.wantPriced, c.wantUnpriced)
			}
		})
	}
	if got, want := ComputeCostUSDx10000(withCached, a.InputTokens, a.OutputTokens), cachedCost(t, withCached, a.InputTokens, 0, a.OutputTokens); got != want {
		t.Errorf("ComputeCostUSDx10000 = %d, want %d: every input token at the input price", got, want)
	}
}

// TestGeminiBackend_CachedTokens: the Gemini API's cachedContentTokenCount is
// the part of the prompt read from the cache, summed over the turns.
func TestGeminiBackend_CachedTokens(t *testing.T) {
	const prompt, cached, candidates = 5000, 3500, 40
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"done"}]}}],"usageMetadata":{"promptTokenCount":%d,"cachedContentTokenCount":%d,"candidatesTokenCount":%d}}`, prompt, cached, candidates)
	}))
	defer srv.Close()
	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	res, err := b.Run(context.Background(), RunRequest{Prompt: "p", Model: "gemini-2.5-flash", Registry: NewToolRegistry(t.TempDir(), nil, NewFSRecorder())})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.InputTokens != prompt || res.CachedInputTokens != cached || res.OutputTokens != candidates {
		t.Errorf("tokens in/cached/out = %d/%d/%d, want %d/%d/%d", res.InputTokens, res.CachedInputTokens, res.OutputTokens, prompt, cached, candidates)
	}
}

// TestRun_GeminiCLIJSONIsPriced: nodes on a gemini CLI that takes
// --output-format json are priced from its stats, each model at its own
// prices and its cached tokens at the cached price; a fan adds every item's
// calls. The run log, the node rows and the run totals show dollars, and no
// unmetered line is logged. The log names the models each node's calls
// used, once. When the stats name a model with no price that spent tokens,
// the calls are unmetered and the log names that model.
func TestRun_GeminiCLIJSONIsPriced(t *testing.T) {
	const model = "gemini-2.5-flash"
	priced := map[string]cliModel{
		"gemini-2.5-pro":   {Requests: 2, Prompt: 240_000, Candidates: 6_000, Cached: 200_000, Thoughts: 9_000},
		"gemini-2.5-flash": {Requests: 1, Prompt: 90_000, Candidates: 1_000, Cached: 60_000, Thoughts: 3_000, Tool: 2_800},
		"gemini-9-preview": {Requests: 1, Errors: 1},
	}
	unpriced := map[string]cliModel{
		"gemini-2.5-flash": priced["gemini-2.5-flash"],
		"gemini-9-preview": {Requests: 1, Prompt: 5_000, Candidates: 200},
	}
	for name, m := range priced {
		if m.Cached == 0 || !isMetered(name) {
			continue
		}
		if in, cached, _ := ratesX10000(t, name); in == cached {
			t.Fatalf("precondition: %q prices a cached token at its input price, so the test cannot tell them apart", name)
		}
	}
	items := []any{"a", "b", "c"}
	yaml := `name: cli
inputs: [items]
nodes:
  cli:
    type: agent
    model: ` + model + `
    prompt: "answer"
    outputs: [answer]
  fan:
    type: parallel_fan
    model: ` + model + `
    prompt_template: "item {item}"
    fan_source: items
    outputs: [answer]
`
	perNode := map[string]int64{"cli": 1, "fan": int64(len(items))}
	for _, c := range []struct {
		name  string
		stats map[string]cliModel
	}{{"priced", priced}, {"an unpriced model", unpriced}} {
		t.Run(c.name, func(t *testing.T) {
			usage, calls, costX1e6, _ := cliCall(t, c.stats)
			var unpricedModel string
			for _, u := range usage {
				if !isMetered(u.Model) {
					unpricedModel = u.Model
				}
			}
			var in, out int64
			for _, u := range usage {
				in, out = in+u.InputTokens, out+u.OutputTokens
			}
			res, store, log, err := geminiCLIRun(t, jsonGeminiCLI(t, `{"answer":"ok"}`, c.stats, ""), yaml, Config{Inputs: map[string]any{"items": items}})
			if err != nil {
				t.Fatal(err)
			}
			var total int64
			for node, n := range perNode {
				var m, u, cost, tin, tout int64
				if err := store.ReadDB.QueryRow(`SELECT metered_calls, unmetered_calls, cost_usd_x10000, tokens_in, tokens_out FROM workflow_node_states WHERE node_name=?`, node).Scan(&m, &u, &cost, &tin, &tout); err != nil {
					t.Fatal(err)
				}
				wantM, wantU, wantCost := n*int64(calls), int64(0), n*(costX1e6/1_000_000)
				if unpricedModel != "" {
					wantM, wantU, wantCost = 0, n*int64(calls), 0
				}
				if m != wantM || u != wantU || cost != wantCost {
					t.Errorf("node %s metered/unmetered/cost = %d/%d/%d, want %d/%d/%d", node, m, u, cost, wantM, wantU, wantCost)
				}
				if tin != n*in || tout != n*out {
					t.Errorf("node %s tokens in/out = %d/%d, want %d/%d", node, tin, tout, n*in, n*out)
				}
				total += wantCost
				// The row names the node's model, so the models the calls
				// used are logged, once per node.
				names := make([]string, len(usage))
				for i, u := range usage {
					names[i] = u.Model
				}
				if slices.Equal(names, []string{model}) {
					t.Fatalf("precondition: the stats name only the node's model %q, so no line names the models used", model)
				}
				line := fmt.Sprintf("node %s: its calls used %s, each priced at its own prices; the node names %q", node, strings.Join(names, ", "), model)
				if k := strings.Count(log, line); k != 1 {
					t.Errorf("%q logged %d times, want once:\n%s", line, k, log)
				}
			}
			wantCost := dollars(total)
			if unpricedModel != "" {
				wantCost = "unmetered"
				line := fmt.Sprintf("model %q has no price in the models config: its calls are reported unmetered", unpricedModel)
				if n := strings.Count(log, line); n != 1 {
					t.Errorf("%q logged %d times, want once:\n%s", line, n, log)
				}
			} else if wantCost == dollars(0) {
				t.Fatalf("precondition: the run prices to %s, which cannot be told from $0 on the two-decimal surfaces", wantCost)
			}
			if !strings.Contains(log, "cost="+wantCost+"\n") {
				t.Errorf("log lacks cost=%s:\n%s", wantCost, log)
			}
			if strings.Contains(log, "reports no token counts") {
				t.Errorf("log says the CLI reports no token counts:\n%s", log)
			}
			snap := readRunCosts(t, store, res.RunID)
			if snap.Totals.Cost != wantCost {
				t.Errorf("total cost label = %q, want %q", snap.Totals.Cost, wantCost)
			}
		})
	}
}

// TestRun_GeminiCLIFanPricesEachItem: a parallel_fan's items are priced one
// by one. When item b's reply has no stats, names a model with no price, or
// reports no tokens for a model that answered, b's calls are unmetered and
// its reason is logged once, and items a and c are still metered at their
// cost, which --max-cost-usd counts.
func TestRun_GeminiCLIFanPricesEachItem(t *testing.T) {
	const model = "gemini-2.5-flash"
	good := map[string]cliModel{model: {Requests: 1, Prompt: 400_000, Candidates: 4_000, Cached: 150_000, Thoughts: 2_000}}
	goodUsage, goodCalls, goodX1e6, unreported := cliCall(t, good)
	if unreported || !isMetered(model) {
		t.Fatalf("precondition: item a's stats on %q are not metered", model)
	}
	goodCost := goodX1e6 / 1_000_000
	var goodIn, goodOut int64
	for _, u := range goodUsage {
		goodIn, goodOut = goodIn+u.InputTokens, goodOut+u.OutputTokens
	}
	const unpricedModel = "gemini-9-preview"
	if isMetered(unpricedModel) {
		t.Fatalf("precondition: %q has a models-config entry", unpricedModel)
	}
	cases := []struct {
		name string
		// stats is item b's stats.models, nil when its reply has none.
		stats map[string]cliModel
		why   string
	}{
		{"no stats", nil, fmt.Sprintf("reports no token counts for model %q (its --output-format json reply has no stats.models)", model)},
		{"an unpriced model", map[string]cliModel{unpricedModel: {Requests: 2, Prompt: 30_000, Candidates: 900}}, fmt.Sprintf("model %q has no price in the models config", unpricedModel)},
		{"answered with no tokens", map[string]cliModel{model: {Requests: 1}}, fmt.Sprintf("reports no token counts for model %q (its --output-format json reply's stats.models reports no tokens for %s, which answered 1 request(s))", model, model)},
	}
	items := []any{"a", "b", "c"}
	yaml := `name: fan
inputs: [items]
nodes:
  fan:
    type: parallel_fan
    model: ` + model + `
    prompt_template: "item {item}"
    fan_source: items
    outputs: [answer]
`
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bReply := map[string]any{"response": `{"answer":"b"}`}
			bCalls, bIn, bOut := 1, int64(0), int64(0)
			if c.stats != nil {
				bReply["stats"] = cliStats(c.stats)
				var bUsage []ModelUsage
				bUsage, bCalls, _, _ = cliCall(t, c.stats)
				for _, u := range bUsage {
					bIn, bOut = bIn+u.InputTokens, bOut+u.OutputTokens
				}
			}
			reply := `case "$input" in *"item b"*) ` + printJSON(t, bReply) + `;; *) ` + printJSON(t, map[string]any{"response": `{"answer":"ok"}`, "stats": cliStats(good)}) + ";; esac\n"
			res, store, log, err := geminiCLIRun(t, jsonReplyCLI(reply), yaml, Config{Inputs: map[string]any{"items": items}})
			if err != nil {
				t.Fatal(err)
			}
			wantM, wantU, wantCost := int64(2*goodCalls), int64(bCalls), 2*goodCost
			var m, u, cost, tin, tout int64
			if err := store.ReadDB.QueryRow(`SELECT metered_calls, unmetered_calls, cost_usd_x10000, tokens_in, tokens_out FROM workflow_node_states WHERE node_name='fan'`).Scan(&m, &u, &cost, &tin, &tout); err != nil {
				t.Fatal(err)
			}
			if m != wantM || u != wantU || cost != wantCost {
				t.Errorf("fan metered/unmetered/cost = %d/%d/%d, want %d/%d/%d", m, u, cost, wantM, wantU, wantCost)
			}
			if wantIn, wantOut := 2*goodIn+bIn, 2*goodOut+bOut; tin != wantIn || tout != wantOut {
				t.Errorf("fan tokens in/out = %d/%d, want %d/%d", tin, tout, wantIn, wantOut)
			}
			if res.CostUSDx10000 != wantCost || int64(res.MeteredCalls) != wantM || int64(res.UnmeteredCalls) != wantU {
				t.Errorf("run cost/metered/unmetered = %d/%d/%d, want %d/%d/%d: --max-cost-usd reads the run's cost", res.CostUSDx10000, res.MeteredCalls, res.UnmeteredCalls, wantCost, wantM, wantU)
			}
			line := c.why + ": its calls are reported unmetered"
			if n := strings.Count(log, line); n != 1 {
				t.Errorf("%q logged %d times, want once:\n%s", line, n, log)
			}
			if n := strings.Count(log, "its calls are reported unmetered"); n != 1 {
				t.Errorf("%d unmetered lines logged, want one, for item b:\n%s", n, log)
			}
			wantLabel := fmt.Sprintf("%s + unmetered (%d calls)", dollars(wantCost), wantU)
			if dollars(wantCost) == dollars(0) {
				t.Fatalf("precondition: items a and c price to %s, which cannot be told from $0 on the two-decimal surfaces", dollars(wantCost))
			}
			if snap := readRunCosts(t, store, res.RunID); snap.Totals.Cost != wantLabel {
				t.Errorf("total cost label = %q, want %q", snap.Totals.Cost, wantLabel)
			}
		})
	}
}
