package census

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

var ErrUIStatsUnavailable = errors.New("UI statistics unavailable")

func ValidateUIStatsSnapshot(snapshot *contract.UIStatsSnapshot) error {
	if snapshot == nil {
		return ErrUIStatsUnavailable
	}
	if snapshot.SchemaVersion < contract.UIStatsMinSchemaVersion || snapshot.SchemaVersion > contract.UIStatsSchemaVersion {
		return fmt.Errorf("unsupported UI statistics schema version %d", snapshot.SchemaVersion)
	}
	if snapshot.GeneratedAt.IsZero() || snapshot.ActivitySince.IsZero() {
		return errors.New("UI statistics timestamps must be set")
	}
	if snapshot.NewCharactersWindowDays < 0 {
		return errors.New("UI statistics new-characters window cannot be negative")
	}
	if snapshot.SourceCharacters < 0 || snapshot.Summary.Total < 0 || snapshot.Summary.Active < 0 || snapshot.Summary.MaxLevel < 0 {
		return errors.New("UI statistics summary counts cannot be negative")
	}
	if snapshot.Summary.Active > snapshot.Summary.Total || snapshot.Summary.MaxLevel > snapshot.Summary.Total {
		return errors.New("UI statistics summary subset exceeds total")
	}
	if !validLocationCounts(snapshot.Summary.Total, snapshot.Summary.MaxLevel, snapshot.Summary.Private, snapshot.Summary.Unchecked) ||
		!validSubset(snapshot.Summary.InFreeCompany, snapshot.Summary.Total) ||
		!validLevelCounts(snapshot, snapshot.Summary.NoLevels, snapshot.Summary.Levels) {
		return errors.New("invalid UI statistics summary privacy counts")
	}
	seenGroups := make(map[string]struct{}, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		if group.Dimension == "" || group.Total < 0 || group.Active < 0 || group.Active > group.Total ||
			!validLocationCounts(group.Total, group.MaxLevel, group.Private, group.Unchecked) ||
			!validSubset(group.InFreeCompany, group.Total) ||
			!validLevelCounts(snapshot, group.NoLevels, group.Levels) ||
			group.NewCharacters < 0 || group.NewCharactersPrevious < 0 || !nonNegative(group.NewCharactersWeekly) {
			return errors.New("invalid UI statistics group")
		}
		key := scopeKey(group.Scope) + "\x00" + group.Dimension + "\x00" + group.Key
		if _, ok := seenGroups[key]; ok {
			return fmt.Errorf("duplicate UI statistics group %q", key)
		}
		seenGroups[key] = struct{}{}
	}
	seenExpansions := make(map[string]struct{}, len(snapshot.Expansions))
	for _, expansion := range snapshot.Expansions {
		if expansion.Expansion == "" || expansion.Count < 0 {
			return errors.New("invalid UI statistics expansion")
		}
		key := scopeKey(expansion.Scope) + "\x00" + expansion.Expansion
		if _, ok := seenExpansions[key]; ok {
			return fmt.Errorf("duplicate UI statistics expansion %q", key)
		}
		seenExpansions[key] = struct{}{}
	}
	seenDays := make(map[string]struct{}, len(snapshot.NewCharacters))
	for _, day := range snapshot.NewCharacters {
		if day.Count < 0 {
			return errors.New("invalid UI statistics daily count")
		}
		if _, err := time.Parse("2006-01-02", day.Day); err != nil {
			return fmt.Errorf("invalid UI statistics day %q", day.Day)
		}
		key := scopeKey(day.Scope) + "\x00" + day.Day
		if _, ok := seenDays[key]; ok {
			return fmt.Errorf("duplicate UI statistics day %q", key)
		}
		seenDays[key] = struct{}{}
	}
	return validateJobsSection(snapshot.Jobs)
}

// nonNegative reports whether no count is negative.
func nonNegative(counts []int64) bool {
	for _, c := range counts {
		if c < 0 {
			return false
		}
	}
	return true
}

// validSubset reports whether part is a count of some of total's characters.
func validSubset(part, total int64) bool {
	return part >= 0 && part <= total
}

