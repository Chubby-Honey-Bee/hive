package runner

// The schema-constrained-outputs replay suite. Each case replays one way a
// local model's reply goes wrong against an httptest OpenAI-compatible
// server, on a node with an output_schema, and grades that the run either
// parsed the reply into the right outputs or rejected it loudly: no silent
// pass. No model is called.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/artifact"
	"github.com/Chubby-Honey-Bee/hive/internal/db"
	"github.com/Chubby-Honey-Bee/hive/internal/schema"
	"github.com/Chubby-Honey-Bee/hive/internal/workflow"
)

// The schema'd node the replay cases run: key_points before verdict, as a
// reasoning-first schema declares them.
const replayYAML = `name: replay
nodes:
  lens:
    type: agent
    model: local-model
    prompt: "Judge the question."
    outputs: [verdict, key_points]
    output_schema:
      type: object
      required: [key_points, verdict]
      properties:
        key_points: {type: array, items: {type: string}, maxItems: 3}
        verdict: {enum: [support, oppose]}
%s`

const replayValid = `{"key_points":["the cache is warm"],"verdict":"support"}`

const replayRepairBlock = "    on_reject:\n      max_repair_iterations: 1\n"

// requestKind classifies a chat request the fake receives.
func isProbe(body map[string]any) bool {
	rf, _ := body["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	return js["name"] == "hive_constraint_probe"
}

func sendsSchema(body map[string]any) bool {
	_, ok := body["response_format"]
	return ok && !isProbe(body)
}

func offersTools(body map[string]any) bool {
	_, ok := body["tools"]
	return ok
}

func messagesOf(body map[string]any) []map[string]any {
	raw, _ := body["messages"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		mm, _ := m.(map[string]any)
		out = append(out, mm)
	}
	return out
}

func lastMessage(body map[string]any) map[string]any {
	msgs := messagesOf(body)
	if len(msgs) == 0 {
		return nil
	}
	return msgs[len(msgs)-1]
}

// isRepair reports a repair attempt: tryRepair's default prompt.
func isRepair(body map[string]any) bool {
	for _, m := range messagesOf(body) {
		if c, _ := m["content"].(string); m["role"] == "user" && strings.Contains(c, "failed acceptance criteria") {
			return true
		}
	}
	return false
}

// rawToolCallReply renders a reply that calls one tool with raw arguments.
func rawToolCallReply(id, name, args string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{
			"content": "",
			"tool_calls": []any{map[string]any{"id": id, "type": "function",
				"function": map[string]any{"name": name, "arguments": args}}},
		}}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
	})
	return string(b)
}

// schemaServer is a fake whose probe answer is probe and whose other calls
// are answered by answer, given the request and its 1-based number among
// the non-probe calls. It keeps every non-probe body.
type schemaServer struct {
	*fakeOpenAI
	calls  atomic.Int64
	probes atomic.Int64
	mu     sync.Mutex
	sent   []map[string]any
}

func newSchemaServer(probe string, answer func(body map[string]any, n int) string) (*schemaServer, *httptest.Server) {
	s := &schemaServer{}
	s.fakeOpenAI = &fakeOpenAI{reply: func(body map[string]any) string {
		if isProbe(body) {
			s.probes.Add(1)
			return probe
		}
		n := int(s.calls.Add(1))
		s.mu.Lock()
		s.sent = append(s.sent, body)
		s.mu.Unlock()
		return answer(body, n)
	}}
	return s, httptest.NewServer(s.fakeOpenAI)
}

func (s *schemaServer) bodies() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.sent...)
}

// probeEnforced answers the probe as a server that constrains decoding and
// keeps the declared key order does.
var probeEnforced = chatReply(`{"probe":"hive-constraint-ok","order":"second"}`, "stop", 1, 1)

type schemaNodeRow struct {
	status, errMsg, rationale, outputs, enforcement string
}

func readSchemaNode(t *testing.T, store *db.Store, node string) schemaNodeRow {
	t.Helper()
	var r schemaNodeRow
	if err := store.ReadDB.QueryRow(
		`SELECT status, COALESCE(error,''), COALESCE(rationale,''), COALESCE(outputs_json,''), COALESCE(schema_enforcement,'')
		 FROM workflow_node_states WHERE node_name=? ORDER BY id DESC LIMIT 1`, node,
	).Scan(&r.status, &r.errMsg, &r.rationale, &r.outputs, &r.enforcement); err != nil {
		t.Fatalf("read node %s: %v", node, err)
	}
	return r
}

func decodeObject(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return m
}

