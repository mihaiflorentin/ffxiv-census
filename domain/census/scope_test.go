package census

import (
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestScopeLevelPicksTheNarrowestField(t *testing.T) {
	tests := []struct {
		scope contract.StatsScope
		level LocationLevel
		key   string
	}{
		{contract.StatsScope{}, LocationGlobal, ""},
		{contract.StatsScope{Region: "EU"}, LocationRegion, "EU"},
		{contract.StatsScope{Datacenter: "Chaos"}, LocationDatacenter, "Chaos"},
		{contract.StatsScope{World: "Ragnarok"}, LocationWorld, "Ragnarok"},
		{contract.StatsScope{Region: "EU", Datacenter: "Chaos", World: "Ragnarok"}, LocationWorld, "Ragnarok"},
	}
	for _, tt := range tests {
		level, key := ScopeLevel(tt.scope)
		if level != tt.level || key != tt.key {
			t.Errorf("ScopeLevel(%+v) = %q %q, want %q %q", tt.scope, level, key, tt.level, tt.key)
		}
		if tt.scope.Region == "" || tt.scope.World == "" {
			if back := ScopeAt(level, key); back != tt.scope {
				t.Errorf("ScopeAt(%q, %q) = %+v, want %+v", level, key, back, tt.scope)
			}
		}
	}
}

func TestScopeHasWorld(t *testing.T) {
	tests := []struct {
		scope contract.StatsScope
		world string
		want  bool
	}{
		{contract.StatsScope{}, "Ragnarok", true},
		{contract.StatsScope{World: "ragnarok"}, "Ragnarok", true},
		{contract.StatsScope{World: "Lich"}, "Ragnarok", false},
		{contract.StatsScope{Datacenter: "chaos"}, "Ragnarok", true},
		{contract.StatsScope{Datacenter: "Light"}, "Ragnarok", false},
		{contract.StatsScope{Region: "eu"}, "Ragnarok", true},
		{contract.StatsScope{Region: "NA"}, "Ragnarok", false},
		{contract.StatsScope{Region: "Nowhere"}, "Ragnarok", false},
	}
	for _, tt := range tests {
		if got := ScopeHasWorld(tt.scope, tt.world); got != tt.want {
			t.Errorf("ScopeHasWorld(%+v, %q) = %v, want %v", tt.scope, tt.world, got, tt.want)
		}
	}
}

func TestKnownScopeAcceptsOnlyCanonicalLocations(t *testing.T) {
	tests := []struct {
		scope contract.StatsScope
		want  bool
	}{
		{contract.StatsScope{}, true},
		{contract.StatsScope{Region: "EU"}, true},
		{contract.StatsScope{Datacenter: "Chaos"}, true},
		{contract.StatsScope{World: "Ragnarok"}, true},
		{contract.StatsScope{World: "ragnarok"}, false},
		{contract.StatsScope{World: "Nowhere"}, false},
		{contract.StatsScope{Datacenter: "Nowhere"}, false},
		{contract.StatsScope{Region: "XX"}, false},
		{contract.StatsScope{Datacenter: "Chaos", World: "Ragnarok"}, false},
	}
	for _, tt := range tests {
		if got := KnownScope(tt.scope); got != tt.want {
			t.Errorf("KnownScope(%+v) = %v, want %v", tt.scope, got, tt.want)
		}
	}
}
