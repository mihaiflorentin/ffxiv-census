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

// rankingWorld is one world of the rankings fixture.
type rankingWorld struct {
	name, dc, region             string
	total, active, atCap, newish int64
	maelstrom                    int64
}

// rankingWorlds: twelve NA worlds and two EU worlds. Growth per 1,000 falls
// with the list order (Adamantoise 60, Cactuar 55, ... ), Lich is the largest
// world, and Bismarck is too small for the rate boards.
var rankingWorlds = []rankingWorld{
	{"Adamantoise", "Aether", "NA", 1000, 500, 100, 60, 500},
	{"Cactuar", "Aether", "NA", 1000, 450, 200, 55, 450},
	{"Faerie", "Aether", "NA", 1000, 400, 300, 50, 400},
	{"Gilgamesh", "Aether", "NA", 1000, 350, 400, 45, 350},
	{"Jenova", "Aether", "NA", 1000, 300, 500, 40, 300},
	{"Midgardsormr", "Aether", "NA", 1000, 250, 600, 35, 250},
	{"Sargatanas", "Aether", "NA", 1000, 200, 700, 30, 200},
	{"Siren", "Aether", "NA", 1000, 150, 800, 25, 150},
	{"Balmung", "Crystal", "NA", 1000, 100, 50, 20, 100},
	{"Brynhildr", "Crystal", "NA", 1000, 90, 40, 15, 90},
	{"Coeurl", "Crystal", "NA", 1000, 80, 30, 10, 80},
	{"Diabolos", "Crystal", "NA", 1000, 70, 20, 5, 70},
	{"Lich", "Light", "EU", 2000, 600, 900, 4, 1200},
	{"Bismarck", "Materia", "OCE", 10, 10, 10, 9, 10},
}

func rankingsSnapshot(version int) *contract.UIStatsSnapshot {
	generated := time.Now().UTC()
	day := generated.Truncate(24*time.Hour).AddDate(0, 0, -2).Format("2006-01-02")
	s := &contract.UIStatsSnapshot{
		SchemaVersion: version,
		GeneratedAt:   generated,
		ActivitySince: generated.Add(-30 * 24 * time.Hour),
		MaxLevel:      100,
	}
	dcs := map[string]*contract.ScopedGroupCount{}
	regions := map[string]*contract.ScopedGroupCount{}
	var dcOrder, regionOrder []string
	for _, w := range rankingWorlds {
		g := contract.ScopedGroupCount{Dimension: "world", Key: w.name, Total: w.total, Active: w.active}
		if version >= 3 {
			g.MaxLevel, g.Private = w.atCap, w.total/10
		}
		if version >= 4 {
			// Schema 4 keeps each world's new characters as window totals.
			s.NewCharactersWindowDays = 90
			g.NewCharacters = w.newish
		} else {
			s.NewCharacters = append(s.NewCharacters, contract.ScopedDailyCount{Scope: contract.StatsScope{World: w.name}, Day: day, Count: w.newish})
		}
		s.Groups = append(s.Groups, g)
		for _, parent := range []struct {
			dim, key string
			into     map[string]*contract.ScopedGroupCount
			order    *[]string
		}{{"datacenter", w.dc, dcs, &dcOrder}, {"region", w.region, regions, &regionOrder}} {
			if parent.into[parent.key] == nil {
				parent.into[parent.key] = &contract.ScopedGroupCount{Dimension: parent.dim, Key: parent.key}
				*parent.order = append(*parent.order, parent.key)
			}
			p := parent.into[parent.key]
			p.Total += g.Total
			p.Active += g.Active
			p.MaxLevel += g.MaxLevel
			p.Private += g.Private
		}
		if version >= 3 {
			scope := contract.StatsScope{World: w.name}
			s.Groups = append(s.Groups,
				contract.ScopedGroupCount{Scope: scope, Dimension: "grand_company", Key: "Maelstrom", Total: w.maelstrom},
				contract.ScopedGroupCount{Scope: scope, Dimension: "grand_company", Key: "None", Total: w.total - w.maelstrom},
			)
		}
		s.Summary.Total += g.Total
		s.Summary.Active += g.Active
		s.Summary.MaxLevel += g.MaxLevel
		s.Summary.Private += g.Private
	}
	for _, key := range dcOrder {
		s.Groups = append(s.Groups, *dcs[key])
	}
	for _, key := range regionOrder {
		s.Groups = append(s.Groups, *regions[key])
	}
	s.SourceCharacters = s.Summary.Total
	if version >= 3 {
		s.PrivacyBuiltAt = generated.Add(-2 * time.Hour)
	}
	return s
}

