package census

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func fc(id, name string, members, active int64) contract.FreeCompanyCount {
	return contract.FreeCompanyCount{ID: id, Name: name, Members: members, Active: active}
}

// fcBoardsFixture: Balmung (Crystal) and Gilgamesh (Aether) in NA, and Ragnarok (Chaos, EU).
func fcBoardsFixture() *contract.StatsFreeCompanies {
	return &contract.StatsFreeCompanies{
		BuiltAt: time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC),
		Worlds: []contract.WorldFreeCompanies{
			{World: "Balmung", Companies: []contract.FreeCompanyCount{fc("1", "Alpha", 40, 5), fc("2", "Beta", 30, 20), fc("3", "Gamma", 30, 20)}},
			{World: "Gilgamesh", Companies: []contract.FreeCompanyCount{fc("4", "Delta", 50, 2), fc("5", "Aardvark", 30, 20)}},
			{World: "Ragnarok", Companies: []contract.FreeCompanyCount{fc("6", "Epsilon", 60, 30)}},
		},
	}
}

func boardNames(rows []FreeCompanyRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%d %s %s", r.Rank, r.Name, r.World))
	}
	return out
}

func TestFreeCompanyBoardMergesWorldsAcrossScopes(t *testing.T) {
	boards := fcBoardsFixture()
	tests := []struct {
		name    string
		measure FreeCompanyMeasure
		scope   contract.StatsScope
		want    []string
	}{
		{"whole game by members", FreeCompanyMembers, contract.StatsScope{},
			[]string{"1 Epsilon Ragnarok", "2 Delta Gilgamesh", "3 Alpha Balmung", "4 Aardvark Gilgamesh", "5 Beta Balmung", "6 Gamma Balmung"}},
		// Ties on both counts break by name, then id.
		{"region by active", FreeCompanyActive, contract.StatsScope{Region: "NA"},
			[]string{"1 Aardvark Gilgamesh", "2 Beta Balmung", "3 Gamma Balmung", "4 Alpha Balmung", "5 Delta Gilgamesh"}},
		{"datacenter", FreeCompanyMembers, contract.StatsScope{Datacenter: "Chaos"}, []string{"1 Epsilon Ragnarok"}},
		{"world, any case", FreeCompanyMembers, contract.StatsScope{World: "gilgamesh"}, []string{"1 Delta Gilgamesh", "2 Aardvark Gilgamesh"}},
		{"unknown world", FreeCompanyMembers, contract.StatsScope{World: "Nowhere"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FreeCompanyBoardFor(boards, FreeCompanyQuery{Measure: tt.measure, Scope: tt.scope})
			if !reflect.DeepEqual(boardNames(got.Rows), tt.want) {
				t.Errorf("board = %v, want %v", boardNames(got.Rows), tt.want)
			}
		})
	}
}

func TestFreeCompanyBoardRowsCarryBothCountsAndTheShareOfTheLeader(t *testing.T) {
	got := FreeCompanyBoardFor(fcBoardsFixture(), FreeCompanyQuery{Measure: FreeCompanyActive, Scope: contract.StatsScope{World: "Balmung"}})
	if len(got.Rows) != 3 {
		t.Fatalf("rows = %v", got.Rows)
	}
	beta := got.Rows[0]
	want := FreeCompanyRow{Rank: 1, ID: "2", Name: "Beta", World: "Balmung", Datacenter: "Crystal", Members: 30, Active: 20, Value: 20, ShareOfLeader: 1}
	if beta != want {
		t.Errorf("leader = %+v, want %+v", beta, want)
	}
	if alpha := got.Rows[2]; alpha.Value != 5 || alpha.ShareOfLeader != 0.25 {
		t.Errorf("Alpha value %d share %v, want 5 and 0.25", alpha.Value, alpha.ShareOfLeader)
	}
}

func TestFreeCompanyBoardKeepsTheTop100(t *testing.T) {
	boards := &contract.StatsFreeCompanies{}
	for w, world := range []string{"Balmung", "Gilgamesh"} {
		var companies []contract.FreeCompanyCount
		for i := 0; i < FreeCompanyBoardSize; i++ {
			companies = append(companies, fc(fmt.Sprintf("%d%03d", w+1, i), fmt.Sprintf("FC %d %03d", w, i), int64(1000-i*2-w), 0))
		}
		boards.Worlds = append(boards.Worlds, contract.WorldFreeCompanies{World: world, Companies: companies})
	}
	got := FreeCompanyBoardFor(boards, FreeCompanyQuery{Measure: FreeCompanyMembers})
	if len(got.Rows) != FreeCompanyBoardSize {
		t.Fatalf("rows = %d, want %d", len(got.Rows), FreeCompanyBoardSize)
	}
	// Members interleave: 1000 (Balmung), 999 (Gilgamesh), 998, ...
	last := got.Rows[len(got.Rows)-1]
	if last.Members != 901 || last.World != "Gilgamesh" {
		t.Errorf("100th = %+v, want Gilgamesh with 901 members", last)
	}
}

func TestFreeCompanyBoardWithoutBoardsIsEmpty(t *testing.T) {
	if got := FreeCompanyBoardFor(nil, FreeCompanyQuery{Measure: FreeCompanyMembers}); len(got.Rows) != 0 {
		t.Errorf("rows = %v, want none", got.Rows)
	}
}

func TestTrimFreeCompanyWorldKeepsEachBoardsTopInBoardOrder(t *testing.T) {
	companies := []contract.FreeCompanyCount{
		// Tied on both counts at the members cut-off: the board breaks the tie by name.
		fc("1", "Zulu", 10, 0), fc("2", "Yankee", 10, 0), fc("3", "Alpha", 10, 0),
		fc("4", "Big", 20, 0),
		// Leads the active board only.
		fc("5", "Lively", 2, 5),
	}
	got := TrimFreeCompanyWorld(companies, 2)
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	// Members board: Big, then Alpha (name breaks the tie). Active board:
	// Lively, then the zero-active companies by members, then name: Big.
	want := []string{"4", "3", "5"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("TrimFreeCompanyWorld ids = %v, want %v (members board order, each company once)", ids, want)
	}
	if len(companies) != 5 || companies[0].ID != "1" {
		t.Error("TrimFreeCompanyWorld changed its input")
	}
}