// TestSchemaReplay_NoSilentPass is the acceptance suite. A case either
// completes with exactly the outputs of the valid object it carries, or ends
// failed or rejected with the reason named.
func TestSchemaReplay_NoSilentPass(t *testing.T) {
	truncated := replayValid[:len(replayValid)/2]
	notFound := "bash: chb: command not found"
	cases := []struct {
		name   string
		repair bool
		answer func(body map[string]any, n int) string
		// parsed is the object a completed node's outputs must equal; ""
		// means the node must not complete.
		parsed string
		// status and reason grade a node that must not complete: its
		// status, and what its error or rationale names.
		status, reason string
		calls          int
		// atDecode: the accepted answer came from a call that sent the
		// schema, to a model the probe (enforced here) showed constrains.
		atDecode bool
	}{
		{
			name: "prose preamble",
			answer: func(map[string]any, int) string {
				return chatReply("Here is my verdict.\n\n"+replayValid, "stop", 1, 1)
			},
			parsed: replayValid, calls: 1,
		},
		{
			name:   "code fence",
			answer: func(map[string]any, int) string { return chatReply("```json\n"+replayValid+"\n```", "stop", 1, 1) },
			parsed: replayValid, calls: 1,
		},
		{
			name:   "cut off at the cap",
			answer: func(map[string]any, int) string { return chatReply(truncated, "length", 1, 1) },
			status: "failed", reason: "stop reason length", calls: 1,
		},
		{
			name:   "truncated with a stop",
			answer: func(map[string]any, int) string { return chatReply(truncated, "stop", 1, 1) },
			status: "rejected", reason: "no JSON object", calls: 2,
		},
		{
			name: "malformed tool arguments",
			answer: func(_ map[string]any, n int) string {
				if n == 1 {
					return rawToolCallReply("call-1", "read_file", `{"path": "README`)
				}
				return chatReply(replayValid, "stop", 1, 1)
			},
			parsed: replayValid, calls: 2,
		},
		{
			name:   "schema violation, repaired once",
			repair: true,
			answer: func(body map[string]any, _ int) string {
				// The repair's own tool loop answers in prose, so only its
				// finalize call, which needs the node's schema, holds.
				if isRepair(body) && sendsSchema(body) {
					return chatReply(replayValid, "stop", 1, 1)
				}
				if isRepair(body) {
					return chatReply("Fixed: the verdict is support.", "stop", 1, 1)
				}
				return chatReply(`{"key_points":["a"],"verdict":"maybe"}`, "stop", 1, 1)
			},
			parsed: replayValid, calls: 4, atDecode: true,
		},
		{
			name: "schema violation, no repair",
			answer: func(map[string]any, int) string {
				return chatReply(`{"key_points":["a"],"verdict":"maybe"}`, "stop", 1, 1)
			},
			status: "rejected", reason: "$.verdict", calls: 2,
		},
		{
			name:   "command not found prose",
			answer: func(map[string]any, int) string { return chatReply(notFound, "stop", 1, 1) },
			status: "rejected", reason: "no JSON object", calls: 2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newSchemaServer(probeEnforced, c.answer)
			defer srv.Close()
			extra := ""
			if c.repair {
				extra = replayRepairBlock
			}
			res, store, log, err := localRun(t, srv, fmt.Sprintf(replayYAML, extra), Config{})
			row := readSchemaNode(t, store, "lens")
			if got := int(f.calls.Load()); got != c.calls {
				t.Errorf("model calls = %d, want %d", got, c.calls)
			}
			if c.parsed != "" {
				if err != nil || row.status != "completed" {
					t.Fatalf("run err %v, node %s (%s); want completed\n%s", err, row.status, row.errMsg, log)
				}
				if got, want := decodeObject(t, row.outputs), decodeObject(t, c.parsed); fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("outputs %v, want %v", got, want)
				}
				want := SchemaPostHocOnly
				if c.atDecode {
					want = SchemaEnforcedAtDecode
				}
				if row.enforcement != want {
					t.Errorf("schema_enforcement %q, want %q\n%s", row.enforcement, want, log)
				}
				return
			}
			if err == nil || res == nil {
				t.Errorf("run err %v: a run whose only node did not complete must fail", err)
			}
			if row.status != c.status {
				t.Fatalf("node %s, want %s\n%s", row.status, c.status, log)
			}
			if said := row.errMsg + " " + row.rationale; !strings.Contains(said, c.reason) {
				t.Errorf("node says %q, want it to name %q", said, c.reason)
			}
			if c.status == "rejected" && !strings.Contains(row.rationale, "output_schema") {
				t.Errorf("rationale %q does not name output_schema", row.rationale)
			}
			// Prose was never aliased onto the first declared output.
			var state string
			_ = store.ReadDB.QueryRow(`SELECT COALESCE(state_json,'') FROM workflow_runs ORDER BY id DESC LIMIT 1`).Scan(&state)
			if strings.Contains(state, `"verdict"`) {
				t.Errorf("state %s holds a verdict from a reply that was rejected", state)
			}
		})
	}
}

// A tool call whose arguments are not JSON is not run on {}: the model is
// told why, and the audit trail keeps the call it made.
func TestSchemaReplay_MalformedToolArgumentsAreRefused(t *testing.T) {
	args := `{"path": "README`
	f, srv := newSchemaServer(probeEnforced, func(_ map[string]any, n int) string {
		if n == 1 {
			return rawToolCallReply("call-1", "read_file", args)
		}
		return chatReply(replayValid, "stop", 1, 1)
	})
	defer srv.Close()
	_, store, _, err := localRun(t, srv, fmt.Sprintf(replayYAML, ""), Config{})
	if err != nil {
		t.Fatal(err)
	}
	bodies := f.bodies()
	if len(bodies) != 2 {
		t.Fatalf("%d calls, want 2", len(bodies))
	}
	told := lastMessage(bodies[1])
	content, _ := told["content"].(string)
	if told["role"] != "tool" || told["tool_call_id"] != "call-1" || !strings.HasPrefix(content, "ERROR: ") || !strings.Contains(content, "not a JSON object") {
		t.Errorf("the model was told %v, want an ERROR tool message for call-1 naming the bad arguments", told)
	}
	var tool, input, output string
	var isErr int
	if err := store.ReadDB.QueryRow(`SELECT tool, input, output, is_error FROM tool_invocations WHERE node_name='lens'`).Scan(&tool, &input, &output, &isErr); err != nil {
		t.Fatalf("no audit row: %v", err)
	}
	if tool != "read_file" || input != args || isErr != 1 || !strings.Contains(output, "was not run") {
		t.Errorf("audit row = (%s, %q, %q, %d), want the refused call with its raw arguments", tool, input, output, isErr)
	}
}

