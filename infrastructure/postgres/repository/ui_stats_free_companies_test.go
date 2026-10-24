package repository_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// characterIndexDefs returns indexname -> indexdef for the characters table.
func characterIndexDefs(t *testing.T, driver contract.DatabaseDriver) map[string]string {
	t.Helper()
	rows, err := driver.FetchMany(context.Background(),
		`SELECT indexname, indexdef FROM pg_indexes WHERE tablename = 'characters'`)
	if err != nil {
		t.Fatalf("query character indexes: %v", err)
	}
	defer rows.Close()
	defs := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatalf("scan index row: %v", err)
		}
		defs[name] = def
	}
	return defs
}

func TestFreeCompanyIndexMigration_ReplacesTheFullIndexWithASlimPartialOne(t *testing.T) {
	driver := newTestDriver(t)
	defs := characterIndexDefs(t, driver)
	def, ok := defs["idx_characters_fc_live"]
	if !ok {
		t.Fatalf("idx_characters_fc_live missing; indexes: %v", defs)
	}
	for _, part := range []string{
		"(fc_id)",
		"INCLUDE (world, latest_achievement_at)",
		"WHERE ((deleted_at IS NULL) AND (fc_id IS NOT NULL))",
	} {
		if !strings.Contains(def, part) {
			t.Errorf("index definition lacks %q:\n%s", part, def)
		}
	}
	if _, ok := defs["idx_characters_fc"]; ok {
		t.Error("the full Free Company index idx_characters_fc is still present")
	}
}

// fcMember is one live character seeded straight into characters.
type fcMember struct {
	id                  int
	world, fcID, fcName string
	latestAchievementAt *time.Time
	lastCensusAt        time.Time
	deleted             bool
}

func seedFCMembers(t *testing.T, driver contract.DatabaseDriver, members ...fcMember) {
	t.Helper()
	for _, m := range members {
		var fcID, fcName any
		if m.fcID != "" {
			fcID, fcName = m.fcID, m.fcName
		}
		var deleted any
		if m.deleted {
			deleted = time.Now().UTC()
		}
		last := m.lastCensusAt
		if last.IsZero() {
			last = time.Now().UTC().Add(-time.Hour)
		}
		mustExec(t, driver, `INSERT INTO characters (id, name, world, datacenter, region, race, tribe, gender,
			fc_id, fc_name, latest_achievement_at, first_seen_at, last_census_at, deleted_at)
			VALUES ($1, 'C', $2, $3, $4, 'Hyur', 'Midlander', 1, $5, $6, $7, NOW() - INTERVAL '90 days', $8, $9)`,
			m.id, m.world, census.WorldDatacenter(m.world), census.RegionForDatacenter(census.WorldDatacenter(m.world)),
			fcID, fcName, m.latestAchievementAt, last, deleted)
	}
}

// companiesOf returns a world's stored Free Companies by id.
func companiesOf(t *testing.T, boards *contract.StatsFreeCompanies, world string) map[string]contract.FreeCompanyCount {
	t.Helper()
	out := map[string]contract.FreeCompanyCount{}
	for _, w := range boards.Worlds {
		if w.World != world {
			continue
		}
		for _, fc := range w.Companies {
			if _, dup := out[fc.ID]; dup {
				t.Fatalf("world %s stores Free Company %s twice", world, fc.ID)
			}
			out[fc.ID] = fc
		}
	}
	return out
}

