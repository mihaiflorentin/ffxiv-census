package census

import (
	"math"
	"sort"
	"time"
)

// Created characters are dated by their Lodestone ID, which the game hands
// out in creation order. From the exact cut (CreationAnchor) on, the ID
// frontier sees each new character within hours of its creation, so its
// first sighting dates it. Below the cut a calibration curve dates the IDs:
// the early date at which each band of IDs earned its company chocobo.

// DefaultCreatedExactFromID is the first character ID the ID frontier saw
// within hours of its creation: on 21 Oct 2026, the first day after it
// caught up with the IDs handed out since the bulk sweep (13 Oct).
const DefaultCreatedExactFromID = 62_745_092

// DefaultCreationMinSamples is how many company chocobos a band of IDs
// needs before its early date anchors the calibration curve.
const DefaultCreationMinSamples = 30

// CreationBand is the character IDs [FirstID, EndID) and the early date
// (10th percentile) at which Samples of them earned their company chocobo.
type CreationBand struct {
	FirstID, EndID int64
	Samples        int64
	Early          time.Time
}

// CreationAnchor is a point of the calibration curve: characters from ID on
// were created at or after At.
type CreationAnchor struct {
	ID int64
	At time.Time
}

// CreationAnchors builds the calibration curve from bands below the exact
// cut, ending with the cut itself. Each band with at least minSamples
// samples anchors its midpoint at its early date. The dates never go back
// as the IDs rise, and never pass the cut's date.
func CreationAnchors(bands []CreationBand, minSamples int64, exact CreationAnchor) []CreationAnchor {
	sorted := append([]CreationBand(nil), bands...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].FirstID < sorted[j].FirstID })
	var anchors []CreationAnchor
	for _, band := range sorted {
		if band.Samples < minSamples || band.EndID > exact.ID {
			continue
		}
		at := band.Early
		if n := len(anchors); n > 0 && at.Before(anchors[n-1].At) {
			at = anchors[n-1].At
		}
		if at.After(exact.At) {
			at = exact.At
		}
		anchors = append(anchors, CreationAnchor{ID: band.FirstID + (band.EndID-band.FirstID)/2, At: at})
	}
	return append(anchors, exact)
}

// CreationBoundaries returns the ID at which each UTC day from from to
// until (both included) begins on the curve, and the first day the curve
// covers: days before its first anchor are left out. Consecutive
// boundaries bound one day's IDs.
func CreationBoundaries(anchors []CreationAnchor, from, until time.Time) (time.Time, []int64) {
	if len(anchors) < 2 {
		return time.Time{}, nil
	}
	if from.Before(anchors[0].At) {
		from = anchors[0].At.Truncate(24 * time.Hour)
		if from.Before(anchors[0].At) {
			from = from.AddDate(0, 0, 1)
		}
	}
	if from.After(until) {
		return time.Time{}, nil
	}
	var ids []int64
	segment := 0
	for day := from; !day.After(until); day = day.AddDate(0, 0, 1) {
		for segment < len(anchors)-2 && anchors[segment+1].At.Before(day) {
			segment++
		}
		a, b := anchors[segment], anchors[segment+1]
		if !a.At.Before(day) {
			ids = append(ids, a.ID)
			continue
		}
		if !b.At.After(day) {
			ids = append(ids, b.ID)
			continue
		}
		share := float64(day.Sub(a.At)) / float64(b.At.Sub(a.At))
		ids = append(ids, a.ID+int64(math.Round(share*float64(b.ID-a.ID))))
	}
	return from, ids
}
