package repository_test

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func incrementalOpts(mode contract.UIStatsRefreshMode, since time.Time) contract.UIStatsRefreshOptions {
	return contract.UIStatsRefreshOptions{ActivitySince: since, MaxLevel: 100, NewCharactersWindowDays: 90, Timeout: time.Minute, Mode: mode}
}

// comparable strips the fields that legitimately differ between two refreshes.
func comparableSnapshot(s *contract.UIStatsSnapshot) *contract.UIStatsSnapshot {
	c := *s
	c.GeneratedAt = time.Time{}
	c.RefreshDuration = 0
	if s.Jobs != nil {
		jobs := *s.Jobs
		jobs.BuiltAt = time.Time{}
		c.Jobs = &jobs
	}
	c.PrivacyBuiltAt = time.Time{}
	c.FreeCompaniesBuiltAt = time.Time{}
	c.LevelsBuiltAt = time.Time{}
	c.Groups = append([]contract.ScopedGroupCount(nil), s.Groups...)
	sort.Slice(c.Groups, func(i, j int) bool {
		a, b := c.Groups[i], c.Groups[j]
		ka := a.Scope.Region + "|" + a.Scope.Datacenter + "|" + a.Scope.World + "|" + a.Dimension + "|" + a.Key
		kb := b.Scope.Region + "|" + b.Scope.Datacenter + "|" + b.Scope.World + "|" + b.Dimension + "|" + b.Key
		return ka < kb
	})
	return &c
}

func seedStatsCharacter(t *testing.T, driver contract.DatabaseDriver, id int, world, dc, region, race string, gender int, firstSeen time.Time, latest *time.Time, level int) {
	t.Helper()
	chars := repository.NewCharacterRepository(driver)
	rec := contract.CharacterRecord{ID: uint32(id), Name: "C" + world, World: world, Datacenter: dc, Region: region, Race: race, Tribe: "T" + race, Gender: uint8(gender), FirstSeenAt: firstSeen}
	var jobs []contract.ClassJobRecord
	if level > 0 {
		jobs = []contract.ClassJobRecord{{CharacterID: rec.ID, ClassJobID: 19, Name: "Paladin", Level: uint8(level)}}
	}
	if err := chars.Upsert(context.Background(), rec, jobs); err != nil {
		t.Fatal(err)
	}
	if err := setLatestAchievement(context.Background(), chars, rec.ID, latest); err != nil {
		t.Fatal(err)
	}
}

func TestUIStatsIncrementalWithoutSnapshotFallsBackToFull(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	now := time.Now().UTC()
	seedStatsCharacter(t, driver, 1, "Balmung", "Crystal", "NA", "Hyur", 1, now.Add(-48*time.Hour), nil, 0)

	result, err := stats.Refresh(context.Background(), incrementalOpts(contract.UIStatsRefreshIncremental, now.Add(-30*24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != contract.UIStatsRefreshFull || result.Snapshot.Summary.Total != 1 {
		t.Fatalf("result mode=%q total=%d, want full fallback with total 1", result.Mode, result.Snapshot.Summary.Total)
	}
}

func TestUIStatsDefaultModeIsFull(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	result, err := stats.Refresh(context.Background(), incrementalOpts("", time.Now().Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != contract.UIStatsRefreshFull {
		t.Fatalf("mode = %q, want full", result.Mode)
	}
}

func TestUIStatsIncrementalMatchesFullRebuild(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-90 * 24 * time.Hour)
	recent := now.Add(-time.Hour)
	aging := now.Add(-29*24*time.Hour - 23*time.Hour) // active now, inactive after the window moves

	seedStatsCharacter(t, driver, 1, "Balmung", "Crystal", "NA", "Hyur", 1, old, &recent, 100)
	seedStatsCharacter(t, driver, 2, "Balmung", "Crystal", "NA", "Lalafell", 2, old, &aging, 90)
	seedStatsCharacter(t, driver, 3, "Ragnarok", "Chaos", "EU", "Hyur", 2, old, nil, 0)
	seedStatsCharacter(t, driver, 4, "Ragnarok", "Chaos", "EU", "Hyur", 1, old, &recent, 100)

	since := now.Add(-30 * 24 * time.Hour)
	if _, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since)); err != nil {
		t.Fatal(err)
	}

	// Changes after the full refresh: a new character in an existing and a new
	// world, a soft delete of an active max-level character and of an old one,
	// a character becoming active, and the activity window moving forward.
	time.Sleep(50 * time.Millisecond)
	after := time.Now().UTC()
	seedStatsCharacter(t, driver, 5, "Balmung", "Crystal", "NA", "Hyur", 1, after, &after, 100)
	seedStatsCharacter(t, driver, 6, "Moogle", "Mana", "JP", "Viera", 2, after, nil, 0)
	mustExec(t, driver, `UPDATE characters SET deleted_at = NOW() WHERE id IN (1, 3)`)
	if err := setLatestAchievement(ctx, repository.NewCharacterRepository(driver), 3, &after); err != nil {
		t.Fatal(err)
	}
	moved := since.Add(2 * time.Hour)

	inc, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, moved))
	if err != nil {
		t.Fatal(err)
	}
	if inc.Mode != contract.UIStatsRefreshIncremental {
		t.Fatalf("mode = %q, want incremental", inc.Mode)
	}
	full, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, moved))
	if err != nil {
		t.Fatal(err)
	}
	// Character 3 gains an achievement after it was counted as unchecked and
	// is then deleted: the accepted privacy drift, covered with the privacy
	// tests. The job section and the level bands are nightly figures the incremental refresh
	// carries forward on purpose (TestUIStatsIncrementalCarriesJobSectionForward,
	// TestUIStatsIncrementalCarriesLevelBandsForwardUnchanged).
	// Every other figure must match exactly.
	incCmp, fullCmp := withoutLevels(withoutPrivacy(comparableSnapshot(inc.Snapshot))), withoutLevels(withoutPrivacy(comparableSnapshot(full.Snapshot)))
	incCmp.Jobs, fullCmp.Jobs = nil, nil
	if !reflect.DeepEqual(incCmp, fullCmp) {
		t.Fatalf("incremental differs from full rebuild\nincremental: %+v\nfull:        %+v", incCmp, fullCmp)
	}
}

