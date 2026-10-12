package contract

import "time"

// CharacterRecord is the persisted census snapshot of a character.
// ID is the Lodestone character ID. FreeCompanyID/Name are nil when the
// character is not in an FC. LatestAchievementID/At are nil until an
// achievement census has run. DeletedAt is non-nil once a 404 confirms the
// character no longer exists.
type CharacterRecord struct {
	ID                  uint32
	Name                string
	World               string
	Datacenter          string
	Region              string
	Race                string
	Tribe               string
	Gender              uint8
	GrandCompany        string
	FreeCompanyID       *string
	FreeCompanyName     *string
	Bio                 string
	ActiveJob           string
	ItemLevel           int
	AchievementsPrivate bool
	// AchievementsCheckedAt is when an achievement census last found the
	// character private, so the private flag was last verified. Nil until it
	// has; only the achievement census writes it, never a character refresh.
	AchievementsCheckedAt *time.Time
	LatestAchievementID   *uint32
	LatestAchievementAt   *time.Time
	FirstSeenAt           time.Time
	LastCensusAt          *time.Time
	DeletedAt             *time.Time
}

// CharacterGearRecord is one equipped gear slot snapshot for a character.
type CharacterGearRecord struct {
	CharacterID uint32
	Slot        string
	ItemID      uint32
	Name        string
	ItemLevel   int
	Dye         *string
	Materia     []string
	UpdatedAt   time.Time
}

// ClassJobRecord is one job/class level snapshot for a character.
type ClassJobRecord struct {
	CharacterID uint32
	ClassJobID  uint8
	Name        string
	Level       uint8
	ExpLevel    uint32
}

// MilestoneKind classifies a tracked achievement.
const (
	MilestoneKindExpansion string = "expansion_msq"
	MilestoneKindJobLevel  string = "job_level"
	MilestoneKindChocobo   string = "chocobo"
)

// MilestoneAchievement is a registry entry describing an achievement we track.
// Expansion is non-nil for expansion_msq milestones (e.g. "Heavensward").
type MilestoneAchievement struct {
	AchievementID uint32
	Kind          string
	Expansion     *string
	Detail        string
}

// CharacterMilestone is an achievement a character has earned that matches a
// registered milestone.
type CharacterMilestone struct {
	CharacterID   uint32
	AchievementID uint32
	AchievedAt    time.Time
}

// CensusRun is one census sweep for operational tracking.
type CensusRun struct {
	ID             int64
	StartedAt      time.Time
	FinishedAt     *time.Time
	CharactersSeen int
	NewCharacters  int
}

// GroupCount is one row of a group-by population aggregate (e.g. per-world).
type GroupCount struct {
	Key    string
	Total  int64
	Active int64
}

// DailyCount is one day's count in a time-series aggregate.
type DailyCount struct {
	Day   string // "2006-01-02"
	Count int64
}

// ExpansionCount is the number of characters who completed an expansion MSQ.
type ExpansionCount struct {
	Expansion string
	Count     int64
}
