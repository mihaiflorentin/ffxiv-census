package ui

import (
	"bytes"
	"html/template"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// fontFaces returns each @font-face block of the stylesheet keyed by family.
func fontFaces(css string) map[string]string {
	faces := map[string]string{}
	for _, m := range regexp.MustCompile(`@font-face\s*\{([^}]*)\}`).FindAllStringSubmatch(css, -1) {
		if fam := regexp.MustCompile(`font-family:\s*"([^"]+)"`).FindStringSubmatch(m[1]); fam != nil {
			faces[fam[1]] = m[1]
		}
	}
	return faces
}

// TestSairaFiguresFaceIsVendored verifies the figures typeface: Saira is
// declared as a variable face with its width axis, preloaded by the shell,
// served from the embedded assets as WOFF2, and shipped with its OFL licence.
func TestSairaFiguresFaceIsVendored(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	face, ok := fontFaces(servedStylesheet(t))["Saira"]
	if !ok {
		t.Fatal("stylesheet must declare a Saira @font-face")
	}
	if !regexp.MustCompile(`font-stretch:\s*\d+(\.\d+)?%\s+100%`).MatchString(face) {
		t.Errorf("Saira face must declare its width axis as a font-stretch range, got %q", face)
	}
	urls := cssURLs(face)
	if len(urls) != 1 || !strings.HasSuffix(urls[0], ".woff2") {
		t.Fatalf("Saira face must load one WOFF2 file, got %v", urls)
	}
	fontPath := resolveAssetURL(urls[0])

	doc := parseHTML(t, serveMuxGet(t, mux, "/").Body.String())
	preloaded := findAll(doc, func(n *html.Node) bool {
		href, _ := attr(n, "href")
		rel, _ := attr(n, "rel")
		return n.DataAtom == atom.Link && rel == "preload" && href == fontPath
	})
	if len(preloaded) != 1 {
		t.Errorf("shell must preload %s once, got %d", fontPath, len(preloaded))
	}

	if ct := serveMuxGet(t, mux, fontPath).Header().Get("Content-Type"); ct != "font/woff2" {
		t.Errorf("GET %s Content-Type %q, want font/woff2", fontPath, ct)
	}

	licence := serveMuxGet(t, mux, "/ui/assets/fonts/OFL-Saira.txt")
	if licence.Code != http.StatusOK {
		t.Fatalf("Saira licence: status %d", licence.Code)
	}
	body := licence.Body.String()
	if !strings.Contains(body, "Saira Project Authors") || !strings.Contains(body, "SIL OPEN FONT LICENSE") {
		t.Error("OFL-Saira.txt must carry the Saira copyright line and the SIL Open Font License")
	}
}

// oklabLightness returns the OKLab L (0-1) of a #RRGGBB colour.
func oklabLightness(hex string) float64 {
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
	return 0.2104542553*l + 0.793617785*m - 0.0040720468*s
}

func TestOklabLightnessKnownValues(t *testing.T) {
	if l := oklabLightness("#FFFFFF"); math.Abs(l-1) > 0.001 {
		t.Errorf("white L = %.3f, want 1", l)
	}
	if l := oklabLightness("#808080"); math.Abs(l-0.5999) > 0.002 {
		t.Errorf("#808080 L = %.3f, want 0.600", l)
	}
}

// inHue reports whether h (degrees) lies in [from, to], wrapping past 360.
func inHue(h, from, to float64) bool {
	if from <= to {
		return h >= from && h <= to
	}
	return h >= from || h <= to
}

// TestSemanticPalettes covers the role and Grand Company mark palettes: each
// colour sits in the hue its meaning names, reaches the 3:1 non-text minimum
// on Surface and Raised (the chart surfaces), and the three roles differ in
// lightness as well as hue so they survive greyscale and colour-vision loss.
func TestSemanticPalettes(t *testing.T) {
	tokens := colorTokens(servedStylesheet(t))
	cases := []struct {
		token     string
		hueFrom   float64
		hueTo     float64
		maxChroma float64
	}{
		{"mark-tank", 235, 275, 1},
		{"mark-healer", 135, 180, 1},
		{"mark-dps", 5, 40, 1},
		{"gc-maelstrom", 0, 30, 1},
		{"gc-twin-adder", 55, 95, 1},
		{"gc-immortal-flames", 220, 280, 0.12},
	}
	for _, tc := range cases {
		hex, ok := tokens[tc.token]
		if !ok {
			t.Errorf("missing --color-%s", tc.token)
			continue
		}
		c, h := oklchHue(hex)
		if !inHue(h, tc.hueFrom, tc.hueTo) {
			t.Errorf("--color-%s %s hue %.0f, want %.0f-%.0f", tc.token, hex, h, tc.hueFrom, tc.hueTo)
		}
		if c > tc.maxChroma {
			t.Errorf("--color-%s %s chroma %.3f, want a muted <= %.2f", tc.token, hex, c, tc.maxChroma)
		}
		for _, s := range []string{"surface", "raised"} {
			if r := contrastRatio(hex, tokens[s]); r < 3 {
				t.Errorf("--color-%s on --color-%s contrast %.2f, want >= 3", tc.token, s, r)
			}
		}
	}
	if t.Failed() {
		return
	}
	// Roles need lightness separation as well as hue.
	roles := []string{"mark-tank", "mark-healer", "mark-dps"}
	for i := range roles {
		for j := i + 1; j < len(roles); j++ {
			a, b := oklabLightness(tokens[roles[i]]), oklabLightness(tokens[roles[j]])
			if math.Abs(a-b) < 0.02 {
				t.Errorf("--color-%s and --color-%s lightness %.3f vs %.3f, want them apart", roles[i], roles[j], a, b)
			}
		}
	}
}

// TestPrimaryNavIsOneScrollableBar verifies the phone navigation is the same
// bar as on desktop, scrolling sideways rather than hiding behind a menu
// toggle: every destination is a plain link with no form-control disclosure
// and no inline handlers. A deferred site script scrolls the current item
// into view; without it the bar still scrolls by hand.
func TestPrimaryNavIsOneScrollableBar(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	doc := parseHTML(t, serveMuxGet(t, mux, "/ui/methodology").Body.String())
	header := findAll(doc, byAtom(atom.Header))[0]

	if inputs := findAll(header, byAtom(atom.Input)); len(inputs) > 0 {
		t.Error("header must not hide the navigation behind a form-control toggle")
	}
	for _, n := range findAll(header, func(*html.Node) bool { return true }) {
		for _, a := range n.Attr {
			if strings.HasPrefix(a.Key, "on") {
				t.Errorf("header element <%s> has inline handler %s", n.Data, a.Key)
			}
		}
	}

	navs := findAll(header, func(n *html.Node) bool {
		_, ok := attr(n, "data-current-into-view")
		return n.DataAtom == atom.Nav && ok
	})
	if len(navs) != 1 {
		t.Fatalf("primary nav must opt in to data-current-into-view, got %d", len(navs))
	}

	rec := serveSiteScript(t, mux, doc)
	if !strings.Contains(rec, "data-current-into-view") {
		t.Error("census.js must scroll the nav marked data-current-into-view")
	}
}

// serveSiteScript checks the shell loads the deferred site script and returns
// its served body.
func serveSiteScript(t *testing.T, mux *http.ServeMux, doc *html.Node) string {
	t.Helper()
	const src = "/ui/assets/census.js"
	found := false
	for _, s := range findAll(doc, byAtom(atom.Script)) {
		if v, _ := attr(s, "src"); strings.HasPrefix(v, src+"?v=") {
			found = true
			if _, deferred := attr(s, "defer"); !deferred {
				t.Error("census.js must load deferred so it never blocks rendering")
			}
		}
	}
	if !found {
		t.Fatalf("shell must load the site script %s", src)
	}
	rec := serveMuxGet(t, mux, src)
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("census.js Content-Type %q, want JavaScript", ct)
	}
	return rec.Body.String()
}

