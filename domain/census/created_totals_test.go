package census

import (
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// createdSnapshot is a schema 5 snapshot generated on 24 Oct 2026 with a
// 90-day window: the current window runs 26 Jul to 23 Oct, the previous one
// 27 Apr to 25 Jul, and the 13 weeks 25 Jul to 23 Oct. The created
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
	weekly := make([]int64, 13)
	weekly[0], weekly[12] = 1, 3
	for i, g := range s.Groups {
		switch g.Key {
		case "Balmung":
			s.Groups[i].Created, s.Groups[i].CreatedPrevious, s.Groups[i].CreatedWeekly = 4, 2, weekly
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

// The weekly series carry both figures, and mark the weeks with an
// estimated day.
func TestNewCharactersWeeklyCarryBothFigures(t *testing.T) {
	s := createdSnapshot()
	world := WorldNewCharactersWeekly(s, "Balmung")
	if len(world) != 13 {
		t.Fatalf("world weeks = %d, want 13", len(world))
	}
	if world[0].Created != 1 || !world[0].Estimated || world[12].Created != 3 || !world[12].Estimated {
		t.Fatalf("world weeks = %+v ... %+v, want 1 and 3 created, both estimated", world[0], world[12])
	}

	global := NewCharactersWeekly(s)
	if len(global) != 13 || global[0].Start != "2026-07-25" || global[12].End != "2026-10-23" {
		t.Fatalf("global weeks = %+v, want 13 from 25 Jul to 23 Oct", global)
	}
	if global[0].Created != 5 || global[12].Created != 5 || global[12].Count != 2 {
		t.Fatalf("global weeks = %+v ... %+v, want 5 created in the first, 5 created and 2 chocobos in the last", global[0], global[12])
	}

	s.CreatedExactFrom = "2026-10-17"
	if weeks := NewCharactersWeekly(s); weeks[11].Estimated != true || weeks[12].Estimated {
		t.Fatalf("exact from the last week's first day: weeks 11/12 estimated = %v/%v, want true/false", weeks[11].Estimated, weeks[12].Estimated)
	}

	none := worldWindowSnapshot()
	none.NewCharacters = []contract.ScopedDailyCount{{Day: "2026-10-22", Count: 2}}
	for _, w := range NewCharactersWeekly(none) {
		if w.Created != 0 || w.Estimated {
			t.Fatalf("week %+v without created figures, want none and not estimated", w)
		}
	}
	if NewCharactersWeekly(legacyWorldSnapshot()) != nil {
		t.Fatal("a schema 3 snapshot has weekly series")
	}
}
