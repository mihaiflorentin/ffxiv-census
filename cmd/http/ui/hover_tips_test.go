package ui

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// TestChartThemeNeverReplacesTheDefaultAnimation guards the site-wide
// tooltip fix: assigning a fresh object to Chart.defaults.animation drops
// Chart.js's resolver wiring and no chart tooltip ever draws. The theme may
// only set fields on the existing default.
func TestChartThemeNeverReplacesTheDefaultAnimation(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	js := serveMuxGet(t, mux, "/ui/assets/chart-theme.js").Body.String()
	wholesale := regexp.MustCompile(`defaults\.animation\s*=[^=]|\.animation\s*=\s*\{`)
	if m := wholesale.FindString(js); m != "" {
		t.Errorf("chart-theme.js replaces the default animation (%q); mutate its fields instead", m)
	}
	if !strings.Contains(js, "Chart.defaults.animation;") {
		t.Error("chart-theme.js should read the existing default animation and mutate it")
	}
}

// focusable reports whether a keyboard user can reach n: it is a link or
// button, carries tabindex="0", or holds such an element.
func focusable(n *html.Node) bool {
	reach := func(m *html.Node) bool {
		if m.Type != html.ElementNode {
			return false
		}
		if m.DataAtom == atom.A || m.DataAtom == atom.Button {
			_, href := attr(m, "href")
			return m.DataAtom == atom.Button || href
		}
		v, ok := attr(m, "tabindex")
		return ok && v == "0"
	}
	return reach(n) || len(findAll(n, reach)) > 0
}

var digits = regexp.MustCompile(`\d`)

// assertTips checks every element of class under doc is a hover tip host:
// a data-tip title, a data-tip-value line carrying a number, and a way in
// for keyboard users. It returns the hosts for spot checks.
func assertTips(t *testing.T, doc *html.Node, class string) []*html.Node {
	t.Helper()
	hosts := findAll(doc, byClass(class))
	if len(hosts) == 0 {
		t.Fatalf("no .%s on the page", class)
	}
	for _, n := range hosts {
		title, _ := attr(n, "data-tip")
		value, _ := attr(n, "data-tip-value")
		if strings.TrimSpace(title) == "" {
			t.Errorf(".%s has no data-tip title", class)
		}
		if !digits.MatchString(value) {
			t.Errorf(".%s %q: data-tip-value %q carries no number", class, title, value)
		}
		if !focusable(n) {
			t.Errorf(".%s %q is not reachable by keyboard", class, title)
		}
	}
	return hosts
}

func tipOf(n *html.Node) string {
	title, _ := attr(n, "data-tip")
	value, _ := attr(n, "data-tip-value")
	return title + " | " + value
}

func hasTip(hosts []*html.Node, want string) bool {
	for _, n := range hosts {
		if tipOf(n) == want {
			return true
		}
	}
	return false
}

func tipList(hosts []*html.Node) string {
	var out []string
	for _, n := range hosts {
		out = append(out, tipOf(n))
	}
	return strings.Join(out, "\n")
}

