package census

import (
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

var armouryBuiltAt = time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)

// worldRow builds one world's job row from per-job [levelled, atCap] counts.
func worldRow(world string, characters, covered int64, counts map[uint8][2]int64) contract.WorldJobCounts {
	row := contract.WorldJobCounts{World: world, Characters: characters, Covered: covered}
	for _, j := range Jobs() {
		c := counts[j.ID]
		row.Levelled = append(row.Levelled, c[0])
		row.AtCap = append(row.AtCap, c[1])
	}
	return row
}

func armourySnapshot() *contract.UIStatsSnapshot {
	section := &contract.StatsJobsSection{BuiltAt: armouryBuiltAt}
	for _, j := range Jobs() {
		section.JobIDs = append(section.JobIDs, int(j.ID))
	}
	section.Worlds = []contract.WorldJobCounts{
		// Crystal (NA)
		worldRow("Balmung", 10, 8, map[uint8][2]int64{19: {6, 3}, 24: {4, 2}, 25: {5, 4}, 36: {2, 1}, 15: {3, 1}}),
		worldRow("Coeurl", 5, 5, map[uint8][2]int64{19: {1, 1}, 22: {3, 2}}),
		// Chaos (EU)
		worldRow("Ragnarok", 4, 4, map[uint8][2]int64{28: {2, 2}, 21: {1, 0}}),
	}
	return &contract.UIStatsSnapshot{SchemaVersion: 3, Jobs: section}
}

func jobIn(t *testing.T, a Armoury, abbreviation string) ArmouryJob {
	t.Helper()
	for _, r := range a.Roles {
		for _, j := range r.Jobs {
			if j.Abbreviation == abbreviation {
				return j
			}
		}
	}
	t.Fatalf("armoury has no %s", abbreviation)
	return ArmouryJob{}
}

func TestArmouryRollsWorldsUpToEveryScope(t *testing.T) {
	snapshot := armourySnapshot()
	tests := []struct {
		name                string
		scope               contract.StatsScope
		characters, covered int64
		pld                 [2]int64
	}{
		{"global", contract.StatsScope{}, 19, 17, [2]int64{7, 4}},
		{"region", contract.StatsScope{Region: "NA"}, 15, 13, [2]int64{7, 4}},
		{"datacenter", contract.StatsScope{Datacenter: "Crystal"}, 15, 13, [2]int64{7, 4}},
		{"world", contract.StatsScope{World: "coeurl"}, 5, 5, [2]int64{1, 1}},
		{"other region", contract.StatsScope{Region: "EU"}, 4, 4, [2]int64{0, 0}},
		{"unknown world", contract.StatsScope{World: "Nowhere"}, 0, 0, [2]int64{0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := ArmouryFor(snapshot, tt.scope)
			if a.Characters != tt.characters || a.Covered != tt.covered {
				t.Fatalf("coverage = %d of %d, want %d of %d", a.Covered, a.Characters, tt.covered, tt.characters)
			}
			pld := jobIn(t, a, "PLD")
			if [2]int64{pld.Levelled, pld.AtCap} != tt.pld {
				t.Fatalf("PLD = %d/%d, want %v", pld.Levelled, pld.AtCap, tt.pld)
			}
			if !a.BuiltAt.Equal(armouryBuiltAt) {
				t.Fatalf("built at %v, want the section's build time", a.BuiltAt)
			}
		})
	}
}

func TestArmouryGroupsEveryJobByRoleInArmouryOrder(t *testing.T) {
	a := ArmouryFor(armourySnapshot(), contract.StatsScope{})
	if len(a.Roles) != len(Roles()) {
		t.Fatalf("roles = %d, want every role group (%d)", len(a.Roles), len(Roles()))
	}
	var order []string
	jobs := 0
	for i, r := range a.Roles {
		if r.Code != Roles()[i].Code {
			t.Fatalf("role %d = %s, want %s", i, r.Code, Roles()[i].Code)
		}
		for _, j := range r.Jobs {
			if j.Role != r.Code {
				t.Fatalf("%s sits under %s", j.Abbreviation, r.Code)
			}
			order = append(order, j.Abbreviation)
			jobs++
		}
	}
	if jobs != len(Jobs()) {
		t.Fatalf("armoury shows %d jobs, want all %d (zero counts included)", jobs, len(Jobs()))
	}
	if order[0] != "PLD" || order[4] != "WHM" || order[len(order)-1] != "FSH" {
		t.Fatalf("order = %v, want the Armoury order", order)
	}
}

func TestArmouryShares(t *testing.T) {
	a := ArmouryFor(armourySnapshot(), contract.StatsScope{Datacenter: "Crystal"})
	blm := jobIn(t, a, "BLM")
	// Shares are of the covered characters (13 on Crystal).
	if !near(blm.Share(MeasureLevelled), 5.0/13) || !near(blm.Share(MeasureAtCap), 4.0/13) {
		t.Fatalf("BLM shares = %v / %v, want 5/13 and 4/13", blm.Share(MeasureLevelled), blm.Share(MeasureAtCap))
	}
	if blm.Count(MeasureLevelled) != 5 || blm.Count(MeasureAtCap) != 4 {
		t.Fatalf("BLM counts = %d / %d", blm.Count(MeasureLevelled), blm.Count(MeasureAtCap))
	}
	// Intensity is the share relative to the most popular job of the measure.
	pld := jobIn(t, a, "PLD")
	if !near(pld.Intensity(MeasureLevelled), 1) || !near(blm.Intensity(MeasureLevelled), 5.0/7) {
		t.Fatalf("intensity PLD %v BLM %v, want 1 and 5/7", pld.Intensity(MeasureLevelled), blm.Intensity(MeasureLevelled))
	}
	if !near(blm.Intensity(MeasureAtCap), 1) {
		t.Fatalf("at cap, BLM and PLD lead with 4: intensity %v, want 1", blm.Intensity(MeasureAtCap))
	}
}

