package ui

import (
	"net/http"
	"strings"
	"testing"
	"time"

	census "github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mockqueue "github.com/mihaiflorentin88/ffxiv-census/mock/queue"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var jobsBuiltAt = time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)

func jobsWorldRow(world string, characters, covered int64, counts map[uint8][2]int64) contract.WorldJobCounts {
	row := contract.WorldJobCounts{World: world, Characters: characters, Covered: covered}
	for _, j := range census.Jobs() {
		row.Levelled = append(row.Levelled, counts[j.ID][0])
		row.AtCap = append(row.AtCap, counts[j.ID][1])
	}
	return row
}

// jobsSnapshot is a hand-built snapshot: Balmung and Coeurl (Crystal, NA)
// and Ragnarok (Chaos, EU). Balmung is not fully covered yet unless full.
func jobsSnapshot(version int, withJobs, full bool) *contract.UIStatsSnapshot {
	generated := time.Now().UTC()
	balmungCovered := int64(8)
	if full {
		balmungCovered = 10
	}
	snapshot := &contract.UIStatsSnapshot{
		SchemaVersion:    version,
		GeneratedAt:      generated,
		ActivitySince:    generated.Add(-30 * 24 * time.Hour),
		MaxLevel:         100,
		SourceCharacters: 19,
		Summary:          contract.StatsSummary{Total: 19, Active: 5},
		Groups: []contract.ScopedGroupCount{
			{Dimension: "region", Key: "NA", Total: 15, Active: 4},
			{Dimension: "region", Key: "EU", Total: 4, Active: 1},
			{Dimension: "datacenter", Key: "Crystal", Total: 15, Active: 4},
			{Dimension: "datacenter", Key: "Chaos", Total: 4, Active: 1},
			{Dimension: "world", Key: "Balmung", Total: 10, Active: 3},
			{Dimension: "world", Key: "Coeurl", Total: 5, Active: 1},
			{Dimension: "world", Key: "Ragnarok", Total: 4, Active: 1},
		},
	}
	if withJobs {
		section := &contract.StatsJobsSection{BuiltAt: jobsBuiltAt}
		for _, j := range census.Jobs() {
			section.JobIDs = append(section.JobIDs, int(j.ID))
		}
		section.Worlds = []contract.WorldJobCounts{
			jobsWorldRow("Balmung", 10, balmungCovered, map[uint8][2]int64{19: {6, 3}, 24: {4, 2}, 25: {5, 4}, 36: {2, 1}, 15: {3, 1}, 22: {1, 0}}),
			jobsWorldRow("Coeurl", 5, 5, map[uint8][2]int64{19: {1, 1}, 22: {3, 2}}),
			jobsWorldRow("Ragnarok", 4, 4, map[uint8][2]int64{28: {2, 2}, 21: {1, 0}}),
		}
		snapshot.Jobs = section
	}
	return snapshot
}

func jobsMux(t *testing.T, snapshot *contract.UIStatsSnapshot) *http.ServeMux {
	t.Helper()
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(snapshot), time.Hour, 24*time.Hour)
	ctrl := NewUIController(svc, mockqueue.NewFake(), stats, testBaseURL)
	mux := http.NewServeMux()
	ctrl.RegisterRoutes(mux)
	return mux
}

// armouryTiles returns each tile's flattened text keyed by its abbreviation.
func armouryTiles(t *testing.T, root *html.Node) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tile := range findAll(root, byClass("armoury-tile")) {
		abbr := findAll(tile, byClass("armoury-abbr"))
		if len(abbr) != 1 {
			t.Fatalf("every tile prints its abbreviation once, got %d", len(abbr))
		}
		out[strings.TrimSpace(textOf(abbr[0]))] = strings.Join(strings.Fields(textOf(tile)), " ")
	}
	return out
}