func TestUIStatsFullRefreshFreeCompanyBoards(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-30 * 24 * time.Hour)
	recent, old := now.Add(-24*time.Hour), now.Add(-60*24*time.Hour)

	seedFCMembers(t, driver,
		// Alpha: three live members, one active; a deleted member does not count.
		// Its name comes from the most recently censused member (a rename).
		fcMember{id: 1, world: "Balmung", fcID: "100", fcName: "Old Alpha", latestAchievementAt: &recent, lastCensusAt: now.Add(-72 * time.Hour)},
		fcMember{id: 2, world: "Balmung", fcID: "100", fcName: "Alpha", latestAchievementAt: &old, lastCensusAt: now.Add(-time.Hour)},
		fcMember{id: 3, world: "Balmung", fcID: "100", fcName: "Old Alpha", lastCensusAt: now.Add(-48 * time.Hour)},
		fcMember{id: 4, world: "Balmung", fcID: "100", fcName: "Alpha", latestAchievementAt: &recent, deleted: true},
		// Beta: two members, both active.
		fcMember{id: 5, world: "Balmung", fcID: "200", fcName: "Beta", latestAchievementAt: &recent},
		fcMember{id: 6, world: "Balmung", fcID: "200", fcName: "Beta", latestAchievementAt: &recent},
		// Gamma has one member seen: under the 2-member minimum.
		fcMember{id: 7, world: "Balmung", fcID: "300", fcName: "Gamma", latestAchievementAt: &recent},
		// No Free Company.
		fcMember{id: 8, world: "Balmung"},
		fcMember{id: 9, world: "Ragnarok", fcID: "400", fcName: "Delta"},
		fcMember{id: 10, world: "Ragnarok", fcID: "400", fcName: "Delta"},
	)

	result, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since))
	if err != nil {
		t.Fatal(err)
	}
	boards, err := stats.LoadFreeCompanies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !boards.BuiltAt.Equal(result.Snapshot.GeneratedAt) {
		t.Errorf("Free Company boards built at %v, want the refresh time %v", boards.BuiltAt, result.Snapshot.GeneratedAt)
	}
	if !result.Snapshot.FreeCompaniesBuiltAt.Equal(result.Snapshot.GeneratedAt) {
		t.Errorf("snapshot FreeCompaniesBuiltAt = %v, want %v", result.Snapshot.FreeCompaniesBuiltAt, result.Snapshot.GeneratedAt)
	}

	balmung := companiesOf(t, boards, "Balmung")
	want := map[string]contract.FreeCompanyCount{
		"100": {ID: "100", Name: "Alpha", Members: 3, Active: 1},
		"200": {ID: "200", Name: "Beta", Members: 2, Active: 2},
	}
	if !reflect.DeepEqual(balmung, want) {
		t.Errorf("Balmung Free Companies = %+v, want %+v", balmung, want)
	}
	ragnarok := companiesOf(t, boards, "Ragnarok")
	if !reflect.DeepEqual(ragnarok, map[string]contract.FreeCompanyCount{"400": {ID: "400", Name: "Delta", Members: 2}}) {
		t.Errorf("Ragnarok Free Companies = %+v", ragnarok)
	}

	// Characters in a Free Company per location: every live member, whatever its company's size.
	loaded, err := stats.LoadCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inFC := map[string]int64{}
	for _, g := range loaded.Groups {
		if g.Scope == (contract.StatsScope{}) && (g.Dimension == "world" || g.Dimension == "region") {
			inFC[g.Dimension+":"+g.Key] = g.InFreeCompany
		}
	}
	if inFC["world:Balmung"] != 6 || inFC["world:Ragnarok"] != 2 || inFC["region:NA"] != 6 {
		t.Errorf("in a Free Company = %v, want Balmung 6, Ragnarok 2, NA 6", inFC)
	}
	if loaded.Summary.InFreeCompany != 8 {
		t.Errorf("summary in a Free Company = %d, want 8", loaded.Summary.InFreeCompany)
	}
}

func TestUIStatsFreeCompanyBoardsKeepTheTop100PerWorldForEachMeasure(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-30 * 24 * time.Hour)
	recent := now.Add(-24 * time.Hour)

	// 150 companies of 3 inactive members (ids 1001-1150) fill the members
	// board; ties on both counts keep the lowest ids. 120 companies of 2
	// members, one active (ids 2001-2120), lead the active board.
	mustExec(t, driver, `INSERT INTO characters (id, world, datacenter, region, fc_id, fc_name, first_seen_at)
		SELECT 100000 + g * 3 + m, 'Balmung', 'Crystal', 'NA', (1000 + g)::text, 'Big ' || g, NOW() - INTERVAL '90 days'
		FROM generate_series(1, 150) g, generate_series(0, 2) m`)
	mustExec(t, driver, `INSERT INTO characters (id, world, datacenter, region, fc_id, fc_name, first_seen_at, latest_achievement_at)
		SELECT 200000 + g * 2 + m, 'Balmung', 'Crystal', 'NA', (2000 + g)::text, 'Lively ' || g, NOW() - INTERVAL '90 days',
		       CASE WHEN m = 0 THEN $1::timestamptz END
		FROM generate_series(1, 120) g, generate_series(0, 1) m`, recent)

	if _, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since)); err != nil {
		t.Fatal(err)
	}
	boards, err := stats.LoadFreeCompanies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	balmung := companiesOf(t, boards, "Balmung")
	if len(balmung) != 200 {
		t.Fatalf("Balmung stores %d Free Companies, want 200 (the top 100 of each board)", len(balmung))
	}
	for id := 1001; id <= 1150; id++ {
		_, kept := balmung[strconv.Itoa(id)]
		if kept != (id <= 1100) {
			t.Errorf("members board: company %d kept = %v", id, kept)
		}
	}
	for id := 2001; id <= 2120; id++ {
		_, kept := balmung[strconv.Itoa(id)]
		if kept != (id <= 2100) {
			t.Errorf("active board: company %d kept = %v", id, kept)
		}
	}
}

