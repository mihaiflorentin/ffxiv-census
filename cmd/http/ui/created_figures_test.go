package ui

import (
	"net/http"
	"reflect"
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

// createdFiguresSnapshot is a schema 5 snapshot generated on Oct 24 with a
// 14-day window: the current window and its two weeks run Oct 10 to Oct 23,
// the previous window Sep 26 to Oct 9. The created characters are exact
// from Oct 17. Balmung has 3 created (1 the window before) and 10 company
// chocobos.
func createdFiguresSnapshot() *contract.UIStatsSnapshot {
	s := rankingsSnapshot(contract.UIStatsSchemaVersion)
	s.GeneratedAt = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	s.ActivitySince = s.GeneratedAt.Add(-90 * 24 * time.Hour)
	s.NewCharactersWindowDays = 14
	s.CreatedBuiltAt, s.CreatedExactFrom = s.GeneratedAt, "2026-10-17"
	s.Created = []contract.ScopedDailyCount{{Day: "2026-10-01", Count: 9}, {Day: "2026-10-12", Count: 4}, {Day: "2026-10-20", Count: 6}}
	s.NewCharacters = []contract.ScopedDailyCount{{Day: "2026-10-18", Count: 2}}
	for i, g := range s.Groups {
		if g.Scope == (contract.StatsScope{}) && g.Dimension == "world" {
			s.Groups[i].Created, s.Groups[i].CreatedPrevious = 0, 0
			if g.Key == "Balmung" {
				s.Groups[i].Created, s.Groups[i].CreatedPrevious, s.Groups[i].CreatedWeekly = 3, 1, []int64{1, 2}
				s.Groups[i].NewCharacters, s.Groups[i].NewCharactersWeekly = 10, []int64{3, 7}
			}
		}
	}
	return s
}

func snapshotMux(t *testing.T, s *contract.UIStatsSnapshot) *http.ServeMux {
	t.Helper()
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(s), time.Hour, 24*time.Hour)
	mux := http.NewServeMux()
	NewUIController(svc, mockqueue.NewFake(), stats, testBaseURL).RegisterRoutes(mux)
	return mux
}

// newCharactersCardText reads the "New characters" stat card.
func newCharactersCardText(t *testing.T, doc *html.Node) string {
	t.Helper()
	for _, card := range findAll(doc, byClass("stat-card")) {
		if text := flatText(card); strings.HasPrefix(text, "New characters, last") {
			return text
		}
	}
	t.Fatal("no New characters card")
	return ""
}

