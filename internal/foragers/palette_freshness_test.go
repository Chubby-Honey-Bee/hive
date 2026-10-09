package foragers

import (
	"os"
	"path/filepath"
	"testing"
)

// foragers/palette.json is generated, checked in, and documented in
// foragers/README.md as the canonical externalised theme that non-Go
// consumers read. No Go code reads it, so this test is what notices when it
// stops matching the personas it is generated from.
func TestPaletteJSONMatchesTheForagerPersonas(t *testing.T) {
	root := filepath.Join("..", "..", "foragers")
	all, err := Load(root)
	if err != nil {
		t.Fatalf("load foragers: %v", err)
	}
	want, err := PaletteJSON(all)
	if err != nil {
		t.Fatalf("render palette: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "palette.json"))
	if err != nil {
		t.Fatalf("read palette.json: %v", err)
	}
	if string(got) != string(want)+"\n" && string(got) != string(want) {
		t.Errorf("foragers/palette.json is stale — regenerate it:\n"+
			"    chb palette --out foragers/palette.json\n"+
			"have %d bytes, want %d", len(got), len(want))
	}
}
