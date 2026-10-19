package repository_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestCharacterRepository_UpsertAndGet(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	fcID := "fc-123"
	fcName := "Crystal Guard"
	rec := contract.CharacterRecord{
		ID:                  1001,
		Name:                "Tataru Taru",
		World:               "Louisoix",
		Datacenter:          "Chaos",
		Region:              "EU",
		Race:                "Lalafell",
		Tribe:               "Plainsfolk",
		Gender:              1,
		GrandCompany:        "Maelstrom",
		FreeCompanyID:       &fcID,
		FreeCompanyName:     &fcName,
		Bio:                 "Accounting master",
		ActiveJob:           "MIN",
		ItemLevel:           650,
		AchievementsPrivate: false,
		FirstSeenAt:         time.Now().UTC().Truncate(time.Millisecond),
	}

	jobs := []contract.ClassJobRecord{
		{CharacterID: 1001, ClassJobID: 1, Name: "Gladiator", Level: 50, ExpLevel: 100},
		{CharacterID: 1001, ClassJobID: 16, Name: "Miner", Level: 90, ExpLevel: 5000},
	}

	if err := repo.Upsert(ctx, rec, jobs); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := repo.Get(ctx, 1001)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected character, got nil")
	}
	if got.Name != rec.Name || got.World != rec.World {
		t.Errorf("got name=%q world=%q, want %q %q", got.Name, got.World, rec.Name, rec.World)
	}

	gotJobs, err := repo.GetJobs(ctx, 1001)
	if err != nil {
		t.Fatalf("GetJobs: %v", err)
	}
	if len(gotJobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(gotJobs))
	}
}

func TestCharacterRepository_UpsertPersistsMaxJobLevel(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	rec := contract.CharacterRecord{ID: 2001, Name: "Maximus", World: "Balmung", FirstSeenAt: now}
	if err := repo.Upsert(ctx, rec, []contract.ClassJobRecord{
		{CharacterID: 2001, ClassJobID: 19, Name: "Paladin", Level: 90},
		{CharacterID: 2001, ClassJobID: 21, Name: "Warrior", Level: 100},
	}); err != nil {
		t.Fatal(err)
	}

	var maxLevel int
	row, err := driver.FetchOne(ctx, `SELECT max_job_level FROM characters WHERE id = $1`, 2001)
	if err != nil {
		t.Fatal(err)
	}
	if err := row.Scan(&maxLevel); err != nil {
		t.Fatal(err)
	}
	if maxLevel != 100 {
		t.Fatalf("max_job_level = %d, want 100", maxLevel)
	}

	if err := repo.Upsert(ctx, rec, []contract.ClassJobRecord{
		{CharacterID: 2001, ClassJobID: 19, Name: "Paladin", Level: 80},
	}); err != nil {
		t.Fatal(err)
	}
	row, err = driver.FetchOne(ctx, `SELECT max_job_level FROM characters WHERE id = $1`, 2001)
	if err != nil {
		t.Fatal(err)
	}
	if err := row.Scan(&maxLevel); err != nil {
		t.Fatal(err)
	}
	if maxLevel != 80 {
		t.Fatalf("updated max_job_level = %d, want 80", maxLevel)
	}
}

