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

// grandCompanySnapshot is a hand-built v3 snapshot: 100 characters globally,
// an EU split, and Balmung (Crystal, NA) with its own split.
func grandCompanySnapshot(version int) *contract.UIStatsSnapshot {
	generated := time.Now().UTC()
	eu := contract.StatsScope{Region: "EU"}
	balmung := contract.StatsScope{World: "Balmung"}
	groups := []contract.ScopedGroupCount{
		{Dimension: "world", Key: "Balmung", Total: 40, Active: 12},
		{Dimension: "race", Key: "Roegadyn", Total: 10, Active: 3},
		{Dimension: "race", Key: "Hyur", Total: 8, Active: 1},
		{Scope: balmung, Dimension: "race", Key: "Roegadyn", Total: 40, Active: 12},
	}
	if version >= 3 {
		groups = append(groups,
			contract.ScopedGroupCount{Dimension: "grand_company", Key: "Maelstrom", Total: 50, Active: 20},
			contract.ScopedGroupCount{Dimension: "grand_company", Key: "Order of the Twin Adder", Total: 30, Active: 6},
			contract.ScopedGroupCount{Dimension: "grand_company", Key: "Immortal Flames", Total: 15, Active: 9},
			contract.ScopedGroupCount{Dimension: "grand_company", Key: "None", Total: 5, Active: 0},
			contract.ScopedGroupCount{Scope: eu, Dimension: "grand_company", Key: "Immortal Flames", Total: 3, Active: 1},
			contract.ScopedGroupCount{Scope: eu, Dimension: "grand_company", Key: "None", Total: 1, Active: 1},
			contract.ScopedGroupCount{Scope: balmung, Dimension: "grand_company", Key: "Maelstrom", Total: 30, Active: 10},
			contract.ScopedGroupCount{Scope: balmung, Dimension: "grand_company", Key: "Order of the Twin Adder", Total: 10, Active: 2},
			contract.ScopedGroupCount{Dimension: "grand_company_race", Key: "Maelstrom|Roegadyn", Total: 8, Active: 2},
			contract.ScopedGroupCount{Dimension: "grand_company_race", Key: "Order of the Twin Adder|Roegadyn", Total: 2, Active: 1},
			contract.ScopedGroupCount{Dimension: "grand_company_race", Key: "Immortal Flames|Hyur", Total: 4, Active: 1},
			contract.ScopedGroupCount{Dimension: "grand_company_race", Key: "None|Hyur", Total: 4, Active: 0},
		)
	}
	return &contract.UIStatsSnapshot{
		SchemaVersion:    version,
		GeneratedAt:      generated,
		ActivitySince:    generated.Add(-30 * 24 * time.Hour),
		MaxLevel:         100,
		SourceCharacters: 100,
		Summary:          contract.StatsSummary{Total: 100, Active: 35},
		Groups:           groups,
	}
}

func grandCompanyMux(t *testing.T, version int) *http.ServeMux {
	t.Helper()
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(grandCompanySnapshot(version)), time.Hour, 24*time.Hour)
	ctrl := NewUIController(svc, mockqueue.NewFake(), stats, testBaseURL)
	mux := http.NewServeMux()
	ctrl.RegisterRoutes(mux)
	return mux
}

func byClass(class string) func(*html.Node) bool {
	return func(n *html.Node) bool {
		v, _ := attr(n, "class")
		for _, c := range strings.Fields(v) {
			if c == class {
				return true
			}
		}
		return false
	}
}

// standards returns the banner of each company, keyed by its printed name.
func standards(t *testing.T, doc *html.Node) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, banner := range findAll(doc, byClass("standard")) {
		names := findAll(banner, byClass("standard-name"))
		if len(names) != 1 {
			t.Fatalf("every standard prints its company name once, got %d", len(names))
		}
		out[strings.TrimSpace(textOf(names[0]))] = strings.Join(strings.Fields(textOf(banner)), " ")
	}
	return out
}

func TestGrandCompaniesPageRaisesTheStandards(t *testing.T) {
	mux := grandCompanyMux(t, 3)
	rec := serveMuxGet(t, mux, "/ui/grand-companies")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	doc := parseHTML(t, rec.Body.String())
	assertNoInlineStylesOrEmoji(t, doc)

	got := standards(t, doc)
	want := map[string]string{
		"Maelstrom":               "50.0%",
		"Order of the Twin Adder": "30.0%",
		"Immortal Flames":         "15.0%",
		"No Grand Company":        "5.0%",
	}
	if len(got) != len(want) {
		t.Fatalf("standards = %v, want one per company and one for no company", got)
	}
	for name, share := range want {
		if !strings.Contains(got[name], share) {
			t.Errorf("standard %q = %q, want share %s", name, got[name], share)
		}
	}
	none := findAll(doc, byClass("standard-none"))
	if len(none) != 1 || !strings.Contains(textOf(none[0]), "No Grand Company") {
		t.Errorf("no company must be its own outline standard, got %d", len(none))
	}
	for _, slug := range []string{"maelstrom", "twin-adder", "immortal-flames"} {
		if len(findAll(doc, byClass("standard-"+slug))) != 1 {
			t.Errorf("missing the %s standard (its colour class)", slug)
		}
	}

	main := renderedMain(t, doc)
	for _, text := range []string{
		"Show the numbers",
		"40.0%", // Maelstrom active share: 20 of 50
		"How each race leans",
	} {
		if !strings.Contains(main, text) {
			t.Errorf("page is missing %q", text)
		}
	}
	if len(findAll(doc, byAtom(atom.Table))) < 2 {
		t.Error("the split and the race lean each need a data table")
	}
	assertChartsThemedAndDescribed(t, doc, 1)
	if got := extractHeadAttribute(t, rec.Body.String(), `<link rel="canonical" href="([^"]*)">`); got != testBaseURL+"/ui/grand-companies" {
		t.Errorf("canonical = %q", got)
	}
}

