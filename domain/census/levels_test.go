package census

import (
	"reflect"
	"testing"
	"time"

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

// levelsSnapshot uses two bands (1-49, 50+). Balmung and Coeurl are on
// Crystal (NA), Ragnarok on Chaos (EU).
func levelsSnapshot() *contract.UIStatsSnapshot {
	generated := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)
	loc := func(dim, key string, total, noLevels int64, levels ...int64) contract.ScopedGroupCount {
		return contract.ScopedGroupCount{Dimension: dim, Key: key, Total: total, NoLevels: noLevels, Levels: levels}
	}
	return &contract.UIStatsSnapshot{
		SchemaVersion: contract.UIStatsSchemaVersion,
		GeneratedAt:   generated.Add(time.Hour),
		ActivitySince: generated.Add(-30 * 24 * time.Hour),
		MaxLevel:      50,
		LevelBands:    []contract.StatsLevelBand{{Name: "A Realm Reborn", Min: 1, Max: 49}, {Name: LevelCapBand, Min: 50}},
		LevelsBuiltAt: generated,
		Summary:       contract.StatsSummary{Total: 100, NoLevels: 20, Levels: []int64{60, 20}},
		Groups: []contract.ScopedGroupCount{
			loc("region", "NA", 60, 10, 40, 10),
			loc("region", "EU", 40, 10, 20, 10),
			loc("datacenter", "Crystal", 60, 10, 40, 10),
			loc("datacenter", "Chaos", 40, 10, 20, 10),
			loc("world", "Balmung", 40, 0, 30, 10),
			loc("world", "Coeurl", 20, 10, 10, 0),
			loc("world", "Ragnarok", 40, 10, 20, 10),
			{Dimension: "race", Key: "Hyur", Total: 100},
		},
	}
}

func bandCounts(d LevelDistribution) []int64 {
	var out []int64
	for _, b := range d.Bands {
		out = append(out, b.Count)
	}
	return out
}

func TestLevelsForAScopeSharesTheCharactersWithJobLevels(t *testing.T) {
	snapshot := levelsSnapshot()
	if !LevelsReady(snapshot) {
		t.Fatal("snapshot with level figures is not ready")
	}
	whole := LevelsFor(snapshot, contract.StatsScope{})
	if whole.Total != 100 || whole.NoLevels != 20 || whole.Levelled() != 80 {
		t.Fatalf("whole game total=%d no levels=%d levelled=%d, want 100, 20, 80", whole.Total, whole.NoLevels, whole.Levelled())
	}
	if got := whole.Bands[0]; got.Name != "A Realm Reborn" || got.Count != 60 || got.Share != 0.75 {
		t.Errorf("first band = %+v, want A Realm Reborn 60 at 75%%", got)
	}
	if got := whole.Bands[1]; got.Name != LevelCapBand || got.Count != 20 || got.Share != 0.25 {
		t.Errorf("cap band = %+v, want 20 at 25%%", got)
	}
	coeurl := LevelsFor(snapshot, contract.StatsScope{World: "coeurl"})
	if coeurl.Key != "Coeurl" || !reflect.DeepEqual(bandCounts(coeurl), []int64{10, 0}) || coeurl.Bands[0].Share != 1 {
		t.Errorf("Coeurl = %+v", coeurl)
	}
	nowhere := LevelsFor(snapshot, contract.StatsScope{World: "Nowhere"})
	if nowhere.Total != 0 || len(nowhere.Bands) != 2 || nowhere.Bands[0].Share != 0 {
		t.Errorf("unknown world = %+v, want empty bands", nowhere)
	}
}

func TestLevelsAreNotReadyWithoutTheirBuildTime(t *testing.T) {
	snapshot := levelsSnapshot()
	snapshot.LevelsBuiltAt = time.Time{}
	if LevelsReady(snapshot) {
		t.Fatal("a snapshot without level figures reports ready")
	}
	if LevelsReady(nil) {
		t.Fatal("nil snapshot reports ready")
	}
}

func TestLevelsByDatacenterRollUpTheWorlds(t *testing.T) {
	snapshot := levelsSnapshot()
	all := LevelsByDatacenter(snapshot, "")
	if len(all) != 2 || all[0].Key != "Crystal" || all[0].Region != "NA" || all[1].Key != "Chaos" {
		t.Fatalf("datacenters = %+v, want Crystal (NA) then Chaos (EU)", all)
	}
	if got := bandCounts(all[0]); !reflect.DeepEqual(got, []int64{40, 10}) || all[0].Total != 60 || all[0].NoLevels != 10 {
		t.Errorf("Crystal = %+v", all[0])
	}
	eu := LevelsByDatacenter(snapshot, "eu")
	if len(eu) != 1 || eu[0].Key != "Chaos" {
		t.Errorf("EU datacenters = %+v", eu)
	}
}

func TestLevelsByWorldListsTheWorldsOfAScope(t *testing.T) {
	snapshot := levelsSnapshot()
	crystal := LevelsByWorld(snapshot, contract.StatsScope{Datacenter: "Crystal"})
	if len(crystal) != 2 || crystal[0].Key != "Balmung" || crystal[1].Key != "Coeurl" || crystal[0].Datacenter != "Crystal" {
		t.Fatalf("Crystal worlds = %+v", crystal)
	}
	if all := LevelsByWorld(snapshot, contract.StatsScope{}); len(all) != 3 || all[0].Key != "Balmung" || all[2].Key != "Ragnarok" {
		t.Errorf("every world = %+v, want region then datacenter then name order", all)
	}
}

func TestSnapshotMemoKeepsTheLevelFigures(t *testing.T) {
	memo := NewSnapshotMemo()
	snapshot := levelsSnapshot()
	first := memo.Levels(snapshot, contract.StatsScope{Region: "NA"})
	first.Bands[0].Count = -1
	if again := memo.Levels(snapshot, contract.StatsScope{Region: "NA"}); again.Bands[0].Count != 40 {
		t.Fatalf("memo handed out a shared value: %+v", again)
	}
	dcs := memo.LevelsByDatacenter(snapshot, "")
	dcs[0].Bands[0].Count = -1
	if again := memo.LevelsByDatacenter(snapshot, ""); again[0].Bands[0].Count != 40 {
		t.Fatalf("memo handed out shared datacenter rows: %+v", again)
	}
	if worlds := memo.LevelsByWorld(snapshot, contract.StatsScope{Region: "NA"}); len(worlds) != 2 {
		t.Fatalf("NA worlds = %+v", worlds)
	}
	if n := memo.size(); n != 3 {
		t.Fatalf("memo holds %d values, want 3", n)
	}
	memo.Levels(snapshot, contract.StatsScope{World: "Nowhere"})
	if n := memo.size(); n != 3 {
		t.Fatalf("an unknown scope was remembered: %d values", n)
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
