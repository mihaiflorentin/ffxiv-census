package response

import "time"

type CensusSummary struct {
	TotalCharacters    int64   `json:"total_characters"`
	ActiveCharacters   int64   `json:"active_characters"`
	ActiveRatio        float64 `json:"active_ratio"`
	MaxLevelCharacters int64   `json:"max_level_characters"`
	// ActivityWindowDays is the activity window behind ActiveCharacters: a
	// character is active when its latest public achievement is this recent.
	ActivityWindowDays int `json:"activity_window_days"`
}

type CharacterListItem struct {
	ID                  uint32     `json:"id"`
	Name                string     `json:"name"`
	World               string     `json:"world"`
	Datacenter          string     `json:"datacenter"`
	Region              string     `json:"region"`
	Race                string     `json:"race"`
	Gender              uint8      `json:"gender"`
	FreeCompanyID       *string    `json:"free_company_id,omitempty"`
	FreeCompanyName     *string    `json:"free_company_name,omitempty"`
	AchievementsPrivate bool       `json:"achievements_private"`
	LatestAchievementID *uint32    `json:"latest_achievement_id,omitempty"`
	IsActive            bool       `json:"is_active"`
	FirstSeenAt         time.Time  `json:"first_seen_at"`
	LastCensusAt        *time.Time `json:"last_census_at,omitempty"`
}

type PaginatedCharacters struct {
	Items  []CharacterListItem `json:"items"`
	Total  int64               `json:"total"`
	Limit  int                 `json:"limit"`
	Offset int                 `json:"offset"`
}

type CharacterDetail struct {
	Character  CharacterListItem          `json:"character"`
	Jobs       []CharacterJobDetail       `json:"jobs"`
	Milestones []CharacterMilestoneDetail `json:"milestones"`
}

type CharacterJobDetail struct {
	ClassJobID uint8  `json:"class_job_id"`
	Name       string `json:"name"`
	Level      uint8  `json:"level"`
	ExpLevel   uint32 `json:"exp_level"`
}

type CharacterMilestoneDetail struct {
	AchievementID uint32    `json:"achievement_id"`
	AchievedAt    time.Time `json:"achieved_at"`
}

type BreakdownGroup struct {
	Key    string `json:"key"`
	Total  int64  `json:"total"`
	Active int64  `json:"active"`
}

type NewCharactersDay struct {
	Day   string `json:"day"`
	Count int64  `json:"count"`
}

type ExpansionStat struct {
	Expansion string `json:"expansion"`
	Count     int64  `json:"count"`
}

type QueueDepthItem struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}