func TestUIStatsFreeCompanyBoardsAreLeftToTheFullRefresh(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-30 * 24 * time.Hour)

	if _, err := stats.LoadFreeCompanies(ctx); !errors.Is(err, census.ErrUIStatsUnavailable) {
		t.Fatalf("LoadFreeCompanies() before any refresh error = %v, want ErrUIStatsUnavailable", err)
	}
	seedFCMembers(t, driver,
		fcMember{id: 1, world: "Balmung", fcID: "100", fcName: "Alpha"},
		fcMember{id: 2, world: "Balmung", fcID: "100", fcName: "Alpha"},
	)
	full, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since))
	if err != nil {
		t.Fatal(err)
	}

	// A new member joins: the boards and the membership counts wait for the nightly rebuild.
	time.Sleep(50 * time.Millisecond)
	mustExec(t, driver, `INSERT INTO characters (id, world, datacenter, region, fc_id, fc_name, first_seen_at)
		VALUES (3, 'Balmung', 'Crystal', 'NA', '100', 'Alpha', NOW())`)
	inc, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if inc.Mode != contract.UIStatsRefreshIncremental {
		t.Fatalf("refresh mode = %s, want incremental", inc.Mode)
	}
	if !inc.Snapshot.FreeCompaniesBuiltAt.Equal(full.Snapshot.GeneratedAt) {
		t.Errorf("incremental FreeCompaniesBuiltAt = %v, want the full refresh time %v", inc.Snapshot.FreeCompaniesBuiltAt, full.Snapshot.GeneratedAt)
	}
	if inc.Snapshot.Summary.InFreeCompany != 2 {
		t.Errorf("incremental in a Free Company = %d, want the nightly 2", inc.Snapshot.Summary.InFreeCompany)
	}
	boards, err := stats.LoadFreeCompanies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !boards.BuiltAt.Equal(full.Snapshot.GeneratedAt) || companiesOf(t, boards, "Balmung")["100"].Members != 2 {
		t.Errorf("boards after an incremental refresh = %+v, want the full refresh's untouched", boards)
	}
}

func TestUIStatsIncrementalRebuildsWhenThePreviousSnapshotHasNoFreeCompanyBoards(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	since := time.Now().UTC().Add(-30 * 24 * time.Hour)
	seedFCMembers(t, driver, fcMember{id: 1, world: "Balmung"})
	if _, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since)); err != nil {
		t.Fatal(err)
	}
	// A snapshot written before the Free Company boards existed.
	mustExec(t, driver, `UPDATE ui_stats_snapshots SET payload = payload - 'free_companies_built_at' WHERE snapshot_key = 'current'`)
	mustExec(t, driver, `DELETE FROM ui_stats_snapshots WHERE snapshot_key = 'free_companies'`)

	result, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != contract.UIStatsRefreshFull {
		t.Errorf("refresh mode = %s, want a full rebuild", result.Mode)
	}
	if _, err := stats.LoadFreeCompanies(ctx); err != nil {
		t.Errorf("LoadFreeCompanies() after the rebuild: %v", err)
	}
}

func TestFreeCompanySnapshotKeyMigration_AcceptsTheFreeCompanyRow(t *testing.T) {
	driver := newTestDriver(t)
	mustExec(t, driver, `INSERT INTO ui_stats_snapshots (snapshot_key, schema_version, generated_at, activity_since, max_level,
		source_character_count, refresh_duration_ms, payload) VALUES ('free_companies', 3, NOW(), NOW(), 100, 0, 0, '{}')`)
}
