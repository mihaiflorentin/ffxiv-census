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

// SnapshotActivityWindowDays is the activity window a snapshot was built
// with, in whole days: the gap between its activity cutoff and its
// generation time. It is 0 for a nil snapshot.
func SnapshotActivityWindowDays(snapshot *contract.UIStatsSnapshot) int {
	if snapshot == nil {
		return 0
	}
	return WindowDays(snapshot.GeneratedAt.Sub(snapshot.ActivitySince))
}
