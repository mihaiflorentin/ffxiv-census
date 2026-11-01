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

// Active is a latest public achievement or a last observed change within the
// activity window (census.ActiveSince). seedActivity stores, on Balmung:
// 1 public with a recent achievement, 2 private with a recent change,
// 3 private with an old change, 4 private never seen to change, and 5 a
// deleted character with a recent change.
func seedActivity(t *testing.T, driver contract.DatabaseDriver, now time.Time) {
	t.Helper()
	recent := now.Add(-10 * 24 * time.Hour)
	for id := 1; id <= 5; id++ {
		seedStatsCharacter(t, driver, id, "Balmung", "Crystal", "NA", "Hyur", 1, now.Add(-200*24*time.Hour), nil, 50)
	}
	mustExec(t, driver, `UPDATE characters SET latest_achievement_at = $1 WHERE id = 1`, recent)
	mustExec(t, driver, `UPDATE characters SET achievements_private = 1 WHERE id IN (2, 3, 4, 5)`)
	mustExec(t, driver, `UPDATE characters SET last_change_at = $1 WHERE id IN (2, 5)`, recent)
	mustExec(t, driver, `UPDATE characters SET last_change_at = $1 WHERE id = 3`, now.Add(-200*24*time.Hour))
	mustExec(t, driver, `UPDATE characters SET deleted_at = $1 WHERE id = 5`, now)
}

func TestObservedActivity_CharacterRecordCarriesTheLastChange(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	now := time.Now().UTC()
	seedActivity(t, driver, now)

	rec, err := repo.Get(context.Background(), 2)
	if err != nil || rec == nil {
		t.Fatalf("Get(2) = %v, %v", rec, err)
	}
	if rec.LastChangeAt == nil {
		t.Fatal("Get(2).LastChangeAt is nil, want the stored change")
	}
	if rec, _ := repo.Get(context.Background(), 4); rec.LastChangeAt != nil {
		t.Errorf("Get(4).LastChangeAt = %v, want nil (never seen to change)", rec.LastChangeAt)
	}
}

func TestObservedActivity_CountAndListCountAChangeAsActive(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	seedActivity(t, driver, now)
	since := now.Add(-90 * 24 * time.Hour)

	if n, err := repo.CountActive(ctx, since); err != nil || n != 2 {
		t.Errorf("CountActive = %d, %v; want 2 (the achievement and the private change)", n, err)
	}
	recs, err := repo.List(ctx, contract.CharacterFilter{ActiveOnly: true, Since: &since}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []uint32
	for _, r := range recs {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	if want := []uint32{1, 2}; !slices.Equal(ids, want) {
		t.Errorf("active list = %v, want %v", ids, want)
	}
}

func TestObservedActivity_UIStatsCountAChangeAsActive(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	seedActivity(t, driver, now)
	since := now.Add(-90 * 24 * time.Hour)

	full, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since))
	if err != nil {
		t.Fatal(err)
	}
	if got := full.Snapshot.Summary.Active; got != 2 {
		t.Errorf("full refresh active = %d, want 2", got)
	}

	// A private character seen changing after the full refresh counts at
	// the next incremental refresh.
	mustExec(t, driver, `UPDATE characters SET last_change_at = NOW(), last_census_at = NOW() WHERE id = 4`)
	inc, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if inc.Mode != contract.UIStatsRefreshIncremental || inc.Snapshot.Summary.Active != 3 {
		t.Errorf("incremental mode=%q active=%d, want incremental with 3", inc.Mode, inc.Snapshot.Summary.Active)
	}
}

func TestObservedActivity_FreeCompanyMembersCountAChangeAsActive(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	recent, old := now.Add(-10*24*time.Hour), now.Add(-200*24*time.Hour)
	seedFCMembers(t, driver,
		fcMember{id: 1, world: "Balmung", fcID: "100", fcName: "Alpha", latestAchievementAt: &recent},
		fcMember{id: 2, world: "Balmung", fcID: "100", fcName: "Alpha", lastChangeAt: &recent},
		fcMember{id: 3, world: "Balmung", fcID: "100", fcName: "Alpha", lastChangeAt: &old},
	)

	if _, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, now.Add(-90*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	boards, err := stats.LoadFreeCompanies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := companiesOf(t, boards, "Balmung")["100"]; got.Members != 3 || got.Active != 2 {
		t.Errorf("Alpha = %+v, want 3 members, 2 active", got)
	}
}

func TestObservedActivity_LastChangeIndex(t *testing.T) {
	driver := newTestDriver(t)
	def, ok := characterIndexDefs(t, driver)["idx_characters_last_change"]
	if !ok {
		t.Fatal("idx_characters_last_change missing")
	}
	for _, part := range []string{"(last_change_at)", "deleted_at IS NULL", "last_change_at IS NOT NULL"} {
		if !strings.Contains(def, part) {
			t.Errorf("index definition lacks %q:\n%s", part, def)
		}
	}
}
