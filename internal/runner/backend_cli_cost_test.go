package runner

// Tests for pricing a claude CLI call from the usage its --output-format
// json reply carries: cache reads and cache writes apart from uncached
// input, and each model the CLI called. The CLI is a shell script that
// prints the reply; no model is called.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/models"
)

// claudeTokens is one model's tokens in a claude CLI reply, as the Messages
// API counts them: In is the uncached remainder, Read the cache reads and
// Write the cache writes. Canonical, when set, is the modelUsage entry's
// canonicalModel.
type claudeTokens struct {
	In, Out, Read, Write int64
	Canonical            string
}

// claudeReply is the reply `claude -p --output-format json` prints for a
// run: the success result message of Claude Code's print mode. usage is the
// main loop's tokens, with write1h of its cache writes at the 1-hour
// lifetime; ms, when not nil, is its modelUsage.
func claudeReply(result string, turns int, usage claudeTokens, write1h int64, ms map[string]claudeTokens) map[string]any {
	reply := map[string]any{
		"type": "result", "subtype": "success", "is_error": false,
		"num_turns": turns, "result": result, "stop_reason": "end_turn",
		"session_id": "s", "total_cost_usd": 0.0123,
		"usage": map[string]any{
			"input_tokens": usage.In, "output_tokens": usage.Out,
			"cache_read_input_tokens": usage.Read, "cache_creation_input_tokens": usage.Write,
			"cache_creation":  map[string]any{"ephemeral_1h_input_tokens": write1h, "ephemeral_5m_input_tokens": usage.Write - write1h},
			"server_tool_use": map[string]any{"web_search_requests": 0, "web_fetch_requests": 0},
			"service_tier":    "standard",
		},
	}
	if ms != nil {
		mu := map[string]any{}
		for name, m := range ms {
			entry := map[string]any{
				"inputTokens": m.In, "outputTokens": m.Out,
				"cacheReadInputTokens": m.Read, "cacheCreationInputTokens": m.Write,
				"webSearchRequests": 0, "costUSD": 0.01, "contextWindow": 200000, "maxOutputTokens": 32000,
			}
			if m.Canonical != "" {
				entry["canonicalModel"] = m.Canonical
			}
			mu[name] = entry
		}
		reply["modelUsage"] = mu
	}
	return reply
}

// claudeOverloaded is the result of a Claude Code run that ended on an API
// 529.
const claudeOverloaded = `API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`

// claudeErrorReply is the reply Claude Code prints for a run that ended in
// an error after spending usage: is_error set, with subtype, result and
// errors.
func claudeErrorReply(subtype, result string, errs []string, turns int, usage claudeTokens) map[string]any {
	reply := claudeReply(result, turns, usage, 0, nil)
	reply["is_error"], reply["subtype"] = true, subtype
	if result == "" {
		delete(reply, "result")
	}
	if errs != nil {
		reply["errors"] = errs
	}
	return reply
}

// fakeClaude writes a claude CLI that reads its prompt and prints reply.
func fakeClaude(t *testing.T, reply map[string]any) string {
	t.Helper()
	return fakeClaudeSeq(t, reply)
}

