package census

import (
	"sort"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// LocationLevel is a level of the world hierarchy whose groups carry the
// location counts.
type LocationLevel string

const (
	LocationRegion     LocationLevel = "region"
	LocationDatacenter LocationLevel = "datacenter"
	LocationWorld      LocationLevel = "world"
)

// LocationCounts are the census counts of one location (a region, a
// datacenter, a world, or the whole game with an empty Key).
//
//   - AtCap: highest job level at the configured maximum.
//   - Private: achievements hidden.
//   - Unchecked: not hidden, but no latest achievement recorded yet.
//
// Total and the active and at cap counts move every refresh; Private and
// Unchecked are rebuilt nightly (UIStatsSnapshot.PrivacyBuiltAt).
type LocationCounts struct {
	Key                                      string
	Total, Active, AtCap, Private, Unchecked int64
	// InFreeCompany is the characters in any Free Company, rebuilt nightly
	// (UIStatsSnapshot.FreeCompaniesBuiltAt).
	InFreeCompany int64
}

// Public is the number of characters whose achievements were read.
func (c LocationCounts) Public() int64 {
	return c.Total - c.Private - c.Unchecked
}

func locationCounts(g contract.ScopedGroupCount) LocationCounts {
	return LocationCounts{Key: g.Key, Total: g.Total, Active: g.Active, AtCap: g.MaxLevel, Private: g.Private, Unchecked: g.Unchecked, InFreeCompany: g.InFreeCompany}
}

// SnapshotLocations returns every location group at level, sorted by key.
func SnapshotLocations(snapshot *contract.UIStatsSnapshot, level LocationLevel) []LocationCounts {
	if snapshot == nil {
		return nil
	}
	var out []LocationCounts
	for _, g := range snapshot.Groups {
		if g.Scope == (contract.StatsScope{}) && g.Dimension == string(level) && g.Key != "" {
			out = append(out, locationCounts(g))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// SnapshotLocation returns the counts of a stats scope: the summary for the
// whole game, otherwise its region, datacenter or world group (matched
// case-insensitively). An unknown location is all zeros.
func SnapshotLocation(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) LocationCounts {
	if snapshot == nil {
		return LocationCounts{}
	}
	level, key := LocationRegion, scope.Region
	switch {
	case scope.World != "":
		level, key = LocationWorld, scope.World
	case scope.Datacenter != "":
		level, key = LocationDatacenter, scope.Datacenter
	case scope.Region == "":
		s := snapshot.Summary
		return LocationCounts{Total: s.Total, Active: s.Active, AtCap: s.MaxLevel, Private: s.Private, Unchecked: s.Unchecked, InFreeCompany: s.InFreeCompany}
	}
	for _, g := range snapshot.Groups {
		if g.Scope == (contract.StatsScope{}) && g.Dimension == string(level) && strings.EqualFold(g.Key, key) {
			return locationCounts(g)
		}
	}
	return LocationCounts{}
}

// MinimumRankedSize is the smallest population a location needs to appear on
// a rate board (a share such as private ÷ total): a quarter of the median
// population of the candidates, so a handful of characters cannot top it.
func MinimumRankedSize(populations []int64) int64 {
	if len(populations) == 0 {
		return 0
	}
	sorted := append([]int64(nil), populations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	mid := len(sorted) / 2
	median := sorted[mid]
	if len(sorted)%2 == 0 {
		median = (sorted[mid-1] + sorted[mid]) / 2
	}
	return median / 4
}

// PrivacyShare is how a location's characters split by achievement privacy.
// The three shares add up to 1 (all zero for an empty location).
type PrivacyShare struct {
	LocationCounts
	PublicShare, PrivateShare, UncheckedShare float64
}

func newPrivacyShare(c LocationCounts) PrivacyShare {
	return PrivacyShare{
		LocationCounts: c,
		PublicShare:    ratio(c.Public(), c.Total),
		PrivateShare:   ratio(c.Private, c.Total),
		UncheckedShare: ratio(c.Unchecked, c.Total),
	}
}

// PrivacyReady reports whether snapshot carries the privacy figures: a
// schema with the section, built by a full rebuild that counted them.
func PrivacyReady(snapshot *contract.UIStatsSnapshot) bool {
	return SnapshotHasSection(snapshot, SectionPrivacy) && !snapshot.PrivacyBuiltAt.IsZero()
}

// PrivacyFor returns the privacy split of a stats scope.
func PrivacyFor(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) PrivacyShare {
	return newPrivacyShare(SnapshotLocation(snapshot, scope))
}

// PrivacyRegions returns the privacy split of every region, in display order.
func PrivacyRegions(snapshot *contract.UIStatsSnapshot) []PrivacyShare {
	var out []PrivacyShare
	for _, c := range SnapshotLocations(snapshot, LocationRegion) {
		if RegionRank(c.Key) > 0 {
			out = append(out, newPrivacyShare(c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return RegionRank(out[i].Key) < RegionRank(out[j].Key) })
	return out
}

// PrivacyBoard is the worlds with the highest and lowest private share.
type PrivacyBoard struct {
	// MostPrivate is in descending private share, LeastPrivate ascending; a
	// world never appears on both.
	MostPrivate, LeastPrivate []PrivacyShare
	// MinimumSize is the population a world needed to rank, and Excluded how
	// many worlds fell below it (see MinimumRankedSize).
	MinimumSize int64
	Excluded    int
}

// PrivacyWorlds ranks the worlds of region (every region when empty) by
// private share, keeping up to n on each board. Ties break by world name.
func PrivacyWorlds(snapshot *contract.UIStatsSnapshot, region string, n int) PrivacyBoard {
	var candidates []LocationCounts
	for _, c := range SnapshotLocations(snapshot, LocationWorld) {
		if region != "" && !strings.EqualFold(RegionForDatacenter(WorldDatacenter(c.Key)), region) {
			continue
		}
		candidates = append(candidates, c)
	}
	populations := make([]int64, len(candidates))
	for i, c := range candidates {
		populations[i] = c.Total
	}
	board := PrivacyBoard{MinimumSize: MinimumRankedSize(populations)}
	var ranked []PrivacyShare
	for _, c := range candidates {
		if c.Total == 0 || c.Total < board.MinimumSize {
			board.Excluded++
			continue
		}
		ranked = append(ranked, newPrivacyShare(c))
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].PrivateShare != ranked[j].PrivateShare {
			return ranked[i].PrivateShare > ranked[j].PrivateShare
		}
		return ranked[i].Key < ranked[j].Key
	})
	most := min(n, (len(ranked)+1)/2)
	board.MostPrivate = ranked[:most]
	rest := append([]PrivacyShare(nil), ranked[most:]...)
	sort.SliceStable(rest, func(i, j int) bool {
		if rest[i].PrivateShare != rest[j].PrivateShare {
			return rest[i].PrivateShare < rest[j].PrivateShare
		}
		return rest[i].Key < rest[j].Key
	})
	board.LeastPrivate = rest[:min(n, len(rest))]
	return board
}
