package census

import (
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// createdSnapshot is a schema 5 snapshot generated on 24 Oct 2026 with a
// 90-day window: the current window runs 26 Jul to 23 Oct, the previous one
// 27 Apr to 25 Jul. The created
// characters are exact from 21 Oct.
func createdSnapshot() *contract.UIStatsSnapshot {
	s := worldWindowSnapshot()
	s.CreatedExactFrom = "2026-10-21"
	s.CreatedExactFromID = 62_745_092
	s.CreatedBuiltAt = s.GeneratedAt
	s.Created = []contract.ScopedDailyCount{
		{Day: "2026-04-26", Count: 100}, // before both windows
		{Day: "2026-04-27", Count: 1},   // previous window, first day
		{Day: "2026-07-25", Count: 3},   // previous window, last day; the oldest week
		{Day: "2026-07-26", Count: 2},   // current window, first day
		{Day: "2026-10-23", Count: 5},   // current window, last day
		{Day: "2026-10-24", Count: 50},  // generation day: still in progress
	}
	s.NewCharacters = []contract.ScopedDailyCount{{Day: "2026-10-22", Count: 2}}
	daily := make([]int64, 90)
	daily[0], daily[89] = 1, 3
	for i, g := range s.Groups {
		switch g.Key {
		case "Balmung":
			s.Groups[i].Created, s.Groups[i].CreatedPrevious, s.Groups[i].CreatedDaily = 4, 2, daily
		case "Ragnarok":
			s.Groups[i].Created, s.Groups[i].CreatedPrevious = 10, 20
		}
	}
	return s
}

func TestNewCharactersTotalsCarryTheCreatedCharacters(t *testing.T) {
	s := createdSnapshot()
	tests := []struct {
		name              string
		scope             contract.StatsScope
		current, previous int64
	}{
		{name: "whole game reads the daily series", current: 7, previous: 4},
		{name: "a world reads its group", scope: contract.StatsScope{World: "balmung"}, current: 4, previous: 2},
		{name: "a datacenter sums its worlds", scope: contract.StatsScope{Datacenter: "Chaos"}, current: 10, previous: 20},
		{name: "an unknown world is zero", scope: contract.StatsScope{World: "Nowhere"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewCharactersTotals(s, tt.scope).CreatedWindow()
			want := NewCharacterTotals{Days: 90, StartDay: "2026-07-26", Current: tt.current, Previous: tt.previous}
			if got != want {
				t.Fatalf("created window = %+v, want %+v", got, want)
			}
		})
	}
	if got, want := NewSnapshotMemo().WorldNewCharacters(s, "Ragnarok"), NewCharactersTotals(s, contract.StatsScope{World: "Ragnarok"}); got != want || got.Created != 10 {
		t.Fatalf("memo = %+v, want %+v with 10 created", got, want)
	}
}

func TestCreatedReady(t *testing.T) {
	if !CreatedReady(createdSnapshot()) {
		t.Fatal("a snapshot with created figures is not ready")
	}
	if CreatedReady(worldWindowSnapshot()) || CreatedReady(nil) {
		t.Fatal("a snapshot without created figures is ready")
	}
}

func TestCreatedEstimatedBefore(t *testing.T) {
	day, ok := CreatedEstimatedBefore(createdSnapshot())
	if !ok || !day.Equal(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("CreatedEstimatedBefore = %v, %v; want 21 Oct", day, ok)
	}
	if _, ok := CreatedEstimatedBefore(worldWindowSnapshot()); ok {
		t.Fatal("a snapshot without created figures has an exact day")
	}
}

// The daily series carry both figures, for the whole game and for a world,
// and mark the days before the exact cut as estimated.
func TestNewCharactersDailyCarryBothFigures(t *testing.T) {
	s := createdSnapshot()
	world := NewCharactersDaily(s, "Balmung")
	if len(world) != 90 {
		t.Fatalf("world days = %d, want 90", len(world))
	}
	if world[0] != (DailyNewCharacters{Day: "2026-07-26", Created: 1, Estimated: true}) ||
		world[87] != (DailyNewCharacters{Day: "2026-10-21"}) ||
		world[89] != (DailyNewCharacters{Day: "2026-10-23", Created: 3}) {
		t.Fatalf("world days = %+v, %+v, %+v; want 1 created estimated on Jul 26, an exact Oct 21, 3 created on Oct 23", world[0], world[87], world[89])
	}
	if !world[86].Estimated {
		t.Errorf("Oct 20 = %+v, want estimated", world[86])
	}

	global := NewCharactersDaily(s, "")
	if len(global) != 90 || global[0].Created != 2 || !global[0].Estimated || global[88].Count != 2 || global[89].Created != 5 {
		t.Fatalf("global days = %+v ... %+v, want 2 created on Jul 26, 2 chocobos on Oct 22, 5 created on Oct 23", global[0], global[88:])
	}

	for _, d := range NewCharactersDaily(worldWindowSnapshot(), "") {
		if d.Created != 0 || d.Estimated {
			t.Fatalf("day %+v without created figures, want none and not estimated", d)
		}
	}
	if legacy := NewCharactersDaily(legacyWorldSnapshot(), ""); len(legacy) != 30 || legacy[29].Count != 99 {
		t.Fatalf("schema 3 whole-game days = %d, want its 30-day window ending Oct 23 with 99", len(legacy))
	}
}
