package mcp

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// handleLine's JSON-RPC and lifecycle behaviour, as the declared revisions
// require it, malformed requests included, which the smoke harness does not
// send.
func TestProtocol_HandleLine(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string // substrings the single response line must contain
		silent   bool     // no response at all
	}{
		{"initialize echoes a supported version",
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
			[]string{`"protocolVersion":"2024-11-05"`, `"id":1`}, false},
		{"initialize answers newest supported for an unknown version",
			`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`,
			[]string{`"protocolVersion":"2025-06-18"`}, false},
		{"initialize without params answers newest supported",
			`{"jsonrpc":"2.0","id":3,"method":"initialize"}`,
			[]string{`"protocolVersion":"2025-06-18"`}, false},
		// name is the command a host launches; title is the brand it shows
		// (docs/naming.md: chb-mcp is the command layer, HIVE the brand).
		{"initialize names the command and the brand",
			`{"jsonrpc":"2.0","id":10,"method":"initialize"}`,
			[]string{`"name":"chb-mcp"`, `"title":"HIVE"`}, false},
		{"ping returns an empty result",
			`{"jsonrpc":"2.0","id":4,"method":"ping"}`, []string{`"result":{}`, `"id":4`}, false},
		{"unknown method is -32601",
			`{"jsonrpc":"2.0","id":5,"method":"nope"}`, []string{`-32601`, `"id":5`}, false},
		{"id 0 is echoed", `{"jsonrpc":"2.0","id":0,"method":"ping"}`, []string{`"id":0`}, false},
		{"string id is echoed", `{"jsonrpc":"2.0","id":"abc","method":"ping"}`, []string{`"id":"abc"`}, false},
		{"notification gets no response",
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil, true},
		{"id-less request is not answered",
			`{"jsonrpc":"2.0","method":"tools/list"}`, nil, true},
		{"null id is an invalid request",
			`{"jsonrpc":"2.0","id":null,"method":"ping"}`, []string{`-32600`, `"id":null`}, false},
		{"malformed line is -32700 with null id",
			`{not json`, []string{`-32700`, `"id":null`}, false},
		// A frame that does not declare JSON-RPC 2.0 is not a request this
		// server can answer.
		{"wrong jsonrpc version is -32600",
			`{"jsonrpc":"1.0","id":7,"method":"tools/list"}`, []string{`-32600`, `"id":7`}, false},
		{"missing jsonrpc member is -32600",
			`{"id":8,"method":"tools/list"}`, []string{`-32600`, `"id":8`}, false},
		{"missing method is -32600, not method-not-found",
			`{"jsonrpc":"2.0","id":9}`, []string{`-32600`, `"id":9`}, false},
		// JSON-RPC 2.0 allows a string or a number for an id and nothing
		// else. A composite id was answered anyway, and echoed back.
		{"array id is -32600 with a null id",
			`{"jsonrpc":"2.0","id":[1,2],"method":"tools/list"}`, []string{`-32600`, `"id":null`}, false},
		{"object id is -32600 with a null id",
			`{"jsonrpc":"2.0","id":{"a":1},"method":"tools/list"}`, []string{`-32600`, `"id":null`}, false},
		{"boolean id is -32600 with a null id",
			`{"jsonrpc":"2.0","id":true,"method":"ping"}`, []string{`-32600`, `"id":null`}, false},
		{"unknown tool is a -32602 protocol error, not a tool result",
			`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"chb_nope","arguments":{}}}`,
			[]string{`-32602`, `"id":6`}, false},
		// A numeric id must be an integer. 1.5 and 1e3 were dispatched and
		// echoed because only the first byte was looked at.
		{"fractional id is -32600 with a null id",
			`{"jsonrpc":"2.0","id":1.5,"method":"ping"}`, []string{`-32600`, `"id":null`}, false},
		{"exponent id is -32600 with a null id",
			`{"jsonrpc":"2.0","id":1e3,"method":"ping"}`, []string{`-32600`, `"id":null`}, false},
		{"negative integer id is echoed",
			`{"jsonrpc":"2.0","id":-3,"method":"ping"}`, []string{`"id":-3`, `"result":{}`}, false},
		// Valid JSON that is not a request object is an invalid request, not
		// a parse error.
		{"a bare number is -32600", `42`, []string{`-32600`, `"id":null`}, false},
		{"a bare string is -32600", `"str"`, []string{`-32600`, `"id":null`}, false},
		{"null is -32600", `null`, []string{`-32600`, `"id":null`}, false},
		{"a batch array is -32600",
			`[{"jsonrpc":"2.0","id":8,"method":"ping"}]`, []string{`-32600`, `"id":null`}, false},
		{"a member of the wrong type is -32600",
			`{"jsonrpc":"2.0","id":1,"method":5}`, []string{`-32600`, `"id":null`}, false},
		// Schema refusals are protocol errors, before the handler runs.
		{"a fractional integer argument is -32602",
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"chb_findings","arguments":{"limit":2.5}}}`,
			[]string{`-32602`, `"id":2`, `whole number`}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			s := &mcpServer{out: json.NewEncoder(&buf), inflight: newInflightRegistry()}
			s.handleLine([]byte(c.in))
			// handleLine dispatches a request on its own goroutine — that is
			// what makes notifications/cancelled reachable while a request
			// runs — so the response lands after it returns. Notifications
			// are still handled inline and add nothing to wait for.
			s.reqWG.Wait()
			out := buf.String()
			if c.silent {
				if out != "" {
					t.Fatalf("expected no response, got %s", out)
				}
				return
			}
			if strings.Count(out, "\n") != 1 {
				t.Fatalf("expected exactly one response line, got %q", out)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("response missing %s\n%s", w, out)
				}
			}
			if strings.Contains(out, `"result"`) && strings.Contains(out, `"error"`) {
				t.Errorf("response sets both result and error: %s", out)
			}
		})
	}
}

// dispatch runs on its own goroutine, and an unrecovered panic in a
// goroutine ends the process, and with it the MCP server and every request
// in flight, so a panicking tool call must fail that request with -32603 and
// nothing else.
//
// A Store with no database behind it makes chb_findings dereference a nil
// *sql.DB — a genuine panic inside a tool handler, not a guarded error.
func TestProtocol_APanickingToolFailsOnlyItsRequest(t *testing.T) {
	var buf bytes.Buffer
	s := &mcpServer{out: json.NewEncoder(&buf), inflight: newInflightRegistry(), store: &db.Store{}}
	s.handleLine([]byte(`{"jsonrpc":"2.0","id":41,"method":"tools/call","params":{"name":"chb_findings","arguments":{}}}`))
	s.reqWG.Wait()
	out := buf.String()
	if !strings.Contains(out, `"id":41`) || !strings.Contains(out, "-32603") {
		t.Fatalf("want a -32603 error for id 41; got %q", out)
	}
	buf.Reset()
	s.handleLine([]byte(`{"jsonrpc":"2.0","id":42,"method":"ping"}`))
	s.reqWG.Wait()
	if !strings.Contains(buf.String(), `"id":42`) {
		t.Errorf("the server stopped answering after a panicking tool call: %q", buf.String())
	}
}

// A request that arrives once the shutdown has begun is answered with an
// error and dispatches nothing: the shutdown waits on the requests it has,
// and a request it does not know of would run against a store it closes.
func TestProtocol_ARequestAfterShutdownBeginsIsRefused(t *testing.T) {
	var buf bytes.Buffer
	s := newTestServer(&buf)
	s.shutdown()
	s.handleLine([]byte(`{"jsonrpc":"2.0","id":43,"method":"tools/call","params":{"name":"chb_summary","arguments":{}}}`))
	s.reqWG.Wait()
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, `"id":43`) || !strings.Contains(out, "-32000") {
		t.Fatalf("want one -32000 error for id 43; got %q", out)
	}
	if strings.Contains(out, "HIVE_DB_PATH") {
		t.Errorf("the tool's handler ran after the shutdown began: %q", out)
	}
}

// tools/list is the contract a host programs against, so this pins the exact
// set, and requires every tool to carry a title and annotations whose hints
// are booleans — an absent hint means destructive and open-world.
func TestProtocol_ToolsListContract(t *testing.T) {
	want := []string{
		"chb_agent_run", "chb_calibration_read", "chb_db_write", "chb_extract_findings", "chb_findings",
		"chb_gen_implement_workflow", "chb_mss_repo_audit",
		"chb_node_rationale", "chb_outcome_record", "chb_preflight", "chb_render_review", "chb_research",
		"chb_run_state", "chb_run_totals", "chb_self_implement", "chb_self_review",
		"chb_set_budget_mode", "chb_status", "chb_summary", "chb_swarm",
	}
	var buf bytes.Buffer
	s := &mcpServer{out: json.NewEncoder(&buf), inflight: newInflightRegistry()}
	s.handleLine([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	s.reqWG.Wait()
	var resp struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Title       string          `json:"title"`
				Annotations map[string]any  `json:"annotations"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("decode tools/list: %v\n%s", err, buf.String())
	}
	var got []string
	for _, tool := range resp.Result.Tools {
		got = append(got, tool.Name)
		if tool.Title == "" {
			t.Errorf("%s has no title", tool.Name)
		}
		if len(tool.InputSchema) == 0 {
			t.Errorf("%s has no inputSchema", tool.Name)
		}
		ro, ok := tool.Annotations["readOnlyHint"].(bool)
		if !ok {
			t.Errorf("%s: readOnlyHint missing or not a boolean", tool.Name)
		}
		hints := []string{"openWorldHint"}
		if !ro {
			hints = append(hints, "destructiveHint", "idempotentHint")
		}
		for _, h := range hints {
			if _, ok := tool.Annotations[h].(bool); !ok {
				t.Errorf("%s: %s missing or not a boolean", tool.Name, h)
			}
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("tools/list names changed\n got: %v\nwant: %v", got, want)
	}
}
