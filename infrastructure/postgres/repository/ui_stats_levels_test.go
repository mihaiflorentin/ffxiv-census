package repository_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// levelCounts is {no levels, ARR, HW, SB, ShB, EW, DT, level cap} of one
// location group.
type levelCounts [8]int64

func levelsOf(noLevels int64, bands []int64) levelCounts {
	var out levelCounts
	out[0] = noLevels
	copy(out[1:], bands)
	return out
}

func locationLevels(t *testing.T, snapshot *contract.UIStatsSnapshot, dimension, key string) levelCounts {
	t.Helper()
	for _, g := range snapshot.Groups {
		if g.Scope == (contract.StatsScope{}) && g.Dimension == dimension && g.Key == key {
			return levelsOf(g.NoLevels, g.Levels)
		}
	}
	t.Fatalf("no %s group %q", dimension, key)
	return levelCounts{}
}

func levelOpts(mode contract.UIStatsRefreshMode, since time.Time) contract.UIStatsRefreshOptions {
	opts := incrementalOpts(mode, since)
	opts.LevelBands = census.LevelBands(census.DefaultExpansions, 100)
	return opts
}

func TestUIStatsFullRefreshCountsLevelBandsPerLocation(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-90 * 24 * time.Hour)

	for i, level := range []int{0, 1, 49, 50, 99, 100} {
		seedStatsCharacter(t, driver, i+1, "Balmung", "Crystal", "NA", "Hyur", 1, old, nil, level)
	}
	seedStatsCharacter(t, driver, 7, "Ragnarok", "Chaos", "EU", "Elezen", 2, old, nil, 100)
	seedStatsCharacter(t, driver, 8, "Ragnarok", "Chaos", "EU", "Elezen", 2, old, nil, 90)
	seedStatsCharacter(t, driver, 9, "Ragnarok", "Chaos", "EU", "Elezen", 2, old, nil, 80)
	mustExec(t, driver, `UPDATE characters SET deleted_at = NOW() WHERE id = 9`)

	opts := levelOpts(contract.UIStatsRefreshFull, now.Add(-30*24*time.Hour))
	if _, err := stats.Refresh(ctx, opts); err != nil {
		t.Fatal(err)
	}
	loaded, err := stats.LoadCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(loaded.LevelBands, opts.LevelBands) {
		t.Errorf("snapshot bands = %v, want %v", loaded.LevelBands, opts.LevelBands)
	}
	if !loaded.LevelsBuiltAt.Equal(loaded.GeneratedAt) {
		t.Errorf("levels built at %v, want the full rebuild's %v", loaded.LevelsBuiltAt, loaded.GeneratedAt)
	}
	if got, want := levelsOf(loaded.Summary.NoLevels, loaded.Summary.Levels), (levelCounts{1, 2, 1, 0, 0, 0, 2, 2}); got != want {
		t.Errorf("summary levels = %v, want %v", got, want)
	}
	balmung := levelCounts{1, 2, 1, 0, 0, 0, 1, 1}
	ragnarok := levelCounts{0, 0, 0, 0, 0, 0, 1, 1}
	for _, tc := range []struct {
		dimension, key string
		want           levelCounts
	}{
		{"world", "Balmung", balmung},
		{"datacenter", "Crystal", balmung},
		{"region", "NA", balmung},
		{"world", "Ragnarok", ragnarok},
		{"datacenter", "Chaos", ragnarok},
		{"region", "EU", ragnarok},
	} {
		if got := locationLevels(t, loaded, tc.dimension, tc.key); got != tc.want {
			t.Errorf("%s %s levels = %v, want %v", tc.dimension, tc.key, got, tc.want)
		}
	}
	for _, g := range loaded.Groups {
		if g.Dimension == "race" && (g.Levels != nil || g.NoLevels != 0) {
			t.Errorf("race group %q carries level counts; only location groups do", g.Key)
		}
	}
}

func TestUIStatsIncrementalCarriesLevelBandsForwardUnchanged(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-90 * 24 * time.Hour)
	since := now.Add(-30 * 24 * time.Hour)

	seedStatsCharacter(t, driver, 1, "Balmung", "Crystal", "NA", "Hyur", 1, old, nil, 40)
	seedStatsCharacter(t, driver, 2, "Balmung", "Crystal", "NA", "Hyur", 1, old, nil, 100)
	seedStatsCharacter(t, driver, 3, "Balmung", "Crystal", "NA", "Hyur", 1, old, nil, 70)
	full, err := stats.Refresh(ctx, levelOpts(contract.UIStatsRefreshFull, since))
	if err != nil {
		t.Fatal(err)
	}

	// After the full rebuild: a new level 95 and a new levelless character,
	// character 2 deleted, and character 1 levelling to 60.
	time.Sleep(50 * time.Millisecond)
	after := time.Now().UTC()
	seedStatsCharacter(t, driver, 4, "Balmung", "Crystal", "NA", "Hyur", 1, after, nil, 95)
	seedStatsCharacter(t, driver, 5, "Balmung", "Crystal", "NA", "Hyur", 1, after, nil, 0)
	// A first character on a world the rebuild never saw: its group has no
	// band counts until the nightly rebuild, and that is still valid.
	seedStatsCharacter(t, driver, 6, "Zalera", "Crystal", "NA", "Hyur", 1, after, nil, 90)
	mustExec(t, driver, `UPDATE characters SET deleted_at = NOW() WHERE id = 2`)
	mustExec(t, driver, `UPDATE characters SET max_job_level = 60 WHERE id = 1`)

	inc, err := stats.Refresh(ctx, levelOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if inc.Mode != contract.UIStatsRefreshIncremental {
		t.Fatalf("mode = %q, want incremental", inc.Mode)
	}
	// The level bands are nightly only: new, deleted and levelled
	// characters all wait for the nightly rebuild, while the total moves.
	want := levelCounts{0, 1, 0, 0, 1, 0, 0, 1}
	if inc.Snapshot.Summary.Total != 5 {
		t.Errorf("incremental summary total = %d, want 5 (the total still moves by the delta)", inc.Snapshot.Summary.Total)
	}
	if got := levelsOf(inc.Snapshot.Summary.NoLevels, inc.Snapshot.Summary.Levels); got != want {
		t.Errorf("incremental summary levels = %v, want %v", got, want)
	}
	for _, g := range [][2]string{{"world", "Balmung"}, {"datacenter", "Crystal"}, {"region", "NA"}} {
		if got := locationLevels(t, inc.Snapshot, g[0], g[1]); got != want {
			t.Errorf("incremental %s %s levels = %v, want %v", g[0], g[1], got, want)
		}
	}
	if got := locationLevels(t, inc.Snapshot, "world", "Zalera"); got != (levelCounts{}) {
		t.Errorf("new world Zalera levels = %v, want none until the nightly rebuild", got)
	}
	if !inc.Snapshot.LevelsBuiltAt.Equal(full.Snapshot.GeneratedAt) {
		t.Errorf("incremental levels built at %v, want the full rebuild's %v", inc.Snapshot.LevelsBuiltAt, full.Snapshot.GeneratedAt)
	}

	rebuilt, err := stats.Refresh(ctx, levelOpts(contract.UIStatsRefreshFull, since))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := levelsOf(rebuilt.Snapshot.Summary.NoLevels, rebuilt.Snapshot.Summary.Levels), (levelCounts{1, 0, 0, 1, 1, 0, 2, 0}); got != want {
		t.Errorf("nightly rebuild summary levels = %v, want %v (the drift corrected)", got, want)
	}
}