func TestCharacterRepository_SummaryCountsUsesStoredMaxLevel(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	recent := now.Add(-time.Hour)

	for id, level := range []uint8{100, 90, 0} {
		charID := uint32(id + 1)
		rec := contract.CharacterRecord{ID: charID, Name: "Summary", World: "Balmung", FirstSeenAt: now}
		var jobs []contract.ClassJobRecord
		if level > 0 {
			jobs = []contract.ClassJobRecord{{CharacterID: charID, ClassJobID: 19, Name: "Paladin", Level: level}}
		}
		if err := repo.Upsert(ctx, rec, jobs); err != nil {
			t.Fatal(err)
		}
		if id < 2 {
			if err := setLatestAchievement(ctx, repo, charID, &recent); err != nil {
				t.Fatal(err)
			}
		}
	}

	total, active, maxLevel, err := repo.SummaryCounts(ctx, now.Add(-24*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || active != 2 || maxLevel != 1 {
		t.Fatalf("SummaryCounts = (%d, %d, %d), want (3, 2, 1)", total, active, maxLevel)
	}
}

func TestCharacterRepository_IDSweepCursorInitializesAndAdvances(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repo.Upsert(ctx, contract.CharacterRecord{ID: 1584838, Name: "Frontier", World: "Aegis", Race: "Hyur", FirstSeenAt: now}, nil); err != nil {
		t.Fatal(err)
	}

	got, err := repo.IDSweepCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1584839 {
		t.Fatalf("initial cursor = %d, want 1584839", got)
	}
	if err := repo.AdvanceIDSweepCursor(ctx, 1584839, 1585389); err != nil {
		t.Fatal(err)
	}
	got, err = repo.IDSweepCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1585389 {
		t.Fatalf("advanced cursor = %d, want 1585389", got)
	}
}

func TestCharacterRepository_IDSweepCursorNeverRewinds(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	if _, err := repo.IDSweepCursor(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.AdvanceIDSweepCursor(ctx, 1, 101); err != nil {
		t.Fatal(err)
	}
	if err := repo.AdvanceIDSweepCursor(ctx, 1, 51); err != nil {
		t.Fatal("stale advancement to an already-covered range should be idempotent:", err)
	}
	got, err := repo.IDSweepCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 101 {
		t.Fatalf("cursor rewound to %d, want 101", got)
	}
}

func TestCharacterRepository_RewindIDSweepCursor(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	if _, err := repo.IDSweepCursor(ctx); err != nil {
		t.Fatal(err)
	}
	if err := repo.AdvanceIDSweepCursor(ctx, 1, 501); err != nil {
		t.Fatal(err)
	}
	if err := repo.RewindIDSweepCursor(ctx, 1, 10); err != nil {
		t.Fatal("stale rewind should be a no-op:", err)
	}
	if got, _ := repo.IDSweepCursor(ctx); got != 501 {
		t.Fatalf("stale rewind moved cursor to %d, want 501", got)
	}
	if err := repo.RewindIDSweepCursor(ctx, 501, 10); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.IDSweepCursor(ctx); got != 10 {
		t.Fatalf("cursor = %d, want 10", got)
	}
}

func TestCharacterRepository_ListAndCount(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := uint32(1); i <= 5; i++ {
		_ = repo.Upsert(ctx, contract.CharacterRecord{
			ID:          i,
			Name:        "Warrior",
			World:       "Ragnarok",
			Datacenter:  "Chaos",
			Region:      "EU",
			FirstSeenAt: now,
		}, nil)
	}

	count, err := repo.Count(ctx, contract.CharacterFilter{World: "Ragnarok"})
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 5 {
		t.Fatalf("expected count=5, got %d", count)
	}

	list, err := repo.List(ctx, contract.CharacterFilter{World: "Ragnarok"}, 3, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 items, got %d", len(list))
	}
}

func TestCharacterRepository_Count_SinceFilter(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	recent := now.Add(-time.Hour)
	old := now.Add(-60 * 24 * time.Hour)

	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 1, Name: "Active", World: "Balmung", FirstSeenAt: now,
	}, nil)
	_ = setLatestAchievement(ctx, repo, 1, &recent)

	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 2, Name: "Inactive", World: "Balmung", FirstSeenAt: now,
	}, nil)
	_ = setLatestAchievement(ctx, repo, 2, &old)

	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 3, Name: "Never", World: "Balmung", FirstSeenAt: now,
	}, nil)

	since := now.Add(-24 * time.Hour)
	count, err := repo.Count(ctx, contract.CharacterFilter{World: "Balmung", Since: &since})
	if err != nil {
		t.Fatalf("Count with Since: %v", err)
	}
	if count != 1 {
		t.Errorf("Count with Since = %d, want 1 (only recent)", count)
	}

	// Without Since, all non-deleted are counted
	count, err = repo.Count(ctx, contract.CharacterFilter{World: "Balmung"})
	if err != nil {
		t.Fatalf("Count without Since: %v", err)
	}
	if count != 3 {
		t.Errorf("Count without Since = %d, want 3", count)
	}
}

