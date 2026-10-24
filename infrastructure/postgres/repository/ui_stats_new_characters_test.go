package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// seedChocobos stores characters who earned their first company chocobo
// (achievement 590) daysAgo complete UTC days before today, at noon.
func seedChocobos(t *testing.T, driver contract.DatabaseDriver, today time.Time, seeds []chocoboSeed) {
	t.Helper()
	ctx := context.Background()
	achievements := repository.NewAchievementRepository(driver)
	if err := achievements.SyncMilestones(ctx, []contract.MilestoneAchievement{
		{AchievementID: 590, Kind: "chocobo", Detail: "My Little Chocobo"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, s := range seeds {
		seedStatsCharacter(t, driver, s.id, s.world, s.dc, s.region, "Hyur", 1, today.AddDate(0, 0, -400), nil, 0)
		achievedAt := today.AddDate(0, 0, -s.daysAgo).Add(12 * time.Hour)
		if err := achievements.UpsertCharacterMilestones(ctx, uint32(s.id), []contract.CharacterMilestone{
			{CharacterID: uint32(s.id), AchievementID: 590, AchievedAt: achievedAt},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

type chocoboSeed struct {
	id                int
	world, dc, region string
	daysAgo           int
}

func windowOpts(mode contract.UIStatsRefreshMode, days int) contract.UIStatsRefreshOptions {
	opts := incrementalOpts(mode, time.Now().UTC().Add(-90*24*time.Hour))
	opts.NewCharactersWindowDays = days
	return opts
}

// With a 10-day window, the daily series covers the 20 complete UTC days
// before today at whole-game scope only, and every world carries its
// current and previous window totals.
func TestUIStatsNewCharactersSeriesSpansTwoWindowsAtGlobalScope(t *testing.T) {
	driver := newTestDriver(t)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seedChocobos(t, driver, today, []chocoboSeed{
		{1, "Balmung", "Crystal", "NA", 0},   // today: still in progress
		{2, "Balmung", "Crystal", "NA", 1},   // current window, last day
		{3, "Balmung", "Crystal", "NA", 10},  // current window, first day
		{4, "Balmung", "Crystal", "NA", 11},  // previous window, last day
		{5, "Ragnarok", "Chaos", "EU", 20},   // previous window, first day
		{6, "Ragnarok", "Chaos", "EU", 21},   // before both windows
		{7, "Ragnarok", "Chaos", "EU", 5},    // current window
		{8, "Coeurl", "Crystal", "NA", 30},   // before both windows
		{9, "Balmung", "Crystal", "NA", 15},  // previous window
		{10, "Balmung", "Crystal", "NA", 15}, // previous window, same day
	})

	result, err := repository.NewUIStatsRepository(driver).Refresh(context.Background(), windowOpts(contract.UIStatsRefreshFull, 10))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := result.Snapshot
	if snapshot.SchemaVersion != 4 || snapshot.NewCharactersWindowDays != 10 {
		t.Fatalf("schema %d window %d, want schema 4 with a 10-day window", snapshot.SchemaVersion, snapshot.NewCharactersWindowDays)
	}
	day := func(daysAgo int) string { return today.AddDate(0, 0, -daysAgo).Format("2006-01-02") }
	want := map[string]int64{day(1): 1, day(5): 1, day(10): 1, day(11): 1, day(15): 2, day(20): 1}
	got := map[string]int64{}
	for _, d := range snapshot.NewCharacters {
		if d.Scope != (contract.StatsScope{}) {
			t.Fatalf("daily row at scope %+v, want whole-game scope only", d.Scope)
		}
		got[d.Day] = d.Count
	}
	if len(got) != len(want) {
		t.Fatalf("daily series = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("daily series = %v, want %v", got, want)
		}
	}

	type totals struct{ current, previous int64 }
	worlds := map[string]totals{}
	for _, g := range snapshot.Groups {
		if g.Scope == (contract.StatsScope{}) && g.Dimension == "world" {
			worlds[g.Key] = totals{g.NewCharacters, g.NewCharactersPrevious}
		}
		if g.Dimension != "world" && (g.NewCharacters != 0 || g.NewCharactersPrevious != 0) {
			t.Fatalf("group %+v carries new-character totals, want world groups only", g)
		}
	}
	wantWorlds := map[string]totals{"Balmung": {2, 3}, "Ragnarok": {1, 1}, "Coeurl": {0, 0}}
	for world, w := range wantWorlds {
		if worlds[world] != w {
			t.Errorf("%s window totals = %+v, want %+v", world, worlds[world], w)
		}
	}
}

// An incremental refresh keeps the snapshot's window; a refresh asking for
// a different window, or on a snapshot from an older schema, rebuilds in full.
func TestUIStatsIncrementalRebuildsOnChangedWindowOrOlderSchema(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seedChocobos(t, driver, today, []chocoboSeed{{1, "Balmung", "Crystal", "NA", 3}})

	if _, err := stats.Refresh(ctx, windowOpts(contract.UIStatsRefreshFull, 10)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	same, err := stats.Refresh(ctx, windowOpts(contract.UIStatsRefreshIncremental, 10))
	if err != nil {
		t.Fatal(err)
	}
	if same.Mode != contract.UIStatsRefreshIncremental {
		t.Fatalf("same window: mode %q, want incremental", same.Mode)
	}

	time.Sleep(20 * time.Millisecond)
	changed, err := stats.Refresh(ctx, windowOpts(contract.UIStatsRefreshIncremental, 30))
	if err != nil {
		t.Fatal(err)
	}
	if changed.Mode != contract.UIStatsRefreshFull || changed.Snapshot.NewCharactersWindowDays != 30 {
		t.Fatalf("changed window: mode %q window %d, want a full rebuild with a 30-day window", changed.Mode, changed.Snapshot.NewCharactersWindowDays)
	}

	if _, err := driver.Execute(ctx, `UPDATE ui_stats_snapshots SET schema_version = 3 WHERE snapshot_key = 'current'`); err != nil {
		t.Fatal(err)
	}
	if _, err := stats.LoadCurrent(ctx); err != nil {
		t.Fatalf("loading a stored schema 3 snapshot: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	older, err := stats.Refresh(ctx, windowOpts(contract.UIStatsRefreshIncremental, 30))
	if err != nil {
		t.Fatal(err)
	}
	if older.Mode != contract.UIStatsRefreshFull {
		t.Fatalf("older schema: mode %q, want a full rebuild", older.Mode)
	}
}

// The new-characters window has one default, the census service's: a
// refresh without a window is refused rather than guessing one.
func TestUIStatsRefreshRequiresTheNewCharactersWindow(t *testing.T) {
	driver := newTestDriver(t)
	opts := windowOpts(contract.UIStatsRefreshFull, 0)
	if _, err := repository.NewUIStatsRepository(driver).Refresh(context.Background(), opts); err == nil {
		t.Fatal("Refresh without a new-characters window succeeded, want an error")
	}
}

// The Free Company boards' active counts keep the activity window of the
// full rebuild that made them: an incremental refresh asking for a
// different activity window rebuilds in full, boards included.
func TestUIStatsIncrementalRebuildsOnChangedActivityWindow(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	seedChocobos(t, driver, today, []chocoboSeed{{1, "Balmung", "Crystal", "NA", 3}})

	if _, err := stats.Refresh(ctx, windowOpts(contract.UIStatsRefreshFull, 10)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	same, err := stats.Refresh(ctx, windowOpts(contract.UIStatsRefreshIncremental, 10))
	if err != nil {
		t.Fatal(err)
	}
	if same.Mode != contract.UIStatsRefreshIncremental {
		t.Fatalf("same activity window: mode %q, want incremental", same.Mode)
	}

	time.Sleep(20 * time.Millisecond)
	opts := windowOpts(contract.UIStatsRefreshIncremental, 10)
	opts.ActivitySince = time.Now().UTC().Add(-30 * 24 * time.Hour)
	changed, err := stats.Refresh(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Mode != contract.UIStatsRefreshFull {
		t.Fatalf("changed activity window: mode %q, want a full rebuild", changed.Mode)
	}
	boards, err := stats.LoadFreeCompanies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := boards.BuiltAt.Sub(boards.ActivitySince).Round(24 * time.Hour); got != 30*24*time.Hour {
		t.Fatalf("boards activity window = %s, want 30 days", got)
	}
}
