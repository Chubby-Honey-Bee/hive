package runner

// Tests for the context-window guard's calibration below the class count
// (runner.md § Context window guard). The server is an httptest fake that
// counts each prompt at a share of the test's own estimate of it; no model
// is called.

import (
	"context"
	"fmt"
	"math"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// countingGuard is one model at a fake OpenAI-compatible endpoint whose
// server counts each prompt at ratio times loneUserPrompt of it, sent
// through a backend with its own calibration. densest is the test's record
// of the most tokens counted per estimated token among the calls the guard
// should learn from. Each reply is 5 tokens, unless overrun is set: then a
// reply the room left in that window cannot hold at the cap sent stops one
// token under the window, as llama.cpp stops a reply with context shift
// off, or, with shift set, runs to the cap past the window, as a server
// that shifts its context does.
type countingGuard struct {
	t       *testing.T
	f       *fakeOllama
	b       *OpenAIBackend
	ratio   atomic.Uint64 // float64 bits
	overrun atomic.Int64  // the window a reply may run into; 0 for none
	shift   atomic.Bool
	densest float64
}

const countingModel = "m:0.8b"

// newCountingGuard is a counting guard at a server the runner found to be
// server.
func newCountingGuard(t *testing.T, server localServer) *countingGuard {
	g := &countingGuard{t: t}
	g.f = &fakeOllama{modelID: countingModel, reply: func(body map[string]any) string {
		msgs, _ := body["messages"].([]any)
		content, _ := msgs[0].(map[string]any)["content"].(string)
		n := g.count(content)
		if w := g.overrun.Load(); w > 0 {
			sent, _ := body["max_tokens"].(float64)
			if room := int(w) - 1 - n; room < int(sent) {
				if g.shift.Load() {
					return chatReply("do", "length", n, int(sent))
				}
				return chatReply("do", "length", n, room)
			}
		}
		return chatReply("done", "stop", n, 5)
	}}
	srv := httptest.NewServer(g.f)
	t.Cleanup(srv.Close)
	g.b = &OpenAIBackend{APIKey: "k", BaseURL: srv.URL + "/v1", Client: srv.Client(), calib: &calibration{}}
	g.b.calib.serverAt(g.b.BaseURL, server)
	return g
}

func (g *countingGuard) setRatio(r float64) { g.ratio.Store(math.Float64bits(r)) }

// count is what the server counts prompt at.
func (g *countingGuard) count(prompt string) int {
	return int(math.Ceil(loneUserPrompt(prompt) * math.Float64frombits(g.ratio.Load())))
}

// send runs prompt through the backend asking for 8,192 output tokens in a
// window of window tokens (0 for none known).
func (g *countingGuard) send(prompt string, window int64) (*RunResult, error) {
	return g.b.Run(context.Background(), RunRequest{Model: countingModel, Prompt: prompt, Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}}, MaxTokens: 8192, ContextWindow: window, ContextWindowSource: "a test"})
}

// learn sends prompt, which must pass.
func (g *countingGuard) learn(prompt string, window int64) {
	g.t.Helper()
	g.capSent(prompt, window)
}

// lowered is prompt's estimate at the scale densest gives.
func (g *countingGuard) lowered(prompt string) int64 {
	return estimateOf(loneUserPrompt(prompt), g.densest/calibrationMargin)
}

// refused checks prompt is refused unsent with an error holding each of want.
func (g *countingGuard) refused(prompt string, window int64, want ...string) {
	g.t.Helper()
	before := len(g.f.sent())
	_, err := g.send(prompt, window)
	if err == nil {
		g.t.Fatalf("a %d-byte prompt was sent; want it refused", len(prompt))
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			g.t.Errorf("the refusal %q lacks %q", err, w)
		}
	}
	if n := len(g.f.sent()) - before; n != 0 {
		g.t.Errorf("%d chat requests reached the server, want none", n)
	}
}

// capSent sends prompt, which must pass, records its count in densest when
// the prompt is long enough to learn from, and returns the output cap its
// request carried and its line for the run log.
func (g *countingGuard) capSent(prompt string, window int64) (int64, string) {
	g.t.Helper()
	before := len(g.f.sent())
	res, err := g.send(prompt, window)
	if err != nil {
		g.t.Fatalf("a %d-byte prompt was refused or failed: %v", len(prompt), err)
	}
	chats := g.f.sent()
	if len(chats) != before+1 {
		g.t.Fatalf("%d chat requests, want %d", len(chats), before+1)
	}
	if len(prompt) >= minCalibrationBytes {
		g.densest = math.Max(g.densest, float64(g.count(prompt))/loneUserPrompt(prompt))
	}
	got, _ := chats[before]["max_tokens"].(float64)
	return int64(got), strings.Join(res.ContextNotes, "\n")
}

