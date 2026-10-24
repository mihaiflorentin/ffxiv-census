package ui

import (
	"math"
	"sort"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// The census taxonomy (race order, clans, regions) lives in domain/census.
// This file maps it onto presentation: categorical colour slots, display
// fallbacks and the data block the chart theme reads.

// unknownLabel names a group whose key is missing from the snapshot.
const unknownLabel = "Unknown"

// orUnknown returns name, or "Unknown" when it is empty.
func orUnknown(name string) string {
	if name == "" {
		return unknownLabel
	}
	return name
}

// raceSlot returns a race's categorical colour slot: its position in the
// race order (1-8), or 0 (neutral) for a race outside it. chart-theme.js
// derives the same slots from the #census-taxonomy block.
func raceSlot(race string) int {
	return census.RaceRank(race)
}

// regionSlot returns a region's categorical colour slot: its display
// position (1-4), or 0 (neutral) for an unknown region. Slots 1-4 validate
// all-pairs, so any ordering of the realm-tally band stays distinguishable.
func regionSlot(code string) int {
	return census.RegionRank(code)
}

// regionDisplayName returns the display name for a region code.
func regionDisplayName(code string) string {
	return census.RegionName(code)
}

// sortRegionCodes orders region codes for display; unknown codes go last,
// alphabetically.
func sortRegionCodes(codes []string) {
	rank := func(code string) int {
		if r := census.RegionRank(code); r > 0 {
			return r
		}
		return len(census.Regions()) + 1
	}
	sort.SliceStable(codes, func(i, j int) bool {
		if ri, rj := rank(codes[i]), rank(codes[j]); ri != rj {
			return ri < rj
		}
		return codes[i] < codes[j]
	})
}

// chartTaxonomy is the #census-taxonomy JSON block in the layout: the order
// chart-theme.js uses for race colours and doughnut ordering.
type chartTaxonomy struct {
	Races   []string `json:"races"`
	Regions []string `json:"regions"`
}

func censusTaxonomy() chartTaxonomy {
	t := chartTaxonomy{Races: census.RaceOrder()}
	for _, r := range census.Regions() {
		t.Regions = append(t.Regions, r.Code)
	}
	return t
}

// percentValue is num/denom as a 0-100 number rounded to one decimal, the
// numeric twin of formatPercent for meters and bars.
func percentValue(num, denom int64) float64 {
	if denom == 0 {
		return 0
	}
	return math.Round(float64(num)/float64(denom)*1000) / 10
}

// RaceRow represents demographic stats for one playable race.
type RaceRow struct {
	Race         string
	Slot         int
	Total        int64
	Active       int64
	ActiveRatio  string
	ShareOfTotal string
	// ShareValue is ShareOfTotal as a 0-100 number for the share meter.
	ShareValue float64
}

// newRaceRows builds one row per race group, with shares of total, in the
// groups' order (largest first).
func newRaceRows(groups []contract.GroupCount, total int64) []RaceRow {
	rows := make([]RaceRow, 0, len(groups))
	for _, g := range groups {
		name := orUnknown(g.Key)
		rows = append(rows, RaceRow{
			Race:         name,
			Slot:         raceSlot(name),
			Total:        g.Total,
			Active:       g.Active,
			ActiveRatio:  formatPercent(g.Active, g.Total),
			ShareOfTotal: formatPercent(g.Total, total),
			ShareValue:   percentValue(g.Total, total),
		})
	}
	return rows
}

// placeScope describes a region / datacenter / world selection for captions
// and tiles: "on Cerberus", "on the Chaos datacenter", "in Europe" or
// "across every region". The narrowest applied filter wins.
func placeScope(region, dc, world string) string {
	switch {
	case world != "":
		return "on " + world
	case dc != "":
		return "on the " + dc + " datacenter"
	case region != "":
		return "in " + regionDisplayName(region)
	}
	return "across every region"
}