// A node whose tool-offering call answers outside its schema gets one
// finalize call: the tools dropped, the schema set in declared order, the
// conversation and its answer kept. What the node records follows the
// probe.
func TestSchemaReplay_FinalizeCall(t *testing.T) {
	prose := "The verdict is support, because the cache is warm."
	for _, c := range []struct {
		probe string
		want  string
		why   string
	}{
		{probeEnforced, SchemaEnforcedAtDecode, OrderDeclared},
		{chatReply(`{"order":"second","probe":"hive-constraint-ok"}`, "stop", 1, 1), SchemaEnforcedAtDecode, OrderSorted},
		{chatReply("It is sunny and mild today.", "stop", 1, 1), SchemaPostHocOnly, ProbeNotEnforced},
		{chatReply("", "stop", 1, 1), SchemaPostHocOnly, ProbeNoAnswer},
	} {
		f, srv := newSchemaServer(c.probe, func(body map[string]any, _ int) string {
			if sendsSchema(body) {
				return chatReply(replayValid, "stop", 1, 1)
			}
			return chatReply(prose, "stop", 1, 1)
		})
		_, store, log, err := localRun(t, srv, fmt.Sprintf(replayYAML, ""), Config{})
		srv.Close()
		if err != nil {
			t.Fatalf("run: %v\n%s", err, log)
		}
		bodies := f.bodies()
		if len(bodies) != 2 {
			t.Fatalf("%d calls, want the answer and one finalize call", len(bodies))
		}
		first, fin := bodies[0], bodies[1]
		if !offersTools(first) || sendsSchema(first) {
			t.Errorf("first call: tools %v, schema %v; want tools and no schema", offersTools(first), sendsSchema(first))
		}
		if offersTools(fin) || !sendsSchema(fin) {
			t.Errorf("finalize call: tools %v, schema %v; want the schema and no tools", offersTools(fin), sendsSchema(fin))
		}
		msgs := messagesOf(fin)
		if len(msgs) < 2 || msgs[len(msgs)-2]["role"] != "assistant" || msgs[len(msgs)-2]["content"] != prose {
			t.Errorf("the finalize call does not carry the answer it replaces: %v", msgs)
		}
		js := fin["response_format"].(map[string]any)["json_schema"].(map[string]any)
		if js["name"] != "lens" || js["strict"] != false {
			t.Errorf("json_schema name %v strict %v, want lens and false for a schema that is not strict", js["name"], js["strict"])
		}
		row := readSchemaNode(t, store, "lens")
		if row.status != "completed" || row.enforcement != c.want {
			t.Errorf("probe %s: node %s recorded %q, want completed and %q\n%s", c.probe, row.status, row.enforcement, c.want, log)
		}
		if !strings.Contains(log, "node lens: output schema "+c.want) || !strings.Contains(log, c.why) {
			t.Errorf("the run log does not say %s, and why (%s):\n%s", c.want, c.why, log)
		}
	}
}

// A node with `tools: []` offers no tools, so its first call carries the
// schema and needs no finalize call. On a server the probe shows enforcing,
// the node records enforced at decode from that one call.
func TestSchemaReplay_NoToolsSendsTheSchemaFirst(t *testing.T) {
	f, srv := newSchemaServer(probeEnforced, func(body map[string]any, _ int) string {
		if sendsSchema(body) && !offersTools(body) {
			return chatReply(replayValid, "stop", 1, 1)
		}
		return chatReply("The verdict is support.", "stop", 1, 1)
	})
	defer srv.Close()
	_, store, log, err := localRun(t, srv, fmt.Sprintf(replayYAML, "    tools: []\n"), Config{})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, log)
	}
	bodies := f.bodies()
	if len(bodies) != 1 || offersTools(bodies[0]) || !sendsSchema(bodies[0]) {
		t.Fatalf("%d calls, the first offering tools %v and sending the schema %v; want one call with the schema and no tools",
			len(bodies), len(bodies) > 0 && offersTools(bodies[0]), len(bodies) > 0 && sendsSchema(bodies[0]))
	}
	if row := readSchemaNode(t, store, "lens"); row.status != "completed" || row.enforcement != SchemaEnforcedAtDecode {
		t.Errorf("node %s recorded %q, want completed and %q\n%s", row.status, row.enforcement, SchemaEnforcedAtDecode, log)
	}
}

// The body a server receives lists the schema's properties in declared
// order, which a Go map would have sorted.
func TestSchemaReplay_ResponseFormatKeepsOrder(t *testing.T) {
	var raw []byte
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		mu.Lock()
		raw = buf
		mu.Unlock()
		_, _ = w.Write([]byte(chatReply(replayValid, "stop", 1, 1)))
	}))
	defer srv.Close()
	s, err := schema.ParseYAML([]byte("type: object\nproperties:\n  zeta: {type: string}\n  alpha: {type: string}\n  mid: {type: string}"))
	if err != nil {
		t.Fatal(err)
	}
	b := &OpenAIBackend{APIKey: "k", BaseURL: srv.URL + "/v1", Client: &http.Client{}}
	res, err := b.Run(context.Background(), RunRequest{
		Prompt: "p", Model: "m", Registry: &ToolRegistry{Handlers: map[string]ToolHandler{}},
		OutputSchema: s, SchemaName: "forager/skeptic x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.SchemaAtDecode {
		t.Error("a call with no tools sent the schema but the result says it did not")
	}
	mu.Lock()
	body := string(raw)
	mu.Unlock()
	var positions []int
	for _, p := range []string{`"zeta"`, `"alpha"`, `"mid"`} {
		positions = append(positions, strings.Index(body, p))
	}
	if slices.Contains(positions, -1) || !slices.IsSorted(positions) {
		t.Errorf("properties at %v in %s, want the declared order zeta, alpha, mid", positions, body)
	}
	if !strings.Contains(body, `"name":"forager_skeptic_x"`) {
		t.Errorf("the schema name is not in OpenAI's form: %s", body)
	}
	if strings.Contains(body, `"tools"`) {
		t.Errorf("a registry with no tools sent tools: %s", body)
	}
}