func TestArmouryCoverage(t *testing.T) {
	a := ArmouryFor(armourySnapshot(), contract.StatsScope{Datacenter: "Crystal"})
	if a.FullyCovered() || !near(a.Coverage(), 13.0/15) {
		t.Fatalf("Crystal coverage = %v (full %v), want 13/15 and partial", a.Coverage(), a.FullyCovered())
	}
	if eu := ArmouryFor(armourySnapshot(), contract.StatsScope{Region: "EU"}); !eu.FullyCovered() {
		t.Fatal("EU is fully covered")
	}
	if none := ArmouryFor(armourySnapshot(), contract.StatsScope{World: "Nowhere"}); none.Coverage() != 0 || none.HasData() {
		t.Fatal("an empty selection has no coverage and no data")
	}
}

func TestArmouryRoleBalanceCountsAtCapCombatJobsWithoutBlueMage(t *testing.T) {
	a := ArmouryFor(armourySnapshot(), contract.StatsScope{Datacenter: "Crystal"})
	// Tank: PLD 4. Healer: WHM 2. DPS: BLM 4 + DRG 2 (BLU and CUL excluded).
	b := a.Balance
	if b.Tank != 4 || b.Healer != 2 || b.DPS != 6 || b.Total() != 12 {
		t.Fatalf("balance = %+v, want tank 4 healer 2 dps 6", b)
	}
	if !near(b.TankShare(), 4.0/12) || !near(b.HealerShare(), 2.0/12) || !near(b.DPSShare(), 6.0/12) {
		t.Fatalf("shares = %v %v %v", b.TankShare(), b.HealerShare(), b.DPSShare())
	}
}

func TestRoleBalanceByDatacenter(t *testing.T) {
	rows := RoleBalanceByDatacenter(armourySnapshot(), "")
	byDC := map[string]RoleBalance{}
	for _, r := range rows {
		byDC[r.Datacenter] = r.RoleBalance
	}
	if len(rows) != 2 {
		t.Fatalf("datacenters = %v, want only those with at-cap combat jobs (Crystal, Chaos)", rows)
	}
	if byDC["Chaos"] != (RoleBalance{Healer: 2}) || byDC["Crystal"] != (RoleBalance{Tank: 4, Healer: 2, DPS: 6}) {
		t.Fatalf("balances = %+v", byDC)
	}
	if rows[0].Region != "NA" || rows[0].Datacenter != "Crystal" {
		t.Fatalf("first row = %+v, want regions in census order (NA first)", rows[0])
	}
	if eu := RoleBalanceByDatacenter(armourySnapshot(), "eu"); len(eu) != 1 || eu[0].Datacenter != "Chaos" {
		t.Fatalf("EU rows = %+v, want Chaos only", eu)
	}
}

func TestArmouryMostLevelled(t *testing.T) {
	a := ArmouryFor(armourySnapshot(), contract.StatsScope{World: "Balmung"})
	top := a.MostLevelled(3)
	got := []string{}
	for _, j := range top {
		got = append(got, j.Abbreviation)
	}
	if len(got) != 3 || got[0] != "PLD" || got[1] != "BLM" || got[2] != "WHM" {
		t.Fatalf("most levelled = %v, want PLD, BLM, WHM", got)
	}
	// Ties keep the Armoury order; jobs nobody levelled are left out.
	if all := a.MostLevelled(50); len(all) != 5 {
		t.Fatalf("most levelled(50) = %d jobs, want the 5 levelled ones", len(all))
	}
}

func TestArmouryWithoutJobSection(t *testing.T) {
	a := ArmouryFor(&contract.UIStatsSnapshot{SchemaVersion: 3}, contract.StatsScope{})
	if a.HasData() || len(a.Roles) != 0 {
		t.Fatalf("no section, no armoury: %+v", a)
	}
	if SnapshotHasSection(&contract.UIStatsSnapshot{SchemaVersion: 3}, SectionJobs) {
		t.Fatal("a v3 snapshot without a job section does not have it")
	}
	if !SnapshotHasSection(armourySnapshot(), SectionJobs) {
		t.Fatal("a v3 snapshot with a job section has it")
	}
	if SnapshotHasSection(&contract.UIStatsSnapshot{SchemaVersion: 2, Jobs: armourySnapshot().Jobs}, SectionJobs) {
		t.Fatal("a v2 snapshot predates the job section")
	}
}

func TestValidateRejectsMalformedJobSection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*contract.StatsJobsSection)
	}{
		{"short row", func(s *contract.StatsJobsSection) { s.Worlds[0].Levelled = s.Worlds[0].Levelled[:3] }},
		{"negative count", func(s *contract.StatsJobsSection) { s.Worlds[0].AtCap[0] = -1 }},
		{"covered above characters", func(s *contract.StatsJobsSection) { s.Worlds[0].Covered = 11 }},
		{"at cap above levelled", func(s *contract.StatsJobsSection) { s.Worlds[0].AtCap[0] = 7 }},
		{"duplicate world", func(s *contract.StatsJobsSection) { s.Worlds[1].World = "Balmung" }},
		{"no build time", func(s *contract.StatsJobsSection) { s.BuiltAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := validUIStatsSnapshot()
			snapshot.Jobs = armourySnapshot().Jobs
			if err := ValidateUIStatsSnapshot(snapshot); err != nil {
				t.Fatalf("valid section rejected: %v", err)
			}
			tt.mutate(snapshot.Jobs)
			if err := ValidateUIStatsSnapshot(snapshot); err == nil {
				t.Fatal("malformed job section accepted")
			}
		})
	}
}
