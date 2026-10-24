package ui

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// assertNoInlineStylesOrEmoji checks the spec principles: styling lives in
// the stylesheet, and no heading or page content carries emoji.
func assertNoInlineStylesOrEmoji(t *testing.T, doc *html.Node) {
	t.Helper()
	for _, n := range findAll(doc, func(*html.Node) bool { return true }) {
		if _, ok := attr(n, "style"); ok {
			t.Errorf("<%s> carries an inline style attribute", n.Data)
		}
	}
	for _, main := range findAll(doc, byAtom(atom.Main)) {
		if containsEmoji(textOf(main)) {
			t.Errorf("page content contains emoji: %q", textOf(main))
		}
	}
}

// assertChartsThemedAndDescribed checks every chart takes its colours from
// the shared theme (no colour literals in inline scripts) and has an
// accessible name plus a text alternative.
func assertChartsThemedAndDescribed(t *testing.T, doc *html.Node, wantCharts int) {
	t.Helper()
	for _, s := range findAll(doc, byAtom(atom.Script)) {
		if src, _ := attr(s, "src"); src == "" {
			if lit := colourLiteral.FindString(textOf(s)); lit != "" {
				t.Errorf("inline chart script carries a colour literal %q; colours come from the shared theme", lit)
			}
		}
	}
	canvases := findAll(doc, byAtom(atom.Canvas))
	if len(canvases) != wantCharts {
		t.Fatalf("expected %d charts, got %d", wantCharts, len(canvases))
	}
	for _, c := range canvases {
		id, _ := attr(c, "id")
		if role, _ := attr(c, "role"); role != "img" {
			t.Errorf("canvas %q role = %q, want img", id, role)
		}
		if label, _ := attr(c, "aria-label"); strings.TrimSpace(label) == "" {
			t.Errorf("canvas %q has no accessible name", id)
		}
		ref, _ := attr(c, "aria-describedby")
		alt := byID(doc, ref)
		if alt == nil || textOf(alt) == "" {
			t.Errorf("canvas %q aria-describedby %q must reference one non-empty text alternative", id, ref)
		}
	}
}

func TestRacesAndWorldsFollowThePagePrinciples(t *testing.T) {
	for _, path := range []string{
		"/ui/races",
		"/ui/races?region=EU&dc=Chaos&world=Cerberus",
		"/ui/worlds",
		"/ui/worlds?region=EU&dc=Chaos",
	} {
		t.Run(path, func(t *testing.T) {
			assertNoInlineStylesOrEmoji(t, renderPage(t, path))
		})
	}
}

func TestRacesChartsUseSharedThemeAndHaveTextAlternatives(t *testing.T) {
	doc := renderPage(t, "/ui/races")
	// Race share, gender, clans and race by gender.
	assertChartsThemedAndDescribed(t, doc, 4)
}

// TestRacesChartDataTables checks the "Show the numbers" alternatives carry
// the figures: clans grouped under their race, and race by gender.
func TestRacesChartDataTables(t *testing.T) {
	doc := renderPage(t, "/ui/races")
	rows := map[string]bool{}
	for _, row := range tableBodyRows(tableCaptioned(t, doc, "Characters by race and gender")) {
		rows[strings.Join(row, "|")] = true
	}
	// The fixture has two Hyur characters, both with no recorded gender.
	if !rows["Hyur|0|0|2"] {
		t.Errorf("race by gender rows %v missing Hyur|0|0|2 (male, female, unrecorded)", rows)
	}

	clans := tableCaptioned(t, doc, "Characters by clan")
	if len(tableBodyRows(clans)) == 0 {
		t.Error("clan table has no rows")
	}
}
