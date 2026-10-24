package census

import (
	"reflect"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestRolesAreTheArmouryOrder(t *testing.T) {
	want := []Role{
		{Code: RoleTank, Name: "Tank"},
		{Code: RoleHealer, Name: "Healer"},
		{Code: RoleMeleeDPS, Name: "Melee DPS"},
		{Code: RolePhysicalRangedDPS, Name: "Physical Ranged DPS"},
		{Code: RoleMagicalRangedDPS, Name: "Magical Ranged DPS"},
		{Code: RoleDisciplesOfTheHand, Name: "Disciples of the Hand"},
		{Code: RoleDisciplesOfTheLand, Name: "Disciples of the Land"},
	}
	if got := Roles(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Roles() = %v, want %v", got, want)
	}
	Roles()[0].Name = "Mutated"
	if Roles()[0].Name != "Tank" {
		t.Fatal("Roles() must return a copy")
	}
}

// armoury is the expected job table, in the game's Armoury order: official
// job id, abbreviation, name, role.
var armoury = []struct {
	id         uint8
	abbr, name string
	role       string
}{
	{19, "PLD", "Paladin", RoleTank},
	{21, "WAR", "Warrior", RoleTank},
	{32, "DRK", "Dark Knight", RoleTank},
	{37, "GNB", "Gunbreaker", RoleTank},
	{24, "WHM", "White Mage", RoleHealer},
	{28, "SCH", "Scholar", RoleHealer},
	{33, "AST", "Astrologian", RoleHealer},
	{40, "SGE", "Sage", RoleHealer},
	{20, "MNK", "Monk", RoleMeleeDPS},
	{22, "DRG", "Dragoon", RoleMeleeDPS},
	{30, "NIN", "Ninja", RoleMeleeDPS},
	{34, "SAM", "Samurai", RoleMeleeDPS},
	{39, "RPR", "Reaper", RoleMeleeDPS},
	{41, "VPR", "Viper", RoleMeleeDPS},
	{23, "BRD", "Bard", RolePhysicalRangedDPS},
	{31, "MCH", "Machinist", RolePhysicalRangedDPS},
	{38, "DNC", "Dancer", RolePhysicalRangedDPS},
	{25, "BLM", "Black Mage", RoleMagicalRangedDPS},
	{27, "SMN", "Summoner", RoleMagicalRangedDPS},
	{35, "RDM", "Red Mage", RoleMagicalRangedDPS},
	{42, "PCT", "Pictomancer", RoleMagicalRangedDPS},
	{36, "BLU", "Blue Mage", RoleMagicalRangedDPS},
	{8, "CRP", "Carpenter", RoleDisciplesOfTheHand},
	{9, "BSM", "Blacksmith", RoleDisciplesOfTheHand},
	{10, "ARM", "Armorer", RoleDisciplesOfTheHand},
	{11, "GSM", "Goldsmith", RoleDisciplesOfTheHand},
	{12, "LTW", "Leatherworker", RoleDisciplesOfTheHand},
	{13, "WVR", "Weaver", RoleDisciplesOfTheHand},
	{14, "ALC", "Alchemist", RoleDisciplesOfTheHand},
	{15, "CUL", "Culinarian", RoleDisciplesOfTheHand},
	{16, "MIN", "Miner", RoleDisciplesOfTheLand},
	{17, "BTN", "Botanist", RoleDisciplesOfTheLand},
	{18, "FSH", "Fisher", RoleDisciplesOfTheLand},
}

func TestJobsAreTheArmouryInOrder(t *testing.T) {
	jobs := Jobs()
	if len(jobs) != len(armoury) {
		t.Fatalf("Jobs() has %d jobs, want %d", len(jobs), len(armoury))
	}
	for i, want := range armoury {
		got := jobs[i]
		if got.ID != want.id || got.Abbreviation != want.abbr || got.Name != want.name || got.Role != want.role {
			t.Errorf("Jobs()[%d] = %+v, want %d %s %s %s", i, got, want.id, want.abbr, want.name, want.role)
		}
		if got.Order != i+1 {
			t.Errorf("Jobs()[%d].Order = %d, want %d", i, got.Order, i+1)
		}
		if byID, ok := JobByID(want.id); !ok || byID != got {
			t.Errorf("JobByID(%d) = %+v, %v; want %+v", want.id, byID, ok, got)
		}
	}
	Jobs()[0].Name = "Mutated"
	if Jobs()[0].Name != "Paladin" {
		t.Fatal("Jobs() must return a copy")
	}
	if _, ok := JobByID(1); ok {
		t.Fatal("JobByID(1) found a job; base classes are not jobs")
	}
}

func TestFoldJobID(t *testing.T) {
	cases := []struct {
		name    string
		classID uint8
		want    uint8
		ok      bool
	}{
		{"Gladiator", 1, 19, true},
		{"Pugilist", 2, 20, true},
		{"Marauder", 3, 21, true},
		{"Lancer", 4, 22, true},
		{"Archer", 5, 23, true},
		{"Conjurer", 6, 24, true},
		{"Thaumaturge", 7, 25, true},
		{"Arcanist", 26, 27, true}, // Summoner, not Scholar
		{"Rogue", 29, 30, true},
		{"Paladin", 19, 19, true},
		{"Scholar", 28, 28, true},
		{"Blue Mage", 36, 36, true},
		{"Fisher", 18, 18, true},
		{"unknown 0", 0, 0, false},
		{"unknown 43", 43, 0, false},
	}
	for _, c := range cases {
		got, ok := FoldJobID(c.classID)
		if got != c.want || ok != c.ok {
			t.Errorf("FoldJobID(%d %s) = %d, %v; want %d, %v", c.classID, c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestJobLevelCap(t *testing.T) {
	for _, j := range Jobs() {
		want := uint32(100)
		if j.ID == 36 {
			want = 80
		}
		if got := j.LevelCap(100); got != want {
			t.Errorf("%s.LevelCap(100) = %d, want %d", j.Abbreviation, got, want)
		}
	}
	blu, _ := JobByID(36)
	if got := blu.LevelCap(70); got != 70 {
		t.Errorf("BLU.LevelCap(70) = %d, want 70 (never above the configured maximum)", got)
	}
}

func TestJobMasks(t *testing.T) {
	bit := func(ids ...uint) uint64 {
		var m uint64
		for _, id := range ids {
			m |= 1 << id
		}
		return m
	}
	cases := []struct {
		name            string
		jobs            []contract.ClassJobRecord
		levelled, atCap uint64
	}{
		{name: "no jobs", jobs: nil, levelled: 0, atCap: 0},
		{
			name:     "job at cap and job below cap",
			jobs:     []contract.ClassJobRecord{{ClassJobID: 19, Level: 100}, {ClassJobID: 24, Level: 54}},
			levelled: bit(19, 24), atCap: bit(19),
		},
		{
			name:     "base classes fold into their jobs",
			jobs:     []contract.ClassJobRecord{{ClassJobID: 1, Level: 30}, {ClassJobID: 26, Level: 100}},
			levelled: bit(19, 27), atCap: bit(27),
		},
		{
			name:     "blue mage at its own cap",
			jobs:     []contract.ClassJobRecord{{ClassJobID: 36, Level: 80}},
			levelled: bit(36), atCap: bit(36),
		},
		{
			name:     "blue mage below its own cap",
			jobs:     []contract.ClassJobRecord{{ClassJobID: 36, Level: 79}},
			levelled: bit(36), atCap: 0,
		},
		{
			name:     "level zero and unknown ids are ignored",
			jobs:     []contract.ClassJobRecord{{ClassJobID: 21, Level: 0}, {ClassJobID: 63, Level: 100}},
			levelled: 0, atCap: 0,
		},
	}
	for _, c := range cases {
		levelled, atCap := JobMasks(c.jobs, 100)
		if levelled != c.levelled || atCap != c.atCap {
			t.Errorf("%s: JobMasks = %b, %b; want %b, %b", c.name, levelled, atCap, c.levelled, c.atCap)
		}
	}
}