func TestCharacterRepository_Count_MinLevelFilter(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)

	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 1, Name: "Max", World: "Balmung", FirstSeenAt: now,
	}, []contract.ClassJobRecord{
		{CharacterID: 1, ClassJobID: 19, Name: "Paladin", Level: 100},
		{CharacterID: 1, ClassJobID: 21, Name: "Warrior", Level: 90},
	})

	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 2, Name: "Mid", World: "Balmung", FirstSeenAt: now,
	}, []contract.ClassJobRecord{
		{CharacterID: 2, ClassJobID: 19, Name: "Paladin", Level: 80},
	})

	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 3, Name: "Low", World: "Balmung", FirstSeenAt: now,
	}, []contract.ClassJobRecord{
		{CharacterID: 3, ClassJobID: 19, Name: "Paladin", Level: 50},
	})

	count, err := repo.Count(ctx, contract.CharacterFilter{MinLevel: 100})
	if err != nil {
		t.Fatalf("Count MinLevel=100: %v", err)
	}
	if count != 1 {
		t.Errorf("Count MinLevel=100 = %d, want 1", count)
	}

	count, err = repo.Count(ctx, contract.CharacterFilter{MinLevel: 80})
	if err != nil {
		t.Fatalf("Count MinLevel=80: %v", err)
	}
	if count != 2 {
		t.Errorf("Count MinLevel=80 = %d, want 2", count)
	}

	count, err = repo.Count(ctx, contract.CharacterFilter{MinLevel: 50})
	if err != nil {
		t.Fatalf("Count MinLevel=50: %v", err)
	}
	if count != 3 {
		t.Errorf("Count MinLevel=50 = %d, want 3", count)
	}
}

func TestCharacterRepository_ListStaleIDs(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2025, 4, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC)

	// id=1: NULL last_census_at (stale in both modes)
	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 1, Name: "NullCensus", World: "Balmung", FirstSeenAt: old,
	}, nil)
	// id=2: old last_census_at (stale in both modes)
	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 2, Name: "OldCensus", World: "Balmung", FirstSeenAt: old, LastCensusAt: &old,
	}, nil)
	// id=3: recent last_census_at (stale only in zero-cutoff mode)
	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 3, Name: "RecentCensus", World: "Balmung", FirstSeenAt: old, LastCensusAt: &recent,
	}, nil)
	// id=4: the oldest census of all, but deleted: never selected.
	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 4, Name: "Gone", World: "Balmung", FirstSeenAt: old, LastCensusAt: &old,
	}, nil)
	if err := repo.MarkDeleted(ctx, 4, mid); err != nil {
		t.Fatal(err)
	}
	// id=5: deleted with a NULL census: never selected either.
	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 5, Name: "GoneNull", World: "Balmung", FirstSeenAt: old,
	}, nil)
	if err := repo.MarkDeleted(ctx, 5, mid); err != nil {
		t.Fatal(err)
	}

	// Zero cutoff, limit 2: id=1 (NULL first) then id=2 (oldest timestamp).
	got, err := repo.ListStaleIDs(ctx, time.Time{}, 2)
	if err != nil {
		t.Fatalf("ListStaleIDs zero cutoff: %v", err)
	}
	if !slices.Equal(got, []uint32{1, 2}) {
		t.Errorf("zero cutoff limit 2 = %v, want [1 2]", got)
	}

	// Positive cutoff at recent: excludes id=3 (recent.Before(recent) is false).
	got, err = repo.ListStaleIDs(ctx, recent, 10)
	if err != nil {
		t.Fatalf("ListStaleIDs positive cutoff: %v", err)
	}
	if !slices.Equal(got, []uint32{1, 2}) {
		t.Errorf("positive cutoff = %v, want [1 2]", got)
	}

	// Zero cutoff, limit 10: every live character, deleted ones excluded.
	got, err = repo.ListStaleIDs(ctx, time.Time{}, 10)
	if err != nil {
		t.Fatalf("ListStaleIDs zero cutoff all: %v", err)
	}
	if !slices.Equal(got, []uint32{1, 2, 3}) {
		t.Errorf("zero cutoff all = %v, want [1 2 3]", got)
	}
}

