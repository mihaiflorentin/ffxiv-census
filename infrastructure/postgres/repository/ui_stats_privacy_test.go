package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// privacyState is what an achievement census last found for a character.
type privacyState int

const (
	unchecked privacyState = iota // never censused: not private, no latest achievement
	public
	private
)

// seedPrivacyCharacter stores a character with a highest job level and the
// given achievement privacy state. A public character's latest achievement is
// at latest.
func seedPrivacyCharacter(t *testing.T, driver contract.DatabaseDriver, id int, world, dc, region string, level int, state privacyState, firstSeen, latest time.Time) {
	t.Helper()
	seedStatsCharacter(t, driver, id, world, dc, region, "Hyur", 1, firstSeen, nil, level)
	chars := repository.NewCharacterRepository(driver)
	var write contract.AchievementWrite
	switch state {
	case public:
		write = contract.AchievementWrite{CharacterID: uint32(id), LatestAt: &latest}
	case private:
		write = contract.AchievementWrite{CharacterID: uint32(id), Private: true, CheckedAt: latest}
	default:
		return
	}
	if err := chars.StoreResult(context.Background(), contract.CensusResult{Achievements: []contract.AchievementWrite{write}}); err != nil {
		t.Fatal(err)
	}
}

// locationCounts is {total, at cap, private, unchecked} of one location group.
type locationCounts [4]int64

func locationGroup(t *testing.T, snapshot *contract.UIStatsSnapshot, dimension, key string) locationCounts {
	t.Helper()
	for _, g := range snapshot.Groups {
		if g.Scope == (contract.StatsScope{}) && g.Dimension == dimension && g.Key == key {
			return locationCounts{g.Total, g.MaxLevel, g.Private, g.Unchecked}
		}
	}
	t.Fatalf("no %s group %q", dimension, key)
	return locationCounts{}
}

func summaryCounts(s contract.StatsSummary) locationCounts {
	return locationCounts{s.Total, s.MaxLevel, s.Private, s.Unchecked}
}

