package ui

import (
	"net/http"
	"testing"

	"golang.org/x/net/html"
)

// TestStatGridsDeclareTheirTileCount checks each row of stat tiles names its
// layout: four tiles use the 2 x 2 (then 1 x 4) quad grid, three tiles the
// trio grid (headline tile over a pair on phones, one row above), so no page
// stacks its tiles into one tall column on a phone.
func TestStatGridsDeclareTheirTileCount(t *testing.T) {
	rig := newTestRig(t)
	seedFullDashboard(t, rig)
	seedWorldsCharacters(t, rig)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)
	want := map[int]string{3: "stat-grid-trio", 4: "stat-grid-quad"}

	for _, page := range shellPages {
		t.Run(page.name, func(t *testing.T) {
			doc := parseHTML(t, serveMuxGet(t, mux, page.path).Body.String())
			for _, grid := range findAll(doc, func(n *html.Node) bool { return hasClass(n, "stat-grid") }) {
				tiles := 0
				for c := grid.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode && hasClass(c, "stat-card") {
						tiles++
					}
				}
				class, ok := want[tiles]
				if !ok {
					t.Errorf("stat grid has %d tiles; only 3 or 4 have a layout", tiles)
					continue
				}
				if !hasClass(grid, class) {
					got, _ := attr(grid, "class")
					t.Errorf("stat grid with %d tiles has class %q, want %s", tiles, got, class)
				}
			}
		})
	}
}
