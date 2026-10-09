package kv

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func parse(t *testing.T, text string) map[string]string {
	t.Helper()
	m, err := Parse(strings.NewReader(text))
	if err != nil {
		t.Fatalf("Parse(%q): %v", text, err)
	}
	return m
}

func TestHiddenUnquotedValues(t *testing.T) {
	got := parse(t, "name = hive\nport=8080\nempty =\ngreeting = hello world\nurl = http://x/?a=b\n")
	want := map[string]string{"name": "hive", "port": "8080", "empty": "", "greeting": "hello world", "url": "http://x/?a=b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestHiddenQuotedValuesAndEscapes(t *testing.T) {
	got := parse(t, `a = "with # inside"`+"\n"+`b = "tab\there"`+"\n"+`c = "line\nbreak"`+"\n"+`d = "say \"hi\" \\ done"`+"\n"+`e = ""`+"\n")
	want := map[string]string{"a": "with # inside", "b": "tab\there", "c": "line\nbreak", "d": `say "hi" \ done`, "e": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHiddenTrailingComments(t *testing.T) {
	got := parse(t, "a = value # note\nb = \"quoted\" # note\n# whole line\nc = x#y\n")
	want := map[string]string{"a": "value", "b": "quoted", "c": "x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestHiddenDuplicateKeyIsAnError(t *testing.T) {
	_, err := Parse(strings.NewReader("a = 1\nb = 2\na = 3\n"))
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v; want a *ParseError", err)
	}
	if pe.Line != 3 || !strings.Contains(pe.Msg, "duplicate key a") {
		t.Errorf("got line %d msg %q; want line 3, duplicate key a", pe.Line, pe.Msg)
	}
	if err.Error() != "line 3: "+pe.Msg {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestHiddenMalformedLines(t *testing.T) {
	cases := []struct {
		text string
		line int
	}{
		{"a = 1\n[section]\n", 2},
		{"a = \"never closed\n", 1},
		{"\n\n= v\n", 3},
		{"a = \"bad \\q escape\"\n", 1},
		{"a = \"closed\" trailing\n", 1},
		{"just words\n", 1},
	}
	for _, c := range cases {
		_, err := Parse(strings.NewReader(c.text))
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("Parse(%q) error %v; want a *ParseError", c.text, err)
			continue
		}
		if pe.Line != c.line {
			t.Errorf("Parse(%q) line %d, want %d", c.text, pe.Line, c.line)
		}
	}
}

func TestHiddenEmptyInputIsAnEmptyMap(t *testing.T) {
	got := parse(t, "")
	if got == nil || len(got) != 0 {
		t.Errorf("got %#v, want a non-nil empty map", got)
	}
}
