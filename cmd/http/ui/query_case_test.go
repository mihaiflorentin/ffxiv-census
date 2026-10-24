package ui

import (
	"bytes"
	"net/http"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func renderedMain(t *testing.T, doc *html.Node) string {
	t.Helper()
	mains := findAll(doc, byAtom(atom.Main))
	if len(mains) != 1 {
		t.Fatalf("expected one main landmark, got %d", len(mains))
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, mains[0]); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// TestFilterQueryValuesAreCaseInsensitive checks a filter value in any case
// renders exactly the page its canonical spelling renders: the same figures,
// the same current pill and selected options, the same chips and scope text.
func TestFilterQueryValuesAreCaseInsensitive(t *testing.T) {
	cases := []struct{ typed, canonical string }{
		{"/ui/races?region=na", "/ui/races?region=NA"},
		{"/ui/races?region=Eu&dc=chaos", "/ui/races?region=EU&dc=Chaos"},
		{"/ui/races?world=cerberus", "/ui/races?world=Cerberus"},
		{"/ui/races?region=eu&dc=CHAOS&world=cerberus", "/ui/races?region=EU&dc=Chaos&world=Cerberus"},
		{"/ui/worlds?region=na", "/ui/worlds?region=NA"},
		{"/ui/worlds?region=eu&dc=chaos", "/ui/worlds?region=EU&dc=Chaos"},
		{"/ui/worlds?dc=chaos", "/ui/worlds?dc=Chaos"},
	}
	for _, tc := range cases {
		t.Run(tc.typed, func(t *testing.T) {
			got := renderedMain(t, renderPage(t, tc.typed))
			want := renderedMain(t, renderPage(t, tc.canonical))
			if got != want {
				t.Errorf("%s renders differently from %s\n got: %s\nwant: %s", tc.typed, tc.canonical, got, want)
			}
		})
	}
}

func TestLowerCaseRegionMarksItsPillAndChip(t *testing.T) {
	doc := renderPage(t, "/ui/worlds?region=na")
	var current []string
	for _, a := range findAll(doc, byAtom(atom.A)) {
		if v, _ := attr(a, "aria-current"); v == "true" {
			current = append(current, textOf(a))
		}
	}
	if len(current) != 1 || current[0] != "North America" {
		t.Errorf("current pills = %v, want [North America]", current)
	}
	chips := appliedFilterChips(t, doc)
	if href, ok := chips["Remove filter Region: North America"]; !ok || href != "/ui/worlds" {
		t.Errorf("chips = %v, want the North America chip linking to /ui/worlds", chips)
	}
}

func TestWorldBreakdownPartialAcceptsAnyRegionCase(t *testing.T) {
	rig := newTestRig(t)
	seedFullDashboard(t, rig)
	seedWorldsCharacters(t, rig)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)
	got := serveMuxGet(t, mux, "/ui/partials/world-breakdown?region=eu").Body.String()
	want := serveMuxGet(t, mux, "/ui/partials/world-breakdown?region=EU").Body.String()
	if got != want {
		t.Errorf("?region=eu renders differently from ?region=EU\n got: %s\nwant: %s", got, want)
	}
}
