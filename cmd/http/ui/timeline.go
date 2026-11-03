package ui

import (
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
)

// WeeklyTimeline is a weekly new-character series behind the shared
// "new-characters-weekly" bar chart, its "Show the numbers" table and its
// summary. Labels are axis labels ("Oct 17", the week's first day), Ranges
// the table rows ("Oct 17 – Oct 23"), Starts and Ends the ISO days for
// tooltips and <time> elements; all share one index, oldest week first.
// Counts are the company chocobos earned each week, Created the characters
// created, Estimated the weeks with an estimated created day.
type WeeklyTimeline struct {
	Labels    []string
	Ranges    []string
	Starts    []string
	Ends      []string
	Counts    []int64
	Created   []int64
	Estimated []bool
	// HasCreated marks a series with created characters: the bars chart
	// them and the company chocobos ride along as a line.
	HasCreated bool
	// Total and CreatedTotal sum Counts and Created.
	Total        int64
	CreatedTotal int64
	// PeakLabel is the first day of the busiest week of the charted bars;
	// empty without new characters.
	PeakLabel string
	PeakCount int64
	// Where names the place, "on Balmung"; empty for the whole game.
	Where string
	// EstimatedBefore is the display day ("Oct 21") before which the
	// series' created days are estimated; empty when none are.
	EstimatedBefore string
}

// Weeks is how many weeks the series has.
func (w WeeklyTimeline) Weeks() int { return len(w.Counts) }

// weeklyTimeline builds the chart data of a weekly series; hasCreated
// charts the created characters as the bars.
func weeklyTimeline(weeks []census.WeeklyCount, hasCreated bool) WeeklyTimeline {
	tl := WeeklyTimeline{HasCreated: hasCreated}
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
		tl.Created = append(tl.Created, week.Created)
		tl.Estimated = append(tl.Estimated, hasCreated && week.Estimated)
		tl.Total += week.Count
		tl.CreatedTotal += week.Created
		bar := week.Count
		if hasCreated {
			bar = week.Created
		}
		if bar > tl.PeakCount {
			tl.PeakCount, tl.PeakLabel = bar, start.Format("Jan 2")
		}
	}
	return tl
}