func rankingsMux(t *testing.T, version int) *http.ServeMux {
	t.Helper()
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(rankingsSnapshot(version)), time.Hour, 24*time.Hour)
	ctrl := NewUIController(svc, mockqueue.NewFake(), stats, testBaseURL)
	mux := http.NewServeMux()
	ctrl.RegisterRoutes(mux)
	return mux
}

// ladderEntry is one rung of a rendered ladder.
type ladderEntry struct {
	rank, name, href, note, value string
}

// ladderEntries reads every rung of the ladder under root, top and expanded
// alike, in order.
func ladderEntries(t *testing.T, root *html.Node) []ladderEntry {
	t.Helper()
	var out []ladderEntry
	for _, li := range findAll(root, byClass("ladder-rung")) {
		e := ladderEntry{}
		if n := findAll(li, byClass("ladder-rank")); len(n) == 1 {
			e.rank = flatText(n[0])
		}
		if n := findAll(li, byClass("ladder-name")); len(n) == 1 {
			e.name = flatText(n[0])
			if links := findAll(n[0], byAtom(atom.A)); len(links) == 1 {
				e.href, _ = attr(links[0], "href")
			}
		}
		if n := findAll(li, byClass("ladder-note")); len(n) == 1 {
			e.note = flatText(n[0])
		}
		if n := findAll(li, byClass("ladder-value")); len(n) == 1 {
			e.value = flatText(n[0])
		}
		out = append(out, e)
	}
	return out
}

func TestRankingsPageDefaultsToFastestGrowingWorlds(t *testing.T) {
	rec := serveMuxGet(t, rankingsMux(t, contract.UIStatsSchemaVersion), "/ui/rankings")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	doc := parseHTML(t, rec.Body.String())
	assertNoInlineStylesOrEmoji(t, doc)

	board := byID(doc, "rankings-board")
	if board == nil {
		t.Fatal("no #rankings-board region")
	}
	headings := findAll(board, func(n *html.Node) bool { _, ok := attr(n, "data-switch-heading"); return ok })
	if len(headings) != 1 || flatText(headings[0]) != "Fastest-growing worlds" {
		t.Fatalf("board heading = %v", headings)
	}
	if got, _ := attr(board, "data-switch-announce"); got != "Fastest-growing worlds, every region" {
		t.Errorf("announcement = %q", got)
	}

	rungs := ladderEntries(t, board)
	if len(rungs) != 13 {
		t.Fatalf("ladder has %d rungs, want the 13 worlds large enough to rank", len(rungs))
	}
	want := ladderEntry{rank: "Rank 1", name: "Adamantoise", href: "/ui/worlds/Adamantoise", note: "Aether", value: "60.0 per 1,000"}
	if rungs[0] != want {
		t.Errorf("leader = %+v, want %+v", rungs[0], want)
	}
	if rungs[12].name != "Lich" || rungs[12].rank != "Rank 13" {
		t.Errorf("last rung = %+v, want Lich at 13", rungs[12])
	}

	// The top ten show; the rest wait in a native disclosure.
	more := findAll(board, byClass("ladder-more"))
	if len(more) != 1 || more[0].DataAtom != atom.Details {
		t.Fatalf("want one <details class=ladder-more>, got %d", len(more))
	}
	if got := len(ladderEntries(t, more[0])); got != 3 {
		t.Errorf("Show all hides %d rungs, want 3", got)
	}
	if summary := findAll(more[0], byAtom(atom.Summary)); len(summary) != 1 || flatText(summary[0]) != "Show all 13 worlds" {
		t.Errorf("summary = %v", summary)
	}

	main := flatText(mainOf(t, doc))
	for _, line := range []string{
		"New characters in the last 90 days for every 1,000 characters on the world.",
		"1 world with fewer than 250 characters is left out",
	} {
		if !strings.Contains(main, line) {
			t.Errorf("page lacks %q", line)
		}
	}
}

func TestRankingsPageHasEveryMetric(t *testing.T) {
	mux := rankingsMux(t, 3)
	for _, tc := range []struct {
		query, heading, leader, value string
	}{
		{"metric=growth-count", "Most new characters", "Adamantoise", "60 new"},
		{"metric=activity", "Most active worlds", "Adamantoise", "50.0%"},
		{"metric=veterans", "Most veterans", "Siren", "80.0%"},
		{"metric=population", "Largest worlds", "Lich", fmt.Sprintf("%s characters", "2,000")},
		{"metric=privacy", "Most private worlds", "Adamantoise", "10.0%"},
		{"metric=gc-maelstrom", "Strongest Maelstrom lean", "Lich", "60.0%"},
		{"metric=gc-twin-adder", "Strongest Twin Adder lean", "Adamantoise", "0.0%"},
		{"metric=gc-immortal-flames", "Strongest Immortal Flames lean", "Adamantoise", "0.0%"},
		{"metric=population&by=datacenter", "Largest datacenters", "Aether", "8,000 characters"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			doc := parseHTML(t, serveMuxGet(t, mux, "/ui/rankings?"+tc.query).Body.String())
			board := byID(doc, "rankings-board")
			heading := findAll(board, func(n *html.Node) bool { _, ok := attr(n, "data-switch-heading"); return ok })
			if len(heading) != 1 || flatText(heading[0]) != tc.heading {
				t.Fatalf("heading = %q, want %q", flatText(heading[0]), tc.heading)
			}
			rungs := ladderEntries(t, board)
			if len(rungs) == 0 || rungs[0].name != tc.leader || rungs[0].value != tc.value {
				t.Fatalf("leader = %+v, want %s at %s", rungs, tc.leader, tc.value)
			}
		})
	}
}