func TestJobsPageShowsTheArmoury(t *testing.T) {
	mux := jobsMux(t, jobsSnapshot(3, true, true))
	rec := serveMuxGet(t, mux, "/ui/jobs")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	doc := parseHTML(t, rec.Body.String())
	assertNoInlineStylesOrEmoji(t, doc)

	tiles := armouryTiles(t, doc)
	if len(tiles) != len(census.Jobs()) {
		t.Fatalf("tiles = %d, want one per job (%d)", len(tiles), len(census.Jobs()))
	}
	for abbr, want := range map[string][]string{
		"PLD": {"Paladin", "7"},
		"BLU": {"Blue Mage", "2"},
		"FSH": {"Fisher", "0"},
	} {
		for _, w := range want {
			if !strings.Contains(tiles[abbr], w) {
				t.Errorf("tile %s = %q, want %q", abbr, tiles[abbr], w)
			}
		}
	}
	for _, role := range census.Roles() {
		found := false
		for _, h := range findAll(doc, byClass("armoury-role-name")) {
			if strings.TrimSpace(textOf(h)) == role.Name {
				found = true
			}
		}
		if !found {
			t.Errorf("missing the %s role heading", role.Name)
		}
	}
	if got := currentOption(switchOptions(t, doc, "Count jobs")); got != "Levelled" {
		t.Errorf("current measure = %q, want Levelled", got)
	}

	main := strings.Join(strings.Fields(textOf(findAll(doc, byAtom(atom.Main))[0])), " ")
	for _, text := range []string{
		"Rebuilt nightly, last on 24 October 2026",
		"Party balance at the level cap",
		"Crystal",
		"Chaos",
		"Show the numbers",
	} {
		if !strings.Contains(main, text) {
			t.Errorf("page is missing %q", text)
		}
	}
	if strings.Contains(main, "older characters are still being added") {
		t.Error("full coverage must not show the coverage line")
	}
	if len(findAll(doc, byAtom(atom.Table))) < 2 {
		t.Error("the Armoury and the role balance each need a data table")
	}
	if got := extractHeadAttribute(t, rec.Body.String(), `<link rel="canonical" href="([^"]*)">`); got != testBaseURL+"/ui/jobs" {
		t.Errorf("canonical = %q", got)
	}
	if got := extractHeadAttribute(t, rec.Body.String(), `<meta property="og:url" content="([^"]*)">`); got != testBaseURL+"/ui/jobs" {
		t.Errorf("og:url = %q", got)
	}
	nav := findAll(doc, func(n *html.Node) bool {
		v, _ := attr(n, "aria-current")
		return n.DataAtom == atom.A && v == "page"
	})
	if len(nav) == 0 || strings.TrimSpace(textOf(nav[0])) != "Jobs" {
		t.Error("the Jobs nav item must be current")
	}
}

func TestJobsPageAtCapSwitchSwapsTheArmoury(t *testing.T) {
	mux := jobsMux(t, jobsSnapshot(3, true, true))
	page := parseHTML(t, serveMuxGet(t, mux, "/ui/jobs?dc=crystal").Body.String())
	options := switchOptions(t, page, "Count jobs")
	if href, _ := attr(options[1], "href"); href != "/ui/jobs?dc=Crystal&measure=at-cap" {
		t.Errorf("at-cap option href = %q, want it to keep the filter", href)
	}
	target, _ := attr(options[1], "hx-target")
	regionID := strings.TrimPrefix(target, "#")

	rec := switchGet(t, mux, "/ui/jobs?dc=Crystal&measure=at-cap", map[string]string{"HX-Request": "true", "HX-Target": regionID})
	body := rec.Body.String()
	if strings.Contains(body, "<html") {
		t.Fatal("an HTMX switch request must get the region fragment, not the page")
	}
	frag := parseHTML(t, body)
	region := byID(frag, regionID)
	if region == nil {
		t.Fatalf("fragment must be the region #%s itself", regionID)
	}
	if announce, _ := attr(region, "data-switch-announce"); !strings.Contains(announce, "level cap") {
		t.Errorf("announcement %q must say what changed", announce)
	}
	if got := currentOption(switchOptions(t, frag, "Count jobs")); got != "At the level cap" {
		t.Errorf("swapped current option = %q", got)
	}
	tiles := armouryTiles(t, frag)
	if !strings.Contains(tiles["PLD"], "4") || !strings.Contains(tiles["BLM"], "4") || !strings.Contains(tiles["DRG"], "2") {
		t.Errorf("at-cap tiles = PLD %q BLM %q DRG %q, want 4, 4, 2", tiles["PLD"], tiles["BLM"], tiles["DRG"])
	}

	// An unknown measure falls back to levelled.
	doc := parseHTML(t, serveMuxGet(t, mux, "/ui/jobs?measure=bogus").Body.String())
	if got := currentOption(switchOptions(t, doc, "Count jobs")); got != "Levelled" {
		t.Errorf("unknown measure current option = %q, want Levelled", got)
	}
}

func TestJobsPageCoverageLine(t *testing.T) {
	mux := jobsMux(t, jobsSnapshot(3, true, false))
	main := renderedMain(t, parseHTML(t, serveMuxGet(t, mux, "/ui/jobs").Body.String()))
	if !strings.Contains(main, "Based on 17 of 19 characters; older characters are still being added.") {
		t.Errorf("partial coverage must say so, got %s", main)
	}
	// Ragnarok is fully covered even while the backfill runs elsewhere.
	eu := renderedMain(t, parseHTML(t, serveMuxGet(t, mux, "/ui/jobs?region=EU").Body.String()))
	if strings.Contains(eu, "older characters are still being added") {
		t.Error("a fully covered selection shows no coverage line")
	}
}