// The New characters card counts the created characters with their trend,
// and the company chocobos beside them; the weekly chart carries both and
// marks the estimated weeks.
func TestNewCharacterFiguresCountTheCreatedCharacters(t *testing.T) {
	mux := snapshotMux(t, createdFiguresSnapshot())
	tests := []struct {
		path, card, summary string
		weeks               [][]string
	}{
		{
			path:    "/",
			card:    "New characters, last 14 days 10 Up +1 (+11.1%) on the previous 14 days 2 reached their company chocobo Days before Oct 17 estimated from character IDs",
			summary: "10 characters created in the last 2 weeks, peaking at 6 in the week of Oct 17. 2 earned their first company chocobo. Weeks before Oct 17 are estimated from character IDs.",
			weeks:   [][]string{{"Oct 10 – Oct 16", "4 estimated", "0"}, {"Oct 17 – Oct 23", "6", "2"}},
		},
		{
			path:    "/ui/worlds/Balmung",
			card:    "New characters, last 14 days 3 Up +2 (+200.0%) on the previous 14 days 10 reached their company chocobo Days before Oct 17 estimated from character IDs",
			summary: "3 characters created on Balmung in the last 2 weeks, peaking at 2 in the week of Oct 17. 10 earned their first company chocobo. Weeks before Oct 17 are estimated from character IDs.",
			weeks:   [][]string{{"Oct 10 – Oct 16", "1 estimated", "3"}, {"Oct 17 – Oct 23", "2", "7"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := serveMuxGet(t, mux, tt.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d", rec.Code)
			}
			doc := parseHTML(t, rec.Body.String())
			if got := newCharactersCardText(t, doc); got != tt.card {
				t.Errorf("card = %q\nwant   %q", got, tt.card)
			}
			if got := flatText(byID(doc, "weekly-summary")); got != tt.summary {
				t.Errorf("summary = %q\nwant      %q", got, tt.summary)
			}
			caption := "New characters per week"
			if tt.path != "/" {
				caption += " on Balmung"
			}
			if got := tableBodyRows(tableCaptioned(t, doc, caption)); !reflect.DeepEqual(got, tt.weeks) {
				t.Errorf("weekly table = %v, want %v", got, tt.weeks)
			}
			if strings.Contains(rec.Body.String(), "New characters per day") {
				t.Error("the daily chart is gone: new characters are charted per week")
			}
		})
	}
}

// The world tables count the created characters.
func TestWorldTablesCountTheCreatedCharacters(t *testing.T) {
	mux := snapshotMux(t, createdFiguresSnapshot())
	for _, path := range []string{"/ui/worlds?region=NA", "/ui/partials/world-breakdown?region=NA"} {
		doc := parseHTML(t, serveMuxGet(t, mux, path).Body.String())
		found := false
		for _, tr := range findAll(doc, byAtom(atom.Tr)) {
			cells := rowCells(tr)
			if len(cells) > 0 && strings.Contains(flatText(cells[0]), "Balmung") {
				found = true
				if got := flatText(cells[3]); got != "3" {
					t.Errorf("%s: Balmung new = %q, want the 3 created", path, got)
				}
			}
		}
		if !found {
			t.Errorf("%s: no Balmung row", path)
		}
	}
}

// The Races page is about races: it no longer repeats the new-characters card.
func TestRacesPageHasNoNewCharactersCard(t *testing.T) {
	body := serveMuxGet(t, snapshotMux(t, createdFiguresSnapshot()), "/ui/races").Body.String()
	if strings.Contains(body, "New characters, last") {
		t.Error("the Races page repeats the new-characters card")
	}
}

// Before the created figures exist the card and the chart count the company
// chocobos, as they did.
func TestNewCharacterFiguresFallBackToTheChocobos(t *testing.T) {
	s := createdFiguresSnapshot()
	s.SchemaVersion, s.CreatedBuiltAt, s.CreatedExactFrom, s.Created = 4, time.Time{}, "", nil
	doc := parseHTML(t, serveMuxGet(t, snapshotMux(t, s), "/ui/worlds/Balmung").Body.String())
	if got, want := newCharactersCardText(t, doc), "New characters, last 14 days 10 Up +10 on the previous 14 days"; got != want {
		t.Errorf("card = %q, want %q", got, want)
	}
	if got, want := tableBodyRows(tableCaptioned(t, doc, "New characters per week on Balmung")), [][]string{{"Oct 10 – Oct 16", "3"}, {"Oct 17 – Oct 23", "7"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("weekly table = %v, want %v", got, want)
	}
	if got, want := flatText(byID(doc, "weekly-summary")), "10 new characters on Balmung in the last 2 weeks, peaking at 7 in the week of Oct 17."; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

// In its place the Races page names the most chosen race and its share.
func TestRacesPageNamesTheMostChosenRace(t *testing.T) {
	s := createdFiguresSnapshot()
	s.Groups = append(s.Groups,
		contract.ScopedGroupCount{Dimension: "race", Key: "Hyur", Total: 30},
		contract.ScopedGroupCount{Dimension: "race", Key: "Miqo'te", Total: 70},
	)
	doc := parseHTML(t, serveMuxGet(t, snapshotMux(t, s), "/ui/races").Body.String())
	for _, card := range findAll(doc, byClass("stat-card")) {
		if text := flatText(card); strings.HasPrefix(text, "Most chosen race") {
			if want := "Most chosen race Miqo'te 70.0% of these characters"; text != want {
				t.Errorf("card = %q, want %q", text, want)
			}
			return
		}
	}
	t.Fatal("no Most chosen race card")
}
