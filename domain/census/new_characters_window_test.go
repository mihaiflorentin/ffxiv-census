package census

import (
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// TestNewCharactersWindowCoversCompleteDays checks both 30-day windows end
// on the last complete UTC day: the generation day is still in progress, so
// counting it would bias the trend down and disagree with the timeline.
func TestNewCharactersWindowCoversCompleteDays(t *testing.T) {
	snapshot := validUIStatsSnapshot()
	snapshot.GeneratedAt = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	snapshot.NewCharacters = []contract.ScopedDailyCount{
		{Day: "2026-10-24", Count: 50}, // partial generation day: left out
		{Day: "2026-10-23", Count: 5},  // last day of the current window
		{Day: "2026-09-24", Count: 3},  // first day of the current window
		{Day: "2026-09-23", Count: 7},  // last day of the previous window
		{Day: "2026-08-25", Count: 11}, // first day of the previous window
		{Day: "2026-08-24", Count: 13}, // outside both
	}
	got := NewCharactersWindow(snapshot, contract.StatsScope{})
	want := NewCharacterTotals{Current: 8, Previous: 18, StartDay: "2026-09-24", Days: 30}
	if got != want {
		t.Fatalf("NewCharactersWindow() = %+v, want %+v", got, want)
	}
}

// A schema 4 snapshot records its window: both windows are that many
// complete UTC days, the current one ending the day before generation.
func TestNewCharactersWindowReadsTheSnapshotWindow(t *testing.T) {
	snapshot := validUIStatsSnapshot()
	snapshot.GeneratedAt = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	snapshot.NewCharactersWindowDays = 90
	snapshot.NewCharacters = []contract.ScopedDailyCount{
		{Day: "2026-10-24", Count: 50}, // partial generation day: left out
		{Day: "2026-10-23", Count: 5},  // last day of the current window
		{Day: "2026-07-26", Count: 3},  // first day of the current window
		{Day: "2026-07-25", Count: 7},  // last day of the previous window
		{Day: "2026-04-27", Count: 11}, // first day of the previous window
		{Day: "2026-04-26", Count: 13}, // outside both
	}
	got := NewCharactersWindow(snapshot, contract.StatsScope{})
	want := NewCharacterTotals{Current: 8, Previous: 18, StartDay: "2026-07-26", Days: 90}
	if got != want {
		t.Fatalf("NewCharactersWindow() = %+v, want %+v", got, want)
	}
	if days := SnapshotNewCharactersWindowDays(snapshot); days != 90 {
		t.Fatalf("SnapshotNewCharactersWindowDays() = %d, want 90", days)
	}
}

// A snapshot from before the window was recorded used a fixed 30 days.
func TestSnapshotNewCharactersWindowDaysDefaultsToThirtyOnOlderSnapshots(t *testing.T) {
	if days := SnapshotNewCharactersWindowDays(validUIStatsSnapshot()); days != 30 {
		t.Fatalf("SnapshotNewCharactersWindowDays(older) = %d, want 30", days)
	}
	if days := SnapshotNewCharactersWindowDays(nil); days != 0 {
		t.Fatalf("SnapshotNewCharactersWindowDays(nil) = %d, want 0", days)
	}
}

// World, datacenter and region totals sum the world groups' window totals;
// the daily series is whole-game only.
func TestNewCharactersWindowSumsWorldTotalsForLocations(t *testing.T) {
	snapshot := validUIStatsSnapshot()
	snapshot.GeneratedAt = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	snapshot.NewCharactersWindowDays = 90
	snapshot.Groups = append(snapshot.Groups,
		contract.ScopedGroupCount{Dimension: "world", Key: "Balmung", Total: 100, NewCharacters: 4, NewCharactersPrevious: 2},
		contract.ScopedGroupCount{Dimension: "world", Key: "Coeurl", Total: 100, NewCharacters: 1, NewCharactersPrevious: 6},
		contract.ScopedGroupCount{Dimension: "world", Key: "Ragnarok", Total: 100, NewCharacters: 10, NewCharactersPrevious: 20},
		// A scoped world group never carries window totals.
		contract.ScopedGroupCount{Scope: contract.StatsScope{Region: "NA"}, Dimension: "world", Key: "Balmung", Total: 100, NewCharacters: 99},
	)
	tests := []struct {
		scope             contract.StatsScope
		current, previous int64
	}{
		{contract.StatsScope{World: "balmung"}, 4, 2},
		{contract.StatsScope{Datacenter: "Crystal"}, 5, 8},
		{contract.StatsScope{Region: "EU"}, 10, 20},
		{contract.StatsScope{Region: "NA", Datacenter: "Crystal"}, 5, 8},
		{contract.StatsScope{World: "Nowhere"}, 0, 0},
	}
	for _, tt := range tests {
		got := NewCharactersWindow(snapshot, tt.scope)
		if got.Current != tt.current || got.Previous != tt.previous || got.Days != 90 {
			t.Errorf("NewCharactersWindow(%+v) = %+v, want current %d previous %d over 90 days", tt.scope, got, tt.current, tt.previous)
		}
	}
}