// fakeClaudeSeq writes a claude CLI that reads its prompt and prints the
// next of replies for that prompt, and the last one from then on. It exits
// as Claude Code does: 1 after a reply marked is_error, else 0.
func fakeClaudeSeq(t *testing.T, replies ...map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	var cases strings.Builder
	for i, reply := range replies {
		js, err := json.Marshal(reply)
		if err != nil {
			t.Fatal(err)
		}
		pattern := fmt.Sprint(i)
		if i == len(replies)-1 {
			pattern = "*"
		}
		code := 0
		if reply["is_error"] == true {
			code = 1
		}
		fmt.Fprintf(&cases, "%s)\ncat <<'CLAUDE_JSON'\n%s\nCLAUDE_JSON\nexit %d;;\n", pattern, js, code)
	}
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nf='" + filepath.Join(dir, "seen-") + "'$(cat | cksum | cut -d' ' -f1)\n" +
		"n=$(cat \"$f\" 2>/dev/null || echo 0)\necho $((n+1)) >\"$f\"\ncase $n in\n" + cases.String() + "esac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// claudePriceX10000 is one of a model's prices in 1/10000 USD per 1M tokens,
// read from its models-config entry: p when the entry lists it, else
// fallback.
func claudePriceX10000(p *float64, fallback int64) int64 {
	if p == nil {
		return fallback
	}
	return int64(math.Round(*p * 10_000))
}

// claudeCostX1e6 is what model's tokens cost, times 1,000,000, in 1/10000
// USD, with write1h of its cache writes at the 1-hour lifetime: uncached
// input at the input price, reads at the cached price, writes at the write
// price for their lifetime, output at the output price.
func claudeCostX1e6(t *testing.T, model string, m claudeTokens, write1h int64) int64 {
	t.Helper()
	e, ok := models.Load().Models[model]
	if !ok {
		t.Fatalf("precondition: %q has no models-config entry", model)
	}
	in := int64(math.Round(e.InputPerMTokUSD * 10_000))
	write := claudePriceX10000(e.CacheWritePerMTokUSD, in)
	return m.In*in + m.Read*claudePriceX10000(e.CachedInputPerMTokUSD, in) +
		(m.Write-write1h)*write + write1h*claudePriceX10000(e.CacheWrite1hPerMTokUSD, write) +
		m.Out*int64(math.Round(e.OutputPerMTokUSD*10_000))
}

// TestCLIBackend_CacheTokens: a claude CLI reply's cache reads and writes
// count as input, apart from the uncached remainder. With modelUsage, each
// model that spent tokens is its own entry, a "[1m]" id folds into its
// model, and the main loop's 1-hour writes go to the model that wrote the
// most first. Without it, usage gives the tokens.
func TestCLIBackend_CacheTokens(t *testing.T) {
	main := claudeTokens{In: 40, Out: 700, Read: 90_000, Write: 12_000}
	sonnet := claudeTokens{In: 30, Out: 600, Read: 80_000, Write: 11_000}
	haiku := claudeTokens{In: 500, Out: 50, Write: 1_500}
	entry := func(name string, m claudeTokens, write1h int64) ModelUsage {
		return ModelUsage{Model: name, InputTokens: m.In + m.Read + m.Write, CachedInputTokens: m.Read, CacheWriteTokens: m.Write, CacheWrite1hTokens: write1h, OutputTokens: m.Out}
	}
	// A Bedrock id is keyed as the provider's id, which the models config
	// does not list; its canonicalModel is. claude-opus-5-5 is listed, and
	// Claude Code 2.1.236, which predates it, gives it claude-opus-5's
	// canonical id.
	const bedrock, bedrockCanon, newer, newerCanon = "us.anthropic.claude-sonnet-4-5-20250929-v1:0", "claude-sonnet-4-5", "claude-opus-5-5", "claude-opus-5"
	if isMetered(bedrock) || !isMetered(bedrockCanon) || !isMetered(newer) || !isMetered(newerCanon) {
		t.Fatalf("precondition: %s listed, or one of %s, %s, %s not listed, in the models config", bedrock, bedrockCanon, newer, newerCanon)
	}
	canon := func(m claudeTokens, id string) claudeTokens { m.Canonical = id; return m }
	cases := []struct {
		name    string
		write1h int64
		ms      map[string]claudeTokens
		want    []ModelUsage
		// whole is the reply's tokens when want is empty.
		whole ModelUsage
	}{
		{name: "usage alone", write1h: 8_000, whole: entry("", main, 8_000)},
		{name: "1-hour writes past the writes", write1h: main.Write + 5, whole: entry("", main, main.Write)},
		{
			name: "one model", write1h: 8_000,
			ms:   map[string]claudeTokens{"claude-sonnet-5": sonnet},
			want: []ModelUsage{entry("claude-sonnet-5", sonnet, 8_000)},
		},
		{
			// sonnet wrote the most, so it takes the 1-hour writes up to its
			// own; haiku takes the rest.
			name: "two models", write1h: sonnet.Write + 1_000,
			ms: map[string]claudeTokens{"claude-sonnet-5": sonnet, "claude-haiku-4-5-20251001": haiku, "claude-opus-5": {}},
			want: []ModelUsage{
				entry("claude-haiku-4-5-20251001", haiku, 1_000),
				entry("claude-sonnet-5", sonnet, sonnet.Write),
			},
		},
		{
			name: "a 1M-context id", write1h: 0,
			ms: map[string]claudeTokens{"claude-opus-5[1m]": sonnet, "claude-opus-5": haiku},
			want: []ModelUsage{entry("claude-opus-5", claudeTokens{
				In: sonnet.In + haiku.In, Out: sonnet.Out + haiku.Out, Read: sonnet.Read + haiku.Read, Write: sonnet.Write + haiku.Write,
			}, 0)},
		},
		{name: "modelUsage that spent nothing", write1h: 8_000, ms: map[string]claudeTokens{"claude-sonnet-5": {}}, whole: entry("", main, 8_000)},
		{
			name: "a provider's id priced by its canonical id", write1h: 0,
			ms:   map[string]claudeTokens{bedrock: canon(sonnet, bedrockCanon)},
			want: []ModelUsage{entry(bedrockCanon, sonnet, 0)},
		},
		{
			name: "a provider's id with no canonical id", write1h: 0,
			ms:   map[string]claudeTokens{bedrock: sonnet},
			want: []ModelUsage{entry(bedrock, sonnet, 0)},
		},
		{
			name: "a listed id priced by itself", write1h: 0,
			ms:   map[string]claudeTokens{newer: canon(haiku, newerCanon)},
			want: []ModelUsage{entry(newer, haiku, 0)},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &CLIBackend{CLIPath: fakeClaude(t, claudeReply("done", 3, main, c.write1h, c.ms)), ScratchDir: t.TempDir()}
			res, err := b.Run(context.Background(), RunRequest{Prompt: "p"})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !slices.Equal(res.ByModel, c.want) {
				t.Errorf("ByModel = %+v, want %+v", res.ByModel, c.want)
			}
			whole := c.whole
			for _, u := range c.want {
				whole.InputTokens += u.InputTokens
				whole.CachedInputTokens += u.CachedInputTokens
				whole.CacheWriteTokens += u.CacheWriteTokens
				whole.CacheWrite1hTokens += u.CacheWrite1hTokens
				whole.OutputTokens += u.OutputTokens
			}
			got := ModelUsage{InputTokens: res.InputTokens, CachedInputTokens: res.CachedInputTokens, CacheWriteTokens: res.CacheWriteTokens, CacheWrite1hTokens: res.CacheWrite1hTokens, OutputTokens: res.OutputTokens}
			if got != whole {
				t.Errorf("tokens = %+v, want %+v", got, whole)
			}
			if res.FinalText != "done" || res.Turns != 3 {
				t.Errorf("FinalText %q, Turns %d; want \"done\", 3", res.FinalText, res.Turns)
			}
		})
	}
}

