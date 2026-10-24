package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func seedGrandCompanyCharacter(t *testing.T, driver contract.DatabaseDriver, id int, world, dc, region, race, gc string, firstSeen time.Time, latest *time.Time) {
	t.Helper()
	chars := repository.NewCharacterRepository(driver)
	rec := contract.CharacterRecord{ID: uint32(id), Name: "GC" + world, World: world, Datacenter: dc, Region: region, Race: race, Tribe: "T" + race, Gender: 1, GrandCompany: gc, FirstSeenAt: firstSeen}
	if err := chars.Upsert(context.Background(), rec, nil); err != nil {
		t.Fatal(err)
	}
	if err := setLatestAchievement(context.Background(), chars, rec.ID, latest); err != nil {
		t.Fatal(err)
	}
}

func groupCounts(snapshot *contract.UIStatsSnapshot, dimension string, scope contract.StatsScope) map[string][2]int64 {
	out := map[string][2]int64{}
	for _, g := range census.SnapshotGroups(snapshot, dimension, scope) {
		out[g.Key] = [2]int64{g.Total, g.Active}
	}
	return out
}

func assertCounts(t *testing.T, label string, got, want map[string][2]int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for key, w := range want {
		if got[key] != w {
			t.Fatalf("%s[%q] = %v, want %v (all: %v)", label, key, got[key], w, got)
		}
	}
}

func TestUIStatsFullRefreshGrandCompanyGroups(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-90 * 24 * time.Hour)
	recent := now.Add(-time.Hour)

	seedGrandCompanyCharacter(t, driver, 1, "Balmung", "Crystal", "NA", "Roegadyn", "Maelstrom", old, &recent)
	seedGrandCompanyCharacter(t, driver, 2, "Balmung", "Crystal", "NA", "Roegadyn", "Maelstrom", old, nil)
	seedGrandCompanyCharacter(t, driver, 3, "Balmung", "Crystal", "NA", "Elezen", "Order of the Twin Adder", old, &recent)
	seedGrandCompanyCharacter(t, driver, 4, "Ragnarok", "Chaos", "EU", "Hyur", "Immortal Flames", old, nil)
	seedGrandCompanyCharacter(t, driver, 5, "Ragnarok", "Chaos", "EU", "Hyur", "", old, &recent)

	result, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, now.Add(-30*24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := stats.LoadCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.SchemaVersion != contract.UIStatsSchemaVersion || loaded.SchemaVersion != contract.UIStatsSchemaVersion {
		t.Fatalf("schema version refreshed=%d loaded=%d, want %d", result.Snapshot.SchemaVersion, loaded.SchemaVersion, contract.UIStatsSchemaVersion)
	}

	assertCounts(t, "global grand companies", groupCounts(loaded, "grand_company", contract.StatsScope{}), map[string][2]int64{
		"Maelstrom": {2, 1}, "Order of the Twin Adder": {1, 1}, "Immortal Flames": {1, 0}, "None": {1, 1},
	})
	assertCounts(t, "EU grand companies", groupCounts(loaded, "grand_company", contract.StatsScope{Region: "EU"}), map[string][2]int64{
		"Immortal Flames": {1, 0}, "None": {1, 1},
	})
	assertCounts(t, "Crystal grand companies", groupCounts(loaded, "grand_company", contract.StatsScope{Datacenter: "Crystal"}), map[string][2]int64{
		"Maelstrom": {2, 1}, "Order of the Twin Adder": {1, 1},
	})
	assertCounts(t, "Balmung grand companies", groupCounts(loaded, "grand_company", contract.StatsScope{World: "Balmung"}), map[string][2]int64{
		"Maelstrom": {2, 1}, "Order of the Twin Adder": {1, 1},
	})
	assertCounts(t, "global grand company by race", groupCounts(loaded, "grand_company_race", contract.StatsScope{}), map[string][2]int64{
		"Maelstrom|Roegadyn": {2, 1}, "Order of the Twin Adder|Elezen": {1, 1}, "Immortal Flames|Hyur": {1, 0}, "None|Hyur": {1, 1},
	})
	if cross := groupCounts(loaded, "grand_company_race", contract.StatsScope{World: "Balmung"}); len(cross) != 0 {
		t.Fatalf("grand company by race must be global only, got world groups %v", cross)
	}
}

func TestUIStatsIncrementalMovesGrandCompaniesByDelta(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-90 * 24 * time.Hour)
	recent := now.Add(-time.Hour)
	since := now.Add(-30 * 24 * time.Hour)

	seedGrandCompanyCharacter(t, driver, 1, "Balmung", "Crystal", "NA", "Roegadyn", "Maelstrom", old, &recent)
	seedGrandCompanyCharacter(t, driver, 2, "Balmung", "Crystal", "NA", "Elezen", "Order of the Twin Adder", old, nil)
	if _, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since)); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)
	after := time.Now().UTC()
	seedGrandCompanyCharacter(t, driver, 3, "Balmung", "Crystal", "NA", "Hyur", "Immortal Flames", after, &after)
	seedGrandCompanyCharacter(t, driver, 4, "Ragnarok", "Chaos", "EU", "Hyur", "", after, nil)
	mustExec(t, driver, `UPDATE characters SET deleted_at = NOW() WHERE id = 2`)

	inc, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if inc.Mode != contract.UIStatsRefreshIncremental {
		t.Fatalf("mode = %q, want incremental", inc.Mode)
	}
	assertCounts(t, "global grand companies", groupCounts(inc.Snapshot, "grand_company", contract.StatsScope{}), map[string][2]int64{
		"Maelstrom": {1, 1}, "Immortal Flames": {1, 1}, "None": {1, 0},
	})
	assertCounts(t, "Balmung grand companies", groupCounts(inc.Snapshot, "grand_company", contract.StatsScope{World: "Balmung"}), map[string][2]int64{
		"Maelstrom": {1, 1}, "Immortal Flames": {1, 1},
	})
	assertCounts(t, "global grand company by race", groupCounts(inc.Snapshot, "grand_company_race", contract.StatsScope{}), map[string][2]int64{
		"Maelstrom|Roegadyn": {1, 1}, "Immortal Flames|Hyur": {1, 1}, "None|Hyur": {1, 0},
	})
}

func TestUIStatsIncrementalAfterV2SnapshotRebuildsFully(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-30 * 24 * time.Hour)
	seedGrandCompanyCharacter(t, driver, 1, "Balmung", "Crystal", "NA", "Roegadyn", "Maelstrom", now.Add(-72*time.Hour), nil)
	if _, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since)); err != nil {
		t.Fatal(err)
	}
	// The snapshot stored before the upgrade: v2, with no Grand Company groups.
	mustExec(t, driver, `UPDATE ui_stats_snapshots SET schema_version = 2,
		payload = jsonb_set(payload, '{groups}', (SELECT COALESCE(jsonb_agg(g), '[]'::jsonb) FROM jsonb_array_elements(payload->'groups') g
			WHERE g->>'dimension' NOT LIKE 'grand_company%'))`)
	time.Sleep(20 * time.Millisecond)

	r, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != contract.UIStatsRefreshFull || r.Snapshot.SchemaVersion != contract.UIStatsSchemaVersion {
		t.Fatalf("mode=%q schema=%d, want a full rebuild at the current schema", r.Mode, r.Snapshot.SchemaVersion)
	}
	assertCounts(t, "global grand companies", groupCounts(r.Snapshot, "grand_company", contract.StatsScope{}), map[string][2]int64{
		"Maelstrom": {1, 0},
	})
}
