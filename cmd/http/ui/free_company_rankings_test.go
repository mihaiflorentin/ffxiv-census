package ui

import (
	"fmt"
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

var fcBuiltAt = time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)

// fcRankingsBoards: twelve Balmung (Crystal, NA) companies, "Company 01"
// with 120 members down to "Company 12" with 10, active members rising the
// other way; and one Ragnarok (Chaos, EU) company.
func fcRankingsBoards() *contract.StatsFreeCompanies {
	var balmung []contract.FreeCompanyCount
	for i := 1; i <= 12; i++ {
		balmung = append(balmung, contract.FreeCompanyCount{
			ID: fmt.Sprintf("90000000000000%02d", i), Name: fmt.Sprintf("Company %02d", i),
			Members: int64(130 - i*10), Active: int64(i),
		})
	}
	return &contract.StatsFreeCompanies{
		BuiltAt: fcBuiltAt,
		Worlds: []contract.WorldFreeCompanies{
			{World: "Balmung", Companies: balmung},
			{World: "Ragnarok", Companies: []contract.FreeCompanyCount{{ID: "9100", Name: "Chaos Crew", Members: 40, Active: 30}}},
		},
	}
}

// fcRankingsMux serves a version snapshot; boards nil means the Free Company
// row is missing.
func fcRankingsMux(t *testing.T, version int, boards *contract.StatsFreeCompanies) *http.ServeMux {
	t.Helper()
	snapshot := rankingsSnapshot(version)
	if version >= 3 {
		snapshot.FreeCompaniesBuiltAt = fcBuiltAt
		for i, g := range snapshot.Groups {
			if g.Scope == (contract.StatsScope{}) && g.Key == "Balmung" {
				snapshot.Groups[i].InFreeCompany = 400
			}
		}
		snapshot.Summary.InFreeCompany = 400
	}
	repo := mockrepo.NewUIStatsFake(snapshot)
	repo.FreeCompanies = boards
	if boards == nil {
		repo.FreeCompaniesErr = census.ErrUIStatsUnavailable
	}
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	stats := census.NewUIStatsService(repo, time.Hour, 24*time.Hour)
	ctrl := NewUIController(svc, mockqueue.NewFake(), stats, testBaseURL)
	mux := http.NewServeMux()
	ctrl.RegisterRoutes(mux)
	return mux
}

// fcRungLinks returns a rung's world link and Lodestone link.
func fcRungLinks(t *testing.T, li *html.Node) (world, lodestone string) {
	t.Helper()
	if n := findAll(li, byClass("ladder-note")); len(n) == 1 {
		if a := findAll(n[0], byAtom(atom.A)); len(a) == 1 {
			world, _ = attr(a[0], "href")
		}
	}
	if a := findAll(li, byClass("ladder-external")); len(a) == 1 {
		lodestone, _ = attr(a[0], "href")
	}
	return world, lodestone
}

func TestFreeCompanyRankingsDefaultToTheLargestCompanies(t *testing.T) {
	rec := serveMuxGet(t, fcRankingsMux(t, 3, fcRankingsBoards()), "/ui/rankings/free-companies")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	doc := parseHTML(t, rec.Body.String())
	assertNoInlineStylesOrEmoji(t, doc)

	board := byID(doc, "fc-board")
	if board == nil {
		t.Fatal("no #fc-board region")
	}
	headings := findAll(board, func(n *html.Node) bool { _, ok := attr(n, "data-switch-heading"); return ok })
	if len(headings) != 1 || flatText(headings[0]) != "Largest Free Companies" {
		t.Fatalf("board heading = %v", headings)
	}
	if got, _ := attr(board, "data-switch-announce"); got != "Largest Free Companies, every world" {
		t.Errorf("announcement = %q", got)
	}
	if got := currentOption(switchOptions(t, doc, "Rank Free Companies by")); got != "Most members" {
		t.Errorf("current measure = %q", got)
	}

	rungs := ladderEntries(t, board)
	if len(rungs) != 13 {
		t.Fatalf("ladder has %d rungs, want 13", len(rungs))
	}
	want := ladderEntry{rank: "Rank 1", name: "Company 01", note: "Balmung", value: "120 members"}
	if rungs[0] != want {
		t.Errorf("leader = %+v, want %+v", rungs[0], want)
	}
	if rungs[1].name != "Company 02" || rungs[2].name != "Company 03" || rungs[8].name != "Chaos Crew" {
		t.Errorf("order = %v (Chaos Crew ties Company 09 on members and wins on active)", rungs[:9])
	}
	li := findAll(board, byClass("ladder-rung"))
	world, lodestone := fcRungLinks(t, li[0])
	if world != "/ui/worlds/Balmung" {
		t.Errorf("world link = %q", world)
	}
	if lodestone != "https://na.finalfantasyxiv.com/lodestone/freecompany/9000000000000001/" {
		t.Errorf("Lodestone link = %q", lodestone)
	}
	if detail := flatText(findAll(li[0], byClass("ladder-detail"))[0]); detail != "1 active" {
		t.Errorf("detail = %q, want the other measure", detail)
	}

	// The top ten show; the rest wait in a native disclosure.
	more := findAll(board, byClass("ladder-more"))
	if len(more) != 1 {
		t.Fatalf("want one ladder-more, got %d", len(more))
	}
	if summary := findAll(more[0], byAtom(atom.Summary)); len(summary) != 1 || flatText(summary[0]) != "Show all 13 Free Companies" {
		t.Errorf("summary = %v", summary)
	}

	text := flatText(board)
	for _, line := range []string{"Members seen by the census", "Rebuilt nightly"} {
		if !strings.Contains(text, line) {
			t.Errorf("board lacks %q", line)
		}
	}
	if strings.Contains(text, "hide their achievements") {
		t.Error("the hidden-achievements note belongs to the active board")
	}
}

