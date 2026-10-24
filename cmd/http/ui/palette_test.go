package ui

import (
	"fmt"
	"math"
	"strconv"
	"testing"
)

// oklchHue returns the OKLCH chroma and hue (degrees) of a #RRGGBB colour.
func oklchHue(hex string) (chroma, hue float64) {
	lin := func(i int) float64 {
		v, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	r, g, b := lin(0), lin(1), lin(2)
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	a := 1.9779984951*l - 2.428592205*m + 0.4505937099*s
	bb := 0.0259040371*l + 0.7827717662*m - 0.808675766*s
	return math.Hypot(a, bb), math.Mod(math.Atan2(bb, a)*180/math.Pi+360, 360)
}

func TestOklchHueKnownValues(t *testing.T) {
	if _, h := oklchHue("#BA8C01"); h < 75 || h > 90 {
		t.Errorf("hue of #BA8C01 = %.1f, want the amber range", h)
	}
	if _, h := oklchHue("#4194F9"); h < 250 || h > 262 {
		t.Errorf("hue of #4194F9 = %.1f, want blue", h)
	}
}

// TestChartPaletteHasNoGold enforces the spec's "Gold is dropped entirely":
// no categorical slot may sit in the gold / amber / mustard hue range.
func TestChartPaletteHasNoGold(t *testing.T) {
	tokens := colorTokens(servedStylesheet(t))
	for i := 1; i <= 8; i++ {
		name := fmt.Sprintf("chart-%d", i)
		hex, ok := tokens[name]
		if !ok {
			t.Fatalf("missing --color-%s", name)
		}
		if c, h := oklchHue(hex); c >= 0.05 && h >= 60 && h <= 115 {
			t.Errorf("--color-%s %s has a gold hue (%.0f degrees)", name, hex, h)
		}
	}
}
