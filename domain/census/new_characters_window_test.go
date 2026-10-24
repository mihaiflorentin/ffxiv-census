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
	want := NewCharacterTotals{Current: 8, Previous: 18, StartDay: "2026-09-24"}
	if got != want {
		t.Fatalf("NewCharactersWindow() = %+v, want %+v", got, want)
	}
}
