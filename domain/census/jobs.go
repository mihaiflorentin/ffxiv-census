package census

import "github.com/mihaiflorentin88/ffxiv-census/port/contract"

// The job taxonomy: every job by its official job id, with its abbreviation,
// name, role group and Armoury display order, plus the rules for counting a
// character's jobs. The character write path, the job bitmask backfill and
// the Jobs pages all read these from here, so they count jobs the same way.

// Role group codes, in the Armoury's order.
const (
	RoleTank               = "tank"
	RoleHealer             = "healer"
	RoleMeleeDPS           = "melee"
	RolePhysicalRangedDPS  = "physical-ranged"
	RoleMagicalRangedDPS   = "magical-ranged"
	RoleLimited            = "limited"
	RoleDisciplesOfTheHand = "hand"
	RoleDisciplesOfTheLand = "land"
)

// DefaultMaxLevel is the level cap used when none is configured.
const DefaultMaxLevel uint32 = 100

// BlueMageLevelCap is the level cap of Blue Mage, a limited job that stays
// below the game's level cap (80 since patch 6.45, unchanged in Dawntrail).
const BlueMageLevelCap uint32 = 80

// BeastmasterLevelCap is the level cap of Beastmaster, the limited job
// added in patch 7.56.
const BeastmasterLevelCap uint32 = 50

// Role is one role group: its code and display name.
type Role struct {
	Code string
	Name string
}

var roles = []Role{
	{Code: RoleTank, Name: "Tank"},
	{Code: RoleHealer, Name: "Healer"},
	{Code: RoleMeleeDPS, Name: "Melee DPS"},
	{Code: RolePhysicalRangedDPS, Name: "Physical Ranged DPS"},
	{Code: RoleMagicalRangedDPS, Name: "Magical Ranged DPS"},
	{Code: RoleLimited, Name: "Limited Jobs"},
	{Code: RoleDisciplesOfTheHand, Name: "Disciples of the Hand"},
	{Code: RoleDisciplesOfTheLand, Name: "Disciples of the Land"},
}

// Roles returns every role group in the Armoury's order.
func Roles() []Role {
	return append([]Role(nil), roles...)
}

// Job is one job. ID is its official job id, which is also its bit position
// in a job bitmask. Order is its 1-based Armoury position.
type Job struct {
	ID           uint8
	Abbreviation string
	Name         string
	Role         string
	Order        int
	// cap is the job's own level cap; 0 means the configured maximum level.
	cap uint32
}

// LevelCap returns the job's level cap given the configured maximum level:
// the maximum itself, or a limited job's lower cap (never above the maximum).
func (j Job) LevelCap(maxLevel uint32) uint32 {
	if j.cap > 0 && j.cap < maxLevel {
		return j.cap
	}
	return maxLevel
}

