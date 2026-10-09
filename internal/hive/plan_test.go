package hive

import (
	"testing"
	"unicode/utf8"
)

func intVal(i int) *int { return &i }

func TestSignalsFromMaps(t *testing.T) {
	tests := []struct {
		name  string
		input []map[string]any
		want  []Signal
	}{
		{
			name:  "nil input returns nil",
			input: nil,
			want:  nil,
		},
		{
			name:  "empty slice returns nil",
			input: []map[string]any{},
			want:  nil,
		},
		{
			name: "happy path: single stop_signal with coords",
			input: []map[string]any{
				{
					"signal_type": "stop_signal",
					"target_d1":   1,
					"target_d2":   2,
					"target_d3":   3,
					"target_d4":   4,
				},
			},
			want: []Signal{
				{SignalType: "stop_signal", TargetD1: intVal(1), TargetD2: intVal(2), TargetD3: intVal(3), TargetD4: intVal(4)},
			},
		},
		{
			name: "nil coord fields produce nil pointers",
			input: []map[string]any{
				{
					"signal_type": "waggle_dance",
				},
			},
			want: []Signal{
				{SignalType: "waggle_dance", TargetD1: nil, TargetD2: nil, TargetD3: nil, TargetD4: nil},
			},
		},
		{
			name: "missing signal_type key yields empty string",
			input: []map[string]any{
				{"target_d1": 7},
			},
			want: []Signal{
				{SignalType: "", TargetD1: intVal(7)},
			},
		},
		{
			name: "non-string signal_type is formatted via fmt.Sprintf",
			input: []map[string]any{
				{"signal_type": 42},
			},
			want: []Signal{
				{SignalType: "42"},
			},
		},
		{
			name: "multiple entries are all converted",
			input: []map[string]any{
				{"signal_type": "alarm", "target_d1": 1},
				{"signal_type": "tremble_dance", "target_d2": 5},
			},
			want: []Signal{
				{SignalType: "alarm", TargetD1: intVal(1)},
				{SignalType: "tremble_dance", TargetD2: intVal(5)},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := signalsFromMaps(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d, want %d; got %#v", len(got), len(tc.want), got)
			}
			for i, g := range got {
				w := tc.want[i]
				if g.SignalType != w.SignalType {
					t.Errorf("[%d] SignalType=%q, want %q", i, g.SignalType, w.SignalType)
				}
				ptrEq := func(label string, a, b *int) {
					if a == nil && b == nil {
						return
					}
					if (a == nil) != (b == nil) {
						t.Errorf("[%d] %s: got %v, want %v", i, label, a, b)
						return
					}
					if *a != *b {
						t.Errorf("[%d] %s: got %d, want %d", i, label, *a, *b)
					}
				}
				ptrEq("TargetD1", g.TargetD1, w.TargetD1)
				ptrEq("TargetD2", g.TargetD2, w.TargetD2)
				ptrEq("TargetD3", g.TargetD3, w.TargetD3)
				ptrEq("TargetD4", g.TargetD4, w.TargetD4)
			}
		})
	}
}

func TestMapGet(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want string
	}{
		{"key present, string value", map[string]any{"k": "hello"}, "k", "hello"},
		{"key missing returns empty", map[string]any{"other": "x"}, "k", ""},
		{"nil map returns empty", nil, "k", ""},
		{"key present, nil value returns empty", map[string]any{"k": nil}, "k", ""},
		{"key present, int value uses Sprintf", map[string]any{"k": 42}, "k", "42"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapGet(tc.m, tc.key)
			if got != tc.want {
				t.Errorf("mapGet(..., %q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestClip(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"empty string", "", 5, ""},
		{"shorter than limit", "hello", 10, "hello"},
		{"equal to limit", "hello", 5, "hello"},
		{"longer than limit", "hello world", 5, "hello"},
		{"zero limit", "hello", 0, ""},
		{"multi-byte safe (byte count)", "abc", 2, "ab"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := clip(tc.s, tc.n)
			if got != tc.want {
				t.Errorf("clip(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
			}
		})
	}
}

// Gap descriptions are clipped into action prompts at a character boundary,
// so a clipped multi-byte character never leaves invalid UTF-8.
func TestClipKeepsUTF8Valid(t *testing.T) {
	got := clip("ab—cd", 3) // "—" is 3 bytes starting at index 2
	if !utf8.ValidString(got) || got != "ab" {
		t.Errorf("clip = %q, want \"ab\"", got)
	}
}
