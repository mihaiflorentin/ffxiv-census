package census

import (
	"sort"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// LevelCapBand names the last level band: the characters with a job at the
// configured maximum level.
const LevelCapBand = "Level cap"

// LevelBands are the level bands of the Levels figures, in expansion order.
// Each expansion's band runs from the previous expansion's level cap (1 for
// the first) to just below its own; the last band is the configured maximum
// level and above. Expansions whose band would start at or past the maximum
// are left out, so an announced expansion with no released levels adds no
// band. A new expansion in the configuration adds its band with no code
// change.
func LevelBands(expansions []ExpansionConfig, maxLevel uint32) []contract.StatsLevelBand {
	sorted := append([]ExpansionConfig(nil), expansions...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].LevelCap < sorted[j].LevelCap })
	var bands []contract.StatsLevelBand
	from := uint32(1)
	for _, e := range sorted {
		if e.LevelCap <= from || from >= maxLevel {
			continue
		}
		to := min(e.LevelCap, maxLevel) - 1
		bands = append(bands, contract.StatsLevelBand{Name: e.Name, Min: from, Max: to})
		from = to + 1
	}
	if from < maxLevel && len(bands) > 0 {
		// The maximum lies past the last expansion's cap: its band reaches it.
		bands[len(bands)-1].Max = maxLevel - 1
	}
	return append(bands, contract.StatsLevelBand{Name: LevelCapBand, Min: maxLevel})
}

// LevelBandIndex is the index of the band holding level, or -1 for a level
// below every band (a character with no job levels).
func LevelBandIndex(bands []contract.StatsLevelBand, level uint32) int {
	for i, b := range bands {
		if level >= b.Min && (b.Max == 0 || level <= b.Max) {
			return i
		}
	}
	return -1
}