// TestUsageCost_CacheWrites: tokens written to the prompt cache cost the
// model's cache-write price for their lifetime, and cache reads its cached
// price; a model whose entry lists no cache-write price prices a write as
// input.
func TestUsageCost_CacheWrites(t *testing.T) {
	const listed, bare = "claude-sonnet-4-6", "gpt-5.4"
	cfg := models.Load()
	if e := cfg.Models[listed]; e.CacheWritePerMTokUSD == nil || e.CacheWrite1hPerMTokUSD == nil || e.CachedInputPerMTokUSD == nil {
		t.Fatalf("precondition: %q lists no cache prices", listed)
	}
	if e, ok := cfg.Models[bare]; !ok || e.CacheWritePerMTokUSD != nil || e.CacheWrite1hPerMTokUSD != nil {
		t.Fatalf("precondition: %q is unlisted or lists a cache-write price", bare)
	}
	m := claudeTokens{In: 20_000, Out: 3_000, Read: 500_000, Write: 90_000}
	const write1h = 60_000
	for _, model := range []string{listed, bare} {
		u := ModelUsage{Model: model, InputTokens: m.In + m.Read + m.Write, CachedInputTokens: m.Read, CacheWriteTokens: m.Write, CacheWrite1hTokens: write1h, OutputTokens: m.Out}
		cost, priced, _ := usageCostUSDx10000([]ModelUsage{u})
		if want := claudeCostX1e6(t, model, m, write1h) / 1_000_000; cost != want || !priced {
			t.Errorf("%s: cost %d (priced %v), want %d", model, cost, priced, want)
		}
	}
	whole, _, _ := usageCostUSDx10000([]ModelUsage{{Model: listed, InputTokens: m.In + m.Read + m.Write, OutputTokens: m.Out}})
	cached, _, _ := usageCostUSDx10000([]ModelUsage{{Model: listed, InputTokens: m.In + m.Read + m.Write, CachedInputTokens: m.Read, CacheWriteTokens: m.Write, CacheWrite1hTokens: write1h, OutputTokens: m.Out}})
	if whole == cached {
		t.Errorf("cost %d with the cache split out equals the cost with every input token at the input price", cached)
	}
}

