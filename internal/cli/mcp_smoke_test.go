package cli

import (
	"encoding/json"
	"testing"
)

// The smoke harness's parser keeps each response by its id and reports every
// other stdout line as stray: a diagnostic on the protocol channel, a
// response with a null id and one with neither a result nor an error, so
// none of them goes green.
func TestParseRPCFrames(t *testing.T) {
	out := `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}
{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"nope"}}

chb-mcp: a diagnostic on stdout
{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse"}}
{"jsonrpc":"2.0","id":3}
`
	results, stray := parseRPCFrames(out)
	if string(results[1]) != `{"ok":true}` {
		t.Errorf("id 1 result = %s", results[1])
	}
	if _, ok := results[2]; !ok {
		t.Error("id 2 error object not recorded")
	}
	if len(results) != 2 {
		t.Errorf("results = %v, want ids 1 and 2 only", results)
	}
	if len(stray) != 3 {
		t.Errorf("stray = %q, want the diagnostic, the null-id frame and the empty response", stray)
	}
}

// A check that looks only at text passes a tool that failed with a message
// containing the text. textContains requires isError:false as well.
func TestTextContainsRequiresSuccess(t *testing.T) {
	check := textContains(`"pass": true`)
	ok := json.RawMessage(`{"isError":false,"content":[{"type":"text","text":"{\"pass\": true}"}]}`)
	failedTool := json.RawMessage(`{"isError":true,"content":[{"type":"text","text":"{\"pass\": true}"}]}`)
	failedPreflight := json.RawMessage(`{"isError":false,"content":[{"type":"text","text":"{\"pass\": false}"}]}`)
	if !check(ok) {
		t.Error("a successful result containing the text failed the check")
	}
	if check(failedTool) {
		t.Error("isError:true passed the check")
	}
	if check(failedPreflight) {
		t.Error(`"pass": false passed a check for "pass": true`)
	}
	if !isErrorTrue(failedTool) || isErrorTrue(ok) {
		t.Error("isErrorTrue misread isError")
	}
}