// strict is sent true only for a schema that qualifies for OpenAI's strict
// mode, which refuses any other.
func TestSchemaReplay_StrictOnlyWhenTheSchemaQualifies(t *testing.T) {
	for _, text := range []string{
		"type: object\nproperties: {a: {type: string}}\nrequired: [a]\nadditionalProperties: false",
		"type: object\nproperties: {a: {type: string}}",
	} {
		s, err := schema.ParseYAML([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		rf := responseFormat(s, "n")
		if got := rf["json_schema"].(map[string]any)["strict"]; got != s.Strict() {
			t.Errorf("%q: strict %v, want %v", text, got, s.Strict())
		}
	}
}

// Each fan item is checked against the schema; one that breaks it is a
// failed item, named and not joined.
func TestSchemaReplay_FanItems(t *testing.T) {
	const fanYAML = `name: fan
inputs: [items]
nodes:
  fan:
    type: parallel_fan
    model: local-model
    prompt_template: "Look at {item}."
    fan_source: items
    outputs: [finding]
    output_schema:
      type: object
      required: [finding]
      properties:
        finding: {type: string, maxLength: 20}
`
	answer := func(body map[string]any, _ int) string {
		for _, m := range messagesOf(body) {
			c, _ := m["content"].(string)
			if m["role"] == "user" && strings.Contains(c, "Look at bad") {
				return chatReply("I could not look.", "stop", 1, 1)
			}
		}
		for _, m := range messagesOf(body) {
			c, _ := m["content"].(string)
			if i := strings.Index(c, "Look at "); m["role"] == "user" && i >= 0 {
				item := strings.TrimSuffix(c[i+len("Look at "):], ".")
				return chatReply(fmt.Sprintf(`{"finding":"about %s"}`, item), "stop", 1, 1)
			}
		}
		return chatReply("?", "stop", 1, 1)
	}
	for _, c := range []struct {
		items     []any
		completed bool
	}{
		{[]any{"alpha", "bad", "gamma"}, true},
		{[]any{"bad", "bad"}, false},
	} {
		t.Setenv("HIVE_MAX_PARALLEL_FAN", "1")
		_, srv := newSchemaServer(probeEnforced, answer)
		_, store, log, err := localRun(t, srv, fanYAML, Config{Inputs: map[string]any{"items": c.items}})
		srv.Close()
		row := readSchemaNode(t, store, "fan")
		if (row.status == "completed") != c.completed {
			t.Fatalf("items %v: node %s (%s), run err %v; want completed=%v\n%s", c.items, row.status, row.errMsg, err, c.completed, log)
		}
		for i, it := range c.items {
			named := strings.Contains(log+row.errMsg, fmt.Sprintf("item %d: output_schema", i+1))
			if (it == "bad") != named {
				t.Errorf("items %v: item %d (%v) named as breaking the schema = %v", c.items, i+1, it, named)
			}
			if c.completed && it != "bad" && !strings.Contains(row.rationale, "about "+it.(string)) {
				t.Errorf("the joined text lacks item %v's answer: %s", it, row.rationale)
			}
			marked := strings.Contains(row.rationale, fmt.Sprintf("## Item %d\n\n(no finding: the reply broke the node's output schema)", i+1))
			if c.completed && (it == "bad") != marked {
				t.Errorf("items %v: item %d (%v) marked as having no answer = %v: %s", c.items, i+1, it, marked, row.rationale)
			}
		}
		if c.completed && strings.Contains(row.rationale, "could not look") {
			t.Errorf("a failed item's text was joined: %s", row.rationale)
		}
		if c.completed {
			// The joined outputs break the schema as one reply, so the
			// engine did not check them again: the runner checked each item.
			s, _ := workflow.OutputSchemas(fanYAML)
			if v := workflow.CheckOutput(s["fan"], decodeObject(t, row.outputs)); len(v) == 0 {
				t.Errorf("the joined outputs hold the schema as one reply, so this case does not show they go unchecked: %s", row.outputs)
			}
		}
	}
}

// An empty fan makes no call at all, schema or not, so it checked no answer
// and records no schema_enforcement.
func TestSchemaReplay_EmptyFan(t *testing.T) {
	f, srv := newSchemaServer(probeEnforced, func(map[string]any, int) string { return chatReply(replayValid, "stop", 1, 1) })
	defer srv.Close()
	yaml := "name: fan\ninputs: [items]\nnodes:\n  fan:\n    type: parallel_fan\n    model: local-model\n    prompt_template: \"{item}\"\n    fan_source: items\n    outputs: [finding]\n    output_schema: {type: object, required: [finding], properties: {finding: {type: string}}}\n"
	_, store, log, err := localRun(t, srv, yaml, Config{Inputs: map[string]any{"items": []any{}}})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, log)
	}
	if row := readSchemaNode(t, store, "fan"); row.status != "completed" || f.calls.Load() != 0 || row.enforcement != "" {
		t.Errorf("node %s after %d calls, recording %q; want completed after none, recording nothing", row.status, f.calls.Load(), row.enforcement)
	}
}