// Three counts at about 0.53 of the class count lower the estimate to the
// densest of them over the margin. A prompt the server counts inside a
// 4,096-token window with room for the reply, but whose class count leaves
// too little room, is refused before the third count and sent after it,
// with the cap the lowered estimate leaves. A prompt that leaves too little
// room even at the densest count is still refused, and a denser count
// raises the estimate again.
func TestContextGuard_LowersAfterThreeCounts(t *testing.T) {
	const window = 4096
	g := newCountingGuard(t, serverOllama)
	g.setRatio(0.53)
	fits := longPrompt(10000)
	raw := estimateOf(loneUserPrompt(fits), 1)
	if raw >= window || window-raw >= minReplyRoom {
		t.Fatalf("the test prompt is %d tokens by text class; want it inside the %d window but leaving under %d for the reply", raw, window, minReplyRoom)
	}
	if n := g.count(fits); window-int64(n) < minReplyRoom {
		t.Fatalf("the server counts the test prompt at %d, leaving under %d of the %d window for the reply", n, minReplyRoom, window)
	}
	for i, p := range []string{longPrompt(3000), longPrompt(3100), longPrompt(3200)} {
		g.refused(fits, window, fmt.Sprintf("about %d tokens by estimate", raw), fmt.Sprintf("leaves %d of", window-raw), "estimated by text class)")
		if t.Failed() {
			t.Fatalf("after %d count(s), want the class count's rule", i)
		}
		g.learn(p, window)
	}

	est := g.lowered(fits)
	got, note := g.capSent(fits, window)
	if want := window - est; got != want {
		t.Errorf("after three counts: max_tokens %d, want %d, the window less %d", got, want, est)
	}
	for _, want := range []string{fmt.Sprintf("prompt about %d tokens by estimate", est), "lowered to", "densest of 3 counted calls", fmt.Sprintf("must fit at %d tokens by text class", raw)} {
		if !strings.Contains(note, want) {
			t.Errorf("the run log line %q lacks %q", note, want)
		}
	}

	tooBig := longPrompt(18000)
	est = g.lowered(tooBig)
	if window-est >= minReplyRoom {
		t.Fatalf("the oversized test prompt leaves %d for the reply at the densest count; want under %d", window-est, minReplyRoom)
	}
	g.refused(tooBig, window, fmt.Sprintf("about %d tokens by estimate", est), fmt.Sprintf("leaves %d of", window-est))

	g.setRatio(0.85)
	dense := longPrompt(4000)
	want := window - g.lowered(dense)
	if got, _ := g.capSent(dense, window); got != want {
		t.Errorf("the denser prompt: max_tokens %d, want %d", got, want)
	}
	mid := longPrompt(7000)
	want = window - g.lowered(mid)
	if got, _ := g.capSent(mid, window); got != want {
		t.Errorf("after the denser count: max_tokens %d, want %d, the window less its estimate at the denser count", got, want)
	}
	est = g.lowered(fits)
	if window-est >= minReplyRoom {
		t.Fatalf("at the denser count the first test prompt leaves %d for the reply; want under %d", window-est, minReplyRoom)
	}
	g.refused(fits, window, fmt.Sprintf("about %d tokens by estimate", est), fmt.Sprintf("leaves %d of", window-est))
}

// A lowered estimate sets only the room for the reply. A prompt whose class
// count reaches the window is refused even when the counts of earlier
// prompts put it well inside: those counts cannot show it is no denser.
func TestContextGuard_ClassCountHoldsALoweredPrompt(t *testing.T) {
	const window = 4096
	g := newCountingGuard(t, serverOllama)
	g.setRatio(0.53)
	for _, p := range []string{longPrompt(3000), longPrompt(3100), longPrompt(3200)} {
		g.learn(p, window)
	}
	prompt := longPrompt(12500)
	raw, est := estimateOf(loneUserPrompt(prompt), 1), g.lowered(prompt)
	if raw < window || window-est < minReplyRoom {
		t.Fatalf("the test prompt is %d tokens by text class and %d lowered; want the first at or past the %d window and the second leaving %d for the reply", raw, est, window, minReplyRoom)
	}
	g.refused(prompt, window, fmt.Sprintf("the prompt is %d tokens by text class", raw), fmt.Sprintf("put it at about %d", est), "could lose its earliest part")
}

