package ui

import (
	"net/http"
	"strings"
	"testing"
	"time"

	census "github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var levelsBuiltAt = time.Date(2026, 10, 24, 2, 0, 0, 0, time.UTC)

// levelsPageSnapshot is a hand-built v4 snapshot with the seven default
// bands: Crystal (Balmung, Coeurl) in NA and Chaos (Ragnarok) in EU. Band
// counts run ARR, HW, SB, ShB, EW, DT, level cap.
func levelsPageSnapshot(ready bool) *contract.UIStatsSnapshot {
	generated := time.Now().UTC()
	loc := func(dim, key string, total, noLevels int64, levels ...int64) contract.ScopedGroupCount {
		return contract.ScopedGroupCount{Dimension: dim, Key: key, Total: total, Active: total / 4, NoLevels: noLevels, Levels: levels}
	}
	s := &contract.UIStatsSnapshot{
		SchemaVersion:    contract.UIStatsSchemaVersion,
		GeneratedAt:      generated,
		ActivitySince:    generated.Add(-30 * 24 * time.Hour),
		MaxLevel:         100,
		SourceCharacters: 1000,
		LevelBands:       census.LevelBands(census.DefaultExpansions, 100),
		Summary:          contract.StatsSummary{Total: 1000, Active: 250, NoLevels: 200, Levels: []int64{600, 40, 30, 30, 20, 40, 40}},
		Groups: []contract.ScopedGroupCount{
			loc("region", "NA", 600, 100, 380, 20, 20, 20, 10, 30, 20),
			loc("region", "EU", 400, 100, 220, 20, 10, 10, 10, 10, 20),
			loc("datacenter", "Crystal", 600, 100, 380, 20, 20, 20, 10, 30, 20),
			loc("datacenter", "Chaos", 400, 100, 220, 20, 10, 10, 10, 10, 20),
			loc("world", "Balmung", 400, 50, 250, 10, 10, 10, 10, 20, 40),
			loc("world", "Coeurl", 200, 50, 130, 10, 10, 0, 0, 0, 0),
			loc("world", "Ragnarok", 400, 100, 220, 20, 10, 10, 10, 10, 20),
		},
	}
	if ready {
		s.LevelsBuiltAt = levelsBuiltAt
	}
	return s
}

func levelsMux(t *testing.T, snapshot *contract.UIStatsSnapshot) *http.ServeMux {
	t.Helper()
	return privacyMux(t, snapshot)
}

func levelsDoc(t *testing.T, mux *http.ServeMux, path string) *html.Node {
	t.Helper()
	rec := serveMuxGet(t, mux, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status = %d: %s", path, rec.Code, rec.Body.String())
	}
	return parseHTML(t, rec.Body.String())
}

// rowNames returns the visible names of the stacked rows in a list.
func rowNames(list *html.Node) []string {
	var names []string
	for _, n := range findAll(list, byClass("level-row-name")) {
		names = append(names, strings.Fields(textOf(n))[0])
	}
	return names
}

func TestLevelsPageShowsTheDistributionByBand(t *testing.T) {
	doc := levelsDoc(t, levelsMux(t, levelsPageSnapshot(true)), "/ui/levels")
	assertNoInlineStylesOrEmoji(t, doc)
	main := renderedMain(t, doc)

	bands := findAll(doc, byClass("level-band-row"))
	wantBands := []string{"A Realm Reborn", "Heavensward", "Stormblood", "Shadowbringers", "Endwalker", "Dawntrail", "Level cap"}
	if len(bands) != len(wantBands) {
		t.Fatalf("band rows = %d, want %d", len(bands), len(wantBands))
	}
	for i, want := range wantBands {
		if got := flatText(bands[i]); !strings.HasPrefix(got, want) {
			t.Errorf("band row %d = %q, want it to start with %q", i, got, want)
		}
	}
	if got := flatText(bands[0]); !strings.Contains(got, "1–50") || !strings.Contains(got, "600") || !strings.Contains(got, "75.0%") {
		t.Errorf("first band row = %q, want levels 1–50, 600 characters, 75.0%%", got)
	}
	if got := flatText(bands[6]); !strings.Contains(got, "100") || !strings.Contains(got, "5.0%") {
		t.Errorf("cap band row = %q, want level 100 at 5.0%%", got)
	}
	if !strings.Contains(main, "75.0%") || !strings.Contains(main, "never took a job past level 50") {
		t.Error("the lede must say how many never took a job past level 50")
	}
	if !strings.Contains(main, "200 characters with no job levels yet") {
		t.Error("characters with no job levels are counted apart")
	}
	if len(findAll(doc, byClass("freshness"))) == 0 || !strings.Contains(main, "nightly") {
		t.Error("the page says the figures are rebuilt nightly")
	}
	if len(findAll(doc, byClass("chart-data"))) < 2 {
		t.Error("every chart needs a Show the numbers table")
	}
	assertTips(t, doc, "level-band-row")
	if n := len(findAll(doc, byAttr("data-entrance"))); n != 1 {
		t.Errorf("entrances = %d, want exactly one", n)
	}
}

func byAttr(key string) func(*html.Node) bool {
	return func(n *html.Node) bool {
		_, ok := attr(n, key)
		return ok
	}
}

func TestLevelsPageComparesEveryDatacenterAndAsksForAPlaceForWorlds(t *testing.T) {
	doc := levelsDoc(t, levelsMux(t, levelsPageSnapshot(true)), "/ui/levels")
	dcs := byID(doc, "level-datacenters")
	if dcs == nil {
		t.Fatal("no per-datacenter rows")
	}
	if got := rowNames(dcs); strings.Join(got, ",") != "Crystal,Chaos" {
		t.Errorf("datacenter rows = %v, want Crystal then Chaos", got)
	}
	segments := assertTips(t, dcs, "level-segment")
	if len(segments) != 14 {
		t.Errorf("segments = %d, want 7 per datacenter", len(segments))
	}
	if tip := tipOf(segments[0]); !strings.Contains(tip, "Crystal") || !strings.Contains(tip, "A Realm Reborn") || !strings.Contains(tip, "380") {
		t.Errorf("first segment tip = %q", tip)
	}
	worlds := byID(doc, "level-worlds")
	if worlds == nil {
		t.Fatal("no per-world panel")
	}
	if len(findAll(worlds, byClass("level-row"))) != 0 {
		t.Error("without a place the world panel asks for one instead of listing every world")
	}
	if !linksTo(worlds, "/ui/levels?region=NA#level-worlds") {
		t.Error("the world panel offers each region")
	}
}

func TestLevelsPageFiltersNarrowTheRows(t *testing.T) {
	mux := levelsMux(t, levelsPageSnapshot(true))

	na := levelsDoc(t, mux, "/ui/levels?region=na")
	if got := rowNames(byID(na, "level-datacenters")); strings.Join(got, ",") != "Crystal" {
		t.Errorf("NA datacenters = %v", got)
	}
	if got := rowNames(byID(na, "level-worlds")); strings.Join(got, ",") != "Balmung,Coeurl" {
		t.Errorf("NA worlds = %v", got)
	}
	if !strings.Contains(renderedMain(t, na), "in North America") {
		t.Error("the scope names the region")
	}

	rec := serveMuxGet(t, mux, "/ui/levels?world=balmung")
	if got := extractHeadAttribute(t, rec.Body.String(), `<link rel="canonical" href="([^"]*)">`); got != testBaseURL+"/ui/levels" {
		t.Errorf("filtered canonical = %q, want the clean path", got)
	}
	balmung := parseHTML(t, rec.Body.String())
	current := findAll(byID(balmung, "level-worlds"), byClass("level-row-current"))
	if len(current) != 1 || !strings.HasPrefix(flatText(current[0]), "Balmung") {
		t.Errorf("Balmung must be the marked row among Crystal's worlds")
	}
	if dc := findAll(byID(balmung, "level-datacenters"), byClass("level-row-current")); len(dc) != 1 || !strings.HasPrefix(flatText(dc[0]), "Crystal") {
		t.Error("Crystal must be the marked datacenter")
	}
	if got := flatText(findAll(balmung, byClass("level-band-row"))[6]); !strings.Contains(got, "40") || !strings.Contains(got, "11.4%") {
		t.Errorf("Balmung cap band = %q, want 40 at 11.4%%", got)
	}

	empty := levelsDoc(t, mux, "/ui/levels?world=Cerberus")
	if len(findAll(empty, byClass("level-band-row"))) != 0 {
		t.Error("an empty selection shows no bands")
	}
	if !strings.Contains(renderedMain(t, empty), "No level figures for this selection.") || !linksTo(empty, "/ui/levels") {
		t.Error("an empty selection explains itself and links back")
	}
}

func TestLevelsPageWaitsForTheNextRefresh(t *testing.T) {
	v3 := levelsPageSnapshot(true)
	v3.SchemaVersion = 3
	for name, snapshot := range map[string]*contract.UIStatsSnapshot{
		"v3":                v3,
		"v4 without levels": levelsPageSnapshot(false),
	} {
		t.Run(name, func(t *testing.T) {
			doc := levelsDoc(t, levelsMux(t, snapshot), "/ui/levels")
			if !strings.Contains(renderedMain(t, doc), "Level figures arrive with the next refresh.") {
				t.Error("missing the next-refresh state")
			}
			if len(findAll(doc, byClass("level-band-row"))) != 0 {
				t.Error("no bands before the figures exist")
			}
			world := renderedMain(t, levelsDoc(t, levelsMux(t, snapshot), "/ui/worlds/Balmung"))
			if !strings.Contains(world, "Level figures arrive with the next refresh.") {
				t.Error("the world block waits for the next refresh too")
			}
		})
	}
}

func TestWorldDetailShowsItsLevelBand(t *testing.T) {
	doc := levelsDoc(t, levelsMux(t, levelsPageSnapshot(true)), "/ui/worlds/Balmung")
	bars := findAll(doc, byClass("level-strip"))
	if len(bars) != 1 {
		t.Fatalf("world detail needs one compact level band, got %d", len(bars))
	}
	segments := assertTips(t, bars[0], "level-segment")
	if len(segments) != 7 {
		t.Errorf("segments = %d, want 7", len(segments))
	}
	if !linksTo(doc, "/ui/levels?world=Balmung") {
		t.Error("the block links to the Levels page filtered to the world")
	}
	if !strings.Contains(renderedMain(t, doc), "71.4%") {
		t.Error("the block names the share that never took a job past level 50")
	}
}

func TestLevelsLedeNamesTheLargestBand(t *testing.T) {
	bands := census.LevelBands(census.DefaultExpansions, 100)
	row := func(counts ...int64) LevelRowView {
		d := census.LevelsFor(&contract.UIStatsSnapshot{LevelBands: bands, Summary: contract.StatsSummary{Total: 100, Levels: counts}}, contract.StatsScope{})
		return newLevelRow(d, "The whole game", "", false)
	}
	for _, tc := range []struct {
		name               string
		row                LevelRowView
		share, lede, aside string
	}{
		{"first", row(60, 10, 5, 5, 5, 5, 10), "60.0%", "never took a job past level 50", "10.0% have a job at the level cap."},
		{"cap", row(5, 5, 5, 5, 5, 5, 70), "70.0%", "have a job at the level cap", "5.0% never took a job past level 50."},
		{"middle", row(5, 5, 5, 5, 50, 20, 10), "50.0%", "peaked in Endwalker's levels, 81–90", "10.0% have a job at the level cap."},
	} {
		if tc.row.Lede.Share != tc.share || tc.row.Lede.Text != tc.lede || tc.row.Lede.Aside != tc.aside {
			t.Errorf("%s lede = %+v, want %s %q %q", tc.name, tc.row.Lede, tc.share, tc.lede, tc.aside)
		}
	}
}

func TestLevelsInNavSitemapAndMethodology(t *testing.T) {
	mux := levelsMux(t, levelsPageSnapshot(true))
	doc := levelsDoc(t, mux, "/ui/levels")
	nav := byID(doc, "primary-nav")
	var current string
	for _, a := range findAll(nav, byAtom(atom.A)) {
		if c, _ := attr(a, "aria-current"); c == "page" {
			current = textOf(a)
		}
	}
	if current != "Levels" {
		t.Errorf("current nav = %q, want Levels", current)
	}
	if body := serveMuxGet(t, mux, "/sitemap.xml").Body.String(); !strings.Contains(body, "<loc>"+testBaseURL+"/ui/levels</loc>") {
		t.Errorf("sitemap is missing the Levels page:\n%s", body)
	}

	method := levelsDoc(t, mux, "/ui/methodology")
	found := false
	for _, h := range findAll(method, byAtom(atom.H2)) {
		if strings.TrimSpace(textOf(h)) == "Levels" {
			found = true
		}
	}
	if !found {
		t.Fatal("Methodology needs a Levels section")
	}
	main := renderedMain(t, method)
	for _, text := range []string{"Heavensward", "51–60", "No job levels yet", "nightly", "after their first census", "not how far the character has got in the main scenario"} {
		if !strings.Contains(main, text) {
			t.Errorf("Methodology Levels section must explain %q", text)
		}
	}
	// The bands are nightly only: nothing moves them between rebuilds.
	if strings.Contains(main, "join their band") {
		t.Error("Methodology must not say characters join their band between rebuilds")
	}
}

func TestLevelsNumbersTableHasNoRangeForCharactersWithoutJobLevels(t *testing.T) {
	doc := levelsDoc(t, levelsMux(t, levelsPageSnapshot(true)), "/ui/levels")
	var cells []string
	for _, tr := range findAll(doc, byAtom(atom.Tr)) {
		if th := findAll(tr, byAtom(atom.Th)); len(th) > 0 && strings.TrimSpace(textOf(th[0])) == "No job levels yet" {
			for _, td := range findAll(tr, byAtom(atom.Td)) {
				cells = append(cells, strings.TrimSpace(textOf(td)))
			}
		}
	}
	if len(cells) < 2 {
		t.Fatalf("no 'No job levels yet' row with a range and a count, cells = %q", cells)
	}
	if cells[0] != "–" {
		t.Errorf("'No job levels yet' level range = %q, want –", cells[0])
	}
	if cells[1] != "200" {
		t.Errorf("'No job levels yet' count = %q, want 200", cells[1])
	}
}
