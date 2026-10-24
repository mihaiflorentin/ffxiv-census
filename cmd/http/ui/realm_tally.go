package ui

import (
	"fmt"
	"math"
	"net/url"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// RealmTally is the dashboard headline: the total population figure over a
// single band split proportionally by region. It is rendered server-side
// (inline SVG plus an equivalent text table) so it works without JavaScript.
type RealmTally struct {
	Total    int64
	Segments []RealmSegment
}

// RealmSegment is one region's slice of the realm-tally band.
type RealmSegment struct {
	Code  string // region code as stored in the snapshot (NA, EU, JP, OCE)
	Name  string // display name (North America, ...)
	Total int64
	// Share is the region's share of the census, formatted like "42.1%".
	Share string
	// Offset and Width position the segment on the band in percent of its
	// width, formatted as SVG length attributes ("12.5%").
	Offset string
	Width  string
	// LabelX is the x position of the in-band label (segment start + padding).
	LabelX string
	// ShowLabel is true when the segment is wide enough for its in-band code.
	ShowLabel bool
	Href      string
	// Slot is the categorical chart slot (1-8) that colours this region;
	// 0 means the neutral "other" colour.
	Slot int
}

// regionNames maps snapshot region codes to display names.
var regionNames = map[string]string{
	"NA":  "North America",
	"EU":  "Europe",
	"JP":  "Japan",
	"OCE": "Oceania",
}

// regionSlots fixes each region's categorical colour so a region keeps its
// colour whatever its rank. Slots 1-4 validate all-pairs, so any ordering of
// the band is safe to tell apart.
var regionSlots = map[string]int{"NA": 1, "EU": 2, "JP": 3, "OCE": 4}

// regionDisplayName returns the display name for a region code.
func regionDisplayName(code string) string {
	if name, ok := regionNames[code]; ok {
		return name
	}
	return code
}

// minLabelShare is the smallest share (in percent) whose segment carries an
// in-band label; narrower segments rely on the key, tooltip and table.
const minLabelShare = 9.0

// newRealmTally builds the realm tally from the snapshot's region groups,
// already sorted by population. Shares are of the census total; the band is
// the sum of segments, so any population without a region leaves track.
func newRealmTally(total int64, regions []contract.GroupCount) RealmTally {
	denom := total
	var sum int64
	for _, r := range regions {
		sum += r.Total
	}
	if sum > denom {
		denom = sum
	}
	tally := RealmTally{Total: total}
	if denom == 0 {
		return tally
	}
	offset := 0.0
	for _, r := range regions {
		if r.Total <= 0 {
			continue
		}
		code := r.Key
		if code == "" {
			code = "Unknown"
		}
		// Round once so the drawn width, the label and the offsets agree.
		pct := math.Round(float64(r.Total)/float64(denom)*1000) / 10
		tally.Segments = append(tally.Segments, RealmSegment{
			Code:      code,
			Name:      regionDisplayName(code),
			Total:     r.Total,
			Share:     formatPercent(r.Total, denom),
			Offset:    fmt.Sprintf("%.1f%%", offset),
			Width:     fmt.Sprintf("%.1f%%", pct),
			LabelX:    fmt.Sprintf("%.1f%%", offset+1.2),
			ShowLabel: pct >= minLabelShare,
			Href:      "/ui/worlds?region=" + url.QueryEscape(code),
			Slot:      regionSlots[code],
		})
		offset += pct
	}
	return tally
}