// A count the check fails does not count toward the three a model needs
// before its estimate may fall: a server that cut a prompt counted part of
// it.
func TestContextGuard_AFailedCountDoesNotLower(t *testing.T) {
	const window = 4096
	g := newCountingGuard(t, serverOllama)
	g.setRatio(0.53)
	g.learn(longPrompt(3000), window)
	g.learn(longPrompt(3100), window)
	cut := longPrompt(20000)
	g.setRatio(minCutTokens / loneUserPrompt(cut))
	if n := g.count(cut); n < minCutTokens || int64(n)*cutBytesPerToken >= int64(len(cut)) || float64(n)/loneUserPrompt(cut) > g.densest {
		t.Fatalf("the cut prompt is counted at %d; want a count the check fails as cut, no denser than the others", n)
	}
	if _, err := g.send(cut, 0); err == nil || !strings.Contains(err.Error(), "dropped part of the prompt") {
		t.Fatalf("the cut prompt: %v; want it failed as cut", err)
	}
	g.setRatio(0.53)
	fits := longPrompt(10000)
	raw := estimateOf(loneUserPrompt(fits), 1)
	g.refused(fits, window, fmt.Sprintf("leaves %d of", window-raw))
	g.learn(longPrompt(3200), window)
	want := window - g.lowered(fits)
	if got, _ := g.capSent(fits, window); got != want {
		t.Errorf("after a third count that passed: max_tokens %d, want %d", got, want)
	}
}

// A count above a lowered estimate, in the upper half of the window, passes
// when it is within the prompt's class count, which is what the prompt was
// held to the window at, and raises later estimates.
func TestContextGuard_ADenserCountWithinTheClassCountPasses(t *testing.T) {
	const window = 4096
	g := newCountingGuard(t, serverOllama)
	g.setRatio(0.53)
	for _, p := range []string{longPrompt(3000), longPrompt(3100), longPrompt(3200)} {
		g.learn(p, window)
	}
	fits := longPrompt(10000)
	raw, est := estimateOf(loneUserPrompt(fits), 1), g.lowered(fits)
	g.setRatio(0.65)
	n := int64(g.count(fits))
	if 2*n < window || n <= est || n > raw {
		t.Fatalf("the server counts the test prompt at %d; want at least half the %d window, above its lowered estimate %d and within its class count %d", n, window, est, raw)
	}
	g.learn(fits, window)
	mid := longPrompt(7000)
	want := window - g.lowered(mid)
	if got, _ := g.capSent(mid, window); got != want {
		t.Errorf("after the denser count: max_tokens %d, want %d", got, want)
	}
}

// A count in the upper half of the window above the prompt's class count
// fails the call after the estimate was lowered too: the class count is
// what the prompt was held to the window at, and the error names it. The
// count still raises later estimates.
func TestContextGuard_ACountAboveTheClassCountFails(t *testing.T) {
	const window = 4096
	g := newCountingGuard(t, serverOllama)
	g.setRatio(0.53)
	for _, p := range []string{longPrompt(3000), longPrompt(3100), longPrompt(3200)} {
		g.learn(p, window)
	}
	fits := longPrompt(10000)
	raw := estimateOf(loneUserPrompt(fits), 1)
	g.setRatio(1.05)
	n := int64(g.count(fits))
	if 2*n < window || n <= raw || n+5 > window {
		t.Fatalf("the server counts the test prompt at %d; want at least half the %d window, above its class count %d, and room for a 5-token reply", n, window, raw)
	}
	_, err := g.send(fits, window)
	if want := fmt.Sprintf("the server counted %d prompt tokens, more than the %d estimated by text class", n, raw); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("the call: %v; want it failed with %q", err, want)
	}
	est := estimateOf(loneUserPrompt(fits), float64(n)/loneUserPrompt(fits)/calibrationMargin)
	if window-est >= minReplyRoom {
		t.Fatalf("at the denser count the test prompt leaves %d for the reply; want under %d", window-est, minReplyRoom)
	}
	g.refused(fits, window, fmt.Sprintf("about %d tokens by estimate", est), fmt.Sprintf("leaves %d of", window-est))
}

// A prompt denser than the counts that lowered its estimate gets a cap past
// its real room. A reply that runs into the window then fails naming the
// window, the server's counts and the estimate under them, whether the
// server stops the reply one token under the window, as llama.cpp does with
// context shift off, or shifts its context and runs the reply to the cap.
// Either way the call counts as cut off.
func TestContextGuard_AReplyIntoTheWindowNamesIt(t *testing.T) {
	const window = 4096
	for _, c := range []struct {
		name   string
		server localServer
		shift  bool
		want   string
	}{
		{"stopped at the window", serverLlamaCpp, false, "reply cut off at the context window, short of the output cap"},
		{"run past the window", serverOllama, true, "dropped the earliest part of the conversation while it answered"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newCountingGuard(t, c.server)
			g.setRatio(0.53)
			for _, p := range []string{longPrompt(3000), longPrompt(3100), longPrompt(3200)} {
				g.learn(p, window)
			}
			prompt := longPrompt(10000)
			raw, est := estimateOf(loneUserPrompt(prompt), 1), g.lowered(prompt)
			sent := window - est
			g.setRatio(0.8)
			g.overrun.Store(window)
			g.shift.Store(c.shift)
			n := int64(g.count(prompt))
			replied := window - 1 - n
			if c.shift {
				replied = sent
			}
			if n <= est || n > raw || window-1-n >= sent {
				t.Fatalf("the server counts the test prompt at %d; want it above its lowered estimate %d, within its class count %d, and leaving under the %d cap", n, est, raw, sent)
			}
			res, err := g.send(prompt, window)
			if err == nil {
				t.Fatal("the reply that ran into the window passed")
			}
			for _, want := range []string{
				c.want,
				fmt.Sprintf("counted %d prompt and %d reply tokens", n, replied),
				fmt.Sprintf("%d-token context window", window),
				fmt.Sprintf("an estimate of %d prompt tokens", est),
				fmt.Sprintf("this prompt, %d tokens by text class, is denser", raw),
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error %q lacks %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "the cap was lowered") {
				t.Errorf("the error %q blames the cap", err)
			}
			if res == nil || res.CutOffCalls != 1 {
				t.Errorf("the result %+v; want the call counted as cut off", res)
			}
		})
	}
}

