package census

import (
	"reflect"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestLevelBandsRunFromOneExpansionCapToJustBelowTheNext(t *testing.T) {
	got := LevelBands(DefaultExpansions, 100)
	want := []contract.StatsLevelBand{
		{Name: "A Realm Reborn", Min: 1, Max: 49},
		{Name: "Heavensward", Min: 50, Max: 59},
		{Name: "Stormblood", Min: 60, Max: 69},
		{Name: "Shadowbringers", Min: 70, Max: 79},
		{Name: "Endwalker", Min: 80, Max: 89},
		{Name: "Dawntrail", Min: 90, Max: 99},
		{Name: LevelCapBand, Min: 100},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bands =\n%v\nwant\n%v", got, want)
	}
}

func TestLevelBandOfALevelHonoursTheBoundaries(t *testing.T) {
	bands := LevelBands(DefaultExpansions, 100)
	for _, tc := range []struct {
		level uint32
		want  string
	}{
		{0, ""},
		{1, "A Realm Reborn"},
		{49, "A Realm Reborn"},
		{50, "Heavensward"},
		{99, "Dawntrail"},
		{100, LevelCapBand},
	} {
		i := LevelBandIndex(bands, tc.level)
		got := ""
		if i >= 0 {
			got = bands[i].Name
		}
		if got != tc.want {
			t.Errorf("level %d is in band %q, want %q", tc.level, got, tc.want)
		}
	}
}

func TestLevelBandsExtendWithAFutureExpansion(t *testing.T) {
	expansions := append(append([]ExpansionConfig(nil), DefaultExpansions...), ExpansionConfig{Name: "Next", LevelCap: 110})
	bands := LevelBands(expansions, 110)
	if n := len(bands); n != 8 {
		t.Fatalf("got %d bands, want 8", n)
	}
	if got, want := bands[6], (contract.StatsLevelBand{Name: "Next", Min: 100, Max: 109}); got != want {
		t.Errorf("new expansion band = %v, want %v", got, want)
	}
	if got, want := bands[7], (contract.StatsLevelBand{Name: LevelCapBand, Min: 110}); got != want {
		t.Errorf("cap band = %v, want %v", got, want)
	}
}

func TestLevelBandsStopAtTheConfiguredMaximum(t *testing.T) {
	// A maximum below the last expansion's cap (its levels not released yet)
	// drops the unreachable bands.
	bands := LevelBands(DefaultExpansions, 90)
	if got := bands[len(bands)-2]; got != (contract.StatsLevelBand{Name: "Endwalker", Min: 80, Max: 89}) {
		t.Errorf("last expansion band = %v, want Endwalker 80-89", got)
	}
	if got := bands[len(bands)-1]; got != (contract.StatsLevelBand{Name: LevelCapBand, Min: 90}) {
		t.Errorf("cap band = %v, want level cap from 90", got)
	}
}
