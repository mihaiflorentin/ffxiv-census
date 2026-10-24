package repository_test

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestUIStatsRepositoryLoadCurrentNotFound(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewUIStatsRepository(driver)
	_, err := repo.LoadCurrent(context.Background())
	if !errors.Is(err, census.ErrUIStatsUnavailable) {
		t.Fatalf("LoadCurrent() error = %v, want ErrUIStatsUnavailable", err)
	}
}

func TestUIStatsRefreshScale(t *testing.T) {
	rawRows := os.Getenv("UI_STATS_SCALE_ROWS")
	if rawRows == "" {
		t.Skip("set UI_STATS_SCALE_ROWS to run the opt-in snapshot scale test")
	}
	rows, err := strconv.Atoi(rawRows)
	if err != nil || rows < 1 {
		t.Fatalf("UI_STATS_SCALE_ROWS=%q must be a positive integer", rawRows)
	}

	driver := newTestDriver(t)
	ctx := context.Background()
	if _, err := driver.Execute(ctx, `
		INSERT INTO characters (
			id, name, world, datacenter, region, race, tribe, gender,
			latest_achievement_at, first_seen_at, max_job_level
		)
		SELECT n,
		       'Scale ' || n,
		       (ARRAY['Balmung','Ragnarok','Chocobo','Ravana'])[(n % 4) + 1],
		       (ARRAY['Crystal','Chaos','Mana','Materia'])[(n % 4) + 1],
		       (ARRAY['NA','EU','JP','OCE'])[(n % 4) + 1],
		       (ARRAY['Hyur','Miqote','Lalafell','Viera'])[(n % 4) + 1],
		       (ARRAY['Midlander','Seeker','Plainsfolk','Rava'])[(n % 4) + 1],
		       (n % 2) + 1,
		       NOW() - ((n % 60) || ' days')::interval,
		       NOW(),
		       CASE WHEN n % 3 = 0 THEN 100 ELSE 90 END
		FROM generate_series(1, $1) AS n`, rows); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	result, err := repository.NewUIStatsRepository(driver).Refresh(ctx, contract.UIStatsRefreshOptions{
		ActivitySince: time.Now().UTC().Add(-30 * 24 * time.Hour),
		MaxLevel:      100,
		Timeout:       2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Summary.Total != int64(rows) {
		t.Fatalf("snapshot total = %d, want %d", result.Snapshot.Summary.Total, rows)
	}
	t.Logf("rows=%d refresh=%s payload_bytes=%d", rows, time.Since(started), result.PayloadBytes)
}

func TestUIStatsRepositoryRefreshAndLoadCurrent(t *testing.T) {
	driver := newTestDriver(t)
	chars := repository.NewCharacterRepository(driver)
	achievements := repository.NewAchievementRepository(driver)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	recent := now.Add(-time.Hour)
	old := now.Add(-60 * 24 * time.Hour)

	seed := []struct {
		rec    contract.CharacterRecord
		jobs   []contract.ClassJobRecord
		latest *time.Time
	}{
		{
			rec:  contract.CharacterRecord{ID: 1, Name: "One", World: "Balmung", Datacenter: "Crystal", Region: "NA", Race: "Hyur", Tribe: "Midlander", Gender: 1, FirstSeenAt: old},
			jobs: []contract.ClassJobRecord{{CharacterID: 1, ClassJobID: 19, Name: "Paladin", Level: 100}}, latest: &recent,
		},
		{
			rec:    contract.CharacterRecord{ID: 2, Name: "Two", World: "Balmung", Datacenter: "Crystal", Region: "NA", Race: "Lalafell", Tribe: "Plainsfolk", Gender: 2, FirstSeenAt: old},
			latest: &recent,
		},
		{
			rec:    contract.CharacterRecord{ID: 3, Name: "Three", World: "Ragnarok", Datacenter: "Chaos", Region: "EU", Race: "Hyur", Tribe: "Highlander", Gender: 2, FirstSeenAt: old},
			latest: &old,
		},
	}
	for _, item := range seed {
		if err := chars.Upsert(ctx, item.rec, item.jobs); err != nil {
			t.Fatal(err)
		}
		if err := setLatestAchievement(ctx, chars, item.rec.ID, item.latest); err != nil {
			t.Fatal(err)
		}
	}

	expansion := "A Realm Reborn"
	if err := achievements.SyncMilestones(ctx, []contract.MilestoneAchievement{
		{AchievementID: 1129, Kind: contract.MilestoneKindExpansion, Expansion: &expansion, Detail: "Before the Dawn"},
		{AchievementID: 590, Kind: "chocobo", Detail: "My Little Chocobo"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := achievements.UpsertCharacterMilestones(ctx, 1, []contract.CharacterMilestone{
		{CharacterID: 1, AchievementID: 1129, AchievedAt: old},
		{CharacterID: 1, AchievementID: 590, AchievedAt: recent},
	}); err != nil {
		t.Fatal(err)
	}

	result, err := stats.Refresh(ctx, contract.UIStatsRefreshOptions{
		ActivitySince: now.Add(-30 * 24 * time.Hour),
		MaxLevel:      100,
		Timeout:       time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped || result.Snapshot == nil {
		t.Fatalf("Refresh() = %#v", result)
	}

	loaded, err := stats.LoadCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Summary != (contract.StatsSummary{Total: 3, Active: 2, MaxLevel: 1}) {
		t.Fatalf("summary = %#v", loaded.Summary)
	}
	globalRaces := census.SnapshotGroups(loaded, "race", contract.StatsScope{})
	if len(globalRaces) != 2 || globalRaces[0].Key != "Hyur" || globalRaces[0].Total != 2 {
		t.Fatalf("global races = %#v", globalRaces)
	}
	naRaces := census.SnapshotGroups(loaded, "race", contract.StatsScope{Region: "NA"})
	if len(naRaces) != 2 {
		t.Fatalf("NA races = %#v", naRaces)
	}
	worlds := census.SnapshotGroups(loaded, "world", contract.StatsScope{})
	if len(worlds) != 2 || worlds[0].Key != "Balmung" || worlds[0].Total != 2 {
		t.Fatalf("worlds = %#v", worlds)
	}
	expansions := census.SnapshotExpansions(loaded, contract.StatsScope{})
	if len(expansions) != 1 || expansions[0].Count != 1 {
		t.Fatalf("expansions = %#v", expansions)
	}
	daily := census.SnapshotDaily(loaded, contract.StatsScope{})
	if len(daily) != 1 || daily[0].Count != 1 || daily[0].Day != recent.Format("2006-01-02") {
		t.Fatalf("daily = %#v", daily)
	}
}

func TestUIStatsRefreshEmitsPreviousWindowDailies(t *testing.T) {
	driver := newTestDriver(t)
	chars := repository.NewCharacterRepository(driver)
	achievements := repository.NewAchievementRepository(driver)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	seen := now.Add(-120 * 24 * time.Hour)

	seed := []struct {
		rec        contract.CharacterRecord
		achievedAt time.Time
	}{
		{rec: contract.CharacterRecord{ID: 1, Name: "Current", World: "Balmung", Datacenter: "Crystal", Region: "NA", Race: "Hyur", FirstSeenAt: seen}, achievedAt: now.Add(-time.Hour)},
		{rec: contract.CharacterRecord{ID: 2, Name: "Previous", World: "Balmung", Datacenter: "Crystal", Region: "NA", Race: "Hyur", FirstSeenAt: seen}, achievedAt: now.Add(-40 * 24 * time.Hour)},
		{rec: contract.CharacterRecord{ID: 3, Name: "Older", World: "Ragnarok", Datacenter: "Chaos", Region: "EU", Race: "Miqo'te", FirstSeenAt: seen}, achievedAt: now.Add(-50 * 24 * time.Hour)},
	}
	if err := achievements.SyncMilestones(ctx, []contract.MilestoneAchievement{
		{AchievementID: 590, Kind: "chocobo", Detail: "My Little Chocobo"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, item := range seed {
		if err := chars.Upsert(ctx, item.rec, nil); err != nil {
			t.Fatal(err)
		}
		if err := achievements.UpsertCharacterMilestones(ctx, item.rec.ID, []contract.CharacterMilestone{
			{CharacterID: item.rec.ID, AchievementID: 590, AchievedAt: item.achievedAt},
		}); err != nil {
			t.Fatal(err)
		}
	}

	result, err := stats.Refresh(ctx, contract.UIStatsRefreshOptions{
		ActivitySince: now.Add(-30 * 24 * time.Hour),
		MaxLevel:      100,
		Timeout:       time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}

	days := map[string]int64{}
	for _, day := range census.SnapshotDaily(result.Snapshot, contract.StatsScope{}) {
		days[day.Day] += day.Count
	}
	for _, item := range seed {
		wantDay := item.achievedAt.Format("2006-01-02")
		if days[wantDay] != 1 {
			t.Fatalf("daily series missing or wrong for %s (achieved %s): %#v", wantDay, item.achievedAt, days)
		}
	}
}

func TestUIStatsSnapshotSchemaV3RoundTripAcceptsV2(t *testing.T) {
	driver := newTestDriver(t)
	chars := repository.NewCharacterRepository(driver)
	achievements := repository.NewAchievementRepository(driver)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	seen := now.Add(-120 * 24 * time.Hour)
	recent := now.Add(-time.Hour)

	rec := contract.CharacterRecord{ID: 1, Name: "One", World: "Balmung", Datacenter: "Crystal", Region: "NA", Race: "Hyur", FirstSeenAt: seen}
	if err := chars.Upsert(ctx, rec, nil); err != nil {
		t.Fatal(err)
	}
	if err := achievements.SyncMilestones(ctx, []contract.MilestoneAchievement{
		{AchievementID: 590, Kind: "chocobo", Detail: "My Little Chocobo"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := achievements.UpsertCharacterMilestones(ctx, 1, []contract.CharacterMilestone{
		{CharacterID: 1, AchievementID: 590, AchievedAt: recent},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := stats.Refresh(ctx, contract.UIStatsRefreshOptions{
		ActivitySince: now.Add(-30 * 24 * time.Hour),
		MaxLevel:      100,
		Timeout:       time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := stats.LoadCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != 3 {
		t.Fatalf("stored snapshot schema version = %d, want 3", loaded.SchemaVersion)
	}
	// A stored v2 snapshot (written before the upgrade) still loads, so the
	// existing pages keep working until the next refresh.
	if _, err := driver.Execute(ctx, `UPDATE ui_stats_snapshots SET schema_version = 2 WHERE snapshot_key = 'current'`); err != nil {
		t.Fatal(err)
	}
	v2, err := stats.LoadCurrent(ctx)
	if err != nil {
		t.Fatalf("loading a stored v2 snapshot: %v", err)
	}
	if v2.SchemaVersion != 2 {
		t.Fatalf("loaded schema version = %d, want 2", v2.SchemaVersion)
	}
	if _, err := driver.Execute(ctx, `UPDATE ui_stats_snapshots SET schema_version = 1 WHERE snapshot_key = 'current'`); err != nil {
		t.Fatal(err)
	}
	if _, err := stats.LoadCurrent(ctx); err == nil || !strings.Contains(err.Error(), "unsupported UI statistics schema version 1") {
		t.Fatalf("loading a stored v1 snapshot must fail with unsupported-version error, got %v", err)
	}
}
