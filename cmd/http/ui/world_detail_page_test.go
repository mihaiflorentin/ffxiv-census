package ui

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// breadcrumb returns the navigation landmark labelled "Breadcrumb".
func breadcrumb(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	navs := findAll(doc, func(n *html.Node) bool {
		label, _ := attr(n, "aria-label")
		return n.DataAtom == atom.Nav && label == "Breadcrumb"
	})
	if len(navs) != 1 {
		t.Fatalf("expected one breadcrumb nav, got %d", len(navs))
	}
	return navs[0]
}

func TestWorldDetailBreadcrumbsLinkRegionAndDatacenterScopes(t *testing.T) {
	doc := renderPage(t, "/ui/worlds/Balmung")
	items := findAll(breadcrumb(t, doc), byAtom(atom.Li))
	if len(items) != 3 {
		t.Fatalf("expected 3 breadcrumb items (region, datacenter, world), got %d", len(items))
	}
	want := []struct{ text, href string }{
		{"North America", "/ui/worlds?region=NA"},
		{"Crystal", "/ui/worlds?region=NA&dc=Crystal"},
	}
	for i, w := range want {
		links := findAll(items[i], byAtom(atom.A))
		if len(links) != 1 {
			t.Fatalf("breadcrumb %d: expected one link, got %d", i, len(links))
		}
		if got := textOf(links[0]); got != w.text {
			t.Errorf("breadcrumb %d text = %q, want %q", i, got, w.text)
		}
		if href, _ := attr(links[0], "href"); href != w.href {
			t.Errorf("breadcrumb %d href = %q, want %q", i, href, w.href)
		}
	}
	world := items[2]
	if len(findAll(world, byAtom(atom.A))) != 0 {
		t.Error("the current world must not link to itself")
	}
	current := findAll(world, func(n *html.Node) bool { v, _ := attr(n, "aria-current"); return v == "page" })
	if len(current) != 1 || textOf(current[0]) != "Balmung" {
		t.Errorf("expected the world crumb to be marked aria-current=page with text Balmung, got %q", textOf(world))
	}
	h1 := findAll(doc, byAtom(atom.H1))
	if len(h1) != 1 || textOf(h1[0]) != "Balmung" {
		t.Errorf("expected one h1 reading Balmung, got %d", len(h1))
	}
}

// TestWorldDetailRendersItsFiguresFromTheSnapshot uses the full dashboard
// fixture: eight active characters on Balmung, one per playable race, one of
// whom finished A Realm Reborn.
func TestWorldDetailRendersItsFiguresFromTheSnapshot(t *testing.T) {
	doc := renderPage(t, "/ui/worlds/Balmung")

	tiles := findAll(doc, func(n *html.Node) bool { v, _ := attr(n, "aria-label"); return v == "Key figures" })
	if len(tiles) != 1 {
		t.Fatalf("expected one key figures section, got %d", len(tiles))
	}
	figures := textOf(tiles[0])
	for _, want := range []string{"Characters 8", "Active players, last 90 days 8", "100.0% of these characters", "New characters, last 90 days"} {
		if !strings.Contains(figures, want) {
			t.Errorf("key figures %q missing %q", figures, want)
		}
	}

	rows := map[string]bool{}
	for _, row := range tableBodyRows(tableCaptioned(t, doc, "Characters by race on Balmung")) {
		rows[strings.Join(row, "|")] = true
	}
	for _, race := range playableRaces {
		if want := race + "|1|12.5%|1"; !rows[want] {
			t.Errorf("race rows %v missing %q (race, characters, share, active)", rows, want)
		}
	}

	meters := findAll(doc, func(n *html.Node) bool {
		label, _ := attr(n, "aria-label")
		return n.DataAtom == atom.Meter && label == "A Realm Reborn completion"
	})
	if len(meters) != 1 {
		t.Fatalf("expected one A Realm Reborn completion meter, got %d", len(meters))
	}
	if v, _ := attr(meters[0], "value"); v != "12.5" {
		t.Errorf("A Realm Reborn meter value = %q, want 12.5", v)
	}
	if got := textOf(meters[0].Parent); !strings.Contains(got, "A Realm Reborn 12.5% 1") {
		t.Errorf("A Realm Reborn completion reads %q, want name, share and count", got)
	}
}

func TestWorldDetailChartsUseSharedThemeAndHaveTextAlternatives(t *testing.T) {
	doc := renderPage(t, "/ui/worlds/Balmung")
	// Race distribution only: the snapshot keeps the daily new-character
	// series for the whole game, not per world.
	assertChartsThemedAndDescribed(t, doc, 1)
}
