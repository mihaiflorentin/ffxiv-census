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
