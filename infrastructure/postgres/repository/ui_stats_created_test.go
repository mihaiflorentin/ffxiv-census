package repository_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// createdOpts is a 10-day window with the exact cut at ID 400,000 and one
// company chocobo enough to anchor a band of IDs.
func createdOpts(mode contract.UIStatsRefreshMode) contract.UIStatsRefreshOptions {
	opts := windowOpts(mode, 10)
	opts.CreatedExactFromID = 400_000
	opts.CreatedMinSamples = 1
	return opts
}

// seedCreated stores the characters of the created-characters tests, today
// being the current UTC day. The company chocobos of 50,000, 150,000 and
// 250,000 anchor the curve at 25, 15 and 5 days ago (10,000 IDs a day), and
// the exact cut's first sighting 3 days ago ends it (75,000 IDs a day after
// 250,000). The window starts 10 days ago, the previous one 20 days ago.
func seedCreated(t *testing.T, driver contract.DatabaseDriver, today time.Time) {
	t.Helper()
	ctx := context.Background()
	day := func(daysAgo int) time.Time { return today.AddDate(0, 0, -daysAgo) }
	achievements := repository.NewAchievementRepository(driver)
	if err := achievements.SyncMilestones(ctx, []contract.MilestoneAchievement{
		{AchievementID: 590, Kind: "chocobo", Detail: "My Little Chocobo"},
	}); err != nil {
		t.Fatal(err)
	}
	long := day(400)
	for _, c := range []struct {
		id    int
		world string
		seen  time.Time
	}{
		{50_000, "Balmung", long},   // before the series
		{95_000, "Balmung", long},   // before the series
		{105_000, "Ragnarok", long}, // 20 days ago: previous window, first day
		{150_000, "Balmung", long},  // 15 days ago
		{199_999, "Balmung", long},  // 11 days ago: previous window, last day
		{205_000, "Balmung", long},  // 10 days ago: current window, first day
		{250_000, "Balmung", long},  // 5 days ago
		{330_000, "Balmung", long},  // 4 days ago
		{340_000, "Balmung", long},  // 4 days ago, but deleted
		{400_000, "Balmung", day(3).Add(9 * time.Hour)},
		{400_001, "Balmung", day(1).Add(23 * time.Hour)},
		{400_002, "Balmung", today.Add(time.Hour)}, // today: still in progress
		{400_003, "Balmung", day(1)},               // deleted
	} {
		seedStatsCharacter(t, driver, c.id, c.world, "Crystal", "NA", "Hyur", 1, c.seen, nil, 0)
	}
	mustExec(t, driver, `UPDATE characters SET deleted_at = NOW() WHERE id IN (340000, 400003)`)
	for id, daysAgo := range map[int]int{50_000: 25, 150_000: 15, 250_000: 5} {
		if err := achievements.UpsertCharacterMilestones(ctx, uint32(id), []contract.CharacterMilestone{
			{CharacterID: uint32(id), AchievementID: 590, AchievedAt: day(daysAgo)},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// Every live character counts as created, public or private: from the exact
// cut by the day of its first sighting, below it by the calibration curve.
// The daily series covers both windows at whole-game scope; each world
// carries its window totals and its daily series over the current window.
func TestUIStatsCreatedCharactersByID(t *testing.T) {
	driver := newTestDriver(t)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seedCreated(t, driver, today)

	result, err := repository.NewUIStatsRepository(driver).Refresh(context.Background(), createdOpts(contract.UIStatsRefreshFull))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	day := func(daysAgo int) string { return today.AddDate(0, 0, -daysAgo).Format("2006-01-02") }
	if snapshot.SchemaVersion != 6 || snapshot.CreatedExactFrom != day(3) || snapshot.CreatedExactFromID != 400_000 || snapshot.CreatedBuiltAt.IsZero() {
		t.Fatalf("schema %d exact from %q (ID %d) built %v, want schema 6 exact from %s (ID 400000) with a build time",
			snapshot.SchemaVersion, snapshot.CreatedExactFrom, snapshot.CreatedExactFromID, snapshot.CreatedBuiltAt, day(3))
	}
	got := map[string]int64{}
	for _, d := range snapshot.Created {
		if d.Scope != (contract.StatsScope{}) {
			t.Fatalf("created row at scope %+v, want whole-game scope only", d.Scope)
		}
		got[d.Day] = d.Count
	}
	want := map[string]int64{day(20): 1, day(15): 1, day(11): 1, day(10): 1, day(5): 1, day(4): 1, day(3): 1, day(1): 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("created series = %v, want %v", got, want)
	}

	type figures struct {
		current, previous int64
		daily             []int64
	}
	worlds := map[string]figures{}
	for _, g := range snapshot.Groups {
		if g.Scope == (contract.StatsScope{}) && g.Dimension == "world" {
			worlds[g.Key] = figures{g.Created, g.CreatedPrevious, g.CreatedDaily}
			continue
		}
		if g.Created != 0 || g.CreatedPrevious != 0 || len(g.CreatedDaily) > 0 {
			t.Fatalf("group %+v carries created figures, want world groups only", g)
		}
	}
	wantWorlds := map[string]figures{
		"Balmung":  {current: 5, previous: 2, daily: []int64{1, 0, 0, 0, 0, 1, 1, 1, 0, 1}},
		"Ragnarok": {current: 0, previous: 1},
	}
	if !reflect.DeepEqual(worlds, wantWorlds) {
		t.Fatalf("world created figures = %+v, want %+v", worlds, wantWorlds)
	}
}

// Counting the created characters reads the whole ID range, so the
// incremental refresh carries the figures of a refresh from the same UTC
// day forward and counts them again on a new day.
func TestUIStatsIncrementalCarriesCreatedCharactersWithinTheDay(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	stats := repository.NewUIStatsRepository(driver)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seedCreated(t, driver, today)

	full, err := stats.Refresh(ctx, createdOpts(contract.UIStatsRefreshFull))
	if err != nil {
		t.Fatal(err)
	}
	// A character first seen yesterday but stored now: the incremental delta
	// does not see it, only a new count does.
	seedStatsCharacter(t, driver, 400_004, "Balmung", "Crystal", "NA", "Hyur", 1, today.AddDate(0, 0, -1).Add(time.Hour), nil, 0)

	time.Sleep(20 * time.Millisecond)
	carried, err := stats.Refresh(ctx, createdOpts(contract.UIStatsRefreshIncremental))
	if err != nil {
		t.Fatal(err)
	}
	if carried.Mode != contract.UIStatsRefreshIncremental {
		t.Fatalf("mode %q, want incremental", carried.Mode)
	}
	if !carried.Snapshot.CreatedBuiltAt.Equal(full.Snapshot.CreatedBuiltAt) || !reflect.DeepEqual(carried.Snapshot.Created, full.Snapshot.Created) {
		t.Fatalf("same day: built %v series %v, want the full refresh's %v %v",
			carried.Snapshot.CreatedBuiltAt, carried.Snapshot.Created, full.Snapshot.CreatedBuiltAt, full.Snapshot.Created)
	}
	if got := balmungCreated(carried.Snapshot); got != 5 {
		t.Fatalf("same day: Balmung created %d, want the carried 5", got)
	}

	mustExec(t, driver, `UPDATE ui_stats_snapshots
		SET payload = jsonb_set(payload::jsonb, '{created_built_at}', to_jsonb((NOW() - INTERVAL '1 day')::timestamptz))
		WHERE snapshot_key = 'current'`)
	time.Sleep(20 * time.Millisecond)
	recounted, err := stats.Refresh(ctx, createdOpts(contract.UIStatsRefreshIncremental))
	if err != nil {
		t.Fatal(err)
	}
	if recounted.Mode != contract.UIStatsRefreshIncremental {
		t.Fatalf("mode %q, want incremental", recounted.Mode)
	}
	if got := balmungCreated(recounted.Snapshot); got != 6 {
		t.Fatalf("new day: Balmung created %d, want 6 once counted again", got)
	}

	// A changed cut counts again at once.
	opts := createdOpts(contract.UIStatsRefreshIncremental)
	opts.CreatedExactFromID = 400_001
	time.Sleep(20 * time.Millisecond)
	moved, err := stats.Refresh(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Snapshot.CreatedExactFromID != 400_001 || moved.Snapshot.CreatedExactFrom != today.AddDate(0, 0, -1).Format("2006-01-02") {
		t.Fatalf("moved cut: exact from %q (ID %d), want yesterday from ID 400001", moved.Snapshot.CreatedExactFrom, moved.Snapshot.CreatedExactFromID)
	}
}

// Without an exact cut there are no created figures.
func TestUIStatsWithoutAnExactCutHasNoCreatedCharacters(t *testing.T) {
	driver := newTestDriver(t)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seedCreated(t, driver, today)
	opts := createdOpts(contract.UIStatsRefreshFull)
	opts.CreatedExactFromID = 0
	result, err := repository.NewUIStatsRepository(driver).Refresh(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if s := result.Snapshot; len(s.Created) > 0 || s.CreatedExactFrom != "" || !s.CreatedBuiltAt.IsZero() || balmungCreated(s) != 0 {
		t.Fatalf("snapshot carries created figures without a cut: %v %q %v", s.Created, s.CreatedExactFrom, s.CreatedBuiltAt)
	}
}

func balmungCreated(snapshot *contract.UIStatsSnapshot) int64 {
	for _, g := range snapshot.Groups {
		if g.Scope == (contract.StatsScope{}) && g.Dimension == "world" && g.Key == "Balmung" {
			return g.Created
		}
	}
	return 0
}
