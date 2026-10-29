package census

import (
	"sort"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// LevelCapBand names the last level band: the characters with a job at the
// configured maximum level.
const LevelCapBand = "Level cap"

// LevelBands are the level bands of the Levels figures, in expansion order.
// Each expansion's band runs from just above the previous expansion's level
// cap (1 for the first) up to its own cap, the highest level that expansion
// allows; the last band is the configured maximum level and above, so the
// band of the expansion that raised the cap to it stops one level short.
// Bands follow level caps, not story progress: a job can be levelled past an
// expansion's cap without its main scenario. Expansions whose band would start at or past the maximum
// are left out, so an announced expansion with no released levels adds no
// band. A new expansion in the configuration adds its band with no code
// change. No expansions means DefaultExpansions and a zero maximum
// DefaultMaxLevel.
func LevelBands(expansions []ExpansionConfig, maxLevel uint32) []contract.StatsLevelBand {
	if len(expansions) == 0 {
		expansions = DefaultExpansions
	}
	if maxLevel == 0 {
		maxLevel = DefaultMaxLevel
	}
	sorted := append([]ExpansionConfig(nil), expansions...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].LevelCap < sorted[j].LevelCap })
	var bands []contract.StatsLevelBand
	from := uint32(1)
	for _, e := range sorted {
		if e.LevelCap < from || from >= maxLevel {
			continue
		}
		to := min(e.LevelCap, maxLevel-1)
		bands = append(bands, contract.StatsLevelBand{Name: e.Name, Min: from, Max: to})
		from = to + 1
	}
	if from < maxLevel && len(bands) > 0 {
		// The maximum lies past the last expansion's cap: its band reaches it.
		bands[len(bands)-1].Max = maxLevel - 1
	}
	return append(bands, contract.StatsLevelBand{Name: LevelCapBand, Min: maxLevel})
}

// LevelBandShare is one level band of a location: its characters and their
// share of the location's characters with job levels.
type LevelBandShare struct {
	contract.StatsLevelBand
	Count int64
	Share float64
}

// LevelDistribution is how one location's characters spread over the level
// bands by highest job level. Key is the location's name ("" for the whole
// game); Region and Datacenter are set on the per-datacenter and per-world
// rows. NoLevels are the characters with no stored job levels, counted
// apart: the band shares are of the characters with job levels and add up
// to 1 (all zero for a location without any).
type LevelDistribution struct {
	Key, Region, Datacenter string
	Total, NoLevels         int64
	Bands                   []LevelBandShare
}

// Levelled is the number of characters with job levels: the bands' sum.
func (d LevelDistribution) Levelled() int64 {
	var n int64
	for _, b := range d.Bands {
		n += b.Count
	}
	return n
}

// clone returns a copy that shares no slice with d.
func (d LevelDistribution) clone() LevelDistribution {
	d.Bands = append([]LevelBandShare(nil), d.Bands...)
	return d
}

func newLevelDistribution(bands []contract.StatsLevelBand, key string, total, noLevels int64, counts []int64) LevelDistribution {
	d := LevelDistribution{Key: key, Total: total, NoLevels: noLevels, Bands: make([]LevelBandShare, len(bands))}
	var levelled int64
	for i := range bands {
		if i < len(counts) {
			levelled += counts[i]
		}
	}
	for i, b := range bands {
		d.Bands[i].StatsLevelBand = b
		if i < len(counts) {
			d.Bands[i].Count = counts[i]
			d.Bands[i].Share = ratio(counts[i], levelled)
		}
	}
	return d
}

// LevelsReady reports whether snapshot carries the level figures: built by a
// full rebuild that counted the level bands.
func LevelsReady(snapshot *contract.UIStatsSnapshot) bool {
	return SnapshotHasSection(snapshot, SectionLevels)
}

