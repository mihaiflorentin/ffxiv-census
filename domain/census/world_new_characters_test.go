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

// A world's daily series has one count per complete UTC day of the current
// window, oldest first (90 for 90 days, Jul 26 to Oct 23). A world without
// new characters has a zero series; a snapshot from before the daily series
// (schema 5 and earlier) has none.
func TestWorldNewCharactersDaily(t *testing.T) {
	s := worldWindowSnapshot()
	daily := make([]int64, 90)
	daily[0], daily[89] = 2, 5
	for i, g := range s.Groups {
		if g.Key == "Balmung" {
			s.Groups[i].NewCharactersDaily = daily
		}
	}
	got := NewCharactersDaily(s, "balmung")
	if len(got) != 90 {
		t.Fatalf("days = %d, want 90", len(got))
	}
	if got[0] != (DailyNewCharacters{Day: "2026-07-26", Count: 2}) || got[89] != (DailyNewCharacters{Day: "2026-10-23", Count: 5}) {
		t.Errorf("first and last day = %+v, %+v; want Jul 26 with 2 and Oct 23 with 5", got[0], got[89])
	}
	if coeurl := NewCharactersDaily(s, "Coeurl"); len(coeurl) != 90 || coeurl[5].Count != 0 {
		t.Errorf("Coeurl days = %+v, want 90 empty days", coeurl)
	}
	s.SchemaVersion = 5
	if older := NewCharactersDaily(s, "Balmung"); older != nil {
		t.Errorf("schema 5 days = %+v, want none", older)
	}
	if legacy := NewCharactersDaily(legacyWorldSnapshot(), "Balmung"); legacy != nil {
		t.Errorf("schema 3 days = %+v, want none", legacy)
	}
}

func TestValidateRejectsNegativeDailyCounts(t *testing.T) {
	s := worldWindowSnapshot()
	s.Groups[len(s.Groups)-1].NewCharactersDaily = []int64{1, -1}
	if err := ValidateUIStatsSnapshot(s); err == nil {
		t.Fatal("a negative daily count must fail validation")
	}
}
