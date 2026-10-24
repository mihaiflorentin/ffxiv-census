package repository

import (
	"errors"
	"fmt"
	"sort"
	"strings"

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
//   - The level band counts are a variable run of columns (one per band,
//     see levelCountColumns) ending each fine-grain query's fineCounts
//     columns; queryFineGrain takes the band count to scan them.
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
	// Private and Unchecked are the achievement privacy counts; see
	// privacyCountColumns.
	Private, Unchecked int64
	// InFreeCompany is the characters in any Free Company.
	InFreeCompany int64
	// NoLevels and Levels are the level band counts (see
	// levelCountColumns), kept on the summary and the location groups only.
	NoLevels int64
	Levels   []int64
}

func (c fineCounts) plus(o fineCounts) fineCounts {
	return fineCounts{
		Total: c.Total + o.Total, Active: c.Active + o.Active, MaxLevel: c.MaxLevel + o.MaxLevel,
		Private: c.Private + o.Private, Unchecked: c.Unchecked + o.Unchecked,
		InFreeCompany: c.InFreeCompany + o.InFreeCompany,
		NoLevels:      c.NoLevels + o.NoLevels, Levels: addLevels(c.Levels, o.Levels),
	}
}

// addLevels adds two runs of level band counts into a new slice.
func addLevels(a, b []int64) []int64 {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make([]int64, max(len(a), len(b)))
	copy(out, a)
	for i, v := range b {
		out[i] += v
	}
	return out
}

func (c fineCounts) statsSummary() contract.StatsSummary {
	return contract.StatsSummary{Total: c.Total, Active: c.Active, MaxLevel: c.MaxLevel, Private: c.Private, Unchecked: c.Unchecked, InFreeCompany: c.InFreeCompany,
		Levels: c.Levels, NoLevels: c.NoLevels}
}

// privacyCountColumns are the achievement privacy counts of a fine-grain
// query over characters: private (achievements hidden) and unchecked (not
// hidden, and no latest achievement recorded yet).
const privacyCountColumns = `COUNT(*) FILTER (WHERE achievements_private = 1) AS private,
		       COUNT(*) FILTER (WHERE achievements_private = 0 AND latest_achievement_at IS NULL) AS unchecked`

// inFreeCompanyColumn counts the characters in any Free Company.
const inFreeCompanyColumn = `COUNT(*) FILTER (WHERE fc_id IS NOT NULL) AS in_free_company`

// levelCountColumns are the level band counts of a fine-grain query:
// no_levels (highest job level below the first band) then one count per
// band, each a COUNT FILTERed by max_job_level against bounds bound as
// parameters $first, $first+1, ... (returned as args, in order). Only the
// full rebuild counts them: the incremental refresh carries them forward.
// No bands is an error: no_levels is bounded by the first band.
func levelCountColumns(bands []contract.StatsLevelBand, first int) (string, []any, error) {
	if len(bands) == 0 {
		return "", nil, errors.New("level count columns: no level bands")
	}
	var cols []string
	var args []any
	param := func(v uint32) string {
		args = append(args, int64(v))
		return fmt.Sprintf("$%d", first+len(args)-1)
	}
	count := func(cond, name string) {
		cols = append(cols, fmt.Sprintf("COUNT(*) FILTER (WHERE %s) AS %s", cond, name))
	}
	count("max_job_level < "+param(bands[0].Min), "no_levels")
	for i, b := range bands {
		name := fmt.Sprintf("level_band_%d", i)
		if b.Open() {
			count("max_job_level >= "+param(b.Min), name)
		} else {
			count(fmt.Sprintf("max_job_level BETWEEN %s AND %s", param(b.Min), param(b.Max)), name)
		}
	}
	return strings.Join(cols, ",\n\t\t       "), args, nil
}

// zeroLevelColumns are levelCountColumns' columns as zeros, for a
// fine-grain query that does not count level bands.
func zeroLevelColumns(bands int) string {
	cols := make([]string, bands+1)
	for i := range cols {
		cols[i] = "0"
	}
	return strings.Join(cols, ", ")
}

// isLocationGroup reports whether key is a location group (the global-scope
// region, datacenter or world totals), the only groups that carry the at cap,
// privacy and level band counts.
func isLocationGroup(key groupKey) bool {
	if key.Scope != (contract.StatsScope{}) {
		return false
	}
	switch key.Dimension {
	case "region", "datacenter", "world":
		return true
	}
	return false
}

// scopedGroupCount is one rolled-up group as the snapshot stores it.
func scopedGroupCount(key groupKey, c fineCounts) contract.ScopedGroupCount {
	g := contract.ScopedGroupCount{Scope: key.Scope, Dimension: key.Dimension, Key: key.Key, Total: c.Total, Active: c.Active}
	if isLocationGroup(key) {
		g.MaxLevel, g.Private, g.Unchecked, g.InFreeCompany = c.MaxLevel, c.Private, c.Unchecked, c.InFreeCompany
		g.Levels, g.NoLevels = c.Levels, c.NoLevels
	}
	return g
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
	if !isLocationGroup(key) {
		c.Levels, c.NoLevels = nil, 0
	}
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
