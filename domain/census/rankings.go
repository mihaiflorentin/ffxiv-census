package census

import (
	"sort"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// RankingMetric is what a ranking board orders worlds or datacenters by.
type RankingMetric string

const (
	// MetricPopulation is the total number of characters.
	MetricPopulation RankingMetric = "population"
	// MetricActivity is active ÷ total.
	MetricActivity RankingMetric = "activity"
	// MetricVeterans is at the level cap ÷ total.
	MetricVeterans RankingMetric = "veterans"
	// MetricPrivacy is achievements private ÷ total.
	MetricPrivacy RankingMetric = "privacy"
	// MetricGrowth is new characters in the new-characters window (see
	// NewCharactersWindow) per 1,000 residents.
	MetricGrowth RankingMetric = "growth"
	// MetricGrowthCount is new characters in the new-characters window.
	MetricGrowthCount RankingMetric = "growth-count"
	// MetricLeanMaelstrom, MetricLeanTwinAdder and MetricLeanImmortalFlames
	// are one Grand Company's members over everyone (including no company).
	MetricLeanMaelstrom      RankingMetric = "gc-maelstrom"
	MetricLeanTwinAdder      RankingMetric = "gc-twin-adder"
	MetricLeanImmortalFlames RankingMetric = "gc-immortal-flames"
)

// rankingMetric describes how a metric reads one candidate.
type rankingMetric struct {
	// rate metrics divide Part by Total (times scale, when set) and leave
	// out small entries.
	rate  bool
	scale float64
	// ready reports whether the snapshot carries the metric's figures.
	ready func(*contract.UIStatsSnapshot) bool
	// part is the counted characters of a candidate at level, and the
	// population they are a share of.
	part rankingPart
}

type rankingPart func(snapshot *contract.UIStatsSnapshot, level LocationLevel, c LocationCounts) (part, total int64)

func countOf(read func(LocationCounts) int64) rankingPart {
	return func(_ *contract.UIStatsSnapshot, _ LocationLevel, c LocationCounts) (int64, int64) {
		return read(c), c.Total
	}
}

// newCharacters is a candidate's new characters in the new-characters window.
func newCharacters(snapshot *contract.UIStatsSnapshot, level LocationLevel, c LocationCounts) (int64, int64) {
	return NewCharactersWindow(snapshot, ScopeAt(level, c.Key)).Current, c.Total
}

// companyMembers reads one Grand Company's members out of the candidate's
// Grand Company split.
func companyMembers(company string) rankingPart {
	return func(snapshot *contract.UIStatsSnapshot, level LocationLevel, c LocationCounts) (int64, int64) {
		split := GrandCompanySplitFor(snapshot, ScopeAt(level, c.Key))
		for _, share := range split.Companies {
			if share.Name == company {
				return share.Total, split.Total
			}
		}
		return 0, split.Total
	}
}

func grandCompaniesReady(snapshot *contract.UIStatsSnapshot) bool {
	return SnapshotHasSection(snapshot, SectionGrandCompanies)
}

var rankingMetrics = map[RankingMetric]rankingMetric{
	MetricPopulation:  {part: countOf(func(c LocationCounts) int64 { return c.Total })},
	MetricActivity:    {rate: true, part: countOf(func(c LocationCounts) int64 { return c.Active })},
	MetricVeterans:    {rate: true, ready: PrivacyReady, part: countOf(func(c LocationCounts) int64 { return c.AtCap })},
	MetricPrivacy:     {rate: true, ready: PrivacyReady, part: countOf(func(c LocationCounts) int64 { return c.Private })},
	MetricGrowth:      {rate: true, scale: 1000, part: newCharacters},
	MetricGrowthCount: {part: newCharacters},

	MetricLeanMaelstrom:      {rate: true, ready: grandCompaniesReady, part: companyMembers(GrandCompanyMaelstrom)},
	MetricLeanTwinAdder:      {rate: true, ready: grandCompaniesReady, part: companyMembers(GrandCompanyTwinAdder)},
	MetricLeanImmortalFlames: {rate: true, ready: grandCompaniesReady, part: companyMembers(GrandCompanyImmortalFlames)},
}

// leanCompanies names the Grand Company each lean metric ranks.
var leanCompanies = map[RankingMetric]string{
	MetricLeanMaelstrom:      GrandCompanyMaelstrom,
	MetricLeanTwinAdder:      GrandCompanyTwinAdder,
	MetricLeanImmortalFlames: GrandCompanyImmortalFlames,
}

// LeanCompany returns the Grand Company a lean metric ranks; false for any
// other metric.
func LeanCompany(metric RankingMetric) (GrandCompany, bool) {
	name, ok := leanCompanies[metric]
	if !ok {
		return GrandCompany{}, false
	}
	for _, gc := range grandCompanies {
		if gc.Name == name {
			return gc, true
		}
	}
	return GrandCompany{}, false
}

// RankingQuery selects one board: a metric, the level ranked (world or
// datacenter) and an optional region filter ("" for every region).
type RankingQuery struct {
	Metric RankingMetric
	By     LocationLevel
	Region string
}

// RankingRow is one entry of a board.
type RankingRow struct {
	// Rank counts from 1 in board order; ties are ordered by key, so every
	// row has its own rank.
	Rank int
	Key  string
	// Parent is a world's datacenter, or a datacenter's region.
	Parent string
	// Value is what the board ranks by: a count, or a share from 0 to 1.
	Value float64
	// ShareOfLeader is Value over the leader's value (0 to 1).
	ShareOfLeader float64
	// Part is the counted characters (active, at cap, ...) and Total the
	// entry's population; for population boards both are the total.
	Part, Total int64
}

// RankingBoard is an ordered board.
type RankingBoard struct {
	Query RankingQuery
	// Ready is false while the snapshot predates the metric's figures.
	Ready bool
	Rows  []RankingRow
	// MinimumSize is the population a rate board needs (see
	// MinimumRankedSize); Excluded counts the entries below it.
	MinimumSize int64
	Excluded    int
}

// Rankings builds the board query asks for from snapshot.
func Rankings(snapshot *contract.UIStatsSnapshot, query RankingQuery) RankingBoard {
	board := RankingBoard{Query: query}
	metric, ok := rankingMetrics[query.Metric]
	if !ok || snapshot == nil || (metric.ready != nil && !metric.ready(snapshot)) {
		return board
	}
	board.Ready = true
	var candidates []LocationCounts
	for _, c := range SnapshotLocations(snapshot, query.By) {
		_, region := locationParent(query.By, c.Key)
		if region == "" || (query.Region != "" && !strings.EqualFold(region, query.Region)) {
			continue
		}
		candidates = append(candidates, c)
	}
	if metric.rate {
		populations := make([]int64, len(candidates))
		for i, c := range candidates {
			populations[i] = c.Total
		}
		board.MinimumSize = MinimumRankedSize(populations)
	}
	for _, c := range candidates {
		if metric.rate && (c.Total == 0 || c.Total < board.MinimumSize) {
			board.Excluded++
			continue
		}
		part, total := metric.part(snapshot, query.By, c)
		value := float64(part)
		if metric.rate {
			value = ratio(part, total)
			if metric.scale > 0 {
				value *= metric.scale
			}
		}
		parent, _ := locationParent(query.By, c.Key)
		board.Rows = append(board.Rows, RankingRow{Key: c.Key, Parent: parent, Value: value, Part: part, Total: total})
	}
	sort.SliceStable(board.Rows, func(i, j int) bool {
		if board.Rows[i].Value != board.Rows[j].Value {
			return board.Rows[i].Value > board.Rows[j].Value
		}
		return board.Rows[i].Key < board.Rows[j].Key
	})
	for i := range board.Rows {
		board.Rows[i].Rank = i + 1
		board.Rows[i].ShareOfLeader = ratioFloat(board.Rows[i].Value, board.Rows[0].Value)
	}
	return board
}

// locationParent returns the parent of a world (its datacenter) or a
// datacenter (its region), and the region it sits in. Both are empty for a
// location outside the known world hierarchy.
func locationParent(level LocationLevel, key string) (parent, region string) {
	switch level {
	case LocationWorld:
		dc := WorldDatacenter(key)
		return dc, RegionForDatacenter(dc)
	case LocationDatacenter:
		region := RegionForDatacenter(CanonicalDatacenter(key))
		return region, region
	}
	return "", ""
}

func ratioFloat(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return part / whole
}
