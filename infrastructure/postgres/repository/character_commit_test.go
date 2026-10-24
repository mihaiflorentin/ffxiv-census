package repository_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// installCommitProbe makes every insert/update on the given tables record the
// synchronous_commit setting of the writing transaction.
func installCommitProbe(t *testing.T, driver contract.DatabaseDriver, tables ...string) {
	t.Helper()
	mustExec(t, driver, `CREATE TABLE commit_probe (tbl text, setting text)`)
	mustExec(t, driver, `
		CREATE FUNCTION commit_probe_fn() RETURNS trigger AS $$
		BEGIN
			INSERT INTO commit_probe VALUES (TG_TABLE_NAME, current_setting('synchronous_commit'));
			RETURN NEW;
		END $$ LANGUAGE plpgsql`)
	for _, tbl := range tables {
		mustExec(t, driver, `CREATE TRIGGER commit_probe_`+tbl+` AFTER INSERT OR UPDATE ON `+tbl+
			` FOR EACH ROW EXECUTE FUNCTION commit_probe_fn()`)
	}
}

func probeSettings(t *testing.T, driver contract.DatabaseDriver, tbl string) map[string]int {
	t.Helper()
	db, err := driver.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT setting, count(*) FROM commit_probe WHERE tbl = $1 GROUP BY setting`, tbl)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	got := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			t.Fatal(err)
		}
		got[s] = n
	}
	return got
}

func TestCharacterRepository_StoreResultCommitsAsynchronously(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()
	installCommitProbe(t, driver, "characters", "character_jobs", "character_milestones")

	latestID, latestAt := uint32(5), time.Now().UTC()
	err := repo.StoreResult(ctx, contract.CensusResult{
		Characters: []contract.CharacterWrite{
			batchWrite(9001, "Seed"),
			batchWrite(9002, "New", contract.ClassJobRecord{ClassJobID: 19, Name: "Paladin", Level: 100}),
		},
		Achievements: []contract.AchievementWrite{{
			CharacterID: 9001,
			Milestones:  []contract.CharacterMilestone{{CharacterID: 9001, AchievementID: 590, AchievedAt: latestAt}},
			LatestID:    &latestID, LatestAt: &latestAt,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tbl := range []string{"characters", "character_jobs", "character_milestones"} {
		got := probeSettings(t, driver, tbl)
		if got["off"] == 0 || got["on"] != 0 {
			t.Fatalf("%s settings during StoreResult = %v, want only off", tbl, got)
		}
	}
	// The setting is transaction-local: it must not leak to the pooled connection.
	var setting string
	db, _ := driver.Acquire(ctx)
	if err := db.QueryRow(`SHOW synchronous_commit`).Scan(&setting); err != nil || setting != "on" {
		t.Fatalf("session synchronous_commit after StoreResult = %q, %v; want on", setting, err)
	}
}

func TestOtherWritePathsStaySynchronous(t *testing.T) {
	driver := newTestDriver(t)
	chars := repository.NewCharacterRepository(driver)
	proxies := repository.NewProxyRepository(driver, testScanPolicy())
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	installCommitProbe(t, driver, "characters", "proxies", "ui_stats_snapshots")

	// Seed (the seed itself goes through StoreResult), then clear the probe.
	if err := chars.Upsert(ctx, batchWrite(9101, "Seed").Record, nil); err != nil {
		t.Fatal(err)
	}
	id := mustInsertProxy(t, proxies, "10.0.0.1", 8080)
	mustExec(t, driver, `DELETE FROM commit_probe`)

	if err := chars.MarkDeleted(ctx, 9101, time.Now()); err != nil {
		t.Fatal(err)
	}
	leases, err := proxies.ClaimScans(ctx, contract.ScanBackground, 1)
	if err != nil || len(leases) != 1 {
		t.Fatalf("ClaimScans = %v, %v", leases, err)
	}
	if err := proxies.ReleaseScan(ctx, leases[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := proxies.RefreshConsumerLock(ctx, id, "owner", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := stats.Refresh(ctx, contract.UIStatsRefreshOptions{
		ActivitySince: time.Now().Add(-24 * time.Hour), MaxLevel: 100, NewCharactersWindowDays: 90, Timeout: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}

	for _, tbl := range []string{"characters", "proxies", "ui_stats_snapshots"} {
		got := probeSettings(t, driver, tbl)
		if got["on"] == 0 || got["off"] != 0 {
			t.Fatalf("%s settings outside StoreResult = %v, want only on", tbl, got)
		}
	}
}

// TestCharacterRepository_StoreResultWritesInAscendingIDOrder makes the lock
// order observable: a foreign transaction holds the lock on one row, so
// StoreResult blocks on it. Every row StoreResult wrote before blocking is
// then visible as a held lock. In ascending order only lower IDs are held;
// any other order would already hold higher IDs, which is the precondition for
// a deadlock between two pods with overlapping IDs.
func TestCharacterRepository_StoreResultWritesInAscendingIDOrder(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()
	for _, id := range []uint32{9201, 9202, 9203, 9204} {
		if err := repo.Upsert(ctx, batchWrite(id, "Seed").Record, nil); err != nil {
			t.Fatal(err)
		}
	}
	db, err := driver.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.ExecContext(ctx, `SELECT id FROM characters WHERE id = 9202 FOR UPDATE`); err != nil {
		t.Fatal(err)
	}

	latestAt := time.Now().UTC()
	done := make(chan error, 1)
	go func() {
		done <- repo.StoreResult(ctx, contract.CensusResult{
			Characters: []contract.CharacterWrite{batchWrite(9204, "A"), batchWrite(9202, "B"), batchWrite(9201, "C")},
			Achievements: []contract.AchievementWrite{
				{CharacterID: 9203, Private: true},
				{CharacterID: 9201, LatestAt: &latestAt},
			},
		})
	}()

	waitForLockWait(t, db)
	for _, id := range []uint32{9203, 9204} {
		probe, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = probe.ExecContext(ctx, `SELECT id FROM characters WHERE id = $1 FOR UPDATE NOWAIT`, id)
		_ = probe.Rollback()
		if err != nil {
			t.Fatalf("row %d already locked while StoreResult waits on 9202: writes are not in ascending ID order (%v)", id, err)
		}
	}

	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("StoreResult: %v", err)
	}
}

func waitForLockWait(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("StoreResult never blocked on the foreign row lock")
}
