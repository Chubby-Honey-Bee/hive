package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// thinkingState is what an endpoint says about a model's thinking
// capability.
type thinkingState int

const (
	// thinkingUnknown: the endpoint does not say. It is not an Ollama server
	// under /v1, or its /api/show answered in another shape.
	thinkingUnknown thinkingState = iota
	// thinkingYes: /api/show lists "thinking" in the model's capabilities.
	thinkingYes
	// thinkingNo: /api/show lists capabilities without "thinking".
	thinkingNo
)

// thinkingEntry caches one (endpoint, model)'s thinking capability, and
// whether the backend has logged not sending it a reasoning level.
type thinkingEntry struct {
	mu     sync.Mutex
	cached bool
	state  thinkingState
	noted  bool
}

// thinkingCache holds a run's thinkingEntry per (endpoint, model). Run makes
// one per run, so a long-lived process (chb-mcp, chb replicate) asks again
// on its next run, and each run's log carries its own note.
type thinkingCache struct {
	mu      sync.Mutex
	entries map[[2]string]*thinkingEntry
}

// entry is the cache's entry for (base, model). A nil cache keeps nothing,
// so its entry is always new.
func (c *thinkingCache) entry(base, model string) *thinkingEntry {
	if c == nil {
		return &thinkingEntry{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := [2]string{base, model}
	e := c.entries[k]
	if e == nil {
		if c.entries == nil {
			c.entries = map[[2]string]*thinkingEntry{}
		}
		e = &thinkingEntry{}
		c.entries[k] = e
	}
	return e
}

// thinking is what b's endpoint says about model's thinking capability. It
// asks once per (endpoint, model) per run, and only an endpoint whose base
// URL ends in /v1, as Ollama's OpenAI-compatible one does. A request that
// got no HTTP answer is not cached, so the next call asks again; any HTTP
// answer is.
func (b *OpenAIBackend) thinking(ctx context.Context, model string) thinkingState {
	if !strings.HasSuffix(b.BaseURL, "/v1") {
		return thinkingUnknown
	}
	e := b.thinkingCache.entry(b.BaseURL, model)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cached {
		return e.state
	}
	state, answered := b.askThinking(ctx, model)
	if answered {
		e.cached, e.state = true, state
	}
	return state
}

// askThinking asks the endpoint, within 30 seconds, what model can do
// (ollamaCapabilities): what it says of thinking, and whether the request
// got an answer to cache.
func (b *OpenAIBackend) askThinking(ctx context.Context, model string) (thinkingState, bool) {
	askCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	caps, ok, answered := b.ollamaCapabilities(askCtx, model)
	return thinkingFrom(caps, ok), answered
}

// thinkingFrom is what caps, a model's capabilities, say of thinking;
// unknown when ok is false, the answer not Ollama's.
func thinkingFrom(caps []string, ok bool) thinkingState {
	switch {
	case !ok:
		return thinkingUnknown
	case slices.Contains(caps, "thinking"):
		return thinkingYes
	}
	return thinkingNo
}

// reasoningToSend is the reasoning_effort b sends model for a node's level:
// nothing when the endpoint reports the model cannot think, since Ollama
// answers a level other than none on such a model with HTTP 400, and the
// model does not think whatever is sent; else the level as set. So a server
// that does not say still gets none, the one level that turns a thinking
// model's default thinking off, and a node that wants no field sets no level.
// note is a line for the run log the first time, per endpoint and model in a
// run, that a level is not sent; "" otherwise.
func (b *OpenAIBackend) reasoningToSend(ctx context.Context, model, level string) (sent, note string) {
	if level == "" || b.thinking(ctx, model) != thinkingNo {
		return level, ""
	}
	e := b.thinkingCache.entry(b.BaseURL, model)
	e.mu.Lock()
	first := !e.noted
	e.noted = true
	e.mu.Unlock()
	if !first {
		return "", ""
	}
	return "", fmt.Sprintf("reasoning: %s reports no thinking capability for %q, so no call to it carries reasoning_effort (a node's reasoning %s is not sent)", withoutUserinfo(b.BaseURL), model, level)
}

// ollamaCapabilities asks an Ollama server what model can do, by POST
// /api/show on the base URL without its /v1. answered is false when the
// request got no HTTP answer, or a 5xx one, which may pass. ok is false when
// the answer is not Ollama's: another server, an older Ollama without
// capabilities, or a model it does not hold.
func (b *OpenAIBackend) ollamaCapabilities(ctx context.Context, model string) (caps []string, ok, answered bool) {
	resp, err := b.showModel(ctx, model)
	if err != nil {
		return nil, false, false
	}
	defer resp.Body.Close()
	return capabilitiesOf(resp)
}

// showModel sends POST /api/show for model to the base URL without its /v1.
func (b *OpenAIBackend) showModel(ctx context.Context, model string) (*http.Response, error) {
	body, _ := json.Marshal(map[string]string{"model": model})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(b.BaseURL, "/v1")+"/api/show", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.APIKey)
	return b.Client.Do(req)
}

// capabilitiesOf reads resp, an /api/show answer, as ollamaCapabilities
// says.
func capabilitiesOf(resp *http.Response) (caps []string, ok, answered bool) {
	if resp.StatusCode >= 500 {
		return nil, false, false
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, true
	}
	caps, ok = decodeCapabilities(resp.Body)
	return caps, ok, true
}

// decodeCapabilities reads the capabilities list in body, an /api/show
// answer; ok is false when it holds none.
func decodeCapabilities(body io.Reader) ([]string, bool) {
	var out struct {
		Capabilities *[]string `json:"capabilities"`
	}
	if json.NewDecoder(body).Decode(&out) != nil || out.Capabilities == nil {
		return nil, false
	}
	return *out.Capabilities, true
}
