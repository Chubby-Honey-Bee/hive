package mss

// LabelColor is the canonical color set for one MSS truth label.
//
//   - Fg    — readable text on a light background (detail-panel chips,
//     tooltip label spans).
//   - Bg    — the light chip tint behind Fg.
//   - Solid — the saturated swatch used for fills on light surfaces
//     (distribution pips/bars, graph-node fills).
//   - Dark  — readable text/border on a dark background (every page's
//     dark scheme).
//   - DarkBg — the dark chip tint behind Dark.
//
// Fg on Bg and Dark on DarkBg each meet WCAG AA for text (4.5:1).
type LabelColor struct {
	Fg     string
	Bg     string
	Solid  string
	Dark   string
	DarkBg string
}

// LabelColors is the single source of truth for MSS truth-label colors:
// the Mermaid export of `chb export-graph` reads them. Never hardcode a
// label hex elsewhere.
var LabelColors = map[Label]LabelColor{
	Definition: {Fg: "#1C7268", Bg: "#E8F5F0", Solid: "#2A9D8F", Dark: "#3FB950", DarkBg: "#0D3D2B"},
	Assumption: {Fg: "#825A00", Bg: "#FFF3D6", Solid: "#E9C46A", Dark: "#E3B341", DarkBg: "#3D2D0D"},
	Guarantee:  {Fg: "#34617F", Bg: "#E0EFF8", Solid: "#457B9D", Dark: "#58A6FF", DarkBg: "#0D2A3D"},
	Unknown:    {Fg: "#B21F2D", Bg: "#FDE8E8", Solid: "#E63946", Dark: "#F85149", DarkBg: "#3D0D0D"},
}

// OrderedLabels is the canonical display order for the four labels —
// definition, guarantee, assumption, unknown — so generated CSS/JS emits
// deterministically (important for byte-stable artifacts).
var OrderedLabels = []Label{Definition, Guarantee, Assumption, Unknown}