func TestJobsPageFiltersAndEmptySelection(t *testing.T) {
	mux := jobsMux(t, jobsSnapshot(3, true, true))
	rec := serveMuxGet(t, mux, "/ui/jobs?world=coeurl")
	doc := parseHTML(t, rec.Body.String())
	tiles := armouryTiles(t, doc)
	if !strings.Contains(tiles["DRG"], "3") {
		t.Errorf("Coeurl DRG = %q, want 3", tiles["DRG"])
	}
	if got := extractHeadAttribute(t, rec.Body.String(), `<link rel="canonical" href="([^"]*)">`); got != testBaseURL+"/ui/jobs" {
		t.Errorf("filtered canonical = %q, want the clean path", got)
	}
	if !strings.Contains(renderedMain(t, doc), "Coeurl") {
		t.Error("the scope must name the selected world")
	}

	empty := parseHTML(t, serveMuxGet(t, mux, "/ui/jobs?world=Cerberus").Body.String())
	if len(findAll(empty, byClass("armoury-tile"))) != 0 {
		t.Error("an empty selection must not show empty tiles")
	}
	if !strings.Contains(renderedMain(t, empty), "No job figures for this selection.") || !linksTo(empty, "/ui/jobs") {
		t.Error("an empty selection explains itself and links back")
	}
}

func TestJobsPageWaitsForTheNextRefresh(t *testing.T) {
	for name, snapshot := range map[string]*contract.UIStatsSnapshot{
		"v2":             jobsSnapshot(2, false, true),
		"v3 without job": jobsSnapshot(3, false, true),
	} {
		t.Run(name, func(t *testing.T) {
			rec := serveMuxGet(t, jobsMux(t, snapshot), "/ui/jobs")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			doc := parseHTML(t, rec.Body.String())
			if !strings.Contains(renderedMain(t, doc), "Job figures arrive with the next refresh.") {
				t.Error("missing the next-refresh state")
			}
			if len(findAll(doc, byClass("armoury-tile"))) != 0 {
				t.Error("no tiles before the figures exist")
			}
		})
	}
}

func TestWorldDetailMostLevelledJobs(t *testing.T) {
	mux := jobsMux(t, jobsSnapshot(3, true, true))
	doc := parseHTML(t, serveMuxGet(t, mux, "/ui/worlds/Balmung").Body.String())
	lists := findAll(doc, byClass("top-jobs"))
	if len(lists) != 1 {
		t.Fatalf("world detail needs one most-levelled jobs list, got %d", len(lists))
	}
	items := findAll(lists[0], byAtom(atom.Li))
	if len(items) != 5 {
		t.Fatalf("top jobs = %d, want five", len(items))
	}
	if first := strings.Join(strings.Fields(textOf(items[0])), " "); !strings.Contains(first, "PLD") || !strings.Contains(first, "Paladin") {
		t.Errorf("first job = %q, want Paladin", first)
	}
	if !linksTo(doc, "/ui/jobs?world=Balmung") {
		t.Error("the block must link to the Jobs page filtered to the world")
	}

	v2 := renderedMain(t, parseHTML(t, serveMuxGet(t, jobsMux(t, jobsSnapshot(2, false, true)), "/ui/worlds/Balmung").Body.String()))
	if !strings.Contains(v2, "Job figures arrive with the next refresh.") {
		t.Error("before the job section exists the world block waits for the next refresh")
	}
}

func TestJobsInSitemapAndMethodology(t *testing.T) {
	mux := jobsMux(t, jobsSnapshot(3, true, true))
	if body := serveMuxGet(t, mux, "/sitemap.xml").Body.String(); !strings.Contains(body, "<loc>"+testBaseURL+"/ui/jobs</loc>") {
		t.Errorf("sitemap is missing the Jobs page:\n%s", body)
	}
	doc := parseHTML(t, serveMuxGet(t, mux, "/ui/methodology").Body.String())
	found := false
	for _, h := range findAll(doc, byAtom(atom.H2)) {
		if strings.TrimSpace(textOf(h)) == "Jobs" {
			found = true
		}
	}
	if !found {
		t.Error("Methodology needs a Jobs section")
	}
	main := renderedMain(t, doc)
	for _, text := range []string{"Arcanist", "Blue Mage", "nightly"} {
		if !strings.Contains(main, text) {
			t.Errorf("Methodology Jobs section must explain %q", text)
		}
	}
}
