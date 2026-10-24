package ui

import (
	"reflect"
	"testing"

	"golang.org/x/net/html/atom"
)

func TestPrimaryNavListsThePagesInOrderAndMarksTheCurrentOne(t *testing.T) {
	mux := privacyMux(t, privacyPageSnapshot(3))
	wantLabels := []string{"Dashboard", "Worlds", "Rankings", "Races", "Grand Companies", "Jobs", "Levels", "Expansions", "Methodology"}
	wantHrefs := []string{"/", "/ui/worlds", "/ui/rankings", "/ui/races", "/ui/grand-companies", "/ui/jobs", "/ui/levels", "/ui/expansions", "/ui/methodology"}

	for path, current := range map[string]string{
		"/":               "Dashboard",
		"/ui/jobs":        "Jobs",
		"/ui/methodology": "Methodology",
		// Private Achievements sits under Methodology.
		"/ui/privacy": "Methodology",
	} {
		doc := parseHTML(t, serveMuxGet(t, mux, path).Body.String())
		nav := byID(doc, "primary-nav")
		if nav == nil {
			t.Fatalf("%s: no primary navigation", path)
		}
		var labels, hrefs, marked []string
		for _, a := range findAll(nav, byAtom(atom.A)) {
			labels = append(labels, textOf(a))
			href, _ := attr(a, "href")
			hrefs = append(hrefs, href)
			if c, ok := attr(a, "aria-current"); ok && c == "page" {
				marked = append(marked, textOf(a))
			}
		}
		if !reflect.DeepEqual(labels, wantLabels) || !reflect.DeepEqual(hrefs, wantHrefs) {
			t.Errorf("%s: nav = %v %v, want %v %v", path, labels, hrefs, wantLabels, wantHrefs)
		}
		if !reflect.DeepEqual(marked, []string{current}) {
			t.Errorf("%s: current = %v, want [%s]", path, marked, current)
		}
	}
}
