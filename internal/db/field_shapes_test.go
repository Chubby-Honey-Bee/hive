package db

import "testing"

// The normalisers every write surface uses keep a JSON-array source_urls,
// which agents/researcher.md documents and the citation subsystem depends
// on, and store a depends_on_ids that arrives as a JSON string as the list
// it holds, not double-encoded.
func TestNormalizeSourceURLs(t *testing.T) {
	cases := []struct {
		in   any
		want string // "" means nil
		bad  bool
	}{
		{nil, "", false},
		{"", "", false},
		{"   ", "", false},
		{"http://a, http://b", "http://a, http://b", false},
		{[]any{"http://a", "http://b"}, `["http://a","http://b"]`, false},
		{[]any{}, "", false},
		{[]any{"http://a", ""}, `["http://a"]`, false},
		{[]any{1.0}, "", true},
		{map[string]any{"a": 1}, "", true},
	}
	for _, c := range cases {
		got, err := NormalizeSourceURLs(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("NormalizeSourceURLs(%#v) accepted it; want an error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeSourceURLs(%#v): %v", c.in, err)
			continue
		}
		if c.want == "" {
			if got != nil {
				t.Errorf("NormalizeSourceURLs(%#v) = %q; want nil", c.in, *got)
			}
			continue
		}
		if got == nil || *got != c.want {
			t.Errorf("NormalizeSourceURLs(%#v) = %v; want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeDependsOnIDs(t *testing.T) {
	cases := []struct {
		in   any
		want string
		bad  bool
	}{
		{nil, "", false},
		{"", "", false},
		{"null", "", false},
		{"[]", "", false},
		{"[1,2]", "[1,2]", false},
		{" [3] ", "[3]", false},
		{[]any{1.0, 2.0}, "[1,2]", false},
		{[]any{}, "", false},
		{"not json", "", true},
		{[]any{"seven"}, "", true},
		{42.0, "", true},
	}
	for _, c := range cases {
		got, err := NormalizeDependsOnIDs(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("NormalizeDependsOnIDs(%#v) accepted it; want an error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeDependsOnIDs(%#v): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeDependsOnIDs(%#v) = %q; want %q", c.in, got, c.want)
		}
	}
}
