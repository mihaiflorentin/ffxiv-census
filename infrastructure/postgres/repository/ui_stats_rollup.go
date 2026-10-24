package repository

import (
	"sort"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// The UI statistics refreshes read characters at one fine grain (region,
// datacenter, world, race, clan, gender, Grand Company) and roll those rows up in Go into
// every scope and dimension the snapshot holds. The full refresh, the
// incremental delta and the incremental active pass all share this grain and
// statsRollup, so the refresh modes cannot disagree about grouping.
//
// Extending the roll-up:
//   - A new count (a filtered COUNT or a SUM in the fine-grain query): add a
//     field to fineCounts, its term to fineCounts.plus, its column to each
//     fine-grain query and to the Scan in queryFineGrain.
//   - A new dimension (say Grand Company): add the column to fineKey,
//     fineGrainKeyColumns, fineGrainGroupBy and the Scan, then one entry in
//     rollupDimensions. A dimension that only exists globally (a cross such
//     as Grand Company by race) sets globalOnly.

// fineGrainKeyColumns and fineGrainGroupBy are the fine-grain key of every
// UI statistics query, in fineKey order.
const fineGrainKeyColumns = `COALESCE(region, '') AS region, COALESCE(datacenter, '') AS datacenter,
		       COALESCE(world, '') AS world, COALESCE(race, '') AS race,
		       COALESCE(tribe, '') AS tribe, gender,
		       COALESCE(grand_company, '') AS grand_company`

const fineGrainGroupBy = `1, 2, 3, 4, 5, 6, 7`

// fineKey is one fine-grain group of characters.
type fineKey struct {
	Region, Datacenter, World string
	Race, Tribe               string
	Gender                    int64
	GrandCompany              string
}

// fineCounts are the counts measured per fine-grain group and per rolled-up
// group. Every count rolls up by plain addition.
type fineCounts struct {
	Total, Active, MaxLevel int64
}

func (c fineCounts) plus(o fineCounts) fineCounts {
	return fineCounts{Total: c.Total + o.Total, Active: c.Active + o.Active, MaxLevel: c.MaxLevel + o.MaxLevel}
}

func (c fineCounts) statsSummary() contract.StatsSummary {
	return contract.StatsSummary{Total: c.Total, Active: c.Active, MaxLevel: c.MaxLevel}
}

// groupKey identifies one scoped group in the snapshot.
type groupKey struct {
	Scope     contract.StatsScope
	Dimension string
	Key       string
}

// rollupDimension is one demographic dimension of the snapshot.
type rollupDimension struct {
	name       string
	key        func(fineKey) string
	globalOnly bool
}

var rollupDimensions = []rollupDimension{
	{name: "race", key: func(k fineKey) string { return k.Race }},
	{name: "tribe", key: func(k fineKey) string { return k.Tribe }},
	{name: "gender", key: func(k fineKey) string { return genderName(k.Gender) }},
	{name: "race_gender", key: func(k fineKey) string { return k.Race + "|" + genderName(k.Gender) }},
	{name: "grand_company", key: func(k fineKey) string { return grandCompanyName(k.GrandCompany) }},
	{name: "grand_company_race", key: func(k fineKey) string { return grandCompanyName(k.GrandCompany) + "|" + k.Race }, globalOnly: true},
}

// statsRollup accumulates fine-grain rows into the summary, the location
// totals (global region, datacenter and world groups) and every demographic
// dimension at the global, region, datacenter and world scopes.
type statsRollup struct {
	summary fineCounts
	groups  map[groupKey]fineCounts
}

func newStatsRollup() *statsRollup {
	return &statsRollup{groups: map[groupKey]fineCounts{}}
}

func (r *statsRollup) add(k fineKey, c fineCounts) {
	r.summary = r.summary.plus(c)

	// Location totals. A character without a world has no world group; the
	// region and datacenter groups keep their empty key as before.
	r.addGroup(groupKey{Dimension: "region", Key: k.Region}, c)
	r.addGroup(groupKey{Dimension: "datacenter", Key: k.Datacenter}, c)
	if k.World != "" {
		r.addGroup(groupKey{Dimension: "world", Key: k.World}, c)
	}

	// Demographics per scope. An empty location is no scope: such characters
	// count only globally.
	scopes := []contract.StatsScope{{}}
	if k.Region != "" {
		scopes = append(scopes, contract.StatsScope{Region: k.Region})
	}
	if k.Datacenter != "" {
		scopes = append(scopes, contract.StatsScope{Datacenter: k.Datacenter})
	}
	if k.World != "" {
		scopes = append(scopes, contract.StatsScope{World: k.World})
	}
	for _, d := range rollupDimensions {
		key := d.key(k)
		for i, scope := range scopes {
			if d.globalOnly && i > 0 {
				break
			}
			r.addGroup(groupKey{Scope: scope, Dimension: d.name, Key: key}, c)
		}
	}
}

func (r *statsRollup) addGroup(key groupKey, c fineCounts) {
	r.groups[key] = r.groups[key].plus(c)
}

// sortedKeys returns the rolled-up group keys in a stable order.
func (r *statsRollup) sortedKeys() []groupKey {
	return sortGroupKeys(r.groups)
}

func sortGroupKeys[V any](groups map[groupKey]V) []groupKey {
	keys := make([]groupKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.Scope.Region != b.Scope.Region {
			return a.Scope.Region < b.Scope.Region
		}
		if a.Scope.Datacenter != b.Scope.Datacenter {
			return a.Scope.Datacenter < b.Scope.Datacenter
		}
		if a.Scope.World != b.Scope.World {
			return a.Scope.World < b.Scope.World
		}
		if a.Dimension != b.Dimension {
			return a.Dimension < b.Dimension
		}
		return a.Key < b.Key
	})
	return keys
}

// grandCompanyName is a Grand Company group key: the stored company name, or
// "None" for a character in no company, so the shares add up to 100%.
func grandCompanyName(stored string) string {
	if stored == "" {
		return census.GrandCompanyNone
	}
	return stored
}

func genderName(value int64) string {
	switch value {
	case 1:
		return "Male"
	case 2:
		return "Female"
	default:
		return "Unknown"
	}
}