// Counts lower an estimate only at a server the runner found to be Ollama,
// LM Studio or llama.cpp, each of which runs a call whose prompt and cap
// together pass the window. In a chain of four nodes, the fourth's class
// count leaves too little room for the reply, and the server counts every
// prompt at 0.53 of it. At Ollama the fourth is sent after three counts,
// with the cap the lowered estimate leaves. At a server that answers only
// the OpenAI-compatible API, with the window from the models config, it
// stays refused: such a server may refuse a cap past the prompt's real room.
func TestContextGuard_LowersOnlyAtALocalServer(t *testing.T) {
	const window, ratio, model = 4096, 0.53, "m:0.8b"
	count := func(p string) int { return int(math.Ceil(loneUserPrompt(p) * ratio)) }
	prompts := []string{longPrompt(3000), longPrompt(3100), longPrompt(3200), longPrompt(10000)}
	var densest float64
	for _, p := range prompts[:3] {
		densest = math.Max(densest, float64(count(p))/loneUserPrompt(p))
	}
	fits := prompts[3]
	raw, est := estimateOf(loneUserPrompt(fits), 1), estimateOf(loneUserPrompt(fits), densest/calibrationMargin)
	if window-raw >= minReplyRoom || window-est < minReplyRoom {
		t.Fatalf("the fourth prompt leaves %d of the %d window by its class count and %d lowered; want under and at least %d", window-raw, window, window-est, minReplyRoom)
	}
	names := []string{"one", "two", "three", "four"}
	yaml := "name: guard\nnodes:\n"
	for i, p := range prompts {
		yaml += fmt.Sprintf("  %s:\n    type: agent\n    model: %s\n    tools: []\n    prompt: %q\n    outputs: [answer]\n", names[i], model, p)
	}
	yaml += "edges:\n  - {from: one, to: two}\n  - {from: two, to: three}\n  - {from: three, to: four}\n"
	for _, c := range []struct {
		name  string
		notPS bool
	}{{"at Ollama", false}, {"at a server that answers only the OpenAI-compatible API", true}} {
		t.Run(c.name, func(t *testing.T) {
			onlyTheServersWindow(t)
			if c.notPS {
				modelContextWindow = func(string) int64 { return window }
			}
			f := &fakeOllama{window: window, notPS: c.notPS, modelID: model, reply: func(body map[string]any) string {
				msgs, _ := body["messages"].([]any)
				content, _ := msgs[0].(map[string]any)["content"].(string)
				return chatReply(`{"answer":"x"}`, "stop", count(content), 5)
			}}
			f.loaded.Store(true)
			srv := httptest.NewServer(f)
			defer srv.Close()
			_, store, log, _ := localRun(t, srv, yaml, Config{MaxOutputTokens: 8192})
			chats := f.sent()
			four := readLocalNodeRow(t, store, "four")
			if !c.notPS {
				if len(chats) != 4 || four.status != "completed" {
					t.Fatalf("%d chat requests and node four %s (%q); want 4 and completed\n%s", len(chats), four.status, four.errMsg, log)
				}
				if got, _ := chats[3]["max_tokens"].(float64); int64(got) != window-est {
					t.Errorf("node four: max_tokens %v, want %d, the window less its lowered estimate %d", chats[3]["max_tokens"], window-est, est)
				}
				return
			}
			if len(chats) != 3 || four.status != "failed" {
				t.Fatalf("%d chat requests and node four %s; want 3 and failed\n%s", len(chats), four.status, log)
			}
			if want := fmt.Sprintf("leaves %d of", window-raw); !strings.Contains(four.errMsg, want) {
				t.Errorf("node four's error %q lacks %q", four.errMsg, want)
			}
		})
	}
}