// TestCLIBackend_ErrorReplyIsCharged: a claude CLI reply with is_error set,
// such as error_max_turns or an API error, which the CLI prints before it
// exits 1, fails the call and names its subtype, result and errors, and
// comes back with the tokens it spent so they are charged. An API error
// that is a rate limit reads as one. A reply that spent none returns no
// result: no call answered. A CLI that exits non-zero after a result not
// marked is_error fails too, and is charged; one that printed no result
// fails with no result.
func TestCLIBackend_ErrorReplyIsCharged(t *testing.T) {
	spent := claudeTokens{In: 12, Out: 300, Read: 40_000, Write: 2_000}
	maxTurns := []string{"Reached maximum number of turns (30)"}
	exitOne := func(t *testing.T, stdout string) string {
		path := filepath.Join(t.TempDir(), "claude")
		script := "#!/bin/sh\ncat >/dev/null\ncat <<'CLAUDE_OUT'\n" + stdout + "\nCLAUDE_OUT\necho boom >&2\nexit 1\n"
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	answered, err := json.Marshal(claudeReply("half", 4, spent, 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		cli   func(t *testing.T) string
		usage claudeTokens
		turns int
		// says is what the error names; rateLimit whether it reads as one.
		says      string
		rateLimit bool
	}{
		{
			name: "max turns",
			cli: func(t *testing.T) string {
				return fakeClaude(t, claudeErrorReply("error_max_turns", "", maxTurns, 30, spent))
			},
			usage: spent, turns: 30, says: "(error_max_turns): " + maxTurns[0],
		},
		{
			name: "an API error after spending",
			cli: func(t *testing.T) string {
				return fakeClaude(t, claudeErrorReply("success", claudeOverloaded, nil, 3, spent))
			},
			usage: spent, turns: 3, says: "is_error=true: " + claudeOverloaded, rateLimit: true,
		},
		{
			name: "spent none",
			cli: func(t *testing.T) string {
				return fakeClaude(t, claudeErrorReply("error_max_turns", "", maxTurns, 30, claudeTokens{}))
			},
			says: "(error_max_turns): " + maxTurns[0],
		},
		{
			name:  "exit 1 after a result not marked is_error",
			cli:   func(t *testing.T) string { return exitOne(t, string(answered)) },
			usage: spent, turns: 4, says: "claude CLI failed: exit status 1\nstderr: boom",
		},
		{
			name: "exit 1 with no result",
			cli:  func(t *testing.T) string { return exitOne(t, "not json") },
			says: "claude CLI failed: exit status 1\nstderr: boom",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := &CLIBackend{CLIPath: c.cli(t), ScratchDir: t.TempDir()}
			res, err := b.Run(context.Background(), RunRequest{Prompt: "p"})
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("err = %v, want one naming %q", err, c.says)
			}
			if got := isRateLimitError(err); got != c.rateLimit {
				t.Errorf("isRateLimitError(%v) = %v, want %v", err, got, c.rateLimit)
			}
			var incomplete *incompleteReplyError
			if errors.As(err, &incomplete) {
				t.Errorf("err %v reads as an incomplete reply", err)
			}
			if c.usage == (claudeTokens{}) {
				if res != nil {
					t.Errorf("result %+v, want none for a run that reports no token", res)
				}
				return
			}
			if res == nil {
				t.Fatal("no result, so the spent tokens are not charged")
			}
			if want := c.usage.In + c.usage.Read + c.usage.Write; res.InputTokens != want || res.OutputTokens != c.usage.Out {
				t.Errorf("tokens in/out = %d/%d, want %d/%d", res.InputTokens, res.OutputTokens, want, c.usage.Out)
			}
			if res.FinalText != "" || callsMade(res) != c.turns {
				t.Errorf("FinalText %q, calls %d; want no text and the %d turns", res.FinalText, callsMade(res), c.turns)
			}
		})
	}
}

