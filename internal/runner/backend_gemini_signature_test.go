package runner

// Tests for Gemini thought signatures: a function-call turn goes back to the
// API with every part exactly as the model sent it, so each part keeps its
// thoughtSignature. The fake validates what Google's thought-signatures page
// (ai.google.dev/gemini-api/docs/generate-content/thought-signatures, last
// updated 2026-09-04) says Gemini 3 validates, and answers a missing
// signature the way that page says the API does. No model is called.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// strictGemini answers step k of one turn with steps[k]'s parts, and refuses
// a request as Gemini 3 does: HTTP 400 when the first functionCall part of a
// model content in the current turn carries no thoughtSignature, or when the
// responses to a step's function calls do not all follow it in one content.
type strictGemini struct {
	steps []string // each a JSON array of parts
	mu    sync.Mutex
	reqs  []map[string]any
}

func (g *strictGemini) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	k := len(g.reqs)
	g.reqs = append(g.reqs, body)
	g.mu.Unlock()
	contents, _ := body["contents"].([]any)
	if msg := geminiSignatureViolation(contents); msg != "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":{"code":400,"message":%q,"status":"INVALID_ARGUMENT"}}`, msg)
		return
	}
	if k >= len(g.steps) {
		http.Error(w, "no step left", http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":%s}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}`, g.steps[k])
}

// geminiSignatureViolation is the API's error for contents, or "" when it
// accepts them. The current turn starts at the last user content holding
// text; each model content after it is one step.
func geminiSignatureViolation(contents []any) string {
	start := 0
	for i, c := range contents {
		m, _ := c.(map[string]any)
		if m["role"] != "user" {
			continue
		}
		for _, p := range geminiTestParts(m) {
			if _, ok := p["text"]; ok {
				start = i
			}
		}
	}
	for i := start; i < len(contents); i++ {
		m, _ := contents[i].(map[string]any)
		if m["role"] != "model" {
			continue
		}
		calls := 0
		for _, p := range geminiTestParts(m) {
			fc, ok := p["functionCall"].(map[string]any)
			if !ok {
				continue
			}
			if calls == 0 {
				if sig, _ := p["thoughtSignature"].(string); sig == "" {
					return fmt.Sprintf("Function call %v in the %d. content block is missing a thought_signature.", fc["name"], i)
				}
			}
			calls++
		}
		if calls == 0 || i+1 >= len(contents) {
			continue
		}
		next, _ := contents[i+1].(map[string]any)
		responses := 0
		for _, p := range geminiTestParts(next) {
			if _, ok := p["functionResponse"]; ok {
				responses++
			}
		}
		if next["role"] != "user" || responses != calls {
			return fmt.Sprintf("Please ensure that the number of function response parts is equal to the number of function call parts of the function call turn (content block %d).", i)
		}
	}
	return ""
}