func TestCharacterRepository_ListDeletedIDs(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	seen := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	deleteAt := map[uint32]time.Time{
		10: cutoff.Add(-48 * time.Hour),  // second oldest
		11: cutoff.Add(-24 * time.Hour),  // third oldest
		12: cutoff.Add(-72 * time.Hour),  // oldest
		13: cutoff,                       // deleted exactly at the cutoff: not yet due
		14: cutoff.Add(24 * time.Hour),   // deleted after the cutoff: not yet due
		15: cutoff.Add(-100 * time.Hour), // resurrected below: no longer deleted
	}
	for id := uint32(10); id <= 16; id++ {
		_ = repo.Upsert(ctx, contract.CharacterRecord{
			ID: id, Name: "C", World: "Balmung", FirstSeenAt: seen, LastCensusAt: &seen,
		}, nil)
		if at, ok := deleteAt[id]; ok {
			if err := repo.MarkDeleted(ctx, id, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	// id=15 comes back: Upsert clears deleted_at. id=16 was never deleted.
	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 15, Name: "Back", World: "Balmung", FirstSeenAt: seen, LastCensusAt: &seen,
	}, nil)

	got, err := repo.ListDeletedIDs(ctx, cutoff, 10)
	if err != nil {
		t.Fatalf("ListDeletedIDs: %v", err)
	}
	if !slices.Equal(got, []uint32{12, 10, 11}) {
		t.Errorf("due deleted = %v, want [12 10 11] (longest-deleted first)", got)
	}

	got, err = repo.ListDeletedIDs(ctx, cutoff, 2)
	if err != nil {
		t.Fatalf("ListDeletedIDs limit: %v", err)
	}
	if !slices.Equal(got, []uint32{12, 10}) {
		t.Errorf("bounded batch = %v, want [12 10]", got)
	}

	got, err = repo.ListDeletedIDs(ctx, cutoff, 0)
	if err != nil {
		t.Fatalf("ListDeletedIDs zero limit: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("zero limit = %v, want none", got)
	}
}

// TestCharacterRepository_PublisherSelectionsUseIndexes pins that neither
// publisher selection sorts the table: each is served in order from a partial
// index. Sequential and bitmap scans are disabled for the session so the check
// asks whether the index can serve the order, not how the planner prices a
// small seeded table.
func TestCharacterRepository_PublisherSelectionsUseIndexes(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()

	db, err := driver.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Enough rows (a tenth deleted, some never censused) for the planner to
	// have statistics to work with.
	mustExec(t, driver, `
		INSERT INTO characters (id, first_seen_at, last_census_at, deleted_at)
		SELECT g, now(),
		       CASE WHEN g % 7 = 0 THEN NULL ELSE now() - make_interval(secs => g) END,
		       CASE WHEN g % 10 = 0 THEN now() - make_interval(secs => g) END
		FROM generate_series(1, 5000) g`)
	mustExec(t, driver, `ANALYZE characters`)
	for _, setting := range []string{`SET enable_seqscan = off`, `SET enable_bitmapscan = off`} {
		if _, err := conn.ExecContext(ctx, setting); err != nil {
			t.Fatal(err)
		}
	}

	explain := func(query string, args ...any) string {
		rows, err := conn.QueryContext(ctx, `EXPLAIN `+query, args...)
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		defer rows.Close()
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(line + "\n")
		}
		return plan.String()
	}

	cases := []struct {
		name  string
		index string
		query string
		args  []any
	}{
		{
			name:  "live characters, no cutoff",
			index: "idx_characters_live_census",
			query: `SELECT id FROM characters WHERE deleted_at IS NULL
			         ORDER BY last_census_at ASC NULLS FIRST, id ASC LIMIT $1`,
			args: []any{300},
		},
		{
			name:  "live characters, with cutoff",
			index: "idx_characters_live_census",
			query: `SELECT id FROM characters WHERE deleted_at IS NULL
			           AND (last_census_at < $1 OR last_census_at IS NULL)
			         ORDER BY last_census_at ASC NULLS FIRST, id ASC LIMIT $2`,
			args: []any{time.Now(), 300},
		},
		{
			name:  "deleted characters re-probe",
			index: "idx_characters_deleted_at",
			query: `SELECT id FROM characters WHERE deleted_at IS NOT NULL AND deleted_at < $1
			         ORDER BY deleted_at ASC, id ASC LIMIT $2`,
			args: []any{time.Now(), 10},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := explain(tc.query, tc.args...)
			if !strings.Contains(plan, tc.index) {
				t.Errorf("plan does not use %s:\n%s", tc.index, plan)
			}
			if strings.Contains(plan, "Sort") {
				t.Errorf("plan sorts instead of reading index order:\n%s", plan)
			}
		})
	}
}