func TestGrandCompaniesPageFiltersAndCanonicalises(t *testing.T) {
	mux := grandCompanyMux(t, 3)
	rec := serveMuxGet(t, mux, "/ui/grand-companies?region=eu")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	doc := parseHTML(t, rec.Body.String())
	got := standards(t, doc)
	if !strings.Contains(got["Immortal Flames"], "75.0%") || !strings.Contains(got["Maelstrom"], "0.0%") {
		t.Errorf("EU standards = %v, want Immortal Flames 75.0%% and Maelstrom 0.0%%", got)
	}
	if got := extractHeadAttribute(t, rec.Body.String(), `<link rel="canonical" href="([^"]*)">`); got != testBaseURL+"/ui/grand-companies" {
		t.Errorf("filtered canonical = %q, want the clean path", got)
	}
	if !linksTo(doc, "/ui/grand-companies") {
		t.Error("the region chip must link back to the unfiltered page")
	}
	if main := renderedMain(t, doc); !strings.Contains(main, "Europe") {
		t.Error("the scope must name the selected region")
	}
}

func TestGrandCompaniesPageEmptySelection(t *testing.T) {
	mux := grandCompanyMux(t, 3)
	doc := parseHTML(t, serveMuxGet(t, mux, "/ui/grand-companies?world=Cerberus").Body.String())
	if len(findAll(doc, byClass("standard"))) != 0 {
		t.Error("an empty selection must not raise empty standards")
	}
	main := renderedMain(t, doc)
	if !strings.Contains(main, "No Grand Company data for this selection.") {
		t.Errorf("missing the empty state, got %s", main)
	}
	if !linksTo(doc, "/ui/grand-companies") {
		t.Error("the empty state must link back to the whole game")
	}
}

func TestGrandCompaniesPageOnV2SnapshotWaitsForNextRefresh(t *testing.T) {
	mux := grandCompanyMux(t, 2)
	rec := serveMuxGet(t, mux, "/ui/grand-companies")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	doc := parseHTML(t, rec.Body.String())
	main := renderedMain(t, doc)
	if !strings.Contains(main, "Grand Company figures arrive with the next refresh.") {
		t.Errorf("v2 snapshot must show the next-refresh state, got %s", main)
	}
	if len(findAll(doc, byClass("standard"))) != 0 {
		t.Error("no standards before the figures exist")
	}
}

func TestWorldDetailGrandCompanyBand(t *testing.T) {
	mux := grandCompanyMux(t, 3)
	doc := parseHTML(t, serveMuxGet(t, mux, "/ui/worlds/Balmung").Body.String())
	bands := findAll(doc, byClass("gc-band"))
	if len(bands) != 1 {
		t.Fatalf("world detail needs one Grand Company band, got %d", len(bands))
	}
	text := strings.Join(strings.Fields(textOf(bands[0].Parent)), " ")
	for _, want := range []string{"Maelstrom", "75.0%", "Order of the Twin Adder", "25.0%"} {
		if !strings.Contains(text, want) {
			t.Errorf("band text %q is missing %q", text, want)
		}
	}
	if !linksTo(doc, "/ui/grand-companies?world=Balmung") {
		t.Error("the band must link to the Grand Companies page filtered to the world")
	}

	v2 := renderedMain(t, parseHTML(t, serveMuxGet(t, grandCompanyMux(t, 2), "/ui/worlds/Balmung").Body.String()))
	if !strings.Contains(v2, "Grand Company figures arrive with the next refresh.") {
		t.Error("on a v2 snapshot the world band waits for the next refresh")
	}
}

func TestGrandCompaniesInSitemapAndMethodology(t *testing.T) {
	mux := grandCompanyMux(t, 3)
	if body := serveMuxGet(t, mux, "/sitemap.xml").Body.String(); !strings.Contains(body, "<loc>"+testBaseURL+"/ui/grand-companies</loc>") {
		t.Errorf("sitemap is missing the Grand Companies page:\n%s", body)
	}
	doc := parseHTML(t, serveMuxGet(t, mux, "/ui/methodology").Body.String())
	found := false
	for _, h := range findAll(doc, byAtom(atom.H2)) {
		if strings.TrimSpace(textOf(h)) == "Grand Companies" {
			found = true
		}
	}
	if !found {
		t.Error("Methodology needs a Grand Companies section")
	}
}
