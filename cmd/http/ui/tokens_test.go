package ui

import (
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var colorTokenPattern = regexp.MustCompile(`--color-([a-z0-9-]+):\s*(#[0-9A-Fa-f]{6})\s*;`)

func servedStylesheet(t *testing.T) string {
	t.Helper()
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)
	return serveMuxGet(t, mux, "/ui/assets/styles.css").Body.String()
}

func colorTokens(css string) map[string]string {
	tokens := map[string]string{}
	for _, m := range colorTokenPattern.FindAllStringSubmatch(css, -1) {
		tokens[m[1]] = m[2]
	}
	return tokens
}

func relativeLuminance(hex string) float64 {
	channel := func(s string) float64 {
		v, _ := strconv.ParseUint(s, 16, 8)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	h := strings.TrimPrefix(hex, "#")
	return 0.2126*channel(h[0:2]) + 0.7152*channel(h[2:4]) + 0.0722*channel(h[4:6])
}

func contrastRatio(a, b string) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestContrastRatioKnownValues pins the contrast helper to known WCAG values
// so the token check below cannot pass by a broken formula.
func TestContrastRatioKnownValues(t *testing.T) {
	if r := contrastRatio("#FFFFFF", "#000000"); math.Abs(r-21) > 0.01 {
		t.Errorf("white on black got %.2f, want 21", r)
	}
	if r := contrastRatio("#777777", "#FFFFFF"); math.Abs(r-4.48) > 0.01 {
		t.Errorf("#777 on white got %.2f, want 4.48", r)
	}
}

// TestTextTokensMeetWCAGAA verifies every colour token used for text reaches
// WCAG 2.2 AA (4.5:1) on each surface it can sit on, including Night text on
// an Aether fill (skip link, primary button). Aether also passes the 3:1
// non-text minimum for the focus ring as a consequence.
func TestTextTokensMeetWCAGAA(t *testing.T) {
	tokens := colorTokens(servedStylesheet(t))
	surfaces := []string{"night", "surface", "raised"}
	text := []string{
		"ink", "ink-muted", "aether", "crystal", "active", "warning", "negative",
		"role-tank", "role-healer", "role-melee", "role-phys-ranged", "role-magic-ranged", "role-crafter", "role-gatherer",
	}
	for _, s := range surfaces {
		if tokens[s] == "" {
			t.Fatalf("surface token --color-%s not defined", s)
		}
		for _, fg := range text {
			if tokens[fg] == "" {
				t.Fatalf("text token --color-%s not defined", fg)
			}
			if r := contrastRatio(tokens[fg], tokens[s]); r < 4.5 {
				t.Errorf("--color-%s on --color-%s contrast %.2f, want >= 4.5", fg, s, r)
			}
		}
	}
	if r := contrastRatio(tokens["night"], tokens["aether"]); r < 4.5 {
		t.Errorf("--color-night on --color-aether contrast %.2f, want >= 4.5", r)
	}
}

// TestStylesheetFocusMotionAndFigures verifies the stylesheet draws a visible
// focus ring, honours the reduced-motion preference and sets tabular figures.
func TestStylesheetFocusMotionAndFigures(t *testing.T) {
	css := servedStylesheet(t)
	if !strings.Contains(css, ":focus-visible") {
		t.Error("stylesheet must style :focus-visible")
	}
	if !strings.Contains(css, "@media (prefers-reduced-motion: reduce)") {
		t.Error("stylesheet must respect prefers-reduced-motion")
	}
	if !strings.Contains(css, "tabular-nums") {
		t.Error("stylesheet must enable tabular figures")
	}
}