// TestRateLimited_ChargesSpentAttempts: a claude CLI run refused as a rate
// limit after it spent tokens is retried, and the result that comes back
// carries every attempt's tokens and calls, each attempt priced as its own
// call: with the answer when a retry answers, and with the error when the
// retries run out. An attempt that spent nothing adds nothing.
func TestRateLimited_ChargesSpentAttempts(t *testing.T) {
	fastBackoff(t)
	t.Setenv("HIVE_RPM_DEFAULT", "6000")
	spent := claudeTokens{In: 20, Out: 900, Read: 60_000, Write: 5_000}
	answer := claudeTokens{In: 30, Out: 1_200, Read: 70_000, Write: 1_000}
	refused := claudeErrorReply("success", claudeOverloaded, nil, 2, spent)
	nothing := claudeErrorReply("success", claudeOverloaded, nil, 0, claudeTokens{})
	answered := claudeReply("done", 5, answer, 0, nil)
	for _, c := range []struct {
		name     string
		replies  []map[string]any
		maxRetry int
		// attempts are the tokens and turns of the attempts charged, in
		// order; text the answer, or "" when the run fails.
		attempts []claudeTokens
		turns    []int
		text     string
	}{
		{"two refusals, then an answer", []map[string]any{refused, refused, answered}, 5, []claudeTokens{spent, spent, answer}, []int{2, 2, 5}, "done"},
		{"refused until the retries run out", []map[string]any{refused}, 2, []claudeTokens{spent, spent, spent}, []int{2, 2, 2}, ""},
		{"a refusal that spent nothing", []map[string]any{nothing, answered}, 5, []claudeTokens{answer}, []int{5}, "done"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ResetRateLimitState()
			const model = "claude-sonnet-4-6"
			wrapped := NewRateLimitedBackend(&CLIBackend{CLIPath: fakeClaudeSeq(t, c.replies...), ScratchDir: t.TempDir()}, "test-charge-spent")
			wrapped.maxRetry = c.maxRetry
			res, err := wrapped.Run(context.Background(), RunRequest{Prompt: "p", Model: model})
			if c.text != "" && err != nil {
				t.Fatalf("Run: %v", err)
			}
			if c.text == "" && (err == nil || !strings.Contains(err.Error(), "rate-limit retry exhausted")) {
				t.Fatalf("err = %v, want the retries exhausted", err)
			}
			if res == nil {
				t.Fatal("no result, so the spent attempts are not charged")
			}
			var in, out, wantCost int64
			calls := 0
			for i, a := range c.attempts {
				in, out = in+a.In+a.Read+a.Write, out+a.Out
				calls += c.turns[i]
				wantCost += claudeCostX1e6(t, model, a, 0) / 1_000_000
			}
			if res.InputTokens != in || res.OutputTokens != out || callsMade(res) != calls || res.FinalText != c.text {
				t.Errorf("tokens in/out %d/%d, calls %d, text %q; want %d/%d, %d, %q", res.InputTokens, res.OutputTokens, callsMade(res), res.FinalText, in, out, calls, c.text)
			}
			parts := spendParts(res)
			if len(parts) != len(c.attempts) {
				t.Fatalf("%d attempts priced, want %d", len(parts), len(c.attempts))
			}
			var cost int64
			for _, p := range parts {
				u := ModelUsage{Model: model, InputTokens: p.InputTokens, CachedInputTokens: p.CachedInputTokens, CacheWriteTokens: p.CacheWriteTokens, CacheWrite1hTokens: p.CacheWrite1hTokens, OutputTokens: p.OutputTokens}
				pc, _, _ := usageCostUSDx10000([]ModelUsage{u})
				cost += pc
			}
			if cost != wantCost {
				t.Errorf("attempts cost %d, want %d", cost, wantCost)
			}
		})
	}
}