// renderThroughShell renders a fixture page body through the real page shell
// (layout, shared components and template helpers), the way a page template
// added by a later ticket renders.
func renderThroughShell(t *testing.T, body string, data any) *html.Node {
	t.Helper()
	tmpl := template.Must(template.New("layout.html").Funcs(templateFuncs).ParseFS(content, "templates/layout.html", componentsTemplate))
	template.Must(tmpl.New("fixture").Parse(`{{define "content"}}` + body + `{{end}}`))
	var out bytes.Buffer
	if err := tmpl.Execute(&out, PageData{Title: "Fixture", StatsUpdated: "2026-10-24 02:51 UTC", Data: data}); err != nil {
		t.Fatalf("render through shell: %v", err)
	}
	doc := parseHTML(t, out.String())
	assertNoInlineStylesOrEmoji(t, doc)
	return doc
}

func mainOf(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	mains := findAll(doc, byAtom(atom.Main))
	if len(mains) != 1 {
		t.Fatalf("expected one main landmark, got %d", len(mains))
	}
	return mains[0]
}

// TestNightlyFreshnessLine verifies the shared "Rebuilt nightly, last on
// <date>" line: a readable date with a machine-readable <time>, and nothing
// at all for a section that has no build time.
func TestNightlyFreshnessLine(t *testing.T) {
	builtAt := time.Date(2026, 10, 24, 2, 51, 0, 0, time.UTC)
	doc := renderThroughShell(t, `{{template "freshness" (nightly .Data)}}`, builtAt)
	main := mainOf(t, doc)
	if got := textOf(main); got != "Rebuilt nightly, last on 24 October 2026" {
		t.Errorf("freshness line = %q", got)
	}
	times := findAll(main, byAtom(atom.Time))
	if len(times) != 1 {
		t.Fatalf("freshness line needs one <time>, got %d", len(times))
	}
	if dt, _ := attr(times[0], "datetime"); dt != "2026-10-24T02:51:00Z" {
		t.Errorf("<time datetime> = %q, want 2026-10-24T02:51:00Z", dt)
	}

	empty := mainOf(t, renderThroughShell(t, `{{template "freshness" (nightly .Data)}}`, time.Time{}))
	if got := textOf(empty); got != "" {
		t.Errorf("a section with no build time renders no freshness line, got %q", got)
	}
}

