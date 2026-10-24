package ui

import (
	"net/http"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// renderPage serves path through the UI mux with a fully seeded fixture and
// returns the parsed document.
func renderPage(t *testing.T, path string) *html.Node {
	t.Helper()
	rig := newTestRig(t)
	seedFullDashboard(t, rig)
	seedWorldsCharacters(t, rig)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)
	rec := serveMuxGet(t, mux, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, rec.Code, rec.Body.String())
	}
	return parseHTML(t, rec.Body.String())
}

func byID(root *html.Node, id string) *html.Node {
	found := findAll(root, func(n *html.Node) bool { v, _ := attr(n, "id"); return id != "" && v == id })
	if len(found) != 1 {
		return nil
	}
	return found[0]
}

func hasClass(n *html.Node, class string) bool {
	v, _ := attr(n, "class")
	for _, c := range strings.Fields(v) {
		if c == class {
			return true
		}
	}
	return false
}

// visibleLabelFor returns the text of the visible <label for=id>, or "".
func visibleLabelFor(doc *html.Node, id string) string {
	for _, l := range findAll(doc, byAtom(atom.Label)) {
		if f, _ := attr(l, "for"); f == id && id != "" && !hasClass(l, "visually-hidden") {
			return textOf(l)
		}
	}
	return ""
}

func TestFilterControlsHaveVisibleLabels(t *testing.T) {
	cases := []struct {
		path   string
		labels map[string]string // select name -> visible label
	}{
		{"/ui/races?region=EU&dc=Chaos", map[string]string{"region": "Region", "dc": "Datacenter", "world": "World"}},
		{"/ui/worlds?region=EU", map[string]string{"dc": "Datacenter"}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			doc := renderPage(t, tc.path)
			selects := findAll(doc, byAtom(atom.Select))
			if len(selects) != len(tc.labels) {
				t.Fatalf("expected %d filter selects, got %d", len(tc.labels), len(selects))
			}
			for _, s := range selects {
				name, _ := attr(s, "name")
				id, _ := attr(s, "id")
				want, ok := tc.labels[name]
				if !ok {
					t.Errorf("unexpected select %q", name)
					continue
				}
				if got := visibleLabelFor(doc, id); got != want {
					t.Errorf("select %q (id %q) visible label = %q, want %q", name, id, got, want)
				}
			}
		})
	}
}

func TestWorldsRegionPillsAreALabelledGroup(t *testing.T) {
	doc := renderPage(t, "/ui/worlds?region=EU")
	groups := findAll(doc, func(n *html.Node) bool { r, _ := attr(n, "role"); return r == "group" })
	var pills *html.Node
	for _, g := range groups {
		ref, _ := attr(g, "aria-labelledby")
		if label := byID(doc, ref); label != nil && textOf(label) == "Region" && !hasClass(label, "visually-hidden") {
			pills = g
		}
	}
	if pills == nil {
		t.Fatal("expected a group labelled by a visible 'Region' label")
	}
	// Pills list the regions that have worlds in the snapshot (NA and EU in
	// this fixture), plus All.
	want := map[string]string{
		"All":           "/ui/worlds",
		"North America": "/ui/worlds?region=NA",
		"Europe":        "/ui/worlds?region=EU",
	}
	var current []string
	for _, a := range findAll(pills, byAtom(atom.A)) {
		href, _ := attr(a, "href")
		if want[textOf(a)] != href {
			t.Errorf("pill %q links to %q, want %q", textOf(a), href, want[textOf(a)])
		}
		delete(want, textOf(a))
		if v, _ := attr(a, "aria-current"); v == "true" {
			current = append(current, textOf(a))
		}
	}
	if len(want) != 0 {
		t.Errorf("missing region pills: %v", want)
	}
	if len(current) != 1 || current[0] != "Europe" {
		t.Errorf("current pill = %v, want [Europe]", current)
	}
}

// appliedFilterChips returns each chip's link text and href from the list
// labelled "Applied filters".
func appliedFilterChips(t *testing.T, doc *html.Node) map[string]string {
	t.Helper()
	lists := findAll(doc, func(n *html.Node) bool {
		label, _ := attr(n, "aria-label")
		return n.DataAtom == atom.Ul && label == "Applied filters"
	})
	if len(lists) == 0 {
		return nil
	}
	if len(lists) > 1 {
		t.Fatalf("expected one applied-filters list, got %d", len(lists))
	}
	chips := map[string]string{}
	for _, a := range findAll(lists[0], byAtom(atom.A)) {
		href, _ := attr(a, "href")
		chips[textOf(a)] = href
	}
	return chips
}

func TestAppliedFiltersRenderAsRemovableChips(t *testing.T) {
	cases := []struct {
		path string
		want map[string]string // chip text -> URL without that filter
	}{
		{"/ui/races", nil},
		{"/ui/races?region=EU&dc=Chaos&world=Cerberus", map[string]string{
			"Remove filter Region: Europe":    "/ui/races?dc=Chaos&world=Cerberus",
			"Remove filter Datacenter: Chaos": "/ui/races?region=EU&world=Cerberus",
			"Remove filter World: Cerberus":   "/ui/races?region=EU&dc=Chaos",
		}},
		{"/ui/races?world=Cerberus", map[string]string{
			"Remove filter World: Cerberus": "/ui/races",
		}},
		{"/ui/worlds", nil},
		{"/ui/worlds?region=EU&dc=Chaos", map[string]string{
			"Remove filter Region: Europe":    "/ui/worlds?dc=Chaos",
			"Remove filter Datacenter: Chaos": "/ui/worlds?region=EU",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := appliedFilterChips(t, renderPage(t, tc.path))
			if len(got) != len(tc.want) {
				t.Fatalf("chips = %v, want %v", got, tc.want)
			}
			for text, href := range tc.want {
				if got[text] != href {
					t.Errorf("chip %q links to %q, want %q (all chips %v)", text, got[text], href, got)
				}
			}
		})
	}
}
