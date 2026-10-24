package ui

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
)

// TestPagesCarryTheCensusTaxonomyForCharts checks every page hands the chart
// theme the race and region order from the domain, so race colours on the
// charts and the server-rendered swatches come from one source.
func TestPagesCarryTheCensusTaxonomyForCharts(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	var wantRegions []string
	for _, r := range census.Regions() {
		wantRegions = append(wantRegions, r.Code)
	}
	for _, page := range shellPages {
		t.Run(page.name, func(t *testing.T) {
			doc := parseHTML(t, serveMuxGet(t, mux, page.path).Body.String())
			node := byID(doc, "census-taxonomy")
			if node == nil {
				t.Fatal("page has no #census-taxonomy data block")
			}
			if typ, _ := attr(node, "type"); typ != "application/json" {
				t.Fatalf("#census-taxonomy type = %q, want application/json", typ)
			}
			var got struct {
				Races   []string `json:"races"`
				Regions []string `json:"regions"`
			}
			if err := json.Unmarshal([]byte(textOf(node)), &got); err != nil {
				t.Fatalf("taxonomy is not JSON: %v (%q)", err, textOf(node))
			}
			if !reflect.DeepEqual(got.Races, census.RaceOrder()) {
				t.Errorf("races = %v, want %v", got.Races, census.RaceOrder())
			}
			if !reflect.DeepEqual(got.Regions, wantRegions) {
				t.Errorf("regions = %v, want %v", got.Regions, wantRegions)
			}
		})
	}
}

// TestChartThemeDoesNotHardCodeTheTaxonomy guards against a second, hand-synced
// copy of the race order in the chart theme.
func TestChartThemeDoesNotHardCodeTheTaxonomy(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)
	js := serveMuxGet(t, mux, "/ui/assets/chart-theme.js").Body.String()
	for _, race := range census.RaceOrder() {
		if strings.Contains(js, `"`+race+`"`) {
			t.Errorf("chart-theme.js hard-codes race %q; read it from #census-taxonomy", race)
		}
	}
}
