package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// seedJobCharacter stores a live character on a world with the given jobs,
// through the normal write path (which sets both job bitmasks).
func seedJobCharacter(t *testing.T, driver contract.DatabaseDriver, id uint32, world, dc, region string, firstSeen time.Time, jobs ...contract.ClassJobRecord) {
	t.Helper()
	chars := repository.NewCharacterRepository(driver, repository.WithMaxLevel(100))
	rec := contract.CharacterRecord{ID: id, Name: "J" + world, World: world, Datacenter: dc, Region: region, Race: "Hyur", Tribe: "Midlander", Gender: 1, FirstSeenAt: firstSeen}
	for i := range jobs {
		jobs[i].CharacterID = id
	}
	if err := chars.Upsert(context.Background(), rec, jobs); err != nil {
		t.Fatal(err)
	}
}

func job(id, level uint8) contract.ClassJobRecord {
	return contract.ClassJobRecord{ClassJobID: id, Name: "job", Level: level}
}

// worldJobs reads one world's row of the job section: covered, characters,
// and per job id its levelled and at-cap counts.
func worldJobs(t *testing.T, section *contract.StatsJobsSection, world string) (covered, characters int64, counts map[int][2]int64) {
	t.Helper()
	if section == nil {
		t.Fatal("snapshot has no job section")
	}
	for _, w := range section.Worlds {
		if w.World != world {
			continue
		}
		if len(w.Levelled) != len(section.JobIDs) || len(w.AtCap) != len(section.JobIDs) {
			t.Fatalf("world %s rows have %d/%d columns, want %d", world, len(w.Levelled), len(w.AtCap), len(section.JobIDs))
		}
		counts = map[int][2]int64{}
		for i, id := range section.JobIDs {
			if w.Levelled[i] != 0 || w.AtCap[i] != 0 {
				counts[id] = [2]int64{w.Levelled[i], w.AtCap[i]}
			}
		}
		return w.Covered, w.Characters, counts
	}
	return 0, 0, nil
}

func TestUIStatsFullRefreshJobSectionFromMasks(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-90 * 24 * time.Hour)

	// Gladiator folds into Paladin; Blue Mage is at its own cap at 80.
	seedJobCharacter(t, driver, 1, "Balmung", "Crystal", "NA", old, job(1, 100), job(36, 80), job(18, 42))
	seedJobCharacter(t, driver, 2, "Balmung", "Crystal", "NA", old, job(19, 90), job(25, 100))
	seedJobCharacter(t, driver, 3, "Balmung", "Crystal", "NA", old) // no jobs: covered, nothing levelled
	seedJobCharacter(t, driver, 4, "Balmung", "Crystal", "NA", old, job(25, 100))
	mustExec(t, driver, `UPDATE characters SET jobs_levelled_mask = NULL, jobs_at_cap_mask = NULL WHERE id = 4`) // not yet backfilled
	seedJobCharacter(t, driver, 5, "Ragnarok", "Chaos", "EU", old, job(24, 100))
	seedJobCharacter(t, driver, 6, "Ragnarok", "Chaos", "EU", old, job(24, 100))
	mustExec(t, driver, `UPDATE characters SET deleted_at = NOW() WHERE id = 6`)

	result, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, now.Add(-30*24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := stats.LoadCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Jobs == nil || !loaded.Jobs.BuiltAt.Equal(result.Snapshot.GeneratedAt) {
		t.Fatalf("job section build time = %v, want the refresh time %v", loaded.Jobs, result.Snapshot.GeneratedAt)
	}

	covered, characters, balmung := worldJobs(t, loaded.Jobs, "Balmung")
	if covered != 3 || characters != 4 {
		t.Errorf("Balmung covered %d of %d, want 3 of 4", covered, characters)
	}
	wantBalmung := map[int][2]int64{19: {2, 1}, 36: {1, 1}, 18: {1, 0}, 25: {1, 1}}
	if len(balmung) != len(wantBalmung) {
		t.Fatalf("Balmung jobs = %v, want %v", balmung, wantBalmung)
	}
	for id, want := range wantBalmung {
		if balmung[id] != want {
			t.Errorf("Balmung job %d = %v, want %v (all %v)", id, balmung[id], want, balmung)
		}
	}

	covered, characters, ragnarok := worldJobs(t, loaded.Jobs, "Ragnarok")
	if covered != 1 || characters != 1 || ragnarok[24] != [2]int64{1, 1} || len(ragnarok) != 1 {
		t.Errorf("Ragnarok covered %d of %d jobs %v, want 1 of 1 with WHM 1/1", covered, characters, ragnarok)
	}
}

func TestUIStatsIncrementalCarriesJobSectionForward(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-30 * 24 * time.Hour)
	seedJobCharacter(t, driver, 1, "Balmung", "Crystal", "NA", now.Add(-72*time.Hour), job(19, 100))

	full, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since))
	if err != nil {
		t.Fatal(err)
	}

	// A new character with jobs arrives: the job figures wait for the nightly rebuild.
	time.Sleep(50 * time.Millisecond)
	seedJobCharacter(t, driver, 2, "Balmung", "Crystal", "NA", time.Now().UTC(), job(19, 100), job(24, 100))

	inc, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if inc.Mode != contract.UIStatsRefreshIncremental {
		t.Fatalf("mode = %q, want incremental", inc.Mode)
	}
	if inc.Snapshot.Summary.Total != 2 {
		t.Fatalf("summary total = %d, want the delta applied (2)", inc.Snapshot.Summary.Total)
	}
	loaded, err := stats.LoadCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Jobs == nil || !loaded.Jobs.BuiltAt.Equal(full.Snapshot.GeneratedAt) {
		t.Fatalf("job section = %+v, want the full refresh's section built at %v", loaded.Jobs, full.Snapshot.GeneratedAt)
	}
	if !loaded.GeneratedAt.After(loaded.Jobs.BuiltAt) {
		t.Fatal("the snapshot itself must be newer than its carried job section")
	}
	covered, characters, balmung := worldJobs(t, loaded.Jobs, "Balmung")
	if covered != 1 || characters != 1 || balmung[19] != [2]int64{1, 1} || len(balmung) != 1 {
		t.Errorf("carried Balmung = %d of %d, %v; want the full refresh's 1 of 1 with PLD 1/1", covered, characters, balmung)
	}
}

func TestUIStatsIncrementalWithoutJobSectionRebuildsFully(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-30 * 24 * time.Hour)
	seedJobCharacter(t, driver, 1, "Balmung", "Crystal", "NA", now.Add(-72*time.Hour), job(19, 100))
	if _, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since)); err != nil {
		t.Fatal(err)
	}
	// A v3 snapshot stored before the job section existed.
	mustExec(t, driver, `UPDATE ui_stats_snapshots SET payload = payload - 'jobs'`)
	time.Sleep(20 * time.Millisecond)

	r, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != contract.UIStatsRefreshFull || r.Snapshot.Jobs == nil {
		t.Fatalf("mode=%q jobs=%v, want a full rebuild with a job section", r.Mode, r.Snapshot.Jobs)
	}
}