// TestEveryCustomVisualShowsItsNumbersOnHover covers each hand-built bar,
// meter, band and tile: each mark names itself and its figures for the
// shared tooltip in census.js, and can take keyboard focus.
func TestEveryCustomVisualShowsItsNumbersOnHover(t *testing.T) {
	get := func(t *testing.T, mux *http.ServeMux, path string) *html.Node {
		t.Helper()
		rec := serveMuxGet(t, mux, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d", path, rec.Code)
		}
		doc := parseHTML(t, rec.Body.String())
		assertNoInlineStylesOrEmoji(t, doc)
		return doc
	}
	rigMux := func(t *testing.T, seed func(*testing.T, *testRig)) *http.ServeMux {
		rig := newTestRig(t)
		seed(t, rig)
		mux := http.NewServeMux()
		rig.ctrl.RegisterRoutes(mux)
		return mux
	}

	t.Run("realm tally", func(t *testing.T) {
		mux := rigMux(t, func(t *testing.T, rig *testRig) {
			seedRegions(t, rig, map[string]int{"NA": 2, "EU": 1, "JP": 1})
		})
		hosts := assertTips(t, get(t, mux, "/"), "realm-segment")
		if !hasTip(hosts, "North America | 2 characters, 50.0%") {
			t.Errorf("realm tips:\n%s", tipList(hosts))
		}
	})

	t.Run("dashboard main scenario", func(t *testing.T) {
		assertTips(t, renderDashboard(t), "progress-item")
	})

	t.Run("world detail main scenario and Grand Company band", func(t *testing.T) {
		assertTips(t, renderPage(t, "/ui/worlds/Balmung"), "progress-item")
		hosts := assertTips(t, get(t, grandCompanyMux(t, 3), "/ui/worlds/Balmung"), "gc-band-segment")
		for _, n := range hosts {
			if role, _ := attr(n, "role"); role != "img" {
				t.Errorf("focusable band segment %q needs role=img", tipOf(n))
			}
			if label, _ := attr(n, "aria-label"); !digits.MatchString(label) {
				t.Errorf("band segment %q needs an aria-label with its figures", tipOf(n))
			}
		}
	})

	t.Run("expansion steps", func(t *testing.T) {
		mux := rigMux(t, seedExpansionFunnel)
		assertTips(t, get(t, mux, "/ui/expansions"), "progression-step")
	})

	t.Run("Grand Company standards and active share", func(t *testing.T) {
		doc := get(t, grandCompanyMux(t, 3), "/ui/grand-companies")
		assertTips(t, doc, "standard")
		assertTips(t, doc, "gc-active")
	})

	t.Run("rankings ladder", func(t *testing.T) {
		assertTips(t, get(t, rankingsMux(t, 3), "/ui/rankings"), "ladder-rung")
	})

	t.Run("Free Company ladder", func(t *testing.T) {
		assertTips(t, get(t, fcRankingsMux(t, 3, fcRankingsBoards()), "/ui/rankings/free-companies"), "ladder-rung")
	})

	t.Run("armoury tiles and party balance", func(t *testing.T) {
		doc := get(t, jobsMux(t, jobsSnapshot(3, true, true)), "/ui/jobs")
		assertTips(t, doc, "armoury-tile")
		hosts := assertTips(t, doc, "balance-segment")
		for _, n := range hosts {
			if role, _ := attr(n, "role"); role != "img" {
				t.Errorf("focusable balance segment %q needs role=img", tipOf(n))
			}
		}
		for _, bar := range findAll(doc, byClass("balance-bar")) {
			if v, _ := attr(bar, "aria-hidden"); v == "true" {
				t.Error("a balance bar holding focusable segments must not be aria-hidden")
			}
		}
	})

	t.Run("privacy veil and world lists", func(t *testing.T) {
		doc := get(t, privacyMux(t, privacyPageSnapshot(3)), "/ui/privacy")
		hosts := assertTips(t, doc, "veil-segment")
		if !hasTip(hosts, "Private | 150 characters, 15.0%") {
			t.Errorf("veil tips:\n%s", tipList(hosts))
		}
		for _, veil := range findAll(doc, byClass("veil")) {
			if v, _ := attr(veil, "aria-hidden"); v == "true" {
				t.Error("the veil holds focusable segments, so it must not be aria-hidden")
			}
		}
		assertTips(t, doc, "privacy-world")
	})
}

// TestOneSharedTooltipMechanism verifies the site script draws the hover
// tooltip for every data-tip host, hidden from assistive technology (the
// figures are in the page's text and tables), and that no page keeps its
// own tooltip script.
func TestOneSharedTooltipMechanism(t *testing.T) {
	rig := newTestRig(t)
	seedRegions(t, rig, map[string]int{"NA": 2, "EU": 1})
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	doc := parseHTML(t, serveMuxGet(t, mux, "/").Body.String())
	js := serveSiteScript(t, mux, doc)
	for _, want := range []string{"[data-tip]", "data-tip-value", `"aria-hidden"`, "census-tip", "Escape"} {
		if !strings.Contains(js, want) {
			t.Errorf("census.js must handle %s", want)
		}
	}
	if len(findAll(doc, byClass("realm-tooltip"))) != 0 {
		t.Error("the realm tally must use the shared tooltip, not its own")
	}

	css := serveMuxGet(t, mux, "/ui/assets/styles.css").Body.String()
	if !strings.Contains(css, ".census-tip") {
		t.Error("styles.css must style the shared .census-tip tooltip")
	}
}

// TestRankingsControlsAreOneRowEach verifies every Rankings control (label
// and switch) is its own row directly in the controls grid, Region included,
// so the stylesheet can line every label and switch up in two columns.
func TestRankingsControlsAreOneRowEach(t *testing.T) {
	cases := map[string]struct {
		mux  *http.ServeMux
		path string
		want int
	}{
		"rankings":       {rankingsMux(t, 3), "/ui/rankings", 4},
		"free companies": {fcRankingsMux(t, 3, fcRankingsBoards()), "/ui/rankings/free-companies", 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			doc := parseHTML(t, serveMuxGet(t, tc.mux, tc.path).Body.String())
			grids := findAll(doc, byClass("rankings-controls"))
			if len(grids) != 1 {
				t.Fatalf("want one controls grid, got %d", len(grids))
			}
			rows := findAll(grids[0], byClass("rankings-control"))
			if len(rows) != tc.want {
				t.Fatalf("want %d control rows, got %d", tc.want, len(rows))
			}
			for _, row := range rows {
				if row.Parent != grids[0] {
					parent, _ := attr(row.Parent, "class")
					t.Errorf("control %q sits inside %q, not directly in the grid", flatText(row), parent)
				}
				if len(findAll(row, byClass("rankings-control-label"))) != 1 || len(findAll(row, byClass("switch"))) != 1 {
					t.Errorf("control %q needs one label and one switch", flatText(row))
				}
			}
		})
	}
}
