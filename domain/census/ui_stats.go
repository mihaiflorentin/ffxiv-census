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
	if snapshot.SourceCharacters < 0 || snapshot.Summary.Total < 0 || snapshot.Summary.Active < 0 || snapshot.Summary.MaxLevel < 0 {
		return errors.New("UI statistics summary counts cannot be negative")
	}
	if snapshot.Summary.Active > snapshot.Summary.Total || snapshot.Summary.MaxLevel > snapshot.Summary.Total {
		return errors.New("UI statistics summary subset exceeds total")
	}
	seenGroups := make(map[string]struct{}, len(snapshot.Groups))
	for _, group := range snapshot.Groups {
		if group.Dimension == "" || group.Total < 0 || group.Active < 0 || group.Active > group.Total {
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
	return nil
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
// 30-day windows, both ending on the last complete UTC day before the
// snapshot's generation day. StartDay is
// the first UTC day (YYYY-MM-DD) of the current window.
type NewCharacterTotals struct {
	Current  int64
	Previous int64
	StartDay string
}

// NewCharactersWindow sums the snapshot's daily new-character series for the
// given stats scope across the current window (the 30 complete UTC days
// before the generation day) and the previous window (the 30 days before
// it). Global and world scopes read their exact-scope series rows;
// datacenter and region scopes sum the member worlds of the world hierarchy,
// with a datacenter selection reducing to that datacenter's world set. A
// selection matching no known world, datacenter, or region totals zero
// rather than falling back to global. Missing days count as zero.
func NewCharactersWindow(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) NewCharacterTotals {
	var totals NewCharacterTotals
	if snapshot == nil {
		return totals
	}
	// The generation day is still in progress; both windows end on the last
	// complete UTC day.
	end := snapshot.GeneratedAt.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	currentStart := end.AddDate(0, 0, -29)
	previousStart := end.AddDate(0, 0, -59)
	totals.StartDay = currentStart.Format("2006-01-02")

	global := scope == (contract.StatsScope{})
	selected := worldSet(scope)
	for _, day := range snapshot.NewCharacters {
		if global {
			if day.Scope != (contract.StatsScope{}) {
				continue
			}
		} else {
			if day.Scope.World == "" {
				continue
			}
			if _, ok := selected[day.Scope.World]; !ok {
				continue
			}
		}
		parsed, err := time.Parse("2006-01-02", day.Day)
		if err != nil {
			continue
		}
		switch {
		case !parsed.Before(currentStart) && !parsed.After(end):
			totals.Current += day.Count
		case !parsed.Before(previousStart) && parsed.Before(currentStart):
			totals.Previous += day.Count
		}
	}
	return totals
}

// worldSet resolves a stats scope to the set of world names whose series
// rows sum up to the scope. World scopes resolve their name to the
// canonical spelling; datacenter and region scopes resolve to the member
// worlds of the world hierarchy.
func worldSet(scope contract.StatsScope) map[string]struct{} {
	set := make(map[string]struct{})
	switch {
	case scope.World != "":
		set[CanonicalWorld(scope.World)] = struct{}{}
	case scope.Datacenter != "":
		for _, world := range WorldsForDatacenter(scope.Datacenter) {
			set[world] = struct{}{}
		}
	case scope.Region != "":
		for _, world := range WorldsForRegion(scope.Region) {
			set[world] = struct{}{}
		}
	}
	return set
}
