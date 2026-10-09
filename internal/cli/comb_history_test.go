package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// `chb comb history` reaches the revision list, and the comb's help calls
// its time axis the Time Wheel, never "chronomantic".
func TestCombHistoryAndTheCombHelp(t *testing.T) {
	comb := newCombCmd()
	got, _, err := comb.Find([]string{"history"})
	if err != nil {
		t.Fatalf("comb history: %v", err)
	}
	if got.Name() != "history" {
		t.Errorf("comb history resolves to %q, want history", got.Name())
	}
	for _, sub := range append(comb.Commands(), comb) {
		for _, help := range []string{sub.Short, sub.Long} {
			if strings.Contains(help, "chronomantic") {
				t.Errorf("comb %s help says chronomantic: %q", sub.Name(), help)
			}
		}
	}
}

// The synthesis heads the global region "Whole comb": the comb is the hive's
// memory, not its voice, and the global region is all of it.
func TestCombSynthesisHeadsTheGlobalRegionWholeComb(t *testing.T) {
	narrative := "every finding, one digest"
	md := renderSynthesisMarkdown([]*db.CombRow{
		{VantageKey: "", VantageKind: db.VantageRegion, Narrative: narrative},
	}, nil, 0)
	if want := "## Whole comb\n\n" + narrative + "\n"; !strings.Contains(md, want) {
		t.Errorf("synthesis lacks %q:\n%s", want, md)
	}
	if strings.Contains(strings.ToLower(md), "voice") {
		t.Errorf("synthesis calls the comb a voice:\n%s", md)
	}
}

// Every cut chb makes in text it prints is by byte length on a rune
// boundary: a cut that falls inside a multi-byte character, such as the em
// dashes model output is full of, leaves valid UTF-8 of at most the cut's
// length.
func TestCutsFallOnARuneBoundary(t *testing.T) {
	// at(n) holds an em dash across byte n, where a cut to n bytes falls.
	at := func(n int) string { return strings.Repeat("a", n-1) + "—" + strings.Repeat("b", 500) }
	logFile, err := os.Create(filepath.Join(t.TempDir(), "validate.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	var validate, replay bytes.Buffer
	(&validateRunner{logFile: logFile, stderr: &validate}).failN("step", at(400))
	(&behaviorReplayer{verbose: true, stderr: &replay}).exitMatches(fixture{Name: "f"}, 1, at(400))
	for _, c := range []struct {
		name, got string
		max       int
	}{
		{"check-agents reason", failedWaveAgentReason(map[string]any{"summary": at(80)}), 80},
		{"mermaid label", mermaidLabel(at(70)), 70},
		{"mcp-smoke snippet", smokeSnippet(json.RawMessage(at(300))), 300 + len("…")},
		{"db-repair statement", repairStmtSnippet(at(60)), 60 + len("…")},
		{"ask and replicate question", truncateForLog(at(60), 60), 60 + len("…")},
		{"validate's failure output", validate.String(), len("  ✗ step\n    out: ") + 400 + 1},
		{"replay-behavior's output head", replay.String(), len("  ✗ f — exit 1 ≠ 0\n") + 400 + 1},
	} {
		if !utf8.ValidString(c.got) || len(c.got) > c.max {
			t.Errorf("%s: %q, want valid UTF-8 of at most %d bytes", c.name, c.got, c.max)
		}
	}
}
