package census

import (
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func validUIStatsSnapshot() *contract.UIStatsSnapshot {
	generated := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	return &contract.UIStatsSnapshot{
		SchemaVersion:    contract.UIStatsSchemaVersion,
		GeneratedAt:      generated,
		ActivitySince:    generated.Add(-30 * 24 * time.Hour),
		MaxLevel:         100,
		SourceCharacters: 3,
		Summary: contract.StatsSummary{
			Total:    3,
			Active:   2,
			MaxLevel: 1,
		},
		Groups: []contract.ScopedGroupCount{
			{Dimension: "race", Key: "Hyur", Total: 2, Active: 1},
			{Dimension: "race", Key: "Lalafell", Total: 1, Active: 1},
			{Scope: contract.StatsScope{Region: "EU"}, Dimension: "race", Key: "Hyur", Total: 1, Active: 1},
		},
		Expansions: []contract.ScopedExpansionCount{
			{Expansion: "A Realm Reborn", Count: 2},
		},
		NewCharacters: []contract.ScopedDailyCount{
			{Day: "2026-08-23", Count: 1},
		},
	}
}

func TestValidateUIStatsSnapshot(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*contract.UIStatsSnapshot)
		ok     bool
	}{
		{name: "valid", ok: true},
		{name: "nil", mutate: func(s *contract.UIStatsSnapshot) { *s = contract.UIStatsSnapshot{} }},
		{name: "wrong schema", mutate: func(s *contract.UIStatsSnapshot) { s.SchemaVersion++ }},
		{name: "zero generated", mutate: func(s *contract.UIStatsSnapshot) { s.GeneratedAt = time.Time{} }},
		{name: "negative total", mutate: func(s *contract.UIStatsSnapshot) { s.Summary.Total = -1 }},
		{name: "active over total", mutate: func(s *contract.UIStatsSnapshot) { s.Summary.Active = 4 }},
		{name: "duplicate group", mutate: func(s *contract.UIStatsSnapshot) { s.Groups = append(s.Groups, s.Groups[0]) }},
		{name: "negative private", mutate: func(s *contract.UIStatsSnapshot) { s.Summary.Private = -1 }},
		{name: "private and unchecked over total", mutate: func(s *contract.UIStatsSnapshot) { s.Summary.Private, s.Summary.Unchecked = 2, 2 }},
		{name: "group unchecked negative", mutate: func(s *contract.UIStatsSnapshot) { s.Groups[0].Unchecked = -1 }},
		{name: "group private over total", mutate: func(s *contract.UIStatsSnapshot) { s.Groups[0].Private = 3 }},
		{name: "group at cap over total", mutate: func(s *contract.UIStatsSnapshot) { s.Groups[0].MaxLevel = 3 }},
		{name: "privacy within total", ok: true, mutate: func(s *contract.UIStatsSnapshot) {
			s.Summary.Private, s.Summary.Unchecked = 1, 2
			s.Groups[0].Private, s.Groups[0].Unchecked, s.Groups[0].MaxLevel = 1, 1, 2
		}},
		{name: "bad day", mutate: func(s *contract.UIStatsSnapshot) { s.NewCharacters[0].Day = "08/23/2026" }},
		{name: "negative new-characters window", mutate: func(s *contract.UIStatsSnapshot) { s.NewCharactersWindowDays = -1 }},
		{name: "group new characters negative", mutate: func(s *contract.UIStatsSnapshot) { s.Groups[0].NewCharacters = -1 }},
		{name: "group previous new characters negative", mutate: func(s *contract.UIStatsSnapshot) { s.Groups[0].NewCharactersPrevious = -1 }},
		{name: "level bands", ok: true, mutate: func(s *contract.UIStatsSnapshot) {
			s.LevelBands = []contract.StatsLevelBand{{Name: "A", Min: 1, Max: 49}, {Name: LevelCapBand, Min: 50}}
			s.Summary.Levels, s.Summary.NoLevels = []int64{1, 1}, 1
			s.Groups[0].Levels = []int64{2, 0}
		}},
		{name: "level band sums apart from the totals", ok: true, mutate: func(s *contract.UIStatsSnapshot) {
			// The incremental refresh moves the totals but carries the
			// nightly level bands unchanged, so their sums may differ.
			s.LevelBands = []contract.StatsLevelBand{{Name: "A", Min: 1, Max: 49}, {Name: LevelCapBand, Min: 50}}
			s.Summary.Levels, s.Summary.NoLevels = []int64{4, 1}, 2
			s.Groups[0].Levels = []int64{5, 0}
		}},
		{name: "summary level band negative", mutate: func(s *contract.UIStatsSnapshot) {
			s.LevelBands = []contract.StatsLevelBand{{Name: "A", Min: 1, Max: 49}, {Name: LevelCapBand, Min: 50}}
			s.Summary.Levels = []int64{-1, 1}
		}},
		{name: "group no levels negative", mutate: func(s *contract.UIStatsSnapshot) { s.Groups[0].NoLevels = -1 }},
		{name: "level counts unlike the bands", mutate: func(s *contract.UIStatsSnapshot) {
			s.LevelBands = []contract.StatsLevelBand{{Name: "A", Min: 1, Max: 49}, {Name: LevelCapBand, Min: 50}}
			s.Groups[0].Levels = []int64{1, 0, 0}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := validUIStatsSnapshot()
			if tt.mutate != nil {
				tt.mutate(snapshot)
			}
			err := ValidateUIStatsSnapshot(snapshot)
			if tt.ok && err != nil {
				t.Fatalf("ValidateUIStatsSnapshot() error = %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("ValidateUIStatsSnapshot() error = nil, want error")
			}
		})
	}
}

func TestUIStatsSnapshotLookups(t *testing.T) {
	snapshot := validUIStatsSnapshot()

	global := SnapshotGroups(snapshot, "race", contract.StatsScope{})
	if len(global) != 2 || global[0].Key != "Hyur" || global[1].Key != "Lalafell" {
		t.Fatalf("global races = %#v", global)
	}

	eu := SnapshotGroups(snapshot, "race", contract.StatsScope{Region: "EU"})
	if len(eu) != 1 || eu[0].Total != 1 {
		t.Fatalf("EU races = %#v", eu)
	}

	if got := SnapshotExpansions(snapshot, contract.StatsScope{}); len(got) != 1 || got[0].Count != 2 {
		t.Fatalf("expansions = %#v", got)
	}
	if got := SnapshotDaily(snapshot, contract.StatsScope{}); len(got) != 1 || got[0].Day != "2026-08-23" {
		t.Fatalf("daily = %#v", got)
	}
}