func TestUIStatsIncrementalWithoutLevelFiguresRebuildsFully(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-30 * 24 * time.Hour)
	seedStatsCharacter(t, driver, 1, "Balmung", "Crystal", "NA", "Hyur", 1, now.Add(-72*time.Hour), nil, 55)
	if _, err := stats.Refresh(ctx, levelOpts(contract.UIStatsRefreshFull, since)); err != nil {
		t.Fatal(err)
	}
	// A snapshot written before the level figures existed.
	mustExec(t, driver, `UPDATE ui_stats_snapshots SET payload = payload - 'levels_built_at' - 'level_bands'`)
	time.Sleep(20 * time.Millisecond)

	r, err := stats.Refresh(ctx, levelOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != contract.UIStatsRefreshFull || r.Snapshot.LevelsBuiltAt.IsZero() || r.Snapshot.Summary.Levels[1] != 1 {
		t.Fatalf("mode=%q built=%v levels=%v, want a full rebuild with the level figures", r.Mode, r.Snapshot.LevelsBuiltAt, r.Snapshot.Summary.Levels)
	}
}

func TestUIStatsIncrementalWithChangedLevelBandsRebuildsFully(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-30 * 24 * time.Hour)
	seedStatsCharacter(t, driver, 1, "Balmung", "Crystal", "NA", "Hyur", 1, now.Add(-72*time.Hour), nil, 105)
	if _, err := stats.Refresh(ctx, levelOpts(contract.UIStatsRefreshFull, since)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)

	// A new expansion in the configuration: the next refresh counts its band.
	opts := levelOpts(contract.UIStatsRefreshIncremental, since)
	opts.MaxLevel = 110
	opts.LevelBands = census.LevelBands(append(append([]census.ExpansionConfig(nil), census.DefaultExpansions...), census.ExpansionConfig{Name: "Next", LevelCap: 110}), 110)
	r, err := stats.Refresh(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != contract.UIStatsRefreshFull || len(r.Snapshot.Summary.Levels) != 8 || r.Snapshot.Summary.Levels[6] != 1 {
		t.Fatalf("mode=%q levels=%v, want a full rebuild counting the new band", r.Mode, r.Snapshot.Summary.Levels)
	}
}

func TestUIStatsIncrementalLevelledThenDeletedCharacterLeavesBandsIntact(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-30 * 24 * time.Hour)
	seedStatsCharacter(t, driver, 1, "Balmung", "Crystal", "NA", "Hyur", 1, now.Add(-72*time.Hour), nil, 40)
	seedStatsCharacter(t, driver, 2, "Balmung", "Crystal", "NA", "Hyur", 1, now.Add(-72*time.Hour), nil, 40)
	if _, err := stats.Refresh(ctx, levelOpts(contract.UIStatsRefreshFull, since)); err != nil {
		t.Fatal(err)
	}
	// Counted at level 40, then levelled to the cap and deleted: the bands
	// keep the nightly rebuild's counts until the next one.
	time.Sleep(20 * time.Millisecond)
	mustExec(t, driver, `UPDATE characters SET max_job_level = 100, deleted_at = NOW() WHERE id = 1`)

	r, err := stats.Refresh(ctx, levelOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != contract.UIStatsRefreshIncremental {
		t.Fatalf("mode = %q, want incremental (the drift waits for the nightly rebuild)", r.Mode)
	}
	if got, want := levelsOf(r.Snapshot.Summary.NoLevels, r.Snapshot.Summary.Levels), (levelCounts{0, 2, 0, 0, 0, 0, 0, 0}); got != want {
		t.Errorf("summary levels = %v, want %v", got, want)
	}
	if got, want := locationLevels(t, r.Snapshot, "world", "Balmung"), (levelCounts{0, 2, 0, 0, 0, 0, 0, 0}); got != want {
		t.Errorf("Balmung levels = %v, want %v", got, want)
	}
}
