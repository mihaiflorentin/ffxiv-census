package ui

import (
	"strconv"
	"testing"
)

func rungsNamed(n int) []LadderRung {
	rungs := make([]LadderRung, n)
	for i := range rungs {
		rungs[i] = LadderRung{Rank: i + 1, Name: "World " + strconv.Itoa(i+1)}
	}
	return rungs
}

func TestNewLadderShowsTheTopTenAndKeepsTheRestBehindShowAll(t *testing.T) {
	short := newLadder(rungsNamed(10), "worlds")
	if len(short.Top) != 10 || len(short.Rest) != 0 || short.AllLabel != "" {
		t.Errorf("ten rungs: top %d, rest %d, label %q; want all ten on top and no disclosure", len(short.Top), len(short.Rest), short.AllLabel)
	}

	long := newLadder(rungsNamed(13), "worlds")
	if len(long.Top) != 10 || long.Top[9].Rank != 10 {
		t.Errorf("thirteen rungs: top = %+v, want ranks 1 to 10", long.Top)
	}
	if len(long.Rest) != 3 || long.Rest[0].Rank != 11 || long.Rest[2].Rank != 13 {
		t.Errorf("thirteen rungs: rest = %+v, want ranks 11 to 13", long.Rest)
	}
	if long.AllLabel != "Show all 13 worlds" {
		t.Errorf("label = %q, want %q", long.AllLabel, "Show all 13 worlds")
	}

	if empty := newLadder(nil, "worlds"); len(empty.Top) != 0 || empty.AllLabel != "" {
		t.Errorf("no rungs: %+v, want an empty ladder", empty)
	}
}

func TestBarWidthIsAPercentageOfTheLeader(t *testing.T) {
	for share, want := range map[float64]string{1: "100.000%", 0.625: "62.500%", 0: "0.000%"} {
		if got := barWidth(share); got != want {
			t.Errorf("barWidth(%v) = %q, want %q", share, got, want)
		}
	}
}
