package review

import "testing"

func TestOrFallback(t *testing.T) {
	cases := []struct{ s, fallback, want string }{
		{"", "default", "default"},
		{"   ", "default", "default"},
		{"\t\n", "default", "default"},
		{"actual", "default", "actual"},
		{"  spaced  ", "default", "  spaced  "},
	}
	for _, tc := range cases {
		if got := orFallback(tc.s, tc.fallback); got != tc.want {
			t.Errorf("orFallback(%q, %q) = %q; want %q", tc.s, tc.fallback, got, tc.want)
		}
	}
}

func TestStringSet(t *testing.T) {
	got := stringSet([]string{"A", "  b ", "C", "a"})
	want := map[string]bool{"a": true, "b": true, "c": true}
	if len(got) != len(want) {
		t.Errorf("len = %d; want %d", len(got), len(want))
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing key %q", k)
		}
	}
}

func TestTrimToOneLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"single", "single"},
		{"a  b   c", "a b c"},
		{"\tleading", "leading"},
		{"trailing\n", "trailing"},
		{"line1\nline2", "line1 line2"},
		{"   only   spaces   ", "only spaces"},
	}
	for _, tc := range cases {
		if got := trimToOneLine(tc.in); got != tc.want {
			t.Errorf("trimToOneLine(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatLocation(t *testing.T) {
	cases := []struct {
		f    Finding
		want string
	}{
		{Finding{File: "", Line: 0}, ""},
		{Finding{File: "x.go", Line: 0}, "[x.go](x.go)"},
		{Finding{File: "x.go", Line: 42}, "[x.go:42](x.go#L42)"},
		{Finding{File: "internal/x/y.go", Line: 100}, "[internal/x/y.go:100](internal/x/y.go#L100)"},
	}
	for _, tc := range cases {
		if got := formatLocation(tc.f); got != tc.want {
			t.Errorf("formatLocation(%+v) = %q; want %q", tc.f, got, tc.want)
		}
	}
}

func TestThousands(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1,000"},
		{12345, "12,345"},
		{1234567, "1,234,567"},
		{1234567890, "1,234,567,890"},
	}
	for _, tc := range cases {
		if got := thousands(tc.in); got != tc.want {
			t.Errorf("thousands(%d) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
