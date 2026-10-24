package census

import (
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// privacySnapshot: 1,000 characters globally; NA holds Balmung (Crystal) and
// Coeurl (Crystal), EU holds Ragnarok and Lich (Chaos/Light), and Bismarck
// (Materia, OCE) is a tiny world.
func privacySnapshot() *contract.UIStatsSnapshot {
	generated := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)
	loc := func(dim, key string, total, active, cap, private, unchecked int64) contract.ScopedGroupCount {
		return contract.ScopedGroupCount{Dimension: dim, Key: key, Total: total, Active: active, MaxLevel: cap, Private: private, Unchecked: unchecked}
	}
	return &contract.UIStatsSnapshot{
		SchemaVersion:  3,
		GeneratedAt:    generated.Add(2 * time.Hour),
		ActivitySince:  generated.Add(-30 * 24 * time.Hour),
		MaxLevel:       100,
		PrivacyBuiltAt: generated,
		Summary:        contract.StatsSummary{Total: 1000, Active: 400, MaxLevel: 300, Private: 150, Unchecked: 50},
		Groups: []contract.ScopedGroupCount{
			loc("region", "NA", 500, 200, 150, 100, 25),
			loc("region", "EU", 490, 196, 148, 45, 25),
			loc("region", "OCE", 10, 4, 2, 5, 0),
			loc("datacenter", "Crystal", 500, 200, 150, 100, 25),
			loc("world", "Balmung", 300, 120, 100, 90, 15),
			loc("world", "Coeurl", 200, 80, 50, 10, 10),
			loc("world", "Ragnarok", 250, 100, 80, 25, 5),
			loc("world", "Lich", 240, 96, 68, 20, 20),
			loc("world", "Bismarck", 10, 4, 2, 5, 0),
			{Dimension: "race", Key: "Hyur", Total: 1000, Active: 400},
		},
	}
}

func TestSnapshotLocationReadsTheCountsOfAScope(t *testing.T) {
	s := privacySnapshot()
	for _, tc := range []struct {
		scope contract.StatsScope
		want  LocationCounts
	}{
		{contract.StatsScope{}, LocationCounts{Total: 1000, Active: 400, AtCap: 300, Private: 150, Unchecked: 50}},
		{contract.StatsScope{Region: "na"}, LocationCounts{Key: "NA", Total: 500, Active: 200, AtCap: 150, Private: 100, Unchecked: 25}},
		{contract.StatsScope{Datacenter: "crystal"}, LocationCounts{Key: "Crystal", Total: 500, Active: 200, AtCap: 150, Private: 100, Unchecked: 25}},
		{contract.StatsScope{World: "Balmung"}, LocationCounts{Key: "Balmung", Total: 300, Active: 120, AtCap: 100, Private: 90, Unchecked: 15}},
		{contract.StatsScope{World: "Nowhere"}, LocationCounts{}},
	} {
		if got := SnapshotLocation(s, tc.scope); got != tc.want {
			t.Errorf("SnapshotLocation(%+v) = %+v, want %+v", tc.scope, got, tc.want)
		}
	}
	if got := SnapshotLocation(s, contract.StatsScope{World: "Balmung"}).Public(); got != 195 {
		t.Errorf("Balmung public = %d, want 195", got)
	}
}

func TestSnapshotLocationsListsOneLevelByName(t *testing.T) {
	got := SnapshotLocations(privacySnapshot(), LocationWorld)
	var keys []string
	for _, l := range got {
		keys = append(keys, l.Key)
	}
	want := []string{"Balmung", "Bismarck", "Coeurl", "Lich", "Ragnarok"}
	if len(keys) != len(want) {
		t.Fatalf("worlds = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("worlds = %v, want %v", keys, want)
		}
	}
	if got[0].AtCap != 100 || got[0].Private != 90 {
		t.Errorf("Balmung = %+v", got[0])
	}
}

func TestPrivacyForSplitsAScopeIntoPublicPrivateAndUnchecked(t *testing.T) {
	p := PrivacyFor(privacySnapshot(), contract.StatsScope{})
	if p.Public() != 800 || p.Private != 150 || p.Unchecked != 50 {
		t.Fatalf("global privacy = %+v", p)
	}
	if !near(p.PublicShare, 0.8) || !near(p.PrivateShare, 0.15) || !near(p.UncheckedShare, 0.05) {
		t.Errorf("global shares public=%v private=%v unchecked=%v, want 0.8/0.15/0.05", p.PublicShare, p.PrivateShare, p.UncheckedShare)
	}
	if empty := PrivacyFor(privacySnapshot(), contract.StatsScope{World: "Nowhere"}); empty.PrivateShare != 0 || empty.PublicShare != 0 {
		t.Errorf("an empty scope has zero shares, got %+v", empty)
	}
}

