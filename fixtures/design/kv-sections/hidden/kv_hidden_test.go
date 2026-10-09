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

func TestHiddenFlatKeys(t *testing.T) {
	got := parse(t, "name = hive\nport=8080\nempty =\nurl = http://x/?a=b\n")
	want := map[string]string{"name": "hive", "port": "8080", "empty": "", "url": "http://x/?a=b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestHiddenSectionsPrefixKeys(t *testing.T) {
	got := parse(t, "top = 1\n[ server ]\nhost = a\n[client]\nhost = b\n")
	want := map[string]string{"top": "1", "server.host": "a", "client.host": "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestHiddenCommentsAndBlanks(t *testing.T) {
	got := parse(t, "# comment\n\n   ; another\nk = v\n  # indented comment\n")
	if !reflect.DeepEqual(got, map[string]string{"k": "v"}) {
		t.Errorf("got %v", got)
	}
}

func TestHiddenLastValueWins(t *testing.T) {
	got := parse(t, "k = 1\nk = 2\n[s]\nk = 3\nk = 4\n")
	want := map[string]string{"k": "2", "s.k": "4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestHiddenMalformedLineNumber(t *testing.T) {
	cases := []struct {
		text string
		line int
	}{
		{"a = 1\n\nnot a pair\n", 3},
		{"[]\n", 1},
		{"# c\n= value\n", 2},
		{"[open\n", 1},
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
		if !strings.HasPrefix(err.Error(), "line "+itoa(c.line)+": ") {
			t.Errorf("Parse(%q) Error() = %q; want it to start with line N:", c.text, err.Error())
		}
	}
}

func TestHiddenEmptyInputIsAnEmptyMap(t *testing.T) {
	got := parse(t, "")
	if got == nil || len(got) != 0 {
		t.Errorf("got %#v, want a non-nil empty map", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