func TestUIStatsFullRefreshCountsAtCapPrivateAndUncheckedPerLocation(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-90 * 24 * time.Hour)
	recent := now.Add(-time.Hour)

	seedPrivacyCharacter(t, driver, 1, "Balmung", "Crystal", "NA", 100, public, old, recent)
	seedPrivacyCharacter(t, driver, 2, "Balmung", "Crystal", "NA", 90, private, old, recent)
	seedPrivacyCharacter(t, driver, 3, "Balmung", "Crystal", "NA", 100, unchecked, old, recent)
	seedPrivacyCharacter(t, driver, 4, "Ragnarok", "Chaos", "EU", 100, private, old, recent)
	seedPrivacyCharacter(t, driver, 5, "Ragnarok", "Chaos", "EU", 50, public, old, old)
	seedPrivacyCharacter(t, driver, 6, "Balmung", "Crystal", "NA", 100, private, old, recent)
	mustExec(t, driver, `UPDATE characters SET deleted_at = NOW() WHERE id = 6`)

	if _, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, now.Add(-30*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	loaded, err := stats.LoadCurrent(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := summaryCounts(loaded.Summary), (locationCounts{5, 3, 2, 1}); got != want {
		t.Errorf("summary {total, at cap, private, unchecked} = %v, want %v", got, want)
	}
	for _, tc := range []struct {
		dimension, key string
		want           locationCounts
	}{
		{"world", "Balmung", locationCounts{3, 2, 1, 1}},
		{"world", "Ragnarok", locationCounts{2, 1, 1, 0}},
		{"datacenter", "Crystal", locationCounts{3, 2, 1, 1}},
		{"datacenter", "Chaos", locationCounts{2, 1, 1, 0}},
		{"region", "NA", locationCounts{3, 2, 1, 1}},
		{"region", "EU", locationCounts{2, 1, 1, 0}},
	} {
		if got := locationGroup(t, loaded, tc.dimension, tc.key); got != tc.want {
			t.Errorf("%s %s {total, at cap, private, unchecked} = %v, want %v", tc.dimension, tc.key, got, tc.want)
		}
	}
	if !loaded.PrivacyBuiltAt.Equal(loaded.GeneratedAt) {
		t.Errorf("privacy built at %v, want the full rebuild's generation time %v", loaded.PrivacyBuiltAt, loaded.GeneratedAt)
	}
}

func TestUIStatsIncrementalRecountsAtCapAndCarriesPrivacyForwardByDelta(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-90 * 24 * time.Hour)
	recent := now.Add(-time.Hour)
	since := now.Add(-30 * 24 * time.Hour)

	seedPrivacyCharacter(t, driver, 1, "Balmung", "Crystal", "NA", 100, public, old, recent)
	seedPrivacyCharacter(t, driver, 2, "Balmung", "Crystal", "NA", 90, private, old, recent)
	seedPrivacyCharacter(t, driver, 3, "Balmung", "Crystal", "NA", 100, unchecked, old, recent)
	seedPrivacyCharacter(t, driver, 6, "Balmung", "Crystal", "NA", 90, public, old, old)
	full, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since))
	if err != nil {
		t.Fatal(err)
	}

	// After the full rebuild: a new unchecked and a new private character, a
	// private character deleted, a counted character turning private, and a
	// counted character reaching the level cap.
	time.Sleep(50 * time.Millisecond)
	after := time.Now().UTC()
	seedPrivacyCharacter(t, driver, 4, "Balmung", "Crystal", "NA", 0, unchecked, after, after)
	seedPrivacyCharacter(t, driver, 5, "Balmung", "Crystal", "NA", 100, private, after, after)
	mustExec(t, driver, `UPDATE characters SET deleted_at = NOW() WHERE id = 2`)
	mustExec(t, driver, `UPDATE characters SET achievements_private = 1 WHERE id = 3`)
	mustExec(t, driver, `UPDATE characters SET max_job_level = 100 WHERE id = 6`)

	inc, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if inc.Mode != contract.UIStatsRefreshIncremental {
		t.Fatalf("mode = %q, want incremental", inc.Mode)
	}
	// At cap is exact; private and unchecked move by the new/deleted delta
	// only, so character 3 turning private waits for the nightly rebuild.
	want := locationCounts{5, 4, 1, 2}
	if got := summaryCounts(inc.Snapshot.Summary); got != want {
		t.Errorf("incremental summary = %v, want %v", got, want)
	}
	for _, g := range [][2]string{{"world", "Balmung"}, {"datacenter", "Crystal"}, {"region", "NA"}} {
		if got := locationGroup(t, inc.Snapshot, g[0], g[1]); got != want {
			t.Errorf("incremental %s %s = %v, want %v", g[0], g[1], got, want)
		}
	}
	if !inc.Snapshot.PrivacyBuiltAt.Equal(full.Snapshot.GeneratedAt) {
		t.Errorf("incremental privacy built at %v, want the full rebuild's %v", inc.Snapshot.PrivacyBuiltAt, full.Snapshot.GeneratedAt)
	}

	rebuilt, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := summaryCounts(rebuilt.Snapshot.Summary), (locationCounts{5, 4, 2, 1}); got != want {
		t.Errorf("nightly rebuild summary = %v, want %v (the drift corrected)", got, want)
	}
	if !rebuilt.Snapshot.PrivacyBuiltAt.Equal(rebuilt.Snapshot.GeneratedAt) {
		t.Errorf("rebuilt privacy built at %v, want %v", rebuilt.Snapshot.PrivacyBuiltAt, rebuilt.Snapshot.GeneratedAt)
	}
}

func TestUIStatsIncrementalWithoutPrivacyFiguresRebuildsFully(t *testing.T) {
	driver := newTestDriver(t)
	stats := repository.NewUIStatsRepository(driver)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-30 * 24 * time.Hour)
	seedPrivacyCharacter(t, driver, 1, "Balmung", "Crystal", "NA", 100, private, now.Add(-72*time.Hour), now)
	if _, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshFull, since)); err != nil {
		t.Fatal(err)
	}
	// A schema 3 snapshot written before the privacy figures existed.
	mustExec(t, driver, `UPDATE ui_stats_snapshots SET payload = payload - 'privacy_built_at'`)
	time.Sleep(20 * time.Millisecond)

	r, err := stats.Refresh(ctx, incrementalOpts(contract.UIStatsRefreshIncremental, since))
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != contract.UIStatsRefreshFull || r.Snapshot.Summary.Private != 1 || r.Snapshot.PrivacyBuiltAt.IsZero() {
		t.Fatalf("mode=%q private=%d built=%v, want a full rebuild with the privacy figures", r.Mode, r.Snapshot.Summary.Private, r.Snapshot.PrivacyBuiltAt)
	}
}
