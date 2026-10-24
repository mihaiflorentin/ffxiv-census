package ui

import (
	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// Timeline is the new-character series behind the line chart, its "Show
// the numbers" table and its one-sentence summary: one new-characters
// window. Days without new characters are filled with zero so every chart
// shows the same window.
type Timeline struct {
	// Labels are axis labels ("Jan 2"), Dates the ISO days for tooltips and
	// the data table, Counts the values; all three share one index.
	Labels []string
	Dates  []string
	Counts []int64
	TimelineSummary
}

// newCharactersTimeline builds the snapshot's new-characters window: its
// complete UTC days before the generation day, for scope. The generation day
// itself is still in progress, so including it would end every chart on a
// false drop.
func newCharactersTimeline(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) Timeline {
	var tl Timeline
	if snapshot == nil {
		return tl
	}
	daily := make(map[string]int64)
	for _, day := range census.SnapshotDaily(snapshot, scope) {
		daily[day.Day] = day.Count
	}
	lastComplete := snapshot.GeneratedAt.UTC().AddDate(0, 0, -1)
	for i := census.SnapshotNewCharactersWindowDays(snapshot) - 1; i >= 0; i-- {
		date := lastComplete.AddDate(0, 0, -i)
		day := date.Format("2006-01-02")
		count := daily[day]
		tl.Labels = append(tl.Labels, date.Format("Jan 2"))
		tl.Dates = append(tl.Dates, day)
		tl.Counts = append(tl.Counts, count)
		tl.Total += count
		if count > tl.PeakCount {
			tl.PeakCount, tl.PeakDate = count, date.Format("Jan 2")
		}
	}
	return tl
}
