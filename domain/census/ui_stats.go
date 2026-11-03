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
			group.NewCharacters < 0 || group.NewCharactersPrevious < 0 || !nonNegative(group.NewCharactersDaily) ||
			group.Created < 0 || group.CreatedPrevious < 0 || !nonNegative(group.CreatedDaily) {
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
	if err := validateDailySeries(snapshot.NewCharacters); err != nil {
		return err
	}
	if err := validateDailySeries(snapshot.Created); err != nil {
		return err
	}
	if snapshot.CreatedExactFrom != "" {
		if _, err := time.Parse("2006-01-02", snapshot.CreatedExactFrom); err != nil {
			return fmt.Errorf("invalid UI statistics created cut day %q", snapshot.CreatedExactFrom)
		}
	}
	return validateJobsSection(snapshot.Jobs)
}

// validateDailySeries rejects a daily series with a negative count, an
// unparseable day or a day twice in one scope.
func validateDailySeries(days []contract.ScopedDailyCount) error {
	seenDays := make(map[string]struct{}, len(days))
	for _, day := range days {
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
	return nil
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
// is the first UTC day (YYYY-MM-DD) of the current window. Current and
// Previous count the characters who earned their first company chocobo,
// Created and CreatedPrevious the characters created (by character ID).
type NewCharacterTotals struct {
	Current         int64
	Previous        int64
	Created         int64
	CreatedPrevious int64
	StartDay        string
	Days            int
}

// CreatedWindow returns the created characters as the window's Current and
// Previous, for the figures that compare two windows.
func (t NewCharacterTotals) CreatedWindow() NewCharacterTotals {
	return NewCharacterTotals{Current: t.Created, Previous: t.CreatedPrevious, StartDay: t.StartDay, Days: t.Days}
}

// CreatedReady reports whether the snapshot carries the created-character
// figures (schema 5).
func CreatedReady(snapshot *contract.UIStatsSnapshot) bool {
	return snapshot != nil && !snapshot.CreatedBuiltAt.IsZero()
}

// CreatedEstimatedBefore returns the first UTC day whose created characters
// are dated exactly, by their first sighting; earlier days are estimated.
// False when the snapshot has no created figures.
func CreatedEstimatedBefore(snapshot *contract.UIStatsSnapshot) (time.Time, bool) {
	if !CreatedReady(snapshot) {
		return time.Time{}, false
	}
	day, err := time.Parse("2006-01-02", snapshot.CreatedExactFrom)
	return day, err == nil
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
	totals.Current, totals.Previous = windowSums(bounds, snapshot.NewCharacters)
	totals.Created, totals.CreatedPrevious = windowSums(bounds, snapshot.Created)
	return totals
}

// windowSums sums a daily series' whole-game rows over the current and the
// previous window.
func windowSums(bounds NewCharactersBounds, days []contract.ScopedDailyCount) (current, previous int64) {
	for _, day := range days {
		if day.Scope != (contract.StatsScope{}) {
			continue
		}
		parsed, err := time.Parse("2006-01-02", day.Day)
		if err != nil {
			continue
		}
		switch {
		case bounds.InCurrent(parsed):
			current += day.Count
		case bounds.InPrevious(parsed):
			previous += day.Count
		}
	}
	return current, previous
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
				totals := index[group.Key]
				totals.Created += group.Created
				totals.CreatedPrevious += group.CreatedPrevious
				index[group.Key] = totals
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
		totals.Created += index[world].Created
		totals.CreatedPrevious += index[world].CreatedPrevious
	}
	return totals
}

// DailyNewCharacters is one UTC day (YYYY-MM-DD) of a new-character
// series: the characters who earned their first company chocobo (Count) and
// the characters created. Estimated marks a created count dated by the
// calibration curve, before CreatedEstimatedBefore.
type DailyNewCharacters struct {
	Day       string
	Count     int64
	Created   int64
	Estimated bool
}

// NewCharactersDaily is a new-character series over the current window, one
// entry per complete UTC day, oldest first: the whole game's (world "")
// from the daily series, or a world's from its group. A world without new
// characters has a zero series; a snapshot from before the world groups'
// daily series (schema 5 and earlier) has none for a world.
func NewCharactersDaily(snapshot *contract.UIStatsSnapshot, world string) []DailyNewCharacters {
	if snapshot == nil {
		return nil
	}
	bounds := SnapshotNewCharactersBounds(snapshot)
	var chocobos, created []int64
	if world != "" {
		if snapshot.SchemaVersion < 6 {
			return nil
		}
		world = CanonicalWorld(world)
		for _, group := range snapshot.Groups {
			if group.Scope == (contract.StatsScope{}) && group.Dimension == "world" && group.Key == world {
				chocobos, created = group.NewCharactersDaily, group.CreatedDaily
				break
			}
		}
	} else {
		chocobos, created = dailyCounts(bounds, snapshot.NewCharacters), dailyCounts(bounds, snapshot.Created)
	}
	exact, hasCreated := CreatedEstimatedBefore(snapshot)
	out := make([]DailyNewCharacters, bounds.Days)
	for i := range out {
		day := bounds.CurrentStart.AddDate(0, 0, i)
		out[i] = DailyNewCharacters{Day: day.Format("2006-01-02"), Estimated: hasCreated && day.Before(exact)}
		if i < len(chocobos) {
			out[i].Count = chocobos[i]
		}
		if i < len(created) {
			out[i].Created = created[i]
		}
	}
	return out
}

// dailyCounts lays a daily series' whole-game rows out over the current
// window (NewCharactersBounds.Day).
func dailyCounts(bounds NewCharactersBounds, days []contract.ScopedDailyCount) []int64 {
	counts := make([]int64, bounds.Days)
	for _, day := range days {
		parsed, err := time.Parse("2006-01-02", day.Day)
		if err != nil || day.Scope != (contract.StatsScope{}) {
			continue
		}
		if i := bounds.Day(parsed); i >= 0 {
			counts[i] += day.Count
		}
	}
	return counts
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