// TestNextRefreshState verifies the shared state for a page whose snapshot
// section is missing (an older snapshot): it names what is coming, says when,
// and reassures that the rest of the page is current.
func TestNextRefreshState(t *testing.T) {
	doc := renderThroughShell(t, `{{template "next-refresh" (nextRefresh .Data)}}`, "Grand Company figures")
	got := textOf(mainOf(t, doc))
	for _, want := range []string{
		"Grand Company figures arrive with the next refresh.",
		"nightly",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("next-refresh state %q missing %q", got, want)
		}
	}
	if marks := findAll(mainOf(t, doc), byAtom(atom.Svg)); len(marks) != 1 {
		t.Errorf("next-refresh state carries one decorative mark, got %d", len(marks))
	} else if hidden, _ := attr(marks[0], "aria-hidden"); hidden != "true" {
		t.Error("the state mark must be hidden from assistive technology")
	}
}

// TestOneEntrancePerPage verifies the motion rule: a page has at most one
// entrance (the dashboard's is the realm tally), it lasts under 400 ms, it is
// off under reduced motion, and it is off for content swapped in after the
// page has settled.
func TestOneEntrancePerPage(t *testing.T) {
	rig := newTestRig(t)
	seedFullDashboard(t, rig)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	for _, tc := range shellPages {
		doc := parseHTML(t, serveMuxGet(t, mux, tc.path).Body.String())
		entrances := findAll(doc, func(n *html.Node) bool { _, ok := attr(n, "data-entrance"); return ok })
		if len(entrances) > 1 {
			t.Errorf("%s has %d entrances, want at most one", tc.path, len(entrances))
		}
		if tc.path == "/" && len(entrances) != 1 {
			t.Errorf("dashboard needs its one entrance, got %d", len(entrances))
		}
	}

	css := servedStylesheet(t)
	m := regexp.MustCompile(`--duration-entrance:\s*(\d+)ms`).FindStringSubmatch(css)
	if m == nil {
		t.Fatal("stylesheet must define --duration-entrance in ms")
	}
	if ms, _ := strconv.Atoi(m[1]); ms >= 400 {
		t.Errorf("--duration-entrance %dms, want under 400ms", ms)
	}
	reduced := css[strings.Index(css, "@media (prefers-reduced-motion: reduce)"):]
	if !strings.Contains(reduced, "[data-entrance]") {
		t.Error("reduced motion must switch entrances off")
	}
	if !regexp.MustCompile(`\.census-settled \[data-entrance\][^{]*\{[^}]*animation:\s*none`).MatchString(css) {
		t.Error("entrances must be off once the page has settled, so HTMX swaps never animate")
	}
}

// TestChartThemeReadsSemanticPalettes verifies the shared chart theme takes
// the role and Grand Company colours from the stylesheet tokens, so charts
// never carry their own copies of those hexes.
func TestChartThemeReadsSemanticPalettes(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	js := serveMuxGet(t, mux, "/ui/assets/chart-theme.js").Body.String()
	for _, token := range []string{"mark-tank", "mark-healer", "mark-dps", "gc-maelstrom", "gc-twin-adder", "gc-immortal-flames"} {
		if !strings.Contains(js, `"--color-`+token+`"`) {
			t.Errorf("chart-theme.js must read --color-%s", token)
		}
	}
	if hex := regexp.MustCompile(`#[0-9A-Fa-f]{6}\b`).FindString(js); hex != "" {
		t.Errorf("chart-theme.js carries a hex colour literal %q; colours come from the tokens", hex)
	}
}
