package census

import (
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// rankingSnapshot is a hand-built v3 snapshot.
//
//	NA  Crystal  Balmung   400 total, 200 active, 100 at cap,  40 private
//	NA  Crystal  Coeurl    300 total,  60 active, 150 at cap,  90 private
//	NA  Aether   Gilgamesh 300 total,  90 active,  30 at cap,  30 private
//	EU  Chaos    Ragnarok  400 total, 100 active, 200 at cap,  20 private
//	EU  Light    Lich      200 total, 100 active,  40 at cap,  10 private
//	OCE Materia  Bismarck   20 total,  20 active,  20 at cap,  20 private (tiny)
//
// Median world population is 300, so the rate boards need 75 characters and
// Bismarck is left out of them.
func rankingSnapshot() *contract.UIStatsSnapshot {
	generated := time.Date(2026, 10, 24, 6, 0, 0, 0, time.UTC)
	loc := func(dim, key string, total, active, cap, private int64) contract.ScopedGroupCount {
		return contract.ScopedGroupCount{Dimension: dim, Key: key, Total: total, Active: active, MaxLevel: cap, Private: private}
	}
	gc := func(world, company string, total int64) contract.ScopedGroupCount {
		return contract.ScopedGroupCount{Scope: contract.StatsScope{World: world}, Dimension: "grand_company", Key: company, Total: total}
	}
	dcGC := func(dc, company string, total int64) contract.ScopedGroupCount {
		return contract.ScopedGroupCount{Scope: contract.StatsScope{Datacenter: dc}, Dimension: "grand_company", Key: company, Total: total}
	}
	day := func(world string, count int64) contract.ScopedDailyCount {
		return contract.ScopedDailyCount{Scope: contract.StatsScope{World: world}, Day: "2026-10-20", Count: count}
	}
	return &contract.UIStatsSnapshot{
		SchemaVersion:  3,
		GeneratedAt:    generated,
		ActivitySince:  generated.Add(-30 * 24 * time.Hour),
		MaxLevel:       100,
		PrivacyBuiltAt: generated.Add(-3 * time.Hour),
		Summary:        contract.StatsSummary{Total: 1620, Active: 570, MaxLevel: 540, Private: 210},
		Groups: []contract.ScopedGroupCount{
			loc("region", "NA", 1000, 350, 280, 160),
			loc("region", "EU", 600, 200, 240, 30),
			loc("region", "OCE", 20, 20, 20, 20),
			loc("datacenter", "Crystal", 700, 260, 250, 130),
			loc("datacenter", "Aether", 300, 90, 30, 30),
			loc("datacenter", "Chaos", 400, 100, 200, 20),
			loc("datacenter", "Light", 200, 100, 40, 10),
			loc("datacenter", "Materia", 20, 20, 20, 20),
			loc("world", "Balmung", 400, 200, 100, 40),
			loc("world", "Coeurl", 300, 60, 150, 90),
			loc("world", "Gilgamesh", 300, 90, 30, 30),
			loc("world", "Ragnarok", 400, 100, 200, 20),
			loc("world", "Lich", 200, 100, 40, 10),
			loc("world", "Bismarck", 20, 20, 20, 20),
			gc("Balmung", "Maelstrom", 100), gc("Balmung", "Order of the Twin Adder", 200), gc("Balmung", "None", 100),
			gc("Coeurl", "Maelstrom", 150), gc("Coeurl", "None", 150),
			gc("Gilgamesh", "Maelstrom", 150), gc("Gilgamesh", "Immortal Flames", 150),
			gc("Ragnarok", "Maelstrom", 100), gc("Ragnarok", "None", 300),
			gc("Lich", "Maelstrom", 50), gc("Lich", "Immortal Flames", 150),
			gc("Bismarck", "Maelstrom", 20),
			dcGC("Crystal", "Maelstrom", 250), dcGC("Crystal", "Order of the Twin Adder", 200), dcGC("Crystal", "None", 250),
			dcGC("Aether", "Maelstrom", 150), dcGC("Aether", "Immortal Flames", 150),
		},
		NewCharacters: []contract.ScopedDailyCount{
			day("Balmung", 8), day("Coeurl", 9), day("Gilgamesh", 3),
			day("Ragnarok", 4), day("Lich", 4), day("Bismarck", 10),
		},
	}
}

func boardKeys(b RankingBoard) []string {
	keys := make([]string, 0, len(b.Rows))
	for _, r := range b.Rows {
		keys = append(keys, r.Key)
	}
	return keys
}

func sameKeys(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestRankingsPopulationRanksWorldsByTotalWithTiesByName(t *testing.T) {
	board := Rankings(rankingSnapshot(), RankingQuery{Metric: MetricPopulation, By: LocationWorld})
	want := []string{"Balmung", "Ragnarok", "Coeurl", "Gilgamesh", "Lich", "Bismarck"}
	if got := boardKeys(board); !sameKeys(got, want) {
		t.Fatalf("population board = %v, want %v", got, want)
	}
	first, third := board.Rows[0], board.Rows[2]
	if first.Rank != 1 || first.Value != 400 || first.ShareOfLeader != 1 || first.Parent != "Crystal" {
		t.Errorf("leader = %+v", first)
	}
	if third.Rank != 3 || third.Value != 300 || third.ShareOfLeader != 0.75 || third.Parent != "Crystal" {
		t.Errorf("third = %+v", third)
	}
	if board.Excluded != 0 {
		t.Errorf("population is not a rate: excluded = %d, want 0", board.Excluded)
	}
}

func TestRankingsRateMetricsLeaveOutSmallWorlds(t *testing.T) {
	for _, tc := range []struct {
		metric RankingMetric
		want   []string
		leader RankingRow
	}{
		{MetricActivity, []string{"Balmung", "Lich", "Gilgamesh", "Ragnarok", "Coeurl"},
			RankingRow{Rank: 1, Key: "Balmung", Parent: "Crystal", Value: 0.5, ShareOfLeader: 1, Part: 200, Total: 400}},
		{MetricVeterans, []string{"Coeurl", "Ragnarok", "Balmung", "Lich", "Gilgamesh"},
			RankingRow{Rank: 1, Key: "Coeurl", Parent: "Crystal", Value: 0.5, ShareOfLeader: 1, Part: 150, Total: 300}},
		{MetricPrivacy, []string{"Coeurl", "Balmung", "Gilgamesh", "Lich", "Ragnarok"},
			RankingRow{Rank: 1, Key: "Coeurl", Parent: "Crystal", Value: 0.3, ShareOfLeader: 1, Part: 90, Total: 300}},
	} {
		t.Run(string(tc.metric), func(t *testing.T) {
			board := Rankings(rankingSnapshot(), RankingQuery{Metric: tc.metric, By: LocationWorld})
			if got := boardKeys(board); !sameKeys(got, tc.want) {
				t.Fatalf("board = %v, want %v", got, tc.want)
			}
			if board.Rows[0] != tc.leader {
				t.Errorf("leader = %+v, want %+v", board.Rows[0], tc.leader)
			}
			if board.Excluded != 1 || board.MinimumSize != 75 {
				t.Errorf("excluded %d below %d, want 1 below 75", board.Excluded, board.MinimumSize)
			}
			if !board.Ready {
				t.Error("a v3 snapshot board is ready")
			}
		})
	}
}

func TestRankingsGrowthPerThousandAndAsACount(t *testing.T) {
	perThousand := Rankings(rankingSnapshot(), RankingQuery{Metric: MetricGrowth, By: LocationWorld})
	if got, want := boardKeys(perThousand), []string{"Coeurl", "Balmung", "Lich", "Gilgamesh", "Ragnarok"}; !sameKeys(got, want) {
		t.Fatalf("growth board = %v, want %v", got, want)
	}
	if lead := perThousand.Rows[0]; lead.Value != 30 || lead.Part != 9 || lead.Total != 300 {
		t.Errorf("growth leader = %+v, want 30 per 1,000 (9 of 300)", lead)
	}
	if perThousand.Excluded != 1 {
		t.Errorf("growth per 1,000 is a rate: excluded = %d, want 1", perThousand.Excluded)
	}

	count := Rankings(rankingSnapshot(), RankingQuery{Metric: MetricGrowthCount, By: LocationWorld})
	if got, want := boardKeys(count), []string{"Bismarck", "Coeurl", "Balmung", "Lich", "Ragnarok", "Gilgamesh"}; !sameKeys(got, want) {
		t.Fatalf("new characters board = %v, want %v", got, want)
	}
	if lead := count.Rows[0]; lead.Value != 10 || lead.Part != 10 || lead.Total != 20 {
		t.Errorf("new characters leader = %+v", lead)
	}
	if count.Excluded != 0 {
		t.Errorf("a raw count leaves nothing out: excluded = %d", count.Excluded)
	}
}

func TestRankingsGrandCompanyLeanOnePerCompany(t *testing.T) {
	for _, tc := range []struct {
		metric RankingMetric
		want   []string
		lead   float64
	}{
		{MetricLeanMaelstrom, []string{"Coeurl", "Gilgamesh", "Balmung", "Lich", "Ragnarok"}, 0.5},
		{MetricLeanTwinAdder, []string{"Balmung", "Coeurl", "Gilgamesh", "Lich", "Ragnarok"}, 0.5},
		{MetricLeanImmortalFlames, []string{"Lich", "Gilgamesh", "Balmung", "Coeurl", "Ragnarok"}, 0.75},
	} {
		t.Run(string(tc.metric), func(t *testing.T) {
			board := Rankings(rankingSnapshot(), RankingQuery{Metric: tc.metric, By: LocationWorld})
			if got := boardKeys(board); !sameKeys(got, tc.want) {
				t.Fatalf("board = %v, want %v", got, tc.want)
			}
			if board.Rows[0].Value != tc.lead || board.Excluded != 1 {
				t.Errorf("leader %+v, excluded %d; want %v and 1", board.Rows[0], board.Excluded, tc.lead)
			}
		})
	}
	lich := Rankings(rankingSnapshot(), RankingQuery{Metric: MetricLeanImmortalFlames, By: LocationWorld}).Rows[0]
	if lich.Part != 150 || lich.Total != 200 {
		t.Errorf("Lich = %d of %d, want 150 of 200", lich.Part, lich.Total)
	}
}

func TestRankingsDatacenterScopeAndRegionFilter(t *testing.T) {
	for _, tc := range []struct {
		name     string
		query    RankingQuery
		want     []string
		parent   string
		excluded int
	}{
		{"datacenters by activity", RankingQuery{Metric: MetricActivity, By: LocationDatacenter},
			[]string{"Light", "Crystal", "Aether", "Chaos"}, "EU", 1},
		{"datacenters of one region", RankingQuery{Metric: MetricPopulation, By: LocationDatacenter, Region: "na"},
			[]string{"Crystal", "Aether"}, "NA", 0},
		{"worlds of one region", RankingQuery{Metric: MetricPopulation, By: LocationWorld, Region: "NA"},
			[]string{"Balmung", "Coeurl", "Gilgamesh"}, "Crystal", 0},
		// The minimum size follows the filtered median: alone in Oceania,
		// Bismarck is no longer small.
		{"minimum size inside the region", RankingQuery{Metric: MetricActivity, By: LocationWorld, Region: "OCE"},
			[]string{"Bismarck"}, "Materia", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			board := Rankings(rankingSnapshot(), tc.query)
			if got := boardKeys(board); !sameKeys(got, tc.want) {
				t.Fatalf("board = %v, want %v", got, tc.want)
			}
			if board.Rows[0].Parent != tc.parent || board.Excluded != tc.excluded {
				t.Errorf("leader parent %q, excluded %d; want %q and %d", board.Rows[0].Parent, board.Excluded, tc.parent, tc.excluded)
			}
		})
	}
}

func TestRankingsWaitForTheFiguresTheyNeed(t *testing.T) {
	v2 := rankingSnapshot()
	v2.SchemaVersion = 2
	v2.PrivacyBuiltAt = time.Time{}
	for metric, ready := range map[RankingMetric]bool{
		MetricGrowth: true, MetricGrowthCount: true, MetricActivity: true, MetricPopulation: true,
		MetricVeterans: false, MetricPrivacy: false,
		MetricLeanMaelstrom: false, MetricLeanTwinAdder: false, MetricLeanImmortalFlames: false,
		"unknown": false,
	} {
		board := Rankings(v2, RankingQuery{Metric: metric, By: LocationWorld})
		if board.Ready != ready || (len(board.Rows) > 0) != ready {
			t.Errorf("%s on a v2 snapshot: ready %v with %d rows, want ready %v", metric, board.Ready, len(board.Rows), ready)
		}
	}
}

func TestRankingsMemoBuildsEachBoardOncePerSnapshot(t *testing.T) {
	memo := NewRankingsMemo()
	snapshot := rankingSnapshot()
	query := RankingQuery{Metric: MetricPopulation, By: LocationWorld}
	first := memo.Board(snapshot, query)
	first.Rows[0].Key = "Scribbled"

	// The same snapshot (by generation time) serves the memoised board, even
	// if a caller changed its copy.
	snapshot.Groups[8].Total = 1 // Balmung shrinks, unseen until a new snapshot
	if got := memo.Board(snapshot, query).Rows[0].Key; got != "Balmung" {
		t.Fatalf("memoised leader = %q, want Balmung", got)
	}

	snapshot.GeneratedAt = snapshot.GeneratedAt.Add(time.Hour)
	if got := memo.Board(snapshot, query).Rows[0].Key; got != "Ragnarok" {
		t.Fatalf("leader after a new snapshot = %q, want Ragnarok", got)
	}
}

func TestLeanCompanyNamesEachGrandCompanyMetricsCompany(t *testing.T) {
	for metric, want := range map[RankingMetric]string{
		MetricLeanMaelstrom:      "Maelstrom",
		MetricLeanTwinAdder:      "Order of the Twin Adder",
		MetricLeanImmortalFlames: "Immortal Flames",
	} {
		gc, ok := LeanCompany(metric)
		if !ok || gc.Name != want {
			t.Errorf("LeanCompany(%s) = %+v, %v; want %s", metric, gc, ok, want)
		}
	}
	if _, ok := LeanCompany(MetricPopulation); ok {
		t.Error("LeanCompany(population): want no company")
	}
}
