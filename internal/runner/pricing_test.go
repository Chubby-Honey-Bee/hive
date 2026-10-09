package runner

import "testing"

func TestResolveAliasForPricing(t *testing.T) {
	cases := []struct{ in, want string }{
		{"haiku", "claude-haiku-4-5"},
		{"  haiku  ", "claude-haiku-4-5"},
		{"HAIKU", "claude-haiku-4-5"},
		{"sonnet", "claude-sonnet-4-6"},
		{"Sonnet", "claude-sonnet-4-6"},
		{"opus", "claude-opus-4-8"},
		{"OPUS", "claude-opus-4-8"},
		// pass-through for non-aliases
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"gpt-5.1", "gpt-5.1"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := resolveAliasForPricing(tc.in); got != tc.want {
			t.Errorf("resolveAliasForPricing(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