// validLevelCounts reports whether level band counts are none negative and,
// when present, one per level band of the snapshot. Their sum is not held to
// the total: the incremental refresh moves the totals but carries the
// nightly level bands forward unchanged.
func validLevelCounts(snapshot *contract.UIStatsSnapshot, noLevels int64, levels []int64) bool {
	if noLevels < 0 || !nonNegative(levels) {
		return false
	}
	return len(levels) == 0 || len(levels) == len(snapshot.LevelBands)
}

// validLocationCounts reports whether the at cap and privacy counts fit
// inside total: none negative, at cap at most total, and private and
// unchecked (disjoint) together at most total.
func validLocationCounts(total, atCap, private, unchecked int64) bool {
	return atCap >= 0 && private >= 0 && unchecked >= 0 && atCap <= total && private+unchecked <= total
}

func SnapshotGroups(snapshot *contract.UIStatsSnapshot, dimension string, scope contract.StatsScope) []contract.GroupCount {
	if snapshot == nil {
		return nil
	}
	var out []contract.GroupCount
	for _, group := range snapshot.Groups {
		if group.Dimension == dimension && group.Scope == scope {
			out = append(out, contract.GroupCount{Key: group.Key, Total: group.Total, Active: group.Active})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func SnapshotExpansions(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) []contract.ExpansionCount {
	if snapshot == nil {
		return nil
	}
	var out []contract.ExpansionCount
	for _, expansion := range snapshot.Expansions {
		if expansion.Scope == scope {
			out = append(out, contract.ExpansionCount{Expansion: expansion.Expansion, Count: expansion.Count})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Expansion < out[j].Expansion })
	return out
}

func SnapshotDaily(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) []contract.DailyCount {
	if snapshot == nil {
		return nil
	}
	var out []contract.DailyCount
	for _, day := range snapshot.NewCharacters {
		if day.Scope == scope {
			out = append(out, contract.DailyCount{Day: day.Day, Count: day.Count})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

func scopeKey(scope contract.StatsScope) string {
	return strings.Join([]string{scope.Region, scope.Datacenter, scope.World}, "\x00")
}

// NewCharacterTotals holds new-character sums for the current and previous
// new-characters windows, both Days complete UTC days long and ending with
// the last complete UTC day before the snapshot's generation day. StartDay
// is the first UTC day (YYYY-MM-DD) of the current window.
type NewCharacterTotals struct {
	Current  int64
	Previous int64
	StartDay string
	Days     int
}

// legacyNewCharactersWindowDays is the fixed window of snapshots built
// before the window was recorded (schema 3 and earlier).
const legacyNewCharactersWindowDays = 30

// SnapshotNewCharactersWindowDays is the new-characters window a snapshot
// was built with, in days: the recorded window, or the fixed 30 days of
// snapshots from before it was recorded. It is 0 for a nil snapshot.
func SnapshotNewCharactersWindowDays(snapshot *contract.UIStatsSnapshot) int {
	if snapshot == nil {
		return 0
	}
	if snapshot.NewCharactersWindowDays > 0 {
		return snapshot.NewCharactersWindowDays
	}
	return legacyNewCharactersWindowDays
}

// NewCharactersTotals sums the snapshot's new characters for the given stats
// scope across the current window (the window's complete UTC days before the
// generation day) and the previous window (the same number of days before
// it). The global scope reads the whole-game daily series. World, datacenter
// and region scopes sum the window totals on the member worlds' groups of
// the world hierarchy, with a datacenter selection reducing to that
// datacenter's world set; snapshots from before the window was recorded sum
// those worlds' daily series rows instead. A selection matching no known
// world, datacenter, or region totals zero rather than falling back to
// global. Missing days count as zero.
func NewCharactersTotals(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) NewCharacterTotals {
	if snapshot == nil {
		return NewCharacterTotals{}
	}
	if scope != (contract.StatsScope{}) {
		return sumWorlds(emptyNewCharacterTotals(SnapshotNewCharactersBounds(snapshot)), worldNewCharacters(snapshot), worldSet(scope))
	}
	bounds := SnapshotNewCharactersBounds(snapshot)
	totals := emptyNewCharacterTotals(bounds)
	for _, day := range snapshot.NewCharacters {
		if day.Scope != (contract.StatsScope{}) {
			continue
		}
		parsed, err := time.Parse("2006-01-02", day.Day)
		if err != nil {
			continue
		}
		switch {
		case bounds.InCurrent(parsed):
			totals.Current += day.Count
		case bounds.InPrevious(parsed):
			totals.Previous += day.Count
		}
	}
	return totals
}

// emptyNewCharacterTotals is a zero count over bounds' windows.
func emptyNewCharacterTotals(bounds NewCharactersBounds) NewCharacterTotals {
	return NewCharacterTotals{Days: bounds.Days, StartDay: bounds.CurrentStart.Format("2006-01-02")}
}

// worldNewCharacters indexes every world's new characters in the current and
// previous window, by world name as the snapshot spells it, in one pass: the
// world groups' window totals, or on snapshots from before the window was
// recorded the per-world daily rows. A world without new characters is
// missing.
func worldNewCharacters(snapshot *contract.UIStatsSnapshot) map[string]NewCharacterTotals {
	bounds := SnapshotNewCharactersBounds(snapshot)
	index := make(map[string]NewCharacterTotals)
	add := func(world string, current, previous int64) {
		totals, ok := index[world]
		if !ok {
			totals = emptyNewCharacterTotals(bounds)
		}
		totals.Current += current
		totals.Previous += previous
		index[world] = totals
	}
	if snapshot.NewCharactersWindowDays > 0 {
		for _, group := range snapshot.Groups {
			if group.Scope == (contract.StatsScope{}) && group.Dimension == "world" {
				add(group.Key, group.NewCharacters, group.NewCharactersPrevious)
			}
		}
		return index
	}
	for _, day := range snapshot.NewCharacters {
		if day.Scope.World == "" {
			continue
		}
		parsed, err := time.Parse("2006-01-02", day.Day)
		if err != nil {
			continue
		}
		switch {
		case bounds.InCurrent(parsed):
			add(day.Scope.World, day.Count, 0)
		case bounds.InPrevious(parsed):
			add(day.Scope.World, 0, day.Count)
		}
	}
	return index
}

// sumWorlds adds the indexed figures of the selected worlds to base (the
// snapshot's empty totals).
func sumWorlds(base NewCharacterTotals, index map[string]NewCharacterTotals, selected map[string]struct{}) NewCharacterTotals {
	totals := base
	for world := range selected {
		totals.Current += index[world].Current
		totals.Previous += index[world].Previous
	}
	return totals
}

// WeeklyCount is one week of a weekly new-character series: the UTC days
// Start to End (YYYY-MM-DD, both inclusive) and the characters who earned
// their first company chocobo in them.
type WeeklyCount struct {
	Start string
	End   string
	Count int64
}

// WorldNewCharactersWeekly is a world's weekly new-character series, oldest
// week first (NewCharactersBounds.Weeks). A world without new characters
// has a zero series; a snapshot from before the series (no recorded window)
// has none.
func WorldNewCharactersWeekly(snapshot *contract.UIStatsSnapshot, world string) []WeeklyCount {
	if snapshot == nil || snapshot.NewCharactersWindowDays <= 0 {
		return nil
	}
	world = CanonicalWorld(world)
	var counts []int64
	for _, group := range snapshot.Groups {
		if group.Scope == (contract.StatsScope{}) && group.Dimension == "world" && group.Key == world {
			counts = group.NewCharactersWeekly
			break
		}
	}
	bounds := SnapshotNewCharactersBounds(snapshot)
	weeks := bounds.Weeks()
	out := make([]WeeklyCount, weeks)
	start := bounds.WeeksStart()
	for i := range out {
		weekStart := start.AddDate(0, 0, 7*i)
		out[i] = WeeklyCount{Start: weekStart.Format("2006-01-02"), End: weekStart.AddDate(0, 0, 6).Format("2006-01-02")}
		if i < len(counts) {
			out[i].Count = counts[i]
		}
	}
	return out
}

// worldSet resolves a stats scope to the set of world names whose series
// rows sum up to the scope. World scopes resolve their name to the
// canonical spelling; datacenter and region scopes resolve to the member
// worlds of the world hierarchy.
func worldSet(scope contract.StatsScope) map[string]struct{} {
	set := make(map[string]struct{})
	switch level, key := ScopeLevel(scope); level {
	case LocationGlobal:
	case LocationWorld:
		set[CanonicalWorld(key)] = struct{}{}
	default:
		for _, world := range Worlds() {
			if ScopeHasWorld(scope, world) {
				set[world] = struct{}{}
			}
		}
	}
	return set
}
