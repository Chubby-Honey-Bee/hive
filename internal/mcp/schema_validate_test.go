package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// An array's elements are checked as well as the array, so severity [1] is
// refused rather than read as the default filter.
func TestValidateToolArgs_ChecksArrayItems(t *testing.T) {
	spec := specByToolName("chb_gen_implement_workflow")
	base := map[string]any{"findings_json": "f.json", "out": "o.yaml"}
	with := func(sev []any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		m["severity"] = sev
		return m
	}
	if err := validateToolArgs(spec, with([]any{"high", "critical"})); err != nil {
		t.Errorf("string elements refused: %v", err)
	}
	for _, bad := range [][]any{{1.0}, {"high", true}, {nil}, {[]any{"high"}}} {
		err := validateToolArgs(spec, with(bad))
		if err == nil {
			t.Errorf("severity %v was accepted", bad)
			continue
		}
		idx := 0
		for i, e := range bad {
			if _, ok := e.(string); !ok {
				idx = i
				break
			}
		}
		if want := fmt.Sprintf("severity[%d] must be string", idx); !strings.Contains(err.Error(), want) {
			t.Errorf("severity %v: error %q does not name %q", bad, err, want)
		}
	}
}
