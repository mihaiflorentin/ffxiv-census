package census

import (
	"math"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// DefaultNewCharactersWindowDays is the new-characters window when none is
// configured.
const DefaultNewCharactersWindowDays = 90

// WindowDays is a census window's length in whole days, rounded to the
// nearest day. Non-positive windows are 0.
func WindowDays(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int(math.Round(d.Hours() / 24))
}

// NewCharactersBounds are the new-characters windows of a generation time.
// End is the start of the generation UTC day, exclusive: that day is still
// in progress, so both windows hold complete UTC days only. The current
// window is [CurrentStart, End) and the previous one
// [PreviousStart, CurrentStart), each Days days long. Every reader of the
// windows (the refresh's SQL parameters, the totals, the timeline and the
// API) takes its boundaries from here.
type NewCharactersBounds struct {
	Days          int
	End           time.Time
	CurrentStart  time.Time
	PreviousStart time.Time
}

// NewCharactersWindowBounds returns the windows of days days for a snapshot
// generated at generatedAt.
func NewCharactersWindowBounds(generatedAt time.Time, days int) NewCharactersBounds {
	end := generatedAt.UTC().Truncate(24 * time.Hour)
	current := end.AddDate(0, 0, -days)
	return NewCharactersBounds{Days: days, End: end, CurrentStart: current, PreviousStart: current.AddDate(0, 0, -days)}
}

// SnapshotNewCharactersBounds returns the windows snapshot was built with
// (SnapshotNewCharactersWindowDays). A nil snapshot has zero bounds.
func SnapshotNewCharactersBounds(snapshot *contract.UIStatsSnapshot) NewCharactersBounds {
	if snapshot == nil {
		return NewCharactersBounds{}
	}
	return NewCharactersWindowBounds(snapshot.GeneratedAt, SnapshotNewCharactersWindowDays(snapshot))
}

// SnapshotDailySeriesStart is the first UTC day the snapshot's whole-game
// daily series covers: the previous window's start from schema 4, and for
// older snapshots (60 days ending with the generation day) the day after.
func SnapshotDailySeriesStart(snapshot *contract.UIStatsSnapshot) time.Time {
	bounds := SnapshotNewCharactersBounds(snapshot)
	if snapshot != nil && snapshot.NewCharactersWindowDays == 0 {
		return bounds.PreviousStart.AddDate(0, 0, 1)
	}
	return bounds.PreviousStart
}

// Weeks is how many 7-day weeks the weekly series has: enough to cover
// the current window, so the oldest week may start before it.
func (b NewCharactersBounds) Weeks() int {
	if b.Days <= 0 {
		return 0
	}
	return (b.Days + 6) / 7
}

// WeeksStart is the first day of the weekly series' oldest week: the weeks
// end with the last complete UTC day.
func (b NewCharactersBounds) WeeksStart() time.Time {
	return b.End.AddDate(0, 0, -7*b.Weeks())
}

// Week is the index of the weekly series' week holding t, or -1 outside it.
func (b NewCharactersBounds) Week(t time.Time) int {
	start := b.WeeksStart()
	if t.Before(start) || !t.Before(b.End) {
		return -1
	}
	return int(t.Sub(start) / (7 * 24 * time.Hour))
}

// InCurrent reports whether t falls in the current window.
func (b NewCharactersBounds) InCurrent(t time.Time) bool {
	return !t.Before(b.CurrentStart) && t.Before(b.End)
}

// InPrevious reports whether t falls in the previous window.
func (b NewCharactersBounds) InPrevious(t time.Time) bool {
	return !t.Before(b.PreviousStart) && t.Before(b.CurrentStart)
}

// SnapshotActivityWindowDays is the activity window a snapshot was built
// with, in whole days: the gap between its activity cutoff and its
// generation time. It is 0 for a nil snapshot.
func SnapshotActivityWindowDays(snapshot *contract.UIStatsSnapshot) int {
	if snapshot == nil {
		return 0
	}
	return WindowDays(snapshot.GeneratedAt.Sub(snapshot.ActivitySince))
}