// LevelsFor returns the level distribution of a stats scope: the summary for
// the whole game, otherwise its region, datacenter or world group (matched
// case-insensitively). An unknown location has empty bands.
func LevelsFor(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) LevelDistribution {
	if snapshot == nil {
		return LevelDistribution{}
	}
	level, key := ScopeLevel(scope)
	if level == LocationGlobal {
		s := snapshot.Summary
		return newLevelDistribution(snapshot.LevelBands, "", s.Total, s.NoLevels, s.Levels)
	}
	for _, g := range snapshot.Groups {
		if g.Scope == (contract.StatsScope{}) && g.Dimension == string(level) && strings.EqualFold(g.Key, key) {
			return newLevelDistribution(snapshot.LevelBands, g.Key, g.Total, g.NoLevels, g.Levels)
		}
	}
	return newLevelDistribution(snapshot.LevelBands, key, 0, 0, nil)
}

// worldLevels are the level counts of every known world group, by world.
type worldLevels struct {
	world, datacenter, region string
	total, noLevels           int64
	levels                    []int64
}

func snapshotWorldLevels(snapshot *contract.UIStatsSnapshot) []worldLevels {
	var out []worldLevels
	for _, g := range snapshot.Groups {
		if g.Scope != (contract.StatsScope{}) || g.Dimension != string(LocationWorld) {
			continue
		}
		dc := WorldDatacenter(g.Key)
		if dc == "" {
			continue
		}
		out = append(out, worldLevels{world: g.Key, datacenter: dc, region: RegionForDatacenter(dc), total: g.Total, noLevels: g.NoLevels, levels: g.Levels})
	}
	return out
}

// LevelsByDatacenter returns the level distribution of every datacenter,
// rolled up from its worlds, optionally narrowed to one region, ordered by
// region (census order) then datacenter name.
func LevelsByDatacenter(snapshot *contract.UIStatsSnapshot, region string) []LevelDistribution {
	if snapshot == nil {
		return nil
	}
	region = CanonicalRegion(region)
	type sum struct {
		region          string
		total, noLevels int64
		levels          []int64
	}
	sums := map[string]*sum{}
	for _, w := range snapshotWorldLevels(snapshot) {
		if region != "" && w.region != region {
			continue
		}
		s := sums[w.datacenter]
		if s == nil {
			s = &sum{region: w.region, levels: make([]int64, len(snapshot.LevelBands))}
			sums[w.datacenter] = s
		}
		s.total += w.total
		s.noLevels += w.noLevels
		for i := range s.levels {
			if i < len(w.levels) {
				s.levels[i] += w.levels[i]
			}
		}
	}
	var out []LevelDistribution
	for dc, s := range sums {
		d := newLevelDistribution(snapshot.LevelBands, dc, s.total, s.noLevels, s.levels)
		d.Region, d.Datacenter = s.region, dc
		out = append(out, d)
	}
	sortLevelRows(out)
	return out
}

// LevelsByWorld returns the level distribution of every world inside scope,
// ordered by region (census order), datacenter, then world name.
func LevelsByWorld(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) []LevelDistribution {
	if snapshot == nil {
		return nil
	}
	var out []LevelDistribution
	for _, w := range snapshotWorldLevels(snapshot) {
		if !ScopeHasWorld(scope, w.world) {
			continue
		}
		d := newLevelDistribution(snapshot.LevelBands, w.world, w.total, w.noLevels, w.levels)
		d.Region, d.Datacenter = w.region, w.datacenter
		out = append(out, d)
	}
	sortLevelRows(out)
	return out
}

// sortLevelRows orders datacenter or world rows by region (census order),
// datacenter, then name (a datacenter row's name is its datacenter).
func sortLevelRows(rows []LevelDistribution) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ra, rb := RegionRank(a.Region), RegionRank(b.Region); ra != rb {
			return ra < rb
		}
		if a.Datacenter != b.Datacenter {
			return a.Datacenter < b.Datacenter
		}
		return a.Key < b.Key
	})
}