// probeYAML's nodes send the probe these combinations: a and b share one,
// c differs in reasoning, e's repair uses another model, and d has no
// schema so sends none.
const probeYAML = `name: probes
nodes:
  a:
    type: agent
    model: m1
    reasoning: none
    prompt: p
    output_schema: {type: object}
  b:
    type: agent
    model: m1
    reasoning: none
    prompt: p
    output_schema: {type: object}
  c:
    type: agent
    model: m1
    reasoning: low
    prompt: p
    output_schema: {type: object}
  d:
    type: agent
    model: m2
    prompt: p
  e:
    type: agent
    model: m1
    prompt: p
    output_schema: {type: object}
    on_reject: {model: m3, max_repair_iterations: 1}
`

// One probe per distinct (endpoint, model, reasoning) a schema'd node or its
// repair sends; none for a node without a schema.
func TestProbeConstraints_OnePerCombination(t *testing.T) {
	type sent struct {
		model, reasoning string
		maxTokens        float64
	}
	var mu sync.Mutex
	var got []sent
	// The server reports both models can think, so each probe carries its
	// nodes' level as sent (reasoningToSend).
	thinks := []string{"completion", "thinking"}
	f := &fakeOpenAI{caps: map[string][]string{"m1": thinks, "m3": thinks}, reply: func(body map[string]any) string {
		if !isProbe(body) {
			t.Errorf("a call that is not a probe: %v", body)
		}
		r, _ := body["reasoning_effort"].(string)
		mt, _ := body["max_tokens"].(float64)
		mu.Lock()
		got = append(got, sent{body["model"].(string), r, mt})
		mu.Unlock()
		return probeEnforced
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("HIVE_PROVIDER_ALLOWLIST", "")
	defn, err := workflow.LoadYAMLString(probeYAML)
	if err != nil {
		t.Fatal(err)
	}
	probes := ProbeConstraints(context.Background(), Config{Provider: "openai"}, defn, nil)

	want := map[[2]string][]string{
		{"m1", "none"}: {"a", "b"},
		{"m1", "low"}:  {"c"},
		{"m1", ""}:     {"e"},
		{"m3", ""}:     {"e on_reject"},
	}
	if len(probes) != len(want) || len(got) != len(want) {
		t.Fatalf("%d probes from %d calls, want %d: %+v", len(probes), len(got), len(want), probes)
	}
	for _, p := range probes {
		nodes, ok := want[[2]string{p.Model, p.Reasoning}]
		if !ok || !slices.Equal(p.Nodes, nodes) || !p.Enforced() || p.BaseURL != srv.URL+"/v1" {
			t.Errorf("probe %+v, want nodes %v, enforced, at %s", p, nodes, srv.URL+"/v1")
		}
	}
	for _, s := range got {
		wantCap := 1024.0
		if s.reasoning == "none" {
			wantCap = 64
		}
		if s.maxTokens != wantCap {
			t.Errorf("probe of %s at reasoning %q capped at %v, want %v", s.model, s.reasoning, s.maxTokens, wantCap)
		}
	}

	t.Setenv("OPENAI_BASE_URL", "")
	if p := ProbeConstraints(context.Background(), Config{Provider: "openai"}, defn, nil); p != nil {
		t.Errorf("probes without OPENAI_BASE_URL: %+v", p)
	}
}

// probeReplies are the one object the probe's schema allows, built from
// probeSchemaJSON itself: its keys in the declared order, and sorted, as a
// server that re-encodes the schema through a map would write them.
func probeReplies(t *testing.T) (declared, sorted string) {
	t.Helper()
	s, err := schema.ParseYAML([]byte(probeSchemaJSON))
	if err != nil {
		t.Fatal(err)
	}
	obj := map[string]any{}
	var parts []string
	for _, p := range s.Properties {
		obj[p.Name] = p.Schema.Const
		k, _ := json.Marshal(p.Name)
		v, _ := json.Marshal(p.Schema.Const)
		parts = append(parts, string(k)+":"+string(v))
	}
	b, _ := json.Marshal(obj)
	declared, sorted = "{"+strings.Join(parts, ",")+"}", string(b)
	if declared == sorted {
		t.Fatal("the probe schema declares its keys in sorted order, so order is not measured")
	}
	return declared, sorted
}

// What a probe's reply says about the server.
func TestClassifyProbe(t *testing.T) {
	reply := func(content, reasoning, finish string) openaiChoice {
		var c openaiChoice
		c.Message.Content, c.Message.Reasoning, c.FinishReason = content, reasoning, finish
		return c
	}
	declared, sorted := probeReplies(t)
	var obj map[string]any
	_ = json.Unmarshal([]byte(declared), &obj)
	onlyProbe, _ := json.Marshal(map[string]any{"probe": obj["probe"]})
	extra := strings.TrimSuffix(declared, "}") + `,"x":1}`
	cases := []struct {
		name        string
		c           openaiChoice
		want, order string
	}{
		{"the object, declared order", reply(declared, "", "stop"), ProbeEnforced, OrderDeclared},
		{"the object, keys sorted", reply(sorted, "", "stop"), ProbeEnforced, OrderSorted},
		{"the object, spaced", reply("\n "+strings.ReplaceAll(declared, ",", " , ")+"\n", "", "stop"), ProbeEnforced, OrderDeclared},
		{"prose", reply("Sunny with light wind.", "", "stop"), ProbeNotEnforced, ""},
		{"prose cut off", reply("Sunny with light", "", "length"), ProbeNotEnforced, ""},
		{"a fenced object", reply("```json\n"+declared+"\n```", "", "stop"), ProbeNotEnforced, ""},
		{"another object", reply(`{"weather":"sunny"}`, "", "stop"), ProbeNotEnforced, ""},
		{"a key missing", reply(string(onlyProbe), "", "stop"), ProbeNotEnforced, ""},
		{"an extra key", reply(extra, "", "stop"), ProbeNotEnforced, ""},
		{"an object cut off", reply(declared[:len(declared)/2], "", "length"), ProbeNoAnswer, ""},
		{"reasoning only", reply("", "Let me think about the weather", "length"), ProbeNoAnswer, ""},
		{"empty", reply("", "", "stop"), ProbeNoAnswer, ""},
	}
	for _, c := range cases {
		if got, order, _ := classifyProbe(c.c, 64); got != c.want || order != c.order {
			t.Errorf("%s: %s, %q; want %s, %q", c.name, got, order, c.want, c.order)
		}
	}
}

// textBackend answers every call with the same text and sends no schema.
type textBackend string

func (b textBackend) Run(context.Context, RunRequest) (*RunResult, error) {
	return &RunResult{FinalText: string(b), Turns: 1}, nil
}

// A node on a backend that sends no schema is checked after the call, and
// says so.
func TestSchemaReplay_OtherBackendIsPostHoc(t *testing.T) {
	store := newTempStore(t)
	dir := t.TempDir()
	wf := filepath.Join(dir, "wf.yaml")
	if err := os.WriteFile(wf, []byte(fmt.Sprintf(replayYAML, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	_, err := Run(context.Background(), store, Config{
		WorkflowYAML: wf, ProjectDir: dir, MaxIterations: 5, Log: &log,
		Backend: textBackend(replayValid),
	})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, log.String())
	}
	row := readSchemaNode(t, store, "lens")
	if row.status != "completed" || row.enforcement != SchemaPostHocOnly || !strings.Contains(log.String(), "sends no schema") {
		t.Errorf("node %s recorded %q; want completed, %q, and the log to say why\n%s", row.status, row.enforcement, SchemaPostHocOnly, log.String())
	}
}

// The probes run after the run row exists and its ID is logged, each
// announced as it is sent, and their tokens and calls are charged to the
// run: the run's totals, in the result and in its cost view, are every
// call's usage the fake handed out, and the run's row holds the probes'.
func TestConstraintProbe_AfterTheRunIDAndCharged(t *testing.T) {
	var mu sync.Mutex
	var in, out, probeIn, probeOut, probes int64
	f := &fakeOpenAI{reply: func(body map[string]any) string {
		content, pi, po := replayValid, int64(7), int64(3)
		mu.Lock()
		defer mu.Unlock()
		if isProbe(body) {
			content, pi, po = `{"probe":"hive-constraint-ok","order":"second"}`, 11, 5
			probeIn, probeOut, probes = probeIn+pi, probeOut+po, probes+1
		}
		in, out = in+pi, out+po
		return chatReply(content, "stop", int(pi), int(po))
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	res, store, log, err := localRun(t, srv, fmt.Sprintf(replayYAML, ""), Config{})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, log)
	}
	runID := strings.Index(log, "workflow run ID:")
	asking := strings.Index(log, "constraint probe: asking local-model")
	found := strings.Index(log, "constraint probe: local-model")
	if runID < 0 || asking < runID || found < asking {
		t.Errorf("log order: run ID at %d, probe sent at %d, probe result at %d; want them in that order\n%s", runID, asking, found, log)
	}
	mu.Lock()
	wantIn, wantOut := in, out
	wantProbe := db.ProbeUsage{TokensIn: probeIn, TokensOut: probeOut, CostUSDx10000: probes * ComputeCostUSDx10000("local-model", 11, 5)}
	if isMetered("local-model") {
		wantProbe.MeteredCalls = probes
	} else {
		wantProbe.UnmeteredCalls = probes
	}
	mu.Unlock()
	if res.InputTokens != wantIn || res.OutputTokens != wantOut {
		t.Errorf("run tokens %d/%d, want %d/%d, every call's usage, the probe's included", res.InputTokens, res.OutputTokens, wantIn, wantOut)
	}
	if calls := int64(res.MeteredCalls + res.UnmeteredCalls); calls != f.chats.Load() {
		t.Errorf("run counts %d calls, the server answered %d", calls, f.chats.Load())
	}
	if got, err := db.ReadProbeUsage(store.ReadDB, res.RunID); err != nil || got != wantProbe {
		t.Errorf("run row's probe usage %+v (%v), want %+v, the probes' own", got, err, wantProbe)
	}

	if tt := readRunCosts(t, store, res.RunID).Totals; tt.TokensIn != wantIn || tt.TokensOut != wantOut || tt.MeteredCalls+tt.UnmeteredCalls != f.chats.Load() {
		t.Errorf("run totals %d/%d tokens over %d calls, want %d/%d over %d, every call the server answered", tt.TokensIn, tt.TokensOut, tt.MeteredCalls+tt.UnmeteredCalls, wantIn, wantOut, f.chats.Load())
	}
}

// A finalize call that gives no answer leaves the tool loop's answer to
// its schema check: it is rejected like any reply that breaks the schema,
// and on_reject repairs it. It does not fail the node.
func TestSchemaReplay_FinalizeCallFails(t *testing.T) {
	prose := "The verdict is support, because the cache is warm."
	s, err := schema.ParseYAML([]byte(`{type: object, required: [key_points, verdict], properties: {key_points: {type: array, items: {type: string}, maxItems: 3}, verdict: {enum: [support, oppose]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	violation := workflow.SummarizeViolations(workflow.CheckOutput(s, workflow.ExtractJSONOutput(prose)))
	for _, c := range []struct {
		name   string
		reply  string // the finalize call's reply
		repair bool
	}{
		{"cut off, repaired", chatReply(replayValid[:len(replayValid)/2], "length", 1, 1), true},
		{"cut off, no repair", chatReply(replayValid[:len(replayValid)/2], "length", 1, 1), false},
		{"empty, repaired", chatReply("", "stop", 1, 1), true},
		{"a tool call, no repair", rawToolCallReply("call-9", "read_file", `{"path":"x"}`), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, srv := newSchemaServer(probeEnforced, func(body map[string]any, _ int) string {
				switch {
				case isRepair(body):
					return chatReply(replayValid, "stop", 1, 1)
				case sendsSchema(body):
					return c.reply
				}
				return chatReply(prose, "stop", 1, 1)
			})
			defer srv.Close()
			extra := ""
			if c.repair {
				extra = replayRepairBlock
			}
			_, store, log, _ := localRun(t, srv, fmt.Sprintf(replayYAML, extra), Config{})
			row := readSchemaNode(t, store, "lens")
			if !strings.Contains(log, "the finalize call gave no answer") {
				t.Errorf("the log does not name the finalize failure:\n%s", log)
			}
			bodies := f.bodies()
			if !c.repair {
				if row.status != "rejected" || !strings.Contains(row.rationale, "output_schema") || !strings.Contains(row.rationale, violation) || len(bodies) != 2 {
					t.Errorf("node %s (%q) after %d calls; want rejected naming %q after the answer and its finalize call\n%s", row.status, row.rationale, len(bodies), violation, log)
				}
				return
			}
			if row.status != "completed" || len(bodies) != 3 {
				t.Fatalf("node %s after %d calls; want completed after the answer, the finalize call and one repair\n%s", row.status, len(bodies), log)
			}
			if got, want := decodeObject(t, row.outputs), decodeObject(t, replayValid); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("outputs %v, want the repair's %v", got, want)
			}
			var asked string
			for _, m := range messagesOf(bodies[2]) {
				if m["role"] == "user" {
					asked, _ = m["content"].(string)
				}
			}
			if !strings.Contains(asked, violation) {
				t.Errorf("the repair was asked %q; want it to name the violation %q", asked, violation)
			}
		})
	}
}

// A finalize call refused as a rate limit returns its error, so the
// rate-limit retry runs the node's call again, as it would for any other
// turn. Any other refused finalize call leaves the answer standing.
func TestSchemaReplay_FinalizeCallRateLimited(t *testing.T) {
	fastBackoff(t)
	prose := "The verdict is support, because the cache is warm."
	s, err := schema.ParseYAML([]byte(`{type: object, required: [key_points, verdict], properties: {key_points: {type: array, items: {type: string}, maxItems: 3}, verdict: {enum: [support, oppose]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		refused []int // the status of each finalize call refused before one answers
		wrapped bool  // run through RateLimitedBackend
	}{
		{"429 then an answer", []int{http.StatusTooManyRequests}, true},
		{"503 twice then an answer", []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable}, true},
		{"429, no retry wrapper", []int{http.StatusTooManyRequests}, false},
		{"500", []int{http.StatusInternalServerError}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ResetRateLimitState()
			var calls, finalizes atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if !sendsSchema(body) {
					_, _ = io.WriteString(w, chatReply(prose, "stop", 1, 1))
					return
				}
				if n := int(finalizes.Add(1)); n <= len(c.refused) {
					http.Error(w, `{"error":"try later"}`, c.refused[n-1])
					return
				}
				_, _ = io.WriteString(w, chatReply(replayValid, "stop", 1, 1))
			}))
			defer srv.Close()
			var b LLMBackend = &OpenAIBackend{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
			if c.wrapped {
				b = NewRateLimitedBackend(b, "openai")
			}
			res, err := b.Run(context.Background(), RunRequest{
				Prompt: "Judge the question.", Model: "local-model",
				Registry: NewToolRegistry(t.TempDir(), nil, NewFSRecorder()), OutputSchema: s, SchemaName: "lens",
			})
			retried := isRateLimitError(&httpStatusError{status: c.refused[0]})
			switch {
			case retried && !c.wrapped:
				if err == nil || !isRateLimitError(err) || !strings.Contains(err.Error(), "the finalize call") {
					t.Fatalf("err = %v; want the finalize call's rate-limit error", err)
				}
				if calls.Load() != 2 {
					t.Errorf("%d calls, want the answer and its finalize call", calls.Load())
				}
			case retried:
				// Each refusal costs one run of the node's call: its answer
				// and its finalize call.
				want := int64(2 * (len(c.refused) + 1))
				if err != nil || res.FinalText != replayValid || !res.SchemaAtDecode || res.FinalizeError != "" {
					t.Fatalf("err %v, answer %q, at decode %v, finalize error %q; want the finalize reply %q", err, res.FinalText, res.SchemaAtDecode, res.FinalizeError, replayValid)
				}
				if calls.Load() != want {
					t.Errorf("%d calls, want %d: the node's call run again after each refusal", calls.Load(), want)
				}
			default:
				if err != nil || res.FinalText != prose || !strings.Contains(res.FinalizeError, fmt.Sprint(c.refused[0])) {
					t.Fatalf("err %v, answer %q, finalize error %q; want the tool loop's answer standing and the status named", err, res.FinalText, res.FinalizeError)
				}
				if calls.Load() != 2 {
					t.Errorf("%d calls, want the answer and its finalize call, not retried", calls.Load())
				}
			}
		})
	}
}

// combRaw is every raw_json the Comb holds for a forager: its head row and
// its revisions.
func combRaw(t *testing.T, store *db.Store, forager string) (head string, revisions []string) {
	t.Helper()
	key := db.ForagerVantageKey(forager)
	_ = store.ReadDB.QueryRow(`SELECT COALESCE(raw_json,'') FROM comb_state WHERE vantage_key=?`, key).Scan(&head)
	rows, err := store.ReadDB.Query(`SELECT COALESCE(raw_json,'') FROM comb_revisions WHERE vantage_key=?`, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r string
		_ = rows.Scan(&r)
		revisions = append(revisions, r)
	}
	return head, revisions
}

// A forager's reply that breaks its schema never reaches the Comb, where a
// cites forager would read it and the ∇ sensor could fire on it; the
// repaired reply does.
func TestSchemaReplay_CombHoldsOnlySchemaValidVerdicts(t *testing.T) {
	bad := `{"key_points":["a"],"verdict":"maybe"}`
	forager := strings.Replace(replayYAML, "  lens:\n", "  forager-x:\n", 1)
	for _, repair := range []bool{true, false} {
		_, srv := newSchemaServer(probeEnforced, func(body map[string]any, _ int) string {
			if isRepair(body) {
				return chatReply(replayValid, "stop", 1, 1)
			}
			return chatReply(bad, "stop", 1, 1)
		})
		extra := ""
		if repair {
			extra = replayRepairBlock
		}
		_, store, log, _ := localRun(t, srv, fmt.Sprintf(forager, extra), Config{})
		srv.Close()
		head, revisions := combRaw(t, store, "x")
		for _, r := range append(revisions, head) {
			if strings.Contains(r, "maybe") {
				t.Errorf("repair %v: the Comb holds the rejected verdict %q", repair, r)
			}
		}
		if repair && head != replayValid {
			t.Errorf("the Comb holds %q, want the repaired reply %q\n%s", head, replayValid, log)
		}
		if !repair && (head != "" || len(revisions) != 0) {
			t.Errorf("no reply held the schema, but the Comb holds %q and %d revisions", head, len(revisions))
		}
		if !strings.Contains(log, "comb forager write (x) skipped") {
			t.Errorf("repair %v: the log does not say the write was skipped\n%s", repair, log)
		}
	}
}

// A fan that runs as one call is one reply: a reply that breaks the schema
// is rejected and goes to on_reject, like an agent node's.
func TestSchemaReplay_OneCallFanIsOneReply(t *testing.T) {
	yaml := "name: fan\nnodes:\n  fan:\n    type: parallel_fan\n    model: local-model\n    prompt_template: \"Look.\"\n    outputs: [finding]\n    output_schema: {type: object, required: [finding], properties: {finding: {type: string}}}\n%s"
	valid := `{"finding":"it is warm"}`
	for _, repair := range []bool{true, false} {
		_, srv := newSchemaServer(probeEnforced, func(body map[string]any, _ int) string {
			if isRepair(body) {
				return chatReply(valid, "stop", 1, 1)
			}
			return chatReply("bash: chb: command not found", "stop", 1, 1)
		})
		extra := ""
		if repair {
			extra = replayRepairBlock
		}
		_, store, log, _ := localRun(t, srv, fmt.Sprintf(yaml, extra), Config{})
		srv.Close()
		row := readSchemaNode(t, store, "fan")
		want := "rejected"
		if repair {
			want = "completed"
		}
		if row.status != want {
			t.Errorf("repair %v: node %s (%s %s), want %s\n%s", repair, row.status, row.errMsg, row.rationale, want, log)
		}
		if !repair && !strings.Contains(row.rationale, "output_schema") {
			t.Errorf("rationale %q does not name output_schema", row.rationale)
		}
		if repair && row.outputs != "" && decodeObject(t, row.outputs)["finding"] != "it is warm" {
			t.Errorf("outputs %s, want the repair's", row.outputs)
		}
	}
}

// What a node records is readable: GetWorkflowNodeStates carries it, and the
// artifact names it per forager and for the synthesizer.
func TestSchemaReplay_EnforcementIsReadable(t *testing.T) {
	yaml := `name: swarm
inputs: [question]
nodes:
  forager-x:
    type: agent
    model: local-model
    prompt: "Judge {question}."
    outputs: [verdict, key_points]
    output_schema:
      type: object
      required: [key_points, verdict]
      properties:
        key_points: {type: array, items: {type: string}}
        verdict: {enum: [support, oppose]}
  queen:
    type: agent
    model: local-model
    prompt: "Sum up."
    outputs: [verdict]
    output_schema: {type: object, required: [verdict], properties: {verdict: {enum: [support, oppose]}}}
edges:
  - {from: forager-x, to: queen}
`
	_, srv := newSchemaServer(probeEnforced, func(body map[string]any, _ int) string {
		for _, m := range messagesOf(body) {
			if c, _ := m["content"].(string); m["role"] == "user" && strings.Contains(c, "Sum up") {
				return chatReply(`{"verdict":"support"}`, "stop", 1, 1)
			}
		}
		return chatReply(replayValid, "stop", 1, 1)
	})
	defer srv.Close()
	res, store, log, err := localRun(t, srv, yaml, Config{Inputs: map[string]any{"question": "q"}})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, log)
	}
	states, err := store.Workflows().GetWorkflowNodeStates(res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]string{}
	for _, s := range states {
		recorded[s.NodeName] = readSchemaNode(t, store, s.NodeName).enforcement
		if s.SchemaEnforcement != recorded[s.NodeName] || s.SchemaEnforcement == "" {
			t.Errorf("%s: GetWorkflowNodeStates says %q, the column holds %q", s.NodeName, s.SchemaEnforcement, recorded[s.NodeName])
		}
	}
	art, err := artifact.BuildArtifact(store, res.RunID, artifact.Determinism{})
	if err != nil {
		t.Fatal(err)
	}
	if art.SchemaEnforcement["x"] != recorded["forager-x"] || art.Synthesizer == nil || art.Synthesizer.SchemaEnforcement != recorded["queen"] {
		t.Errorf("artifact enforcement %v, synthesizer %+v; want the node rows' %v", art.SchemaEnforcement, art.Synthesizer, recorded)
	}
}
