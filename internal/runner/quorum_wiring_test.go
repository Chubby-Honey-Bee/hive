package runner

import "testing"

func TestParseResonates(t *testing.T) {
	cases := []struct {
		name string
		defn map[string]any
		want [][2]string
	}{
		{
			name: "two well-formed pairs",
			defn: map[string]any{"resonates": []any{
				[]any{"empiricist", "skeptic"},
				[]any{"architect", "pragmatist"},
			}},
			want: [][2]string{{"empiricist", "skeptic"}, {"architect", "pragmatist"}},
		},
		{
			name: "no resonates key (research workflow)",
			defn: map[string]any{"nodes": map[string]any{}},
			want: nil,
		},
		{
			name: "malformed entries are skipped",
			defn: map[string]any{"resonates": []any{
				[]any{"a", "b"},
				[]any{"only-one"}, // wrong arity → skip
				[]any{"x", 42},    // non-string → skip (empty b)
				"not-a-pair",      // wrong type → skip
				[]any{"c", "d"},
			}},
			want: [][2]string{{"a", "b"}, {"c", "d"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseResonates(tc.defn)
			if len(got) != len(tc.want) {
				t.Fatalf("parseResonates: got %d pairs %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("pair %d: got %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
