package contract

import (
	"context"
	"time"
)

// UIStatsSchemaVersion is the snapshot schema the refreshes write.
// UIStatsMinSchemaVersion is the oldest stored schema readers still accept:
// a v2 snapshot keeps the existing pages working until the next refresh,
// while the sections added in v3 report as missing (census.SnapshotHasSection).
const (
	UIStatsSchemaVersion    = 3
	UIStatsMinSchemaVersion = 2
)

// StatsScope identifies the aggregation scope. Empty fields mean global.
// At most one field is normally populated.
type StatsScope struct {
	Region     string `json:"region,omitempty"`
	Datacenter string `json:"datacenter,omitempty"`
	World      string `json:"world,omitempty"`
}

type StatsSummary struct {
	Total    int64 `json:"total"`
	Active   int64 `json:"active"`
	MaxLevel int64 `json:"max_level"`
	// Private and Unchecked are the achievement privacy counts (schema 3):
	// achievements hidden, and not hidden but no latest achievement recorded
	// yet. They are rebuilt nightly; see UIStatsSnapshot.PrivacyBuiltAt.
	Private   int64 `json:"private,omitempty"`
	Unchecked int64 `json:"unchecked,omitempty"`
	// InFreeCompany is the characters in any Free Company. Rebuilt nightly
	// with the Free Company boards; see UIStatsSnapshot.FreeCompaniesBuiltAt.
	InFreeCompany int64 `json:"in_free_company,omitempty"`
}

type ScopedGroupCount struct {
	Scope     StatsScope `json:"scope"`
	Dimension string     `json:"dimension"`
	Key       string     `json:"key"`
	Total     int64      `json:"total"`
	Active    int64      `json:"active"`
	// MaxLevel (highest job level at the configured maximum), Private and
	// Unchecked are set on the location groups only: the global-scope
	// region, datacenter and world groups (schema 3). See StatsSummary.
	MaxLevel      int64 `json:"max_level,omitempty"`
	Private       int64 `json:"private,omitempty"`
	Unchecked     int64 `json:"unchecked,omitempty"`
	InFreeCompany int64 `json:"in_free_company,omitempty"`
}

type ScopedExpansionCount struct {
	Scope     StatsScope `json:"scope"`
	Expansion string     `json:"expansion"`
	Count     int64      `json:"count"`
}

type ScopedDailyCount struct {
	Scope StatsScope `json:"scope"`
	Day   string     `json:"day"`
	Count int64      `json:"count"`
}

// UIStatsSnapshot is the complete, versioned read model used by aggregate UI
// and REST routes.
type UIStatsSnapshot struct {
	SchemaVersion    int                    `json:"schema_version"`
	GeneratedAt      time.Time              `json:"generated_at"`
	ActivitySince    time.Time              `json:"activity_since"`
	MaxLevel         uint32                 `json:"max_level"`
	SourceCharacters int64                  `json:"source_characters"`
	RefreshDuration  time.Duration          `json:"refresh_duration"`
	Groups           []ScopedGroupCount     `json:"groups"`
	Expansions       []ScopedExpansionCount `json:"expansions"`
	NewCharacters    []ScopedDailyCount     `json:"new_characters"`
	Summary          StatsSummary           `json:"summary"`
	// Jobs is the job section (schema 3); nil on snapshots built before it.
	Jobs *StatsJobsSection `json:"jobs,omitempty"`
	// PrivacyBuiltAt is when the full rebuild that produced the private and
	// unchecked counts ran. The incremental refresh carries it forward with
	// the counts; zero means the snapshot has no privacy figures.
	PrivacyBuiltAt time.Time `json:"privacy_built_at"`
	// FreeCompaniesBuiltAt is when the full rebuild that produced the Free
	// Company boards (their own row, see StatsFreeCompanies) and the in a Free
	// Company counts ran. The incremental refresh carries it forward; zero
	// means there are no Free Company figures yet.
	FreeCompaniesBuiltAt time.Time `json:"free_companies_built_at"`
}

// UIStatsRefreshMode selects how a refresh builds the snapshot.
type UIStatsRefreshMode string

const (
	// UIStatsRefreshFull rebuilds every figure from the characters table.
	UIStatsRefreshFull UIStatsRefreshMode = "full"
	// UIStatsRefreshIncremental adjusts the previous snapshot's group totals by
	// the characters first seen or soft-deleted since its watermark, and
	// recomputes the active and max-level counts exactly.
	UIStatsRefreshIncremental UIStatsRefreshMode = "incremental"
)

type UIStatsRefreshOptions struct {
	ActivitySince time.Time
	MaxLevel      uint32
	Timeout       time.Duration
	// Mode defaults to UIStatsRefreshFull. An incremental request falls back
	// to a full rebuild when there is no usable previous snapshot or the
	// incremental result fails validation.
	Mode UIStatsRefreshMode
}

type UIStatsRefreshResult struct {
	Snapshot     *UIStatsSnapshot
	Skipped      bool
	PayloadBytes int64
	// Mode is the mode actually used (full after a fallback).
	Mode UIStatsRefreshMode
}

// UIStatsRepository persists and refreshes the bounded analytics read model.
type UIStatsRepository interface {
	LoadCurrent(ctx context.Context) (*UIStatsSnapshot, error)
	Refresh(ctx context.Context, opts UIStatsRefreshOptions) (*UIStatsRefreshResult, error)
	// LoadFreeCompanies reads the Free Company boards the last full refresh
	// wrote; census.ErrUIStatsUnavailable when there are none yet.
	LoadFreeCompanies(ctx context.Context) (*StatsFreeCompanies, error)
}

// UIStatsObserver receives bounded-label cache and refresh measurements.
type UIStatsObserver interface {
	ObserveUIStatsCache(result string, snapshot *UIStatsSnapshot)
	ObserveUIStatsRefresh(result string, duration time.Duration, payloadBytes int64)
}