// TestRun_ClaudeCLICacheIsPriced: nodes on the claude CLI are priced with
// their cache reads and writes, each at its price. With modelUsage, each
// model the CLI called is priced at its own prices, and the log names them
// against the node's model once per node; without it, the node's model
// prices the reply. A fan adds every item's calls. The node rows, the run's
// cost, the run log and the run totals agree, and the cost is more than
// pricing the uncached input alone gave.
func TestRun_ClaudeCLICacheIsPriced(t *testing.T) {
	const nodeModel = "sonnet"
	resolved := models.Load().Resolve(nodeModel)
	main := claudeTokens{In: 60, Out: 4_000, Read: 300_000, Write: 40_000}
	perModel := map[string]claudeTokens{
		"claude-sonnet-5":           {In: 50, Out: 3_800, Read: 290_000, Write: 38_000},
		"claude-haiku-4-5-20251001": {In: 900, Out: 200, Read: 10_000, Write: 2_000},
	}
	const write1h, turns = 30_000, 4
	items := []any{"a", "b"}
	yaml := `name: cli
inputs: [items]
nodes:
  cli:
    type: agent
    model: ` + nodeModel + `
    prompt: "answer"
    outputs: [answer]
  fan:
    type: parallel_fan
    model: ` + nodeModel + `
    prompt_template: "item {item}"
    fan_source: items
    outputs: [answer]
`
	perNode := map[string]int64{"cli": 1, "fan": int64(len(items))}
	for _, c := range []struct {
		name string
		ms   map[string]claudeTokens
	}{{"modelUsage", perModel}, {"usage alone", nil}} {
		t.Run(c.name, func(t *testing.T) {
			// A call's cost, tokens and the models it names, computed from
			// the reply and the models config.
			var costX1e6, in, out int64
			var names []string
			if c.ms == nil {
				costX1e6 = claudeCostX1e6(t, resolved, main, write1h)
				in, out = main.In+main.Read+main.Write, main.Out
			} else {
				for name, m := range c.ms {
					names = append(names, name)
					in, out = in+m.In+m.Read+m.Write, out+m.Out
				}
				slices.Sort(names)
				// The main loop's 1-hour writes go to the model that wrote
				// the most, up to its writes, then to the next.
				byWrites := slices.Clone(names)
				slices.SortFunc(byWrites, func(a, b string) int { return int(c.ms[b].Write - c.ms[a].Write) })
				left := int64(write1h)
				for _, name := range byWrites {
					take := min(left, c.ms[name].Write)
					costX1e6 += claudeCostX1e6(t, name, c.ms[name], take)
					left -= take
				}
			}
			callCost := costX1e6 / 1_000_000
			if old := pricedCost(resolved, main.In, main.Out); callCost <= old {
				t.Fatalf("precondition: a call priced with its cache costs %d, not more than %d for its uncached input alone", callCost, old)
			}
			t.Setenv("CLAUDE_CODE_CLI_PATH", fakeClaude(t, claudeReply(`{"answer":"ok"}`, turns, main, write1h, c.ms)))
			t.Setenv("HIVE_MCP_PATH", filepath.Join(t.TempDir(), "no-chb-mcp"))
			res, store, log, err := providerRun(t, "claude-cli", yaml, Config{Inputs: map[string]any{"items": items}})
			if err != nil {
				t.Fatal(err)
			}
			var total int64
			for node, n := range perNode {
				var m, u, cost, tin, tout int64
				if err := store.ReadDB.QueryRow(`SELECT metered_calls, unmetered_calls, cost_usd_x10000, tokens_in, tokens_out FROM workflow_node_states WHERE node_name=?`, node).Scan(&m, &u, &cost, &tin, &tout); err != nil {
					t.Fatal(err)
				}
				if m != n*turns || u != 0 || cost != n*callCost {
					t.Errorf("node %s metered/unmetered/cost = %d/%d/%d, want %d/0/%d", node, m, u, cost, n*turns, n*callCost)
				}
				if tin != n*in || tout != n*out {
					t.Errorf("node %s tokens in/out = %d/%d, want %d/%d", node, tin, tout, n*in, n*out)
				}
				total += n * callCost
				// The claude CLI is sent the node's model as written.
				line := fmt.Sprintf("node %s: its calls used %s, each priced at its own prices; the node names %q", node, strings.Join(names, ", "), nodeModel)
				if want := min(len(names), 1); strings.Count(log, line) != want {
					t.Errorf("%q logged %d times, want %d:\n%s", line, strings.Count(log, line), want, log)
				}
			}
			if res.CostUSDx10000 != total {
				t.Errorf("run cost = %d, want %d: --max-cost-usd reads it", res.CostUSDx10000, total)
			}
			if dollars(total) == dollars(0) {
				t.Fatalf("precondition: the run prices to %s, which cannot be told from $0 on the two-decimal surfaces", dollars(total))
			}
			if !strings.Contains(log, "cost="+dollars(total)+"\n") {
				t.Errorf("log lacks cost=%s:\n%s", dollars(total), log)
			}
			if snap := readRunCosts(t, store, res.RunID); snap.Totals.Cost != dollars(total) {
				t.Errorf("total cost label = %q, want %q", snap.Totals.Cost, dollars(total))
			}
		})
	}
}

