package foragers

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/Chubby-Honey-Bee/hive/internal/mss"
)

// The Queen wears ♛, in an accent clearly apart from every other forager's,
// the dreamer's and framer's purples included, and from the amber that means
// "assumption" in the MSS palette.
func TestQueen_WearsACrownInHerOwnColour(t *testing.T) {
	all, err := Load(repoForagersDir(t))
	if err != nil {
		t.Fatal(err)
	}
	var queen Forager
	others := map[string]string{}
	for _, f := range all {
		if f.Name == "queen" {
			queen = f
			continue
		}
		others[f.Name] = f.Accent
	}
	if queen.Sigil != "♛" {
		t.Errorf("queen sigil = %q; want ♛", queen.Sigil)
	}
	a := mss.LabelColors[mss.Assumption]
	others["mss assumption fg"], others["mss assumption solid"], others["mss assumption dark"] = a.Fg, a.Solid, a.Dark
	// ΔE*ab (CIE76) of 30 or more reads as a different colour at a glance;
	// ~2 is the just-noticeable difference.
	const minDelta = 30
	for name, accent := range others {
		if d := deltaE(queen.Accent, accent); d < minDelta {
			t.Errorf("queen accent %s is ΔE %.1f from %s %s; want ≥ %d", queen.Accent, d, name, accent, minDelta)
		}
	}
}

// deltaE is the CIE76 distance between two #RRGGBB colours in L*a*b*
// (sRGB, D65 white).
func deltaE(a, b string) float64 {
	la, lb := lab(a), lab(b)
	return math.Sqrt(math.Pow(la[0]-lb[0], 2) + math.Pow(la[1]-lb[1], 2) + math.Pow(la[2]-lb[2], 2))
}

func lab(hex string) [3]float64 {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	lin := func(shift uint) float64 {
		c := float64((v>>shift)&0xFF) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	r, g, b := lin(16), lin(8), lin(0)
	x := (r*0.4124 + g*0.3576 + b*0.1805) / 0.95047
	y := r*0.2126 + g*0.7152 + b*0.0722
	z := (r*0.0193 + g*0.1192 + b*0.9505) / 1.08883
	f := func(t float64) float64 {
		if t > 0.008856 {
			return math.Cbrt(t)
		}
		return 7.787*t + 16.0/116
	}
	return [3]float64{116*f(y) - 16, 500 * (f(x) - f(y)), 200 * (f(y) - f(z))}
}
