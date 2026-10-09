package review

import "testing"

func TestParseRationale_NoJSON(t *testing.T) {
	_, ok := parseRationale("just plain prose, no json here")
	if ok {
		t.Error("expected false for non-JSON text")
	}
}

func TestParseRationale_FinalTextFallbackRejected(t *testing.T) {
	// workflow.ExtractJSONOutput returns {"final_text": s} when no JSON
	// is found — that's a fallback shape, not a lens report.
	_, ok := parseRationale("plain text with no fence")
	if ok {
		t.Error("expected false for fallback final_text shape")
	}
}

func TestParseRationale_MissingLensField(t *testing.T) {
	// JSON without "lens" field is rejected.
	body := "```json\n{\"verdict\":\"clean\",\"findings\":[]}\n```"
	_, ok := parseRationale(body)
	if ok {
		t.Error("expected false when lens field missing")
	}
}

func TestParseRationale_HappyPath(t *testing.T) {
	body := "```json\n" + `{
		"lens":"v1",
		"verdict":"issues",
		"summary":"two findings",
		"findings":[
			{"file":"x.go","line":1,"severity":"high","issue":"bad","fix":"fix it"}
		]
	}` + "\n```"
	report, ok := parseRationale(body)
	if !ok {
		t.Fatal("expected parse success")
	}
	if report.Lens != "v1" {
		t.Errorf("Lens = %q; want v1", report.Lens)
	}
	if len(report.Findings) != 1 {
		t.Errorf("len(findings) = %d; want 1", len(report.Findings))
	}
}

func TestParseRationale_MalformedJSONRejected(t *testing.T) {
	body := "```json\n{not valid json}\n```"
	_, ok := parseRationale(body)
	if ok {
		t.Error("expected false for malformed JSON")
	}
}
