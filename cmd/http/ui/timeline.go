package ui

import (
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// Timeline is a daily new-character series behind the shared
// "new-characters-daily" chart, its "Show the numbers" table and its
// summary: one new-characters window, its complete UTC days before the
// generation day (the generation day is still in progress, so including it
// would end the chart on a false drop). Labels are axis labels ("Oct 23"),
// Dates the ISO days; all share one index, oldest day first. Days without
// new characters are zero. Counts are the company chocobos earned each day,
// Created the characters created, Estimated the days dated by the
// calibration curve.
type Timeline struct {
	Labels    []string
	Dates     []string
	Counts    []int64
	Created   []int64
	Estimated []bool
	// HasCreated marks a series with created characters: the chart draws
	// them, with the company chocobos on their own axis.
	HasCreated bool
	// Total and CreatedTotal sum Counts and Created.
	Total        int64
	CreatedTotal int64
	// PeakDate is the busiest day of the charted series ("Oct 23"); empty
	// without new characters.
	PeakDate  string
	PeakCount int64
	// Days is the window's length; EstimatedBefore the display day ("Oct
	// 21") before which the created days are estimated, empty when none are.
	Days            int
	EstimatedBefore string
	// Where names the place, "on Balmung"; empty for the whole game.
	Where string
}

// newCharactersTimeline builds the daily series over the snapshot's
// new-characters window for the whole game (world "") or one world, named
// in copy by where ("on Balmung"). A world on a snapshot from before the
// world groups' daily series has an empty timeline, which hides the chart.
func newCharactersTimeline(snapshot *contract.UIStatsSnapshot, world, where string) Timeline {
	tl := Timeline{HasCreated: census.CreatedReady(snapshot), Where: where}
	days := census.NewCharactersDaily(snapshot, world)
	tl.Days = len(days)
	exact, _ := census.CreatedEstimatedBefore(snapshot)
	for _, d := range days {
		date, err := time.Parse("2006-01-02", d.Day)
		if err != nil {
			continue
		}
		tl.Labels = append(tl.Labels, date.Format("Jan 2"))
		tl.Dates = append(tl.Dates, d.Day)
		tl.Counts = append(tl.Counts, d.Count)
		tl.Created = append(tl.Created, d.Created)
		tl.Estimated = append(tl.Estimated, d.Estimated)
		tl.Total += d.Count
		tl.CreatedTotal += d.Created
		if d.Estimated {
			tl.EstimatedBefore = exact.Format("Jan 2")
		}
		value := d.Count
		if tl.HasCreated {
			value = d.Created
		}
		if value > tl.PeakCount {
			tl.PeakCount, tl.PeakDate = value, date.Format("Jan 2")
		}
	}
	return tl
}