func TestCharacterRepository_FillGapsCursorInitializesAndAdvances(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	got, err := repo.FillGapsCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("initial fill-gaps cursor = %d, want 0", got)
	}

	if err := repo.AdvanceFillGapsCursor(ctx, 0, 1723); err != nil {
		t.Fatal(err)
	}
	got, err = repo.FillGapsCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1723 {
		t.Fatalf("advanced fill-gaps cursor = %d, want 1723", got)
	}
}

func TestCharacterRepository_FillGapsCursorCAS(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	if err := repo.AdvanceFillGapsCursor(ctx, 0, 500); err != nil {
		t.Fatal(err)
	}

	// Stale advance to an already-covered range is idempotent, never rewinds.
	if err := repo.AdvanceFillGapsCursor(ctx, 0, 300); err != nil {
		t.Fatal("stale advance to an already-covered range should be idempotent:", err)
	}
	got, err := repo.FillGapsCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 500 {
		t.Fatalf("cursor rewound to %d, want 500", got)
	}

	// Stale advance to an uncovered range must fail.
	if err := repo.AdvanceFillGapsCursor(ctx, 0, 900); err == nil {
		t.Fatal("stale advance to an uncovered range should fail")
	}

	// Reset-to-zero (wrap) is a legitimate CAS move when expected matches.
	if err := repo.AdvanceFillGapsCursor(ctx, 500, 0); err != nil {
		t.Fatal(err)
	}
	got, err = repo.FillGapsCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("wrapped cursor = %d, want 0", got)
	}
}

func TestCharacterRepository_FindIDGapsAfter(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, id := range []uint32{3, 4, 8, 15} {
		if err := repo.Upsert(ctx, contract.CharacterRecord{ID: id, Name: "C", World: "W", Race: "Hyur", FirstSeenAt: now}, nil); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("full scan includes region below the lowest character", func(t *testing.T) {
		gaps, err := repo.FindIDGapsAfter(ctx, 0, 15, 10)
		if err != nil {
			t.Fatal(err)
		}
		want := [][2]uint32{{1, 2}, {5, 7}, {9, 14}}
		if len(gaps) != len(want) {
			t.Fatalf("gaps = %v, want %v", gaps, want)
		}
		for i := range want {
			if gaps[i] != want[i] {
				t.Fatalf("gaps = %v, want %v", gaps, want)
			}
		}
	})

	t.Run("straddling gap is trimmed to after the cursor", func(t *testing.T) {
		gaps, err := repo.FindIDGapsAfter(ctx, 6, 15, 10)
		if err != nil {
			t.Fatal(err)
		}
		want := [][2]uint32{{7, 7}, {9, 14}}
		if len(gaps) != len(want) {
			t.Fatalf("gaps = %v, want %v", gaps, want)
		}
		for i := range want {
			if gaps[i] != want[i] {
				t.Fatalf("gaps = %v, want %v", gaps, want)
			}
		}
	})

	t.Run("cursor at maxID yields nothing", func(t *testing.T) {
		gaps, err := repo.FindIDGapsAfter(ctx, 15, 15, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(gaps) != 0 {
			t.Fatalf("gaps = %v, want none", gaps)
		}
	})

	t.Run("limit respected", func(t *testing.T) {
		gaps, err := repo.FindIDGapsAfter(ctx, 0, 15, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(gaps) != 2 {
			t.Fatalf("gaps = %v, want 2", gaps)
		}
	})

	t.Run("empty table yields nothing", func(t *testing.T) {
		emptyRepo := repository.NewCharacterRepository(newTestDriver(t))
		gaps, err := emptyRepo.FindIDGapsAfter(context.Background(), 0, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(gaps) != 0 {
			t.Fatalf("gaps = %v, want none", gaps)
		}
	})
}
