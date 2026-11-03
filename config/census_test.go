package config

import (
	"slices"
	"testing"
)

func TestCensusConfig_Defaults(t *testing.T) {
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Census == nil || cfg.Census.ActivityWindowDays != 90 {
		t.Fatalf("activity_window_days = %v, want 90", cfg.Census)
	}
}

func TestCensusConfig_PrivateRecheckWindowDefaultAndEnv(t *testing.T) {
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Census == nil || cfg.Census.PrivateRecheckDays != 30 {
		t.Fatalf("private_recheck_days = %+v, want 30", cfg.Census)
	}

	t.Setenv("CENSUS_PRIVATE_RECHECK_DAYS", "7")
	cfg, _ = NewConfig()
	if cfg.Census.PrivateRecheckDays != 7 {
		t.Fatalf("CENSUS_PRIVATE_RECHECK_DAYS override: got %v, want 7", cfg.Census.PrivateRecheckDays)
	}
}

func TestCensusConfig_UIStatsDefaultsAndEnv(t *testing.T) {
	t.Setenv("CENSUS_UI_STATS_CACHE_TTL", "2m")
	cfg, err := NewConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Census == nil || cfg.Census.UIStats == nil {
		t.Fatal("census.ui_stats config is nil")
	}
	if cfg.Census.UIStats.CacheTTL != "2m" {
		t.Fatalf("cache_ttl = %q, want 2m", cfg.Census.UIStats.CacheTTL)
	}
	if cfg.Census.UIStats.StaleWarning != "12h" || cfg.Census.UIStats.RefreshTimeout != "2h" {
		t.Fatalf("unexpected UI stats defaults: %#v", cfg.Census.UIStats)
	}
}

func TestCensusConfig_EnvOverride(t *testing.T) {
	t.Setenv("CENSUS_ACTIVITY_WINDOW_DAYS", "45")
	cfg, _ := NewConfig()
	if cfg.Census.ActivityWindowDays != 45 {
		t.Fatalf("CENSUS_ACTIVITY_WINDOW_DAYS override: got %v, want 45", cfg.Census.ActivityWindowDays)
	}
}

func TestCensusConfig_NewCharactersWindowDefaultAndEnv(t *testing.T) {
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Census == nil || cfg.Census.NewCharactersWindowDays != 90 {
		t.Fatalf("new_characters_window_days = %+v, want 90", cfg.Census)
	}

	t.Setenv("CENSUS_NEW_CHARACTERS_WINDOW_DAYS", "60")
	cfg, _ = NewConfig()
	if cfg.Census.NewCharactersWindowDays != 60 {
		t.Fatalf("CENSUS_NEW_CHARACTERS_WINDOW_DAYS override: got %v, want 60", cfg.Census.NewCharactersWindowDays)
	}
}

func TestCensusConfig_MaxLevelAndExpansions(t *testing.T) {
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Census == nil {
		t.Fatal("cfg.Census is nil")
	}
	if cfg.Census.MaxLevel != 100 {
		t.Errorf("max_level = %d, want 100", cfg.Census.MaxLevel)
	}
	if len(cfg.Census.Expansions) != 6 {
		t.Fatalf("len(expansions) = %d, want 6", len(cfg.Census.Expansions))
	}
	arr := cfg.Census.Expansions[0]
	if arr.Name != "A Realm Reborn" || arr.Version != "Patch 2.55" || arr.FinalQuest != "Before the Dawn" || arr.Icon != "🌱" || arr.LevelCap != 50 || arr.AchievementID != 1129 {
		t.Errorf("unexpected ARR config: %+v", arr)
	}
	dt := cfg.Census.Expansions[5]
	if dt.Name != "Dawntrail" || dt.Version != "Patch 7.0" || dt.FinalQuest != "In the Glow of a New Dawn" || dt.Icon != "☀️" || dt.LevelCap != 100 || dt.AchievementID != 3496 {
		t.Errorf("unexpected Dawntrail config: %+v", dt)
	}
}

func TestCensusConfig_CensusTiersDefaultAndEnv(t *testing.T) {
	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	if cfg.Census == nil || !slices.Equal(cfg.Census.TierRevisitHours, []int{36, 72}) {
		t.Fatalf("tier_revisit_hours = %+v, want [36 72]", cfg.Census)
	}
	if cfg.Census.MinRestShare != 50 {
		t.Fatalf("min_rest_share = %d, want 50", cfg.Census.MinRestShare)
	}
	if cfg.Census.ActiveWindowDays != 90 {
		t.Fatalf("active_window_days = %d, want 90", cfg.Census.ActiveWindowDays)
	}
	if cfg.Census.RecentWindowDays != 365 {
		t.Fatalf("recent_window_days = %d, want 365", cfg.Census.RecentWindowDays)
	}
	if cfg.Census.NewCharactersExactFromID != 62745092 {
		t.Fatalf("new_characters_exact_from_id = %d, want 62745092", cfg.Census.NewCharactersExactFromID)
	}

	t.Setenv("CENSUS_ACTIVE_WINDOW_DAYS", "30")
	t.Setenv("CENSUS_RECENT_WINDOW_DAYS", "180")
	t.Setenv("CENSUS_MIN_REST_SHARE", "60")
	t.Setenv("CENSUS_NEW_CHARACTERS_EXACT_FROM_ID", "62800000")
	cfg, _ = NewConfig()
	if cfg.Census.NewCharactersExactFromID != 62800000 {
		t.Fatalf("CENSUS_NEW_CHARACTERS_EXACT_FROM_ID override: got %v, want 62800000", cfg.Census.NewCharactersExactFromID)
	}
	if cfg.Census.ActiveWindowDays != 30 {
		t.Fatalf("CENSUS_ACTIVE_WINDOW_DAYS override: got %v, want 30", cfg.Census.ActiveWindowDays)
	}
	if cfg.Census.RecentWindowDays != 180 {
		t.Fatalf("CENSUS_RECENT_WINDOW_DAYS override: got %v, want 180", cfg.Census.RecentWindowDays)
	}
	if cfg.Census.MinRestShare != 60 {
		t.Fatalf("CENSUS_MIN_REST_SHARE override: got %v, want 60", cfg.Census.MinRestShare)
	}
}