func TestFreeCompanyRankingsByActiveMembersKeepTheFilter(t *testing.T) {
	mux := fcRankingsMux(t, 3, fcRankingsBoards())
	doc := parseHTML(t, serveMuxGet(t, mux, "/ui/rankings/free-companies?measure=active&world=balmung").Body.String())
	board := byID(doc, "fc-board")
	if got, _ := attr(board, "data-switch-announce"); got != "Most active Free Companies, Balmung" {
		t.Errorf("announcement = %q", got)
	}
	rungs := ladderEntries(t, board)
	if len(rungs) != 12 || rungs[0].name != "Company 12" || rungs[0].value != "12 active" {
		t.Fatalf("rungs = %+v", rungs)
	}
	if detail := flatText(findAll(findAll(board, byClass("ladder-rung"))[0], byClass("ladder-detail"))[0]); detail != "10 members" {
		t.Errorf("detail = %q", detail)
	}
	if !strings.Contains(flatText(board), "40.0% of characters on Balmung belong to a Free Company.") {
		t.Error("the board gives the share of characters in a Free Company")
	}
	if !strings.Contains(flatText(board), "hide their achievements") {
		t.Error("the active board notes that hidden achievements can't count as active")
	}

	options := switchOptions(t, doc, "Rank Free Companies by")
	if got := currentOption(options); got != "Most active members" {
		t.Errorf("current = %q", got)
	}
	for _, a := range options {
		href, _ := attr(a, "href")
		want := map[string]string{
			"Most members":        "/ui/rankings/free-companies?world=Balmung",
			"Most active members": "/ui/rankings/free-companies?measure=active&world=Balmung",
		}[textOf(a)]
		if href != want {
			t.Errorf("%s: href %q, want %q", textOf(a), href, want)
		}
		if target, _ := attr(a, "hx-target"); target != "#fc-board" {
			t.Errorf("%s: hx-target %q", textOf(a), target)
		}
	}
	// The filter bar keeps the measure.
	hidden := findAll(doc, func(n *html.Node) bool {
		name, _ := attr(n, "name")
		typ, _ := attr(n, "type")
		return n.DataAtom == atom.Input && typ == "hidden" && name == "measure"
	})
	if len(hidden) != 1 {
		t.Errorf("filter form must keep measure=active, got %d hidden inputs", len(hidden))
	}
}

func TestFreeCompanyRankingsFilterByRegionAndDatacenter(t *testing.T) {
	mux := fcRankingsMux(t, 3, fcRankingsBoards())
	for target, leader := range map[string]string{
		"/ui/rankings/free-companies?region=eu":   "Chaos Crew",
		"/ui/rankings/free-companies?dc=crystal":  "Company 01",
		"/ui/rankings/free-companies?world=Lich":  "",
		"/ui/rankings/free-companies?measure=bad": "Company 01",
	} {
		doc := parseHTML(t, serveMuxGet(t, mux, target).Body.String())
		rungs := ladderEntries(t, byID(doc, "fc-board"))
		if leader == "" {
			if len(rungs) != 0 || !strings.Contains(flatText(byID(doc, "fc-board")), "No Free Companies to rank") {
				t.Errorf("%s: want the empty state, got %+v", target, rungs)
			}
			continue
		}
		if len(rungs) == 0 || rungs[0].name != leader {
			t.Errorf("%s: leader = %+v, want %s", target, rungs, leader)
		}
	}
}

func TestFreeCompanyRankingsHTMXSwapRendersOnlyTheBoard(t *testing.T) {
	mux := fcRankingsMux(t, 3, fcRankingsBoards())
	rec := switchGet(t, mux, "/ui/rankings/free-companies?measure=active", map[string]string{"HX-Request": "true", "HX-Target": "fc-board"})
	body := rec.Body.String()
	if strings.Contains(body, "<header") {
		t.Fatal("an HTMX switch request must get the board alone")
	}
	if !strings.Contains(body, `id="fc-board"`) || !strings.Contains(body, "Most active Free Companies") {
		t.Errorf("fragment = %s", body)
	}
}

