package foragers

import "encoding/json"

// paletteEntry is one forager's externalized theme — what `palette.json`
// surfaces to non-Go consumers.
type paletteEntry struct {
	Name      string `json:"name"`
	Title     string `json:"title"`
	Sigil     string `json:"sigil"`
	Accent    string `json:"accent"`
	Default   bool   `json:"default"`
	Archetype string `json:"archetype"`
}

// palette returns one entry per forager, in the order given (Load sorts
// by name, so the output is deterministic).
func palette(all []Forager) []paletteEntry {
	out := make([]paletteEntry, 0, len(all))
	for _, w := range all {
		out = append(out, paletteEntry{
			Name:      w.Name,
			Title:     w.Title,
			Sigil:     w.Sigil,
			Accent:    w.Accent,
			Default:   w.Default,
			Archetype: w.Archetype,
		})
	}
	return out
}

// PaletteJSON returns the active forager palette as pretty-printed
// JSON, suitable for writing to foragers/palette.json.
func PaletteJSON(all []Forager) ([]byte, error) {
	return json.MarshalIndent(palette(all), "", "  ")
}
