package ui

import (
	"time"

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
	bounds := census.SnapshotNewCharactersBounds(snapshot)
	for date := bounds.CurrentStart; date.Before(bounds.End); date = date.AddDate(0, 0, 1) {
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

// WeeklyTimeline is a world's weekly new-character series behind the world
// page's bar chart, its "Show the numbers" table and its summary. Labels
// are axis labels ("Oct 17", the week's first day), Ranges the table rows
// ("Oct 17 – Oct 23"), Starts and Ends the ISO days for tooltips and
// <time> elements; all share one index, oldest week first.
type WeeklyTimeline struct {
	Labels []string
	Ranges []string
	Starts []string
	Ends   []string
	Counts []int64
	Total  int64
	// PeakLabel is the first day of the busiest week; empty without new
	// characters.
	PeakLabel string
	PeakCount int64
}

// Weeks is how many weeks the series has.
func (w WeeklyTimeline) Weeks() int { return len(w.Counts) }

// weeklyTimeline builds the chart data of a weekly series.
func weeklyTimeline(weeks []census.WeeklyCount) WeeklyTimeline {
	var tl WeeklyTimeline
	for _, week := range weeks {
		start, startErr := time.Parse("2006-01-02", week.Start)
		end, endErr := time.Parse("2006-01-02", week.End)
		if startErr != nil || endErr != nil {
			continue
		}
		tl.Labels = append(tl.Labels, start.Format("Jan 2"))
		tl.Ranges = append(tl.Ranges, start.Format("Jan 2")+" – "+end.Format("Jan 2"))
		tl.Starts = append(tl.Starts, week.Start)
		tl.Ends = append(tl.Ends, week.End)
		tl.Counts = append(tl.Counts, week.Count)
		tl.Total += week.Count
		if week.Count > tl.PeakCount {
			tl.PeakCount, tl.PeakLabel = week.Count, start.Format("Jan 2")
		}
	}
	return tl
}
