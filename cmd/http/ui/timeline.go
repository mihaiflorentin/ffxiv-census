package ui

import (
	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// Timeline is the 30-day new-character series behind the line chart, its
// "Show the numbers" table and its one-sentence summary. Days without new
// characters are filled with zero so every chart shows the same window.
type Timeline struct {
	// Labels are axis labels ("Jan 2"), Dates the ISO days for tooltips and
	// the data table, Counts the values; all three share one index.
	Labels []string
	Dates  []string
	Counts []int64
	TimelineSummary
}

// newCharactersTimeline builds the 30 days ending on the snapshot's
// generation day for scope.
func newCharactersTimeline(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) Timeline {
	var tl Timeline
	if snapshot == nil {
		return tl
	}
	daily := make(map[string]int64)
	for _, day := range census.SnapshotDaily(snapshot, scope) {
		daily[day.Day] = day.Count
	}
	now := snapshot.GeneratedAt.UTC()
	for i := 29; i >= 0; i-- {
		date := now.AddDate(0, 0, -i)
		day := date.Format("2006-01-02")
		count := daily[day]
		tl.Labels = append(tl.Labels, date.Format("Jan 2"))
		tl.Dates = append(tl.Dates, day)
		tl.Counts = append(tl.Counts, count)
		tl.Total += count
		if count > tl.PeakCount {
			tl.PeakCount, tl.PeakDate = count, day
		}
	}
	return tl
}
