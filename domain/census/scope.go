package census

import (
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// LocationGlobal is the level of the whole-game scope (no field set).
const LocationGlobal LocationLevel = ""

// ScopeLevel returns the level a stats scope selects, its narrowest set
// field (world, then datacenter, then region), and the name at that level.
// The whole game is LocationGlobal with an empty name. Every reader that
// branches on a scope's kind goes through here.
func ScopeLevel(scope contract.StatsScope) (LocationLevel, string) {
	switch {
	case scope.World != "":
		return LocationWorld, scope.World
	case scope.Datacenter != "":
		return LocationDatacenter, scope.Datacenter
	case scope.Region != "":
		return LocationRegion, scope.Region
	}
	return LocationGlobal, ""
}

// ScopeAt is the stats scope of one location: the inverse of ScopeLevel.
func ScopeAt(level LocationLevel, key string) contract.StatsScope {
	switch level {
	case LocationWorld:
		return contract.StatsScope{World: key}
	case LocationDatacenter:
		return contract.StatsScope{Datacenter: key}
	case LocationRegion:
		return contract.StatsScope{Region: key}
	}
	return contract.StatsScope{}
}

// worldAt is the name of a world's location at level: the world itself, its
// datacenter or its region ("" for the whole game or an unknown world).
func worldAt(world string, level LocationLevel) string {
	switch level {
	case LocationWorld:
		return world
	case LocationDatacenter:
		return WorldDatacenter(world)
	case LocationRegion:
		return RegionForDatacenter(WorldDatacenter(world))
	}
	return ""
}

// ScopeHasWorld reports whether a world lies inside scope, matching names
// case-insensitively. Every world is inside the whole-game scope.
func ScopeHasWorld(scope contract.StatsScope, world string) bool {
	level, key := ScopeLevel(scope)
	if level == LocationGlobal {
		return true
	}
	return strings.EqualFold(worldAt(world, level), key)
}

// KnownScope reports whether scope is the whole game or exactly one known
// region, datacenter or world in its canonical spelling. Caches keyed on a
// scope only remember known scopes, so request filters cannot grow them.
func KnownScope(scope contract.StatsScope) bool {
	set := 0
	for _, field := range []string{scope.Region, scope.Datacenter, scope.World} {
		if field != "" {
			set++
		}
	}
	if set > 1 {
		return false
	}
	level, key := ScopeLevel(scope)
	switch level {
	case LocationWorld:
		_, ok := worldDatacenters[key]
		return ok
	case LocationDatacenter:
		_, ok := datacenterRegion[key]
		return ok
	case LocationRegion:
		return RegionRank(key) > 0
	}
	return true
}