func TestRankingsSwitchesSwapTheBoardAndKeepTheOtherChoices(t *testing.T) {
	mux := rankingsMux(t, 3)
	doc := parseHTML(t, serveMuxGet(t, mux, "/ui/rankings?metric=activity&by=datacenter&region=na").Body.String())

	for _, tc := range []struct {
		label, current string
		hrefs          map[string]string
	}{
		{"Rank by", "Activity", map[string]string{
			"Growth":          "/ui/rankings?by=datacenter&region=NA",
			"Grand Companies": "/ui/rankings?by=datacenter&metric=gc-maelstrom&region=NA",
		}},
		{"Rank", "Datacenters", map[string]string{"Worlds": "/ui/rankings?metric=activity&region=NA"}},
		{"Region", "North America", map[string]string{"All regions": "/ui/rankings?by=datacenter&metric=activity"}},
	} {
		options := switchOptions(t, doc, tc.label)
		if got := currentOption(options); got != tc.current {
			t.Errorf("%s: current = %q, want %q", tc.label, got, tc.current)
		}
		for _, a := range options {
			href, _ := attr(a, "href")
			if want, ok := tc.hrefs[textOf(a)]; ok && href != want {
				t.Errorf("%s / %s: href %q, want %q", tc.label, textOf(a), href, want)
			}
			if get, _ := attr(a, "hx-get"); get != href {
				t.Errorf("%s / %s: hx-get %q, want %q", tc.label, textOf(a), get, href)
			}
			if target, _ := attr(a, "hx-target"); target != "#rankings-board" {
				t.Errorf("%s / %s: hx-target %q", tc.label, textOf(a), target)
			}
			if push, _ := attr(a, "hx-push-url"); push != "true" {
				t.Errorf("%s / %s: must push its URL", tc.label, textOf(a))
			}
		}
	}

	// Growth and Grand Companies offer their variants; other metrics don't.
	if got := findAll(doc, func(n *html.Node) bool { v, _ := attr(n, "aria-label"); return v == "Count growth as" }); len(got) != 0 {
		t.Error("activity board must not show the growth variants")
	}
	growth := parseHTML(t, serveMuxGet(t, mux, "/ui/rankings").Body.String())
	if got := currentOption(switchOptions(t, growth, "Count growth as")); got != "Per 1,000 residents" {
		t.Errorf("growth variant = %q", got)
	}
	gc := parseHTML(t, serveMuxGet(t, mux, "/ui/rankings?metric=gc-twin-adder").Body.String())
	if got := currentOption(switchOptions(t, gc, "Grand Company")); got != "Twin Adder" {
		t.Errorf("Grand Company variant = %q", got)
	}
}

