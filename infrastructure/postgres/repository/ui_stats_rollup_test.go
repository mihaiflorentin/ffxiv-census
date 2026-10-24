package repository_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite the UI statistics golden files")

// seedRollupFixture stores a spread of characters across regions,
// datacenters, worlds, races, clans and genders, including an unknown gender,
// a character without race or clan, and a soft-deleted character.
func seedRollupFixture(t *testing.T, driver contract.DatabaseDriver, firstSeen, active, inactive time.Time) {
	t.Helper()
	chars := repository.NewCharacterRepository(driver)
	type seed struct {
		id                             uint32
		world, dc, region, race, tribe string
		gender                         uint8
		latest                         *time.Time
		level                          uint8
	}
	seeds := []seed{
		{1, "Balmung", "Crystal", "NA", "Hyur", "Midlander", 1, &active, 100},
		{2, "Balmung", "Crystal", "NA", "Hyur", "Highlander", 2, &inactive, 90},
		{3, "Balmung", "Crystal", "NA", "Lalafell", "Plainsfolk", 2, &active, 0},
		{4, "Coeurl", "Crystal", "NA", "Miqo'te", "Seeker of the Sun", 2, nil, 100},
		{5, "Ragnarok", "Chaos", "EU", "Hyur", "Midlander", 1, &active, 100},
		{6, "Ragnarok", "Chaos", "EU", "Roegadyn", "Hellsguard", 0, &inactive, 50},
		{7, "Lich", "Light", "EU", "Elezen", "Duskwight", 1, nil, 0},
		{8, "Tonberry", "Elemental", "JP", "", "", 2, &active, 100},
		{9, "Tonberry", "Elemental", "JP", "Viera", "Rava", 2, &active, 100},
	}
	for _, s := range seeds {
		rec := contract.CharacterRecord{ID: s.id, Name: "R", World: s.world, Datacenter: s.dc, Region: s.region,
			Race: s.race, Tribe: s.tribe, Gender: s.gender, FirstSeenAt: firstSeen}
		var jobs []contract.ClassJobRecord
		if s.level > 0 {
			jobs = []contract.ClassJobRecord{{CharacterID: s.id, ClassJobID: 19, Name: "Paladin", Level: s.level}}
		}
		if err := chars.Upsert(context.Background(), rec, jobs); err != nil {
			t.Fatal(err)
		}
		if err := setLatestAchievement(context.Background(), chars, s.id, s.latest); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, driver, `UPDATE characters SET deleted_at = $1 WHERE id = 9`, firstSeen)
}

func TestUIStatsFullRefreshMatchesGolden(t *testing.T) {
	driver := newTestDriver(t)
	now := time.Now().UTC()
	seedRollupFixture(t, driver, now.Add(-90*24*time.Hour), now.Add(-time.Hour), now.Add(-60*24*time.Hour))

	result, err := repository.NewUIStatsRepository(driver).Refresh(context.Background(),
		incrementalOpts(contract.UIStatsRefreshFull, now.Add(-30*24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	got := comparableSnapshot(result.Snapshot)
	got.ActivitySince = time.Time{}
	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join("testdata", "ui_stats_full_golden.json")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(encoded)+"\n" {
		t.Fatalf("full refresh snapshot differs from %s\ngot:\n%s", path, encoded)
	}
}

func TestUIStatsRefreshToleratesCharactersWithoutLocation(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-30 * 24 * time.Hour)
	seedStatsCharacter(t, driver, 1, "Balmung", "Crystal", "NA", "Hyur", 1, now.Add(-72*time.Hour), &now, 100)
	seedStatsCharacter(t, driver, 2, "", "", "", "Hyur", 2, now.Add(-72*time.Hour), &now, 100)

	full, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since))
	if err != nil {
		t.Fatalf("full refresh with an unlocated character: %v", err)
	}
	if full.Snapshot.Summary.Total != 2 {
		t.Fatalf("summary total = %d, want 2", full.Snapshot.Summary.Total)
	}
	var globalHyur int64
	for _, g := range full.Snapshot.Groups {
		if g.Scope == (contract.StatsScope{}) && g.Dimension == "race" && g.Key == "Hyur" {
			globalHyur = g.Total
		}
	}
	if globalHyur != 2 {
		t.Fatalf("global Hyur total = %d, want 2", globalHyur)
	}

	time.Sleep(20 * time.Millisecond)
	seedStatsCharacter(t, driver, 3, "", "", "", "Hyur", 1, time.Now().UTC(), nil, 0)
	inc, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if inc.Mode != contract.UIStatsRefreshIncremental || inc.Snapshot.Summary.Total != 3 {
		t.Fatalf("incremental mode=%q total=%d, want incremental with total 3", inc.Mode, inc.Snapshot.Summary.Total)
	}
}

// v2Equivalent is snapshot without the parts schemas 3 and 4 added: the job
// section, the Grand Company groups, the location counts (at cap, private,
// unchecked, in a Free Company) on groups and the summary, and the
// new-characters window with its per-world totals and weekly series.
func v2Equivalent(snapshot *contract.UIStatsSnapshot) *contract.UIStatsSnapshot {
	v2 := *snapshot
	v2.SchemaVersion = 2
	v2.Jobs = nil
	v2.NewCharactersWindowDays = 0
	v2.PrivacyBuiltAt, v2.FreeCompaniesBuiltAt = time.Time{}, time.Time{}
	v2.Summary.Private, v2.Summary.Unchecked, v2.Summary.InFreeCompany = 0, 0, 0
	v2.Groups = nil
	for _, g := range snapshot.Groups {
		if g.Dimension == "grand_company" || g.Dimension == "grand_company_race" {
			continue
		}
		g.MaxLevel, g.Private, g.Unchecked, g.InFreeCompany = 0, 0, 0, 0
		g.NewCharacters, g.NewCharactersPrevious, g.NewCharactersWeekly = 0, 0, nil
		v2.Groups = append(v2.Groups, g)
	}
	return &v2
}

// TestUIStatsFullRefreshPayloadStaysWithinBudget is the spec's payload
// budget: the version 3 payload is at most twice its version 2 equivalent,
// so the snapshot every pod reloads each minute stays small.
func TestUIStatsFullRefreshPayloadStaysWithinBudget(t *testing.T) {
	driver := newTestDriver(t)
	now := time.Now().UTC()
	seedRollupFixture(t, driver, now.Add(-90*24*time.Hour), now.Add(-time.Hour), now.Add(-60*24*time.Hour))

	result, err := repository.NewUIStatsRepository(driver).Refresh(context.Background(),
		incrementalOpts(contract.UIStatsRefreshFull, now.Add(-30*24*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := json.Marshal(v2Equivalent(result.Snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if result.PayloadBytes <= 0 || result.PayloadBytes > 2*int64(len(v2)) {
		t.Fatalf("version 3 payload = %d bytes, want at most twice the version 2 equivalent (%d bytes)", result.PayloadBytes, len(v2))
	}
	t.Logf("payload: v3 %d bytes, v2 equivalent %d bytes (%.2fx)", result.PayloadBytes, len(v2), float64(result.PayloadBytes)/float64(len(v2)))
}