var jobs = func() []Job {
	list := []Job{
		{ID: 19, Abbreviation: "PLD", Name: "Paladin", Role: RoleTank},
		{ID: 21, Abbreviation: "WAR", Name: "Warrior", Role: RoleTank},
		{ID: 32, Abbreviation: "DRK", Name: "Dark Knight", Role: RoleTank},
		{ID: 37, Abbreviation: "GNB", Name: "Gunbreaker", Role: RoleTank},
		{ID: 24, Abbreviation: "WHM", Name: "White Mage", Role: RoleHealer},
		{ID: 28, Abbreviation: "SCH", Name: "Scholar", Role: RoleHealer},
		{ID: 33, Abbreviation: "AST", Name: "Astrologian", Role: RoleHealer},
		{ID: 40, Abbreviation: "SGE", Name: "Sage", Role: RoleHealer},
		{ID: 20, Abbreviation: "MNK", Name: "Monk", Role: RoleMeleeDPS},
		{ID: 22, Abbreviation: "DRG", Name: "Dragoon", Role: RoleMeleeDPS},
		{ID: 30, Abbreviation: "NIN", Name: "Ninja", Role: RoleMeleeDPS},
		{ID: 34, Abbreviation: "SAM", Name: "Samurai", Role: RoleMeleeDPS},
		{ID: 39, Abbreviation: "RPR", Name: "Reaper", Role: RoleMeleeDPS},
		{ID: 41, Abbreviation: "VPR", Name: "Viper", Role: RoleMeleeDPS},
		{ID: 23, Abbreviation: "BRD", Name: "Bard", Role: RolePhysicalRangedDPS},
		{ID: 31, Abbreviation: "MCH", Name: "Machinist", Role: RolePhysicalRangedDPS},
		{ID: 38, Abbreviation: "DNC", Name: "Dancer", Role: RolePhysicalRangedDPS},
		{ID: 25, Abbreviation: "BLM", Name: "Black Mage", Role: RoleMagicalRangedDPS},
		{ID: 27, Abbreviation: "SMN", Name: "Summoner", Role: RoleMagicalRangedDPS},
		{ID: 35, Abbreviation: "RDM", Name: "Red Mage", Role: RoleMagicalRangedDPS},
		{ID: 42, Abbreviation: "PCT", Name: "Pictomancer", Role: RoleMagicalRangedDPS},
		{ID: 36, Abbreviation: "BLU", Name: "Blue Mage", Role: RoleLimited, cap: BlueMageLevelCap},
		{ID: 43, Abbreviation: "BST", Name: "Beastmaster", Role: RoleLimited, cap: BeastmasterLevelCap},
		{ID: 8, Abbreviation: "CRP", Name: "Carpenter", Role: RoleDisciplesOfTheHand},
		{ID: 9, Abbreviation: "BSM", Name: "Blacksmith", Role: RoleDisciplesOfTheHand},
		{ID: 10, Abbreviation: "ARM", Name: "Armorer", Role: RoleDisciplesOfTheHand},
		{ID: 11, Abbreviation: "GSM", Name: "Goldsmith", Role: RoleDisciplesOfTheHand},
		{ID: 12, Abbreviation: "LTW", Name: "Leatherworker", Role: RoleDisciplesOfTheHand},
		{ID: 13, Abbreviation: "WVR", Name: "Weaver", Role: RoleDisciplesOfTheHand},
		{ID: 14, Abbreviation: "ALC", Name: "Alchemist", Role: RoleDisciplesOfTheHand},
		{ID: 15, Abbreviation: "CUL", Name: "Culinarian", Role: RoleDisciplesOfTheHand},
		{ID: 16, Abbreviation: "MIN", Name: "Miner", Role: RoleDisciplesOfTheLand},
		{ID: 17, Abbreviation: "BTN", Name: "Botanist", Role: RoleDisciplesOfTheLand},
		{ID: 18, Abbreviation: "FSH", Name: "Fisher", Role: RoleDisciplesOfTheLand},
	}
	for i := range list {
		list[i].Order = i + 1
	}
	return list
}()

// jobByID indexes jobs by official job id.
var jobByID = func() map[uint8]Job {
	m := make(map[uint8]Job, len(jobs))
	for _, j := range jobs {
		m[j.ID] = j
	}
	return m
}()

// classJob folds each base class into the job it becomes. Arcanist leads to
// both Summoner and Scholar; it counts towards Summoner, the job that shares
// its level on the Lodestone.
var classJob = map[uint8]uint8{
	1:  19, // Gladiator -> Paladin
	2:  20, // Pugilist -> Monk
	3:  21, // Marauder -> Warrior
	4:  22, // Lancer -> Dragoon
	5:  23, // Archer -> Bard
	6:  24, // Conjurer -> White Mage
	7:  25, // Thaumaturge -> Black Mage
	26: 27, // Arcanist -> Summoner
	29: 30, // Rogue -> Ninja
}

// Jobs returns every job in the Armoury's order (grouped by role).
func Jobs() []Job {
	return append([]Job(nil), jobs...)
}

// JobByID returns the job with an official job id. Base classes are not jobs;
// fold them first with FoldJobID.
func JobByID(id uint8) (Job, bool) {
	j, ok := jobByID[id]
	return j, ok
}

// FoldJobID returns the job a class or job id counts towards: a base class
// folds into its job, a job is itself. It reports false for an unknown id.
func FoldJobID(classJobID uint8) (uint8, bool) {
	if id, ok := classJob[classJobID]; ok {
		return id, true
	}
	if _, ok := jobByID[classJobID]; ok {
		return classJobID, true
	}
	return 0, false
}

// JobMasks returns a character's two job bitmasks: the jobs it has levelled
// (level 1 or more) and the jobs it has at their level cap. The bit position
// is the folded official job id. Unknown ids and level 0 entries are ignored.
func JobMasks(classJobs []contract.ClassJobRecord, maxLevel uint32) (levelled, atCap uint64) {
	for _, cj := range classJobs {
		id, ok := FoldJobID(cj.ClassJobID)
		if !ok || cj.Level == 0 {
			continue
		}
		levelled |= 1 << id
		if uint32(cj.Level) >= jobByID[id].LevelCap(maxLevel) {
			atCap |= 1 << id
		}
	}
	return levelled, atCap
}