// TestRun_ClaudeCLIRefusedAttemptsAreCharged: with the rate-limit retry in
// place, as every run has it, a claude CLI call refused with a 529 after it
// spent tokens is retried, and the node's row carries both attempts' tokens,
// calls and cost, each attempt priced as its own call: the refused one at
// the model its modelUsage names, the answer at the node's model. A fan's
// items do the same, and the run's cost adds them all.
func TestRun_ClaudeCLIRefusedAttemptsAreCharged(t *testing.T) {
	fastBackoff(t)
	ResetRateLimitState()
	for k, v := range map[string]string{
		"HIVE_RPM_DEFAULT": "6000", "HIVE_DISABLE_RATE_LIMIT": "",
		"HIVE_PROVIDER": "", "HIVE_PROVIDER_ALLOWLIST": "",
	} {
		t.Setenv(k, v)
	}
	const nodeModel = "sonnet"
	resolved := models.Load().Resolve(nodeModel)
	spent := claudeTokens{In: 20, Out: 900, Read: 60_000, Write: 5_000}
	answer := claudeTokens{In: 30, Out: 1_200, Read: 70_000, Write: 1_000}
	const spentTurns, answerTurns, spentModel = 2, 5, "claude-haiku-4-5-20251001"
	if claudeCostX1e6(t, spentModel, spent, 0) == claudeCostX1e6(t, resolved, spent, 0) {
		t.Fatalf("precondition: %s and %s price the refused attempt alike", spentModel, resolved)
	}
	refused := claudeErrorReply("success", claudeOverloaded, nil, spentTurns, spent)
	refused["modelUsage"] = claudeReply("", 0, spent, 0, map[string]claudeTokens{spentModel: spent})["modelUsage"]
	t.Setenv("CLAUDE_CODE_CLI_PATH", fakeClaudeSeq(t, refused, claudeReply(`{"answer":"ok"}`, answerTurns, answer, 0, nil)))
	t.Setenv("HIVE_MCP_PATH", filepath.Join(t.TempDir(), "no-chb-mcp"))
	items := []any{"a", "b"}
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(`name: cli
inputs: [items]
nodes:
  cli:
    type: agent
    model: `+nodeModel+`
    prompt: "answer"
    outputs: [answer]
  fan:
    type: parallel_fan
    model: `+nodeModel+`
    prompt_template: "item {item}"
    fan_source: items
    outputs: [answer]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	store := newTempStore(t)
	res, err := Run(context.Background(), store, Config{
		WorkflowYAML: wf, ProjectName: "cli", ProjectDir: dir, MaxIterations: 10,
		Provider: "claude-cli", Log: &log, Inputs: map[string]any{"items": items},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Each call is two attempts, each priced on its own.
	callCost := claudeCostX1e6(t, spentModel, spent, 0)/1_000_000 + claudeCostX1e6(t, resolved, answer, 0)/1_000_000
	in, out := spent.In+spent.Read+spent.Write+answer.In+answer.Read+answer.Write, spent.Out+answer.Out
	var total int64
	for node, n := range map[string]int64{"cli": 1, "fan": int64(len(items))} {
		var status string
		var m, cost, tin, tout int64
		if err := store.ReadDB.QueryRow(`SELECT status, metered_calls, cost_usd_x10000, tokens_in, tokens_out FROM workflow_node_states WHERE node_name=?`, node).Scan(&status, &m, &cost, &tin, &tout); err != nil {
			t.Fatal(err)
		}
		if status != "completed" || m != n*(spentTurns+answerTurns) || cost != n*callCost || tin != n*in || tout != n*out {
			t.Errorf("node %s status/calls/cost/in/out = %s/%d/%d/%d/%d, want completed/%d/%d/%d/%d",
				node, status, m, cost, tin, tout, n*(spentTurns+answerTurns), n*callCost, n*in, n*out)
		}
		total += n * callCost
	}
	if res.CostUSDx10000 != total {
		t.Errorf("run cost = %d, want %d: --max-cost-usd reads it", res.CostUSDx10000, total)
	}
}
