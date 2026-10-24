package census

import (
	"math"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestSnapshotHasSection(t *testing.T) {
	tests := []struct {
		name     string
		snapshot *contract.UIStatsSnapshot
		section  SnapshotSection
		want     bool
	}{
		{"v3 has Grand Companies", &contract.UIStatsSnapshot{SchemaVersion: 3}, SectionGrandCompanies, true},
		{"v2 predates Grand Companies", &contract.UIStatsSnapshot{SchemaVersion: 2}, SectionGrandCompanies, false},
		{"no snapshot", nil, SectionGrandCompanies, false},
		{"unknown section", &contract.UIStatsSnapshot{SchemaVersion: 3}, SnapshotSection("nonsense"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SnapshotHasSection(tt.snapshot, tt.section); got != tt.want {
				t.Fatalf("SnapshotHasSection() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateUIStatsSnapshotAcceptsV2ToV4(t *testing.T) {
	for _, version := range []int{2, 3, 4} {
		s := validUIStatsSnapshot()
		s.SchemaVersion = version
		if err := ValidateUIStatsSnapshot(s); err != nil {
			t.Fatalf("v%d: %v", version, err)
		}
	}
	for _, version := range []int{1, 5} {
		s := validUIStatsSnapshot()
		s.SchemaVersion = version
		if err := ValidateUIStatsSnapshot(s); err == nil {
			t.Fatalf("v%d must be rejected", version)
		}
	}
}

func gcSnapshot() *contract.UIStatsSnapshot {
	eu := contract.StatsScope{Region: "EU"}
	chaos := contract.StatsScope{Datacenter: "Chaos"}
	return &contract.UIStatsSnapshot{
		SchemaVersion: 3,
		Groups: []contract.ScopedGroupCount{
			{Dimension: "grand_company", Key: "Maelstrom", Total: 50, Active: 20},
			{Dimension: "grand_company", Key: "Order of the Twin Adder", Total: 30, Active: 6},
			{Dimension: "grand_company", Key: "Immortal Flames", Total: 15, Active: 9},
			{Dimension: "grand_company", Key: "None", Total: 5, Active: 0},
			{Scope: eu, Dimension: "grand_company", Key: "Immortal Flames", Total: 3, Active: 1},
			{Scope: eu, Dimension: "grand_company", Key: "None", Total: 1, Active: 1},
			{Scope: chaos, Dimension: "grand_company", Key: "Immortal Flames", Total: 3, Active: 1},
			{Dimension: "grand_company_race", Key: "Maelstrom|Roegadyn", Total: 8, Active: 2},
			{Dimension: "grand_company_race", Key: "Order of the Twin Adder|Roegadyn", Total: 2, Active: 1},
			{Dimension: "grand_company_race", Key: "Immortal Flames|Hyur", Total: 4, Active: 1},
			{Dimension: "grand_company_race", Key: "None|Hyur", Total: 4, Active: 0},
		},
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestGrandCompanySplitGlobal(t *testing.T) {
	split := GrandCompanySplitFor(gcSnapshot(), contract.StatsScope{})
	if split.Total != 100 || split.Active != 35 {
		t.Fatalf("totals = %d/%d, want 100/35", split.Total, split.Active)
	}
	want := []struct {
		name, slug          string
		total, active       int64
		share, activeShare  float64
		shareOfActivePlayer float64
	}{
		{"Maelstrom", "maelstrom", 50, 20, 0.50, 0.40, 20.0 / 35},
		{"Order of the Twin Adder", "twin-adder", 30, 6, 0.30, 0.20, 6.0 / 35},
		{"Immortal Flames", "immortal-flames", 15, 9, 0.15, 0.60, 9.0 / 35},
		{"None", "none", 5, 0, 0.05, 0, 0},
	}
	if len(split.Companies) != len(want) {
		t.Fatalf("companies = %+v", split.Companies)
	}
	for i, w := range want {
		got := split.Companies[i]
		if got.Name != w.name || got.Slug != w.slug || got.Total != w.total || got.Active != w.active ||
			!near(got.Share, w.share) || !near(got.ActiveShare, w.activeShare) || !near(got.ShareOfActive, w.shareOfActivePlayer) {
			t.Fatalf("company %d = %+v, want %+v", i, got, w)
		}
	}
	if lead := split.Leader(); lead.Name != "Maelstrom" {
		t.Fatalf("leader = %q, want Maelstrom", lead.Name)
	}
}

func TestGrandCompanySplitScopesKeepEveryCompany(t *testing.T) {
	for _, scope := range []contract.StatsScope{{Region: "EU"}, {Datacenter: "Chaos"}} {
		split := GrandCompanySplitFor(gcSnapshot(), scope)
		if len(split.Companies) != 4 {
			t.Fatalf("%+v: companies = %+v, want all four", scope, split.Companies)
		}
		if split.Companies[0].Name != "Maelstrom" || split.Companies[0].Total != 0 || split.Companies[0].Share != 0 {
			t.Fatalf("%+v: an absent company must read zero, got %+v", scope, split.Companies[0])
		}
	}
	eu := GrandCompanySplitFor(gcSnapshot(), contract.StatsScope{Region: "EU"})
	if eu.Total != 4 || !near(eu.Companies[2].Share, 0.75) || !near(eu.Companies[2].ActiveShare, 1.0/3) {
		t.Fatalf("EU split = %+v", eu)
	}
	if empty := GrandCompanySplitFor(gcSnapshot(), contract.StatsScope{World: "Nowhere"}); empty.Total != 0 || empty.Leader().Name != "" {
		t.Fatalf("empty scope = %+v", empty)
	}
}

func TestGrandCompanyRaceLean(t *testing.T) {
	lean := GrandCompanyRaceLean(gcSnapshot())
	if len(lean) != 2 || lean[0].Race != "Hyur" || lean[1].Race != "Roegadyn" {
		t.Fatalf("lean races = %+v, want Hyur then Roegadyn (game order)", lean)
	}
	roe := lean[1]
	if roe.Total != 10 || roe.Leader().Name != "Maelstrom" || !near(roe.Companies[0].Share, 0.8) || !near(roe.Companies[1].Share, 0.2) {
		t.Fatalf("Roegadyn lean = %+v", roe)
	}
	if len(roe.Companies) != 4 || roe.Companies[3].Name != "None" {
		t.Fatalf("Roegadyn companies = %+v", roe.Companies)
	}
}

func TestGrandCompaniesListsTheThreeCompaniesInGameOrder(t *testing.T) {
	want := []GrandCompany{
		{Name: "Maelstrom", Short: "Maelstrom", Slug: "maelstrom"},
		{Name: "Order of the Twin Adder", Short: "Twin Adder", Slug: "twin-adder"},
		{Name: "Immortal Flames", Short: "Immortal Flames", Slug: "immortal-flames"},
	}
	got := GrandCompanies()
	if len(got) != len(want) {
		t.Fatalf("GrandCompanies() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("GrandCompanies()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if GrandCompanyMaelstrom != want[0].Name || GrandCompanyTwinAdder != want[1].Name || GrandCompanyImmortalFlames != want[2].Name {
		t.Error("the company name constants disagree with the table")
	}
	got[0].Name = "changed"
	if GrandCompanies()[0].Name != "Maelstrom" {
		t.Error("GrandCompanies() exposes its backing table")
	}
}