func geminiTestParts(content map[string]any) []map[string]any {
	raw, _ := content["parts"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, p := range raw {
		if m, ok := p.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// TestGeminiStrictFake_RefusesAMissingSignature: the fake refuses a history
// whose signatures are stripped, as the API does.
func TestGeminiStrictFake_RefusesAMissingSignature(t *testing.T) {
	srv := httptest.NewServer(&strictGemini{steps: []string{`[{"text":"ok"}]`}})
	defer srv.Close()
	for _, c := range []struct {
		name, model string
		want        int
	}{
		{"signed", `{"functionCall":{"name":"read_file","args":{"path":"."}},"thoughtSignature":"sig-A"}`, http.StatusOK},
		{"stripped", `{"functionCall":{"name":"read_file","args":{"path":"."}}}`, http.StatusBadRequest},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"contents":[
				{"role":"user","parts":[{"text":"read it"}]},
				{"role":"model","parts":[%s]},
				{"role":"user","parts":[{"functionResponse":{"name":"read_file","response":{"output":"x"}}}]}]}`, c.model)
			resp, err := http.Post(srv.URL+"/models/gemini-3-pro:generateContent", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Fatalf("status %d, want %d (%s)", resp.StatusCode, c.want, got)
			}
			if c.want == http.StatusBadRequest && !bytes.Contains(got, []byte("Function call read_file in the 1. content block is missing a thought_signature")) {
				t.Errorf("error body %s does not name the call and its content block", got)
			}
		})
	}
}

// TestGeminiBackend_Run_ReturnsThoughtSignatures: a tool loop of parallel
// then sequential function calls completes against the strict fake, and each
// request carries every earlier model content exactly as the fake sent it.
func TestGeminiBackend_Run_ReturnsThoughtSignatures(t *testing.T) {
	steps := []string{
		// Parallel calls: the signature is on the first functionCall part only.
		`[{"functionCall":{"name":"read_file","args":{"path":"."}},"thoughtSignature":"sig-A"},
		  {"functionCall":{"name":"glob","args":{"pattern":"*"}}}]`,
		// A sequential step, with text before its call.
		`[{"text":"One more look."},
		  {"functionCall":{"name":"read_file","args":{"path":"."}},"thoughtSignature":"sig-B"}]`,
		// The answer: the signature is on its last part.
		`[{"text":"done","thoughtSignature":"sig-C"}]`,
	}
	g := &strictGemini{steps: steps}
	srv := httptest.NewServer(g)
	defer srv.Close()

	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	reg := NewToolRegistry(t.TempDir(), nil, NewFSRecorder())
	res, err := b.Run(context.Background(), RunRequest{Prompt: "look around", Model: "gemini-3-pro", Registry: reg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	issued := make([][]any, len(steps))
	calls := 0
	for i, s := range steps {
		if err := json.Unmarshal([]byte(s), &issued[i]); err != nil {
			t.Fatal(err)
		}
		for _, p := range issued[i] {
			if _, ok := p.(map[string]any)["functionCall"]; ok {
				calls++
			}
		}
	}
	last := issued[len(issued)-1][0].(map[string]any)["text"]
	if res.FinalText != last || res.Turns != len(steps) || res.ToolUses != calls {
		t.Errorf("text/turns/tool uses = %q/%d/%d, want %q/%d/%d", res.FinalText, res.Turns, res.ToolUses, last, len(steps), calls)
	}
	if len(g.reqs) != len(steps) {
		t.Fatalf("the fake got %d requests, want %d", len(g.reqs), len(steps))
	}
	// Request k holds the prompt, then for each earlier step its model
	// content and the user content answering it.
	for k, req := range g.reqs {
		contents, _ := req["contents"].([]any)
		if len(contents) != 1+2*k {
			t.Fatalf("request %d has %d contents, want %d", k, len(contents), 1+2*k)
		}
		for j := 0; j < k; j++ {
			m, _ := contents[1+2*j].(map[string]any)
			if m["role"] != "model" || !reflect.DeepEqual(m["parts"], issued[j]) {
				t.Errorf("request %d, step %d: model content %v, want parts %v as sent", k, j, m, issued[j])
			}
		}
	}
}

// TestGeminiBackend_Run_ChargesThoughtTokens: a thinking model's thought
// tokens count as output. Google's API reference (ai.google.dev/api/
// generate-content, UsageMetadata, last updated 2026-09-23) gives
// totalTokenCount as prompt + thoughts + candidates, so candidatesTokenCount
// leaves thoughts out; its Thinking guide (last updated 2026-09-25) prices a
// response as its output tokens plus its thinking tokens.
func TestGeminiBackend_Run_ChargesThoughtTokens(t *testing.T) {
	type usage struct{ prompt, candidates, thoughts int64 }
	turns := []struct {
		parts string
		usage usage
	}{
		{`[{"functionCall":{"name":"read_file","args":{"path":"."}},"thoughtSignature":"sig-A"}]`, usage{900, 40, 1500}},
		{`[{"text":"done"}]`, usage{1100, 60, 700}},
	}
	var n int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		k := n
		n++
		mu.Unlock()
		u := turns[k].usage
		fmt.Fprintf(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":%s}}],"usageMetadata":{"promptTokenCount":%d,"candidatesTokenCount":%d,"thoughtsTokenCount":%d,"totalTokenCount":%d}}`,
			turns[k].parts, u.prompt, u.candidates, u.thoughts, u.prompt+u.candidates+u.thoughts)
	}))
	defer srv.Close()

	b := &GeminiBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	res, err := b.Run(context.Background(), RunRequest{Prompt: "look", Model: "gemini-2.5-pro", Registry: NewToolRegistry(t.TempDir(), nil, NewFSRecorder())})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var in, out int64
	for _, turn := range turns {
		in += turn.usage.prompt
		out += turn.usage.candidates + turn.usage.thoughts
	}
	if res.Turns != len(turns) || res.InputTokens != in || res.OutputTokens != out {
		t.Errorf("turns/in/out = %d/%d/%d, want %d/%d/%d", res.Turns, res.InputTokens, res.OutputTokens, len(turns), in, out)
	}
}