func TestPrivacyWorldsRanksByPrivateShareAndLeavesOutSmallWorlds(t *testing.T) {
	board := PrivacyWorlds(privacySnapshot(), "", 2)
	// Populations 300, 200, 250, 240, 10: median 240, minimum 60. Bismarck
	// (10 characters, 50% private) is too small to rank.
	if board.Excluded != 1 || board.MinimumSize != 60 {
		t.Fatalf("excluded=%d minimum=%d, want 1 world under 60", board.Excluded, board.MinimumSize)
	}
	// Private shares: Balmung 30%, Ragnarok 10%, Lich 8.3%, Coeurl 5%.
	if got := worldKeys(board.MostPrivate); got != "Balmung,Ragnarok" {
		t.Errorf("most private = %s, want Balmung,Ragnarok", got)
	}
	if got := worldKeys(board.LeastPrivate); got != "Coeurl,Lich" {
		t.Errorf("least private = %s, want Coeurl,Lich", got)
	}
	if !near(board.MostPrivate[0].PrivateShare, 0.3) {
		t.Errorf("Balmung private share = %v, want 0.3", board.MostPrivate[0].PrivateShare)
	}
}

func TestPrivacyWorldsNarrowsToARegionAndNeverListsAWorldTwice(t *testing.T) {
	board := PrivacyWorlds(privacySnapshot(), "na", 5)
	if got := worldKeys(board.MostPrivate) + "|" + worldKeys(board.LeastPrivate); got != "Balmung|Coeurl" {
		t.Errorf("NA boards = %s, want Balmung|Coeurl", got)
	}
	if board.Excluded != 0 {
		t.Errorf("excluded = %d, want 0", board.Excluded)
	}
}

func TestPrivacyWorldsBreaksTiesByName(t *testing.T) {
	s := privacySnapshot()
	s.Groups = []contract.ScopedGroupCount{
		{Dimension: "world", Key: "Ragnarok", Total: 100, Private: 10},
		{Dimension: "world", Key: "Lich", Total: 100, Private: 10},
		{Dimension: "world", Key: "Odin", Total: 100, Private: 10},
	}
	s.Groups = append(s.Groups,
		contract.ScopedGroupCount{Dimension: "world", Key: "Alpha", Total: 100, Private: 5},
		contract.ScopedGroupCount{Dimension: "world", Key: "Zalera", Total: 100, Private: 5},
		contract.ScopedGroupCount{Dimension: "world", Key: "Cerberus", Total: 100, Private: 20},
	)
	if got := worldKeys(PrivacyWorlds(s, "", 3).LeastPrivate); got != "Alpha,Zalera,Ragnarok" {
		t.Errorf("least private with ties = %s, want Alpha,Zalera,Ragnarok", got)
	}
	s.Groups = s.Groups[:3]
	board := PrivacyWorlds(s, "", 1)
	if got := worldKeys(board.MostPrivate) + "|" + worldKeys(board.LeastPrivate); got != "Lich|Odin" {
		t.Errorf("tied worlds most|least = %s, want Lich|Odin (name order on both boards)", got)
	}
}

func TestMinimumRankedSizeIsAQuarterOfTheMedian(t *testing.T) {
	for _, tc := range []struct {
		populations []int64
		want        int64
	}{
		{nil, 0},
		{[]int64{400}, 100},
		{[]int64{10, 400, 1000}, 100},
		{[]int64{100, 300, 500, 900}, 100}, // even count: median 400
	} {
		if got := MinimumRankedSize(tc.populations); got != tc.want {
			t.Errorf("MinimumRankedSize(%v) = %d, want %d", tc.populations, got, tc.want)
		}
	}
}

func TestPrivacyReportNeedsTheNightlyPrivacyFigures(t *testing.T) {
	s := privacySnapshot()
	if !PrivacyReady(s) {
		t.Error("a v3 snapshot with a privacy build time is ready")
	}
	s.PrivacyBuiltAt = time.Time{}
	if PrivacyReady(s) {
		t.Error("a snapshot without a privacy build time is not ready")
	}
	v2 := privacySnapshot()
	v2.SchemaVersion = 2
	if PrivacyReady(v2) || PrivacyReady(nil) {
		t.Error("a v2 or missing snapshot is not ready")
	}
}

func worldKeys(rows []PrivacyShare) string {
	out := ""
	for i, r := range rows {
		if i > 0 {
			out += ","
		}
		out += r.Key
	}
	return out
}