// withoutPrivacy zeroes the privacy counts, which the incremental refresh
// carries forward by the delta rather than recomputing.
func withoutPrivacy(s *contract.UIStatsSnapshot) *contract.UIStatsSnapshot {
	s.Summary.Private, s.Summary.Unchecked = 0, 0
	for i := range s.Groups {
		s.Groups[i].Private, s.Groups[i].Unchecked = 0, 0
	}
	return s
}

// withoutLevels drops the level band counts, which the incremental refresh
// carries forward unchanged until the nightly rebuild.
func withoutLevels(s *contract.UIStatsSnapshot) *contract.UIStatsSnapshot {
	s.Summary.Levels, s.Summary.NoLevels = nil, 0
	for i := range s.Groups {
		s.Groups[i].Levels, s.Groups[i].NoLevels = nil, 0
	}
	return s
}

func TestUIStatsIncrementalRepeatedRunsDoNotDoubleCount(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	seedStatsCharacter(t, driver, 1, "Balmung", "Crystal", "NA", "Hyur", 1, now.Add(-72*time.Hour), nil, 0)
	since := now.Add(-30 * 24 * time.Hour)
	if _, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	seedStatsCharacter(t, driver, 2, "Balmung", "Crystal", "NA", "Hyur", 1, time.Now().UTC(), nil, 0)
	for i := 0; i < 3; i++ {
		r, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, since))
		if err != nil {
			t.Fatal(err)
		}
		if r.Snapshot.Summary.Total != 2 {
			t.Fatalf("run %d: total = %d, want 2", i, r.Snapshot.Summary.Total)
		}
	}
}

func TestUIStatsIncrementalFallsBackWhenResultInvalid(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	seedStatsCharacter(t, driver, 1, "Balmung", "Crystal", "NA", "Hyur", 1, now.Add(-72*time.Hour), nil, 0)
	since := now.Add(-30 * 24 * time.Hour)
	if _, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since)); err != nil {
		t.Fatal(err)
	}
	// Corrupt the stored totals so a soft delete drives a group negative.
	mustExec(t, driver, `UPDATE ui_stats_snapshots SET payload = jsonb_set(payload, '{groups}', '[]'::jsonb)`)
	time.Sleep(20 * time.Millisecond)
	mustExec(t, driver, `UPDATE characters SET deleted_at = NOW() WHERE id = 1`)

	r, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != contract.UIStatsRefreshFull || r.Snapshot.Summary.Total != 0 {
		t.Fatalf("mode=%q total=%d, want full fallback with total 0", r.Mode, r.Snapshot.Summary.Total)
	}
}
