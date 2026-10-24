package ui

import (
	"fmt"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
)

// Window is the length of a census window (the activity window, the
// new-characters window) for page copy. Copy never hard-codes a window
// length: it reads it from the snapshot through a Window.
type Window struct {
	Days int
}

// windowBetween is the window from since to until, in whole days: for the
// activity window, the snapshot's activity cutoff and its generation time.
func windowBetween(since, until time.Time) Window {
	return Window{Days: census.WindowDays(until.Sub(since))}
}

// Long reads "90 days", for sentences, labels and table headings.
func (w Window) Long() string {
	if w.Days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", w.Days)
}

// Short reads "90d", for tight labels such as "New (90d)".
func (w Window) Short() string {
	return fmt.Sprintf("%dd", w.Days)
}
