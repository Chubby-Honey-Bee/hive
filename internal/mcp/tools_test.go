package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// TestCapabilityToolSpecs_AllValidJSONSchema asserts every spec in the
// list has the three MCP tool fields (name, description, inputSchema)
// and that inputSchema marshals to valid JSON. Catches typos that
// would otherwise only surface when a real MCP host loads the server.
func TestCapabilityToolSpecs_AllValidJSONSchema(t *testing.T) {
	specs := capabilityToolSpecs()
	if len(specs) != 15 {
		t.Errorf("capabilityToolSpecs len = %d; want 15", len(specs))
	}
	seen := make(map[string]bool, len(specs))
	for i, s := range specs {
		m, ok := s.(map[string]any)
		if !ok {
			t.Errorf("spec[%d] is not a map: %T", i, s)
			continue
		}
		name, _ := m["name"].(string)
		desc, _ := m["description"].(string)
		schema, _ := m["inputSchema"].(map[string]any)
		if name == "" {
			t.Errorf("spec[%d] missing name", i)
		}
		if !strings.HasPrefix(name, "chb_") {
			t.Errorf("spec[%d] name %q lacks chb_ prefix", i, name)
		}
		if desc == "" {
			t.Errorf("spec[%d] %s missing description", i, name)
		}
		if schema == nil {
			t.Errorf("spec[%d] %s missing inputSchema", i, name)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("spec[%d] %s inputSchema.type = %v; want 'object'", i, name, schema["type"])
		}
		if _, err := json.Marshal(m); err != nil {
			t.Errorf("spec[%d] %s does not marshal: %v", i, name, err)
		}
		if seen[name] {
			t.Errorf("duplicate tool name: %s", name)
		}
		seen[name] = true
	}

	// Spot-check the well-known tools individually so a future rename
	// trips the test.
	for _, want := range []string{
		"chb_preflight",
		"chb_agent_run",
		"chb_run_totals",
		"chb_node_rationale",
		"chb_run_state",
		"chb_extract_findings",
		"chb_render_review",
		"chb_gen_implement_workflow",
		"chb_self_review",
		"chb_self_implement",
		"chb_swarm",
		"chb_set_budget_mode",
		"chb_mss_repo_audit",
	} {
		if !seen[want] {
			t.Errorf("expected tool %q in capabilityToolSpecs", want)
		}
	}
}

// TestStringArg_TolerantOnMissing asserts the helper returns "" rather
// than panicking when the key is absent or the wrong type.
func TestStringArg_TolerantOnMissing(t *testing.T) {
	m := map[string]any{"a": "ok", "b": 42, "c": nil}
	if stringArg(m, "a") != "ok" {
		t.Error("a should resolve to ok")
	}
	if stringArg(m, "b") != "" {
		t.Error("non-string b should resolve to empty")
	}
	if stringArg(m, "c") != "" {
		t.Error("nil c should resolve to empty")
	}
	if stringArg(m, "missing") != "" {
		t.Error("absent key should resolve to empty")
	}
}

// TestIntArg_HandlesFloatAndIntInputs asserts both float64 (the JSON
// default for numbers) and int values are accepted.
func TestIntArg_HandlesFloatAndIntInputs(t *testing.T) {
	m := map[string]any{
		"asFloat": float64(42),
		"asInt":   7,
		"asStr":   "9",
	}
	if got := intArg(m, "asFloat"); got != 42 {
		t.Errorf("float64 → %d; want 42", got)
	}
	if got := intArg(m, "asInt"); got != 7 {
		t.Errorf("int → %d; want 7", got)
	}
	if got := intArg(m, "asStr"); got != 0 {
		t.Errorf("string → %d; want 0 (no parsing)", got)
	}
	if got := intArgDefault(m, "absent", 99); got != 99 {
		t.Errorf("default fallback → %d; want 99", got)
	}
}