// The boards are rebuilt nightly, so the active members copy states the
// window the boards were built with, not the main snapshot's.
func TestFreeCompanyActiveCopyUsesTheBoardsActivityWindow(t *testing.T) {
	boards := fcRankingsBoards()
	boards.ActivitySince = fcBuiltAt.Add(-75 * 24 * time.Hour)
	mux := fcRankingsMux(t, 3, boards)
	body := serveMuxGet(t, mux, "/ui/rankings/free-companies?measure=active").Body.String()
	if !strings.Contains(body, "public achievement in the last 75 days") {
		t.Errorf("active members copy must read the boards' 75-day window; body:\n%s", body)
	}
}

func TestFreeCompanyRankingsWaitForTheNextRefresh(t *testing.T) {
	for name, mux := range map[string]*http.ServeMux{
		"version 2 snapshot": fcRankingsMux(t, 2, fcRankingsBoards()),
		"missing row":        fcRankingsMux(t, 3, nil),
	} {
		rec := serveMuxGet(t, mux, "/ui/rankings/free-companies")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", name, rec.Code)
		}
		doc := parseHTML(t, rec.Body.String())
		board := byID(doc, "fc-board")
		if len(ladderEntries(t, board)) != 0 {
			t.Errorf("%s: no ladder without boards", name)
		}
		if !strings.Contains(flatText(board), "Free Company figures arrive with the next refresh.") {
			t.Errorf("%s: board lacks the next-refresh state", name)
		}
	}
}

func TestFreeCompanyRankingsAreLinkedFromTheRankingsHeader(t *testing.T) {
	doc := parseHTML(t, serveMuxGet(t, fcRankingsMux(t, 3, fcRankingsBoards()), "/ui/rankings/free-companies").Body.String())
	navs := findAll(doc, func(n *html.Node) bool {
		v, _ := attr(n, "aria-label")
		return n.DataAtom == atom.Nav && v == "Rankings boards"
	})
	if len(navs) != 1 {
		t.Fatalf("want one Rankings boards nav, got %d", len(navs))
	}
	links := findAll(navs[0], byAtom(atom.A))
	if len(links) != 2 || textOf(links[1]) != "Free Companies" {
		t.Fatalf("board links = %d", len(links))
	}
	if v, _ := attr(links[1], "aria-current"); v != "page" {
		t.Error("the Free Company board is marked current")
	}
}

func TestWorldDetailShowsTheFiveLargestFreeCompanies(t *testing.T) {
	mux := fcRankingsMux(t, 3, fcRankingsBoards())
	doc := parseHTML(t, serveMuxGet(t, mux, "/ui/worlds/Balmung").Body.String())
	section := byID(doc, "top-free-companies")
	if section == nil {
		t.Fatal("no #top-free-companies section")
	}
	rungs := ladderEntries(t, section)
	if len(rungs) != 5 || rungs[0].name != "Company 01" || rungs[4].name != "Company 05" {
		t.Errorf("top five = %+v", rungs)
	}
	if !strings.Contains(flatText(section), "40.0% of characters on Balmung belong to a Free Company.") {
		t.Error("the world page gives the share of characters in a Free Company")
	}
	var full string
	for _, a := range findAll(section, byAtom(atom.A)) {
		if textOf(a) == "Every Free Company on Balmung" {
			full, _ = attr(a, "href")
		}
	}
	if full != "/ui/rankings/free-companies?world=Balmung" {
		t.Errorf("full board link = %q", full)
	}

	pending := parseHTML(t, serveMuxGet(t, fcRankingsMux(t, 2, nil), "/ui/worlds/Balmung").Body.String())
	if !strings.Contains(flatText(byID(pending, "top-free-companies")), "Free Company figures arrive with the next refresh.") {
		t.Error("world page waits for the boards on a version 2 snapshot")
	}
	if n := len(findAll(byID(pending, "top-free-companies"), byClass("fc-share"))); n != 0 {
		t.Errorf("a version 2 world page shows %d Free Company shares, want none", n)
	}
}

func TestWorldDetailShowsTheShareInAFreeCompany(t *testing.T) {
	doc := parseHTML(t, serveMuxGet(t, fcRankingsMux(t, 3, fcRankingsBoards()), "/ui/worlds/Balmung").Body.String())
	shares := findAll(byID(doc, "top-free-companies"), byClass("fc-share"))
	if len(shares) != 1 {
		t.Fatalf("want one Free Company share next to the top five, got %d", len(shares))
	}
	if got := flatText(shares[0]); got != "In a Free Company: 40.0%" {
		t.Errorf("share = %q, want %q", got, "In a Free Company: 40.0%")
	}
}
