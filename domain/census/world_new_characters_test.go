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