// TestStringSliceArg_FiltersNonStrings asserts only string elements
// survive when the input is a mixed slice. Non-slice inputs return nil.
func TestStringSliceArg_FiltersNonStrings(t *testing.T) {
	m := map[string]any{
		"mixed":    []any{"a", 1, "b", nil, "c"},
		"empty":    []any{},
		"notslice": "string",
	}
	if got := stringSliceArg(m, "mixed"); !equal(got, []string{"a", "b", "c"}) {
		t.Errorf("mixed = %v; want [a b c]", got)
	}
	if got := stringSliceArg(m, "empty"); len(got) != 0 {
		t.Errorf("empty = %v; want []", got)
	}
	if got := stringSliceArg(m, "notslice"); got != nil {
		t.Errorf("notslice = %v; want nil", got)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// One database has one name on the MCP surface, so no pair of descriptions
// reads as two stores, and that name is not "hive database": hive names the
// file, hive.db, and a project's hive is autonomous mode's state in it.
func TestToolDescriptions_NameTheDatabaseOneWay(t *testing.T) {
	phrase := regexp.MustCompile(`(?i)\b(\w+) (?:database|db)\b`)
	names := map[string][]string{}
	var walk func(tool string, v any)
	walk = func(tool string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if s, ok := e.(string); ok && k == "description" {
					for _, m := range phrase.FindAllString(s, -1) {
						names[strings.ToLower(m)] = append(names[strings.ToLower(m)], tool)
					}
					continue
				}
				walk(tool, e)
			}
		case []any:
			for _, e := range x {
				walk(tool, e)
			}
		}
	}
	for _, raw := range allToolSpecs() {
		spec, _ := raw.(map[string]any)
		walk(spec["name"].(string), spec)
	}
	if len(names) > 1 {
		t.Errorf("the tool descriptions give the database %d names: %v", len(names), names)
	}
	for name, tools := range names {
		if strings.HasPrefix(name, "hive ") {
			t.Errorf("%v call the database %q; hive names the file, hive.db, and autonomous mode's state, not the database", tools, name)
		}
	}
}

// callTool sends one tools/call through handleLine and returns the tool
// result's text, its isError flag, and the protocol error if there was one.
func callTool(t *testing.T, s *mcpServer, buf *bytes.Buffer, name string, args map[string]any) (string, bool, *rpcError) {
	t.Helper()
	buf.Reset()
	line, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.handleLine(line)
	s.reqWG.Wait()
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s response: %v\n%s", name, err, buf.String())
	}
	var texts []string
	for _, c := range resp.Result.Content {
		texts = append(texts, c.Text)
	}
	return strings.Join(texts, "\n"), resp.Result.IsError, resp.Error
}

func newTestServer(buf *bytes.Buffer) *mcpServer {
	return &mcpServer{out: json.NewEncoder(buf), inflight: newInflightRegistry()}
}

// callToolJSON sends one tools/call and returns the JSON its text content holds.
func callToolJSON(t *testing.T, s *mcpServer, buf *bytes.Buffer, name string, args map[string]any) map[string]any {
	t.Helper()
	buf.Reset()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	s.handleLine([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":%s}`, params)))
	s.reqWG.Wait()
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil || len(resp.Result.Content) == 0 || resp.Result.IsError {
		t.Fatalf("%s: %v %s", name, err, buf.String())
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resp.Result.Content[0].Text), &out); err != nil {
		t.Fatalf("%s text: %v %s", name, err, resp.Result.Content[0].Text)
	}
	return out
}

// Every tool that takes run_id takes an integer.
func TestToolSpecs_RunIDIsAnIntegerEverywhere(t *testing.T) {
	for _, raw := range allToolSpecs() {
		spec := raw.(map[string]any)
		props, _ := spec["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if p, ok := props["run_id"].(map[string]any); ok && p["type"] != "integer" {
			t.Errorf("%s declares run_id as %v", spec["name"], p["type"])
		}
	}
}
