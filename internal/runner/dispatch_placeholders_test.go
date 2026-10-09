package runner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// promptRecorder answers every chat request and records its user message.
type promptRecorder struct {
	mu      sync.Mutex
	prompts []string
}

func (p *promptRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Messages []struct{ Role, Content string } `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	for _, m := range body.Messages {
		if m.Role == "user" {
			p.mu.Lock()
			p.prompts = append(p.prompts, m.Content)
			p.mu.Unlock()
		}
	}
	_, _ = w.Write([]byte(chatReply(`{"answer":"x"}`, "stop", 10, 5)))
}

// A context pack that names the runner's own tokens — {comb.…}, {cde.…} and
// a fan's item placeholder — reaches the model as written: the runner fills
// those tokens where the node's template holds them and nowhere else.
func TestDispatch_ContextPackTokensStayAsWritten(t *testing.T) {
	t.Setenv("OLLAMA_CONTEXT_LENGTH", "")
	pack := "HIVE's specs: a lens cites {comb.forager:skeptic}, the framer reads {cde.axis-candidates}, the region is {comb.region}, a fan fills {item}."
	yaml := `name: pack
inputs: [context]
nodes:
  one:
    type: agent
    model: m:8b
    tools: []
    prompt: "CTX: {context}\nAXES: {cde.axis-candidates}"
    outputs: [answer]
  fan:
    type: parallel_fan
    model: m:8b
    tools: []
    prompt: "CTX: {context}\nITEM: {item}"
    fan_source: items
    outputs: [answer]
edges:
  - {from: one, to: fan}
`
	rec := &promptRecorder{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	_, _, log, err := localRun(t, srv, yaml, Config{Inputs: map[string]any{"context": pack, "items": []any{"first", "second"}}})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, log)
	}
	rec.mu.Lock()
	prompts := append([]string(nil), rec.prompts...)
	rec.mu.Unlock()
	if len(prompts) != 3 {
		t.Fatalf("%d prompts, want the node's and one per fan item: %q", len(prompts), prompts)
	}
	one := prompts[0]
	if !strings.HasPrefix(one, "CTX: "+pack+"\nAXES: ") || strings.HasSuffix(one, "{cde.axis-candidates}") {
		t.Errorf("node prompt %q: want the pack as written and the template's own {cde.axis-candidates} filled", one)
	}
	items := prompts[1:]
	sort.Strings(items)
	for i, item := range []string{"first", "second"} {
		if want := "CTX: " + pack + "\nITEM: " + item; items[i] != want {
			t.Errorf("fan item prompt %q, want %q", items[i], want)
		}
	}
}
