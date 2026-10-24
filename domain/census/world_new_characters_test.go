package census

import (
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// worldWindowSnapshot is a schema 4 snapshot whose world groups carry their
// new-characters window totals.
func worldWindowSnapshot() *contract.UIStatsSnapshot {
	s := validUIStatsSnapshot()
	s.GeneratedAt = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	s.NewCharactersWindowDays = 90
	s.Groups = append(s.Groups,
		contract.ScopedGroupCount{Dimension: "world", Key: "Balmung", Total: 100, NewCharacters: 4, NewCharactersPrevious: 2},
		contract.ScopedGroupCount{Dimension: "world", Key: "Ragnarok", Total: 100, NewCharacters: 10, NewCharactersPrevious: 20},
	)
	return s
}

// legacyWorldSnapshot is a schema 3 snapshot: per-world daily rows, no
// recorded window (30 days).
func legacyWorldSnapshot() *contract.UIStatsSnapshot {
	s := validUIStatsSnapshot()
	s.SchemaVersion = 3
	s.GeneratedAt = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	s.NewCharacters = []contract.ScopedDailyCount{
		{Scope: contract.StatsScope{World: "Balmung"}, Day: "2026-10-23", Count: 3},
		{Scope: contract.StatsScope{World: "Balmung"}, Day: "2026-09-10", Count: 5},
		{Scope: contract.StatsScope{World: "Balmung"}, Day: "2026-10-24", Count: 50}, // generation day
		{Scope: contract.StatsScope{World: "Ragnarok"}, Day: "2026-09-30", Count: 7},
		{Day: "2026-10-23", Count: 99},
	}
	return s
}

// The memo's per-world figures are the world's NewCharactersTotals, from
// one index of the snapshot: a page listing every world stays linear.
func TestSnapshotMemoWorldNewCharactersMatchesTheTotals(t *testing.T) {
	for name, s := range map[string]*contract.UIStatsSnapshot{"schema 4": worldWindowSnapshot(), "schema 3": legacyWorldSnapshot()} {
		m := NewSnapshotMemo()
		for _, world := range []string{"Balmung", "balmung", "Ragnarok", "Coeurl", "Nowhere"} {
			got, want := m.WorldNewCharacters(s, world), NewCharactersTotals(s, contract.StatsScope{World: world})
			if got != want {
				t.Errorf("%s: WorldNewCharacters(%q) = %+v, want %+v", name, world, got, want)
			}
		}
	}
}

func TestSnapshotMemoWorldNewCharactersBuildsOncePerSnapshot(t *testing.T) {
	m := NewSnapshotMemo()
	s := worldWindowSnapshot()
	first := m.WorldNewCharacters(s, "Balmung")
	if first.Current != 4 {
		t.Fatalf("WorldNewCharacters = %+v, want 4 current", first)
	}
	if got := m.WorldNewCharacters(emptied(s), "Balmung"); got != first {
		t.Errorf("WorldNewCharacters rebuilt for the same snapshot: %+v", got)
	}
	if got := m.WorldNewCharacters(nil, "Balmung"); got != (NewCharacterTotals{}) {
		t.Errorf("WorldNewCharacters(nil) = %+v, want zero", got)
	}
}

// A world's weekly series is the 7-day buckets ending with the last
// complete UTC day, oldest first, enough of them to cover the current
// window (13 for 90 days, the oldest reaching a day before the window).
func TestWorldNewCharactersWeekly(t *testing.T) {
	s := worldWindowSnapshot()
	weekly := make([]int64, 13)
	weekly[0], weekly[12] = 2, 5
	for i, g := range s.Groups {
		if g.Key == "Balmung" {
			s.Groups[i].NewCharactersWeekly = weekly
		}
	}
	got := WorldNewCharactersWeekly(s, "balmung")
	if len(got) != 13 {
		t.Fatalf("weeks = %d, want 13", len(got))
	}
	first, last := got[0], got[12]
	if first != (WeeklyCount{Start: "2026-07-25", End: "2026-07-31", Count: 2}) {
		t.Errorf("first week = %+v, want Jul 25 to Jul 31 with 2", first)
	}
	if last != (WeeklyCount{Start: "2026-10-17", End: "2026-10-23", Count: 5}) {
		t.Errorf("last week = %+v, want Oct 17 to Oct 23 with 5", last)
	}

	// A world without new characters has a zero series; a snapshot from
	// before the weekly series has none.
	if coeurl := WorldNewCharactersWeekly(s, "Coeurl"); len(coeurl) != 13 || coeurl[5].Count != 0 {
		t.Errorf("Coeurl weeks = %+v, want 13 empty weeks", coeurl)
	}
	if legacy := WorldNewCharactersWeekly(legacyWorldSnapshot(), "Balmung"); legacy != nil {
		t.Errorf("schema 3 weeks = %+v, want none", legacy)
	}
}

func TestValidateRejectsNegativeWeeklyCounts(t *testing.T) {
	s := worldWindowSnapshot()
	s.Groups[len(s.Groups)-1].NewCharactersWeekly = []int64{1, -1}
	if err := ValidateUIStatsSnapshot(s); err == nil {
		t.Fatal("a negative weekly count must fail validation")
	}
}