func TestRankingsHTMXSwapRendersOnlyTheBoard(t *testing.T) {
	mux := rankingsMux(t, 3)
	rec := switchGet(t, mux, "/ui/rankings?metric=veterans&region=EU", map[string]string{"HX-Request": "true", "HX-Target": "rankings-board"})
	body := rec.Body.String()
	if strings.Contains(body, "<header") || strings.Contains(body, "<html") {
		t.Fatal("an HTMX switch request must get the board alone")
	}
	frag, err := html.ParseFragment(strings.NewReader(body), &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
	if err != nil {
		t.Fatal(err)
	}
	root := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, n := range frag {
		root.AppendChild(n)
	}
	board := byID(root, "rankings-board")
	if board == nil {
		t.Fatal("fragment lacks #rankings-board")
	}
	if got, _ := attr(board, "data-switch-announce"); got != "Most veterans, Europe" {
		t.Errorf("announcement = %q", got)
	}
	if rungs := ladderEntries(t, board); len(rungs) != 1 || rungs[0].name != "Lich" {
		t.Errorf("EU veterans = %+v, want Lich alone", rungs)
	}

	// A history restore needs the whole page.
	restore := switchGet(t, mux, "/ui/rankings?metric=veterans", map[string]string{"HX-Request": "true", "HX-Target": "rankings-board", "HX-History-Restore-Request": "true"})
	if !strings.Contains(restore.Body.String(), "<header") {
		t.Error("a history restore must get the full page")
	}
}

func TestRankingsUnknownParametersFallBackToTheDefaults(t *testing.T) {
	mux := rankingsMux(t, 3)
	for _, target := range []string{
		"/ui/rankings?metric=nope&by=galaxy&region=Mars",
		"/ui/rankings?metric=&by=&region=",
	} {
		doc := parseHTML(t, serveMuxGet(t, mux, target).Body.String())
		board := byID(doc, "rankings-board")
		if got, _ := attr(board, "data-switch-announce"); got != "Fastest-growing worlds, every region" {
			t.Errorf("%s: board = %q, want the default", target, got)
		}
	}
	// Values match case-insensitively.
	doc := parseHTML(t, serveMuxGet(t, mux, "/ui/rankings?metric=POPULATION&by=Datacenter&region=eu").Body.String())
	if got, _ := attr(byID(doc, "rankings-board"), "data-switch-announce"); got != "Largest datacenters, Europe" {
		t.Errorf("mixed-case board = %q", got)
	}
}

func TestRankingsWaitForFiguresOnAVersion2Snapshot(t *testing.T) {
	mux := rankingsMux(t, 2)
	growth := parseHTML(t, serveMuxGet(t, mux, "/ui/rankings").Body.String())
	if len(ladderEntries(t, growth)) == 0 {
		t.Error("growth works on a version 2 snapshot")
	}
	for metric, subject := range map[string]string{
		"veterans":     "Level cap figures arrive with the next refresh.",
		"privacy":      "Privacy figures arrive with the next refresh.",
		"gc-maelstrom": "Grand Company figures arrive with the next refresh.",
	} {
		doc := parseHTML(t, serveMuxGet(t, mux, "/ui/rankings?metric="+metric).Body.String())
		board := byID(doc, "rankings-board")
		if len(ladderEntries(t, board)) != 0 {
			t.Errorf("%s: no ladder before its figures exist", metric)
		}
		if !strings.Contains(flatText(board), subject) {
			t.Errorf("%s: board lacks %q", metric, subject)
		}
		if len(switchOptions(t, doc, "Rank by")) != 6 {
			t.Errorf("%s: the switches stay usable while waiting", metric)
		}
	}
}

func TestRankingsHeaderLinksTheBoards(t *testing.T) {
	doc := parseHTML(t, serveMuxGet(t, rankingsMux(t, 3), "/ui/rankings").Body.String())
	navs := findAll(doc, func(n *html.Node) bool {
		v, _ := attr(n, "aria-label")
		return n.DataAtom == atom.Nav && v == "Rankings boards"
	})
	if len(navs) != 1 {
		t.Fatalf("want one Rankings boards nav, got %d", len(navs))
	}
	links := findAll(navs[0], byAtom(atom.A))
	if len(links) != 2 || textOf(links[0]) != "Worlds and datacenters" || textOf(links[1]) != "Free Companies" {
		t.Fatalf("board links = %d", len(links))
	}
	if v, _ := attr(links[0], "aria-current"); v != "page" {
		t.Error("the current board is marked")
	}
}

func TestRankingsShowsNightlyFreshnessForPrivacyOnly(t *testing.T) {
	mux := rankingsMux(t, 3)
	privacy := parseHTML(t, serveMuxGet(t, mux, "/ui/rankings?metric=privacy").Body.String())
	if len(findAll(privacy, byClass("freshness"))) != 1 {
		t.Error("the privacy board says when it was rebuilt")
	}
	growth := parseHTML(t, serveMuxGet(t, mux, "/ui/rankings").Body.String())
	if len(findAll(growth, byClass("freshness"))) != 0 {
		t.Error("growth moves every refresh: no nightly line")
	}
}

func TestRankingsGrandCompanyBoardsWearTheCompanyColour(t *testing.T) {
	mux := rankingsMux(t, 3)
	for metric, tone := range map[string]string{
		"gc-maelstrom": "ladder-tone-maelstrom", "gc-twin-adder": "ladder-tone-twin-adder", "gc-immortal-flames": "ladder-tone-immortal-flames",
	} {
		doc := parseHTML(t, serveMuxGet(t, mux, "/ui/rankings?metric="+metric).Body.String())
		if len(findAll(doc, byClass(tone))) != 1 {
			t.Errorf("%s ladder lacks %s", metric, tone)
		}
	}
	if !strings.Contains(servedStylesheet(t), ".ladder-tone-maelstrom") {
		t.Error("stylesheet must colour the Maelstrom ladder")
	}
}
