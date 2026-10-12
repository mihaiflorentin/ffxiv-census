package contract

import (
	"context"
	"time"
)

// CharacterFilter is an optional AND-combined filter for List/Count. Empty
// fields are ignored. World/Datacenter/Region/Race/GrandCompany/FreeCompanyID
// match exactly; Name is a case-insensitive substring match.
type CharacterFilter struct {
	World         string
	Datacenter    string
	Region        string
	Race          string
	Name          string
	GrandCompany  string
	FreeCompanyID string
	ActiveOnly    bool
	Since         *time.Time // when non-nil, only characters with latest_achievement_at >= Since
	MinLevel      uint32
	SortBy        string // "id", "name", "world", "created_at", "updated_at", "achievement_points"
	SortOrder     string // "asc", "desc"
}

// DemographicCounts holds tribe, gender, and race×gender breakdowns from a
// single query.
type DemographicCounts struct {
	Tribes      []GroupCount
	Genders     []GroupCount
	RaceGenders []GroupCount // Key format: "Race|Gender"
}

// CharacterWrite is one finished character result: the profile row and the
// full set of jobs the character currently has.
type CharacterWrite struct {
	Record CharacterRecord
	Jobs   []ClassJobRecord
}

// CharacterRepository persists character snapshots and their job levels.
type CharacterRepository interface {
	// Upsert replaces the character row and its jobs in one transaction.
	// first_seen_at is preserved on conflict; deleted_at is cleared.
	Upsert(ctx context.Context, rec CharacterRecord, jobs []ClassJobRecord) error
	// StoreResult stores a whole census result in ONE transaction: every
	// character and its jobs (each as Upsert does), then each achievement write
	// (milestones upserted additively, then the character's achievement
	// summary updated; a Private write only flags the character private).
	// Atomic: on error nothing in the result is committed. An empty result is
	// a no-op.
	StoreResult(ctx context.Context, result CensusResult) error
	// UpsertGear replaces all gear slots for a character in one transaction.
	UpsertGear(ctx context.Context, charID uint32, gear []CharacterGearRecord) error
	// GetGear returns the character's equipped gear slots (empty if none).
	GetGear(ctx context.Context, charID uint32) ([]CharacterGearRecord, error)
	// FindIDGapsAfter returns missing/unscanned ID ranges [[start, end], ...]
	// strictly after afterID and up to maxID, ordered ascending. A gap that
	// straddles afterID is trimmed so only its portion above afterID returns.
	FindIDGapsAfter(ctx context.Context, afterID, maxID uint32, limit int) ([][2]uint32, error)
	// FillGapsCursor returns the last ID queued by the fill-gaps scanner.
	// On first use it is initialized to 0 (nothing scanned yet).
	FillGapsCursor(ctx context.Context) (uint32, error)
	// AdvanceFillGapsCursor advances the cursor only when it still equals
	// expected. A stale advance must never move the cursor backward.
	AdvanceFillGapsCursor(ctx context.Context, expected, next uint32) error
	// Get returns the character, or nil (no error) if not found.
	Get(ctx context.Context, id uint32) (*CharacterRecord, error)
	// GetJobs returns the character's job levels (empty if none).
	GetJobs(ctx context.Context, id uint32) ([]ClassJobRecord, error)
	// MarkDeleted records that the character no longer exists on Lodestone.
	MarkDeleted(ctx context.Context, id uint32, at time.Time) error
	// ListStale returns up to limit characters ordered by last_census_at ASC
	// NULLS FIRST, id ASC. When cutoff is the zero time.Time the age predicate
	// is disabled and all non-deleted characters are eligible; a positive
	// cutoff filters to rows whose last_census_at is before the cutoff (NULL
	// last_census_at counts as stale in both modes).
	ListStale(ctx context.Context, cutoff time.Time, limit int) ([]CharacterRecord, error)
	// List returns up to limit non-deleted characters matching filter,
	// ordered by id, starting at offset (pagination for the REST API).
	List(ctx context.Context, filter CharacterFilter, limit, offset int) ([]CharacterRecord, error)
	// Stream iterates non-deleted characters matching filter ordered by id,
	// invoking fn for each record. Returning an error from fn halts the stream.
	Stream(ctx context.Context, filter CharacterFilter, fn func(rec CharacterRecord) error) error
	// Count returns the number of non-deleted characters matching filter.
	Count(ctx context.Context, filter CharacterFilter) (int64, error)
	// CountActive returns the number of non-deleted characters whose
	// latest_achievement_at is at or after since.
	CountActive(ctx context.Context, since time.Time) (int64, error)
	// SummaryCounts returns total, active (latest_achievement_at >= since), and
	// max-level (denormalized maximum job level >= maxLevel) counts in one query.
	SummaryCounts(ctx context.Context, since time.Time, maxLevel uint32) (total, active, maxLevelCount int64, err error)
	// Breakdown groups non-deleted characters by column (one of
	// race|world|datacenter|region), with total and active counts per group.
	// Active counts rows whose latest_achievement_at is at or after since.
	// Groups are ordered by total count descending.
	Breakdown(ctx context.Context, column string, since time.Time, filter CharacterFilter) ([]GroupCount, error)
	// MultiBreakdown returns group-by counts for multiple columns in a single
	// query using UNION ALL. Returns a map[column][]GroupCount. Supported
	// columns: race, world, datacenter, region.
	MultiBreakdown(ctx context.Context, columns []string, since time.Time, filter CharacterFilter) (map[string][]GroupCount, error)
	// DemographicBreakdown returns tribe, gender, and race×gender character
	// counts in a single query. RaceGenders keys use "Race|Gender" format.
	DemographicBreakdown(ctx context.Context, since time.Time, filter CharacterFilter) (*DemographicCounts, error)
	// NewPerDay returns non-deleted characters first seen in [since, until),
	// counted per UTC day, ordered ascending by day.
	NewPerDay(ctx context.Context, since, until time.Time, filter CharacterFilter) ([]DailyCount, error)
	// MaxID returns the maximum character ID in the repository (excluding deleted
	// characters), or 0 if no characters exist.
	MaxID(ctx context.Context) (uint32, error)
	// IDSweepCursor returns the next unscanned character ID. On first use it is
	// initialized to one past the highest stored character ID.
	IDSweepCursor(ctx context.Context) (uint32, error)
	// AdvanceIDSweepCursor advances the cursor only when it still equals
	// expected. A stale advance is idempotent when the stored cursor is already
	// at or beyond next, and must never move the cursor backward.
	AdvanceIDSweepCursor(ctx context.Context, expected, next uint32) error
}
