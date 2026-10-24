package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

const uiStatsAdvisoryLockID int64 = 0x4646584956554953

// newCharactersLookbackDays is how far back the daily new-character series
// reaches: the 30-day headline window plus the 30-day comparison window.
const newCharactersLookbackDays = 60

type UIStatsRepository struct {
	driver contract.DatabaseDriver
}

func NewUIStatsRepository(driver contract.DatabaseDriver) contract.UIStatsRepository {
	return &UIStatsRepository{driver: driver}
}

func (r *UIStatsRepository) LoadCurrent(ctx context.Context) (*contract.UIStatsSnapshot, error) {
	row, err := r.driver.FetchOne(ctx, `
		SELECT schema_version, generated_at, activity_since, max_level,
		       source_character_count, refresh_duration_ms, payload
		FROM ui_stats_snapshots
		WHERE snapshot_key = 'current'`)
	if err != nil {
		return nil, err
	}
	var (
		schemaVersion                       int
		generatedAt, activitySince          time.Time
		maxLevel                            uint32
		sourceCharacters, refreshDurationMS int64
		payload                             []byte
	)
	if err := row.Scan(&schemaVersion, &generatedAt, &activitySince, &maxLevel, &sourceCharacters, &refreshDurationMS, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, census.ErrUIStatsUnavailable
		}
		return nil, err
	}
	var snapshot contract.UIStatsSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil, fmt.Errorf("decode UI statistics snapshot: %w", err)
	}
	snapshot.SchemaVersion = schemaVersion
	snapshot.GeneratedAt = generatedAt.UTC()
	snapshot.ActivitySince = activitySince.UTC()
	snapshot.MaxLevel = maxLevel
	snapshot.SourceCharacters = sourceCharacters
	snapshot.RefreshDuration = time.Duration(refreshDurationMS) * time.Millisecond
	if err := census.ValidateUIStatsSnapshot(&snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (r *UIStatsRepository) Refresh(ctx context.Context, opts contract.UIStatsRefreshOptions) (*contract.UIStatsRefreshResult, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	if opts.ActivitySince.IsZero() {
		return nil, errors.New("UI statistics activity cutoff is required")
	}
	if opts.MaxLevel == 0 {
		return nil, errors.New("UI statistics max level is required")
	}

	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, uiStatsAdvisoryLockID).Scan(&locked); err != nil {
		return nil, fmt.Errorf("acquire UI statistics refresh lock: %w", err)
	}
	if !locked {
		return &contract.UIStatsRefreshResult{Skipped: true}, nil
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, uiStatsAdvisoryLockID)
	}()

	started := time.Now()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, fmt.Errorf("begin UI statistics refresh: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The watermark is read first, in the same snapshot as the counts: a
	// character first seen or deleted at or before it is in this snapshot.
	var watermark time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&watermark); err != nil {
		return nil, fmt.Errorf("read UI statistics watermark: %w", err)
	}
	watermark = watermark.UTC()

	snapshot := &contract.UIStatsSnapshot{
		SchemaVersion: contract.UIStatsSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		ActivitySince: opts.ActivitySince.UTC(),
		MaxLevel:      opts.MaxLevel,
	}
	usedMode := contract.UIStatsRefreshFull
	if opts.Mode == contract.UIStatsRefreshIncremental {
		ok, err := r.readIncrementalCharacterStats(ctx, tx, snapshot, watermark)
		if err != nil {
			return nil, err
		}
		if ok {
			usedMode = contract.UIStatsRefreshIncremental
		} else {
			snapshot.Groups, snapshot.Summary, snapshot.SourceCharacters = nil, contract.StatsSummary{}, 0
		}
	}
	if usedMode == contract.UIStatsRefreshFull {
		if err := r.readCharacterStats(ctx, tx, snapshot); err != nil {
			return nil, err
		}
	}
	if err := r.readMilestoneStats(ctx, tx, snapshot); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit UI statistics read snapshot: %w", err)
	}

	snapshot.RefreshDuration = time.Since(started)
	if err := census.ValidateUIStatsSnapshot(snapshot); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode UI statistics snapshot: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO ui_stats_snapshots (
			snapshot_key, schema_version, generated_at, activity_since, max_level,
			source_character_count, refresh_duration_ms, payload, watermark_at
		) VALUES ('current', $1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (snapshot_key) DO UPDATE SET
			schema_version = excluded.schema_version,
			generated_at = excluded.generated_at,
			activity_since = excluded.activity_since,
			max_level = excluded.max_level,
			source_character_count = excluded.source_character_count,
			refresh_duration_ms = excluded.refresh_duration_ms,
			payload = excluded.payload,
			watermark_at = excluded.watermark_at`,
		snapshot.SchemaVersion, snapshot.GeneratedAt, snapshot.ActivitySince, snapshot.MaxLevel,
		snapshot.SourceCharacters, snapshot.RefreshDuration.Milliseconds(), payload, watermark); err != nil {
		return nil, fmt.Errorf("publish UI statistics snapshot: %w", err)
	}
	return &contract.UIStatsRefreshResult{
		Snapshot:     snapshot,
		PayloadBytes: int64(len(payload)),
		Mode:         usedMode,
	}, nil
}

func (r *UIStatsRepository) readCharacterStats(ctx context.Context, tx *sql.Tx, snapshot *contract.UIStatsSnapshot) error {
	// One hashed pass over live characters at the fine grain; every scope and
	// dimension is rolled up in Go.
	roll, err := queryFineGrain(ctx, tx, `
		SELECT `+fineGrainKeyColumns+`,
		       COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE latest_achievement_at >= $1) AS active,
		       COUNT(*) FILTER (WHERE max_job_level >= $2) AS max_level
		FROM characters
		WHERE deleted_at IS NULL
		GROUP BY `+fineGrainGroupBy, snapshot.ActivitySince, snapshot.MaxLevel)
	if err != nil {
		return fmt.Errorf("query UI character statistics: %w", err)
	}
	snapshot.Summary = roll.summary.statsSummary()
	snapshot.SourceCharacters = roll.summary.Total
	for _, key := range roll.sortedKeys() {
		counts := roll.groups[key]
		snapshot.Groups = append(snapshot.Groups, contract.ScopedGroupCount{
			Scope: key.Scope, Dimension: key.Dimension, Key: key.Key, Total: counts.Total, Active: counts.Active,
		})
	}
	return nil
}

// queryFineGrain runs a fine-grain statistics query (the fineGrainKeyColumns
// followed by the fineCounts columns) and rolls its rows up.
func queryFineGrain(ctx context.Context, tx *sql.Tx, query string, args ...any) (*statsRollup, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roll := newStatsRollup()
	for rows.Next() {
		var key fineKey
		var counts fineCounts
		if err := rows.Scan(&key.Region, &key.Datacenter, &key.World, &key.Race, &key.Tribe, &key.Gender,
			&counts.Total, &counts.Active, &counts.MaxLevel); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		roll.add(key, counts)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate: %w", err)
	}
	return roll, nil
}

// readIncrementalCharacterStats builds the character statistics from the
// previous snapshot: group totals move by the characters first seen and
// soft-deleted in (previous watermark, watermark], and the active and
// max-level counts are recomputed exactly from their indexed columns. Both
// queries return fine-grain rows rolled up by the same statsRollup as the
// full refresh. It reports false when there is no usable previous snapshot or
// the adjusted result is invalid, so the caller falls back to a full rebuild.
//
// Drift accepted until the next full rebuild: a character whose location or
// race changes after it was counted, a soft delete that is later reverted,
// and a character whose first_seen_at precedes the watermark but that commits
// after this read snapshot.
func (r *UIStatsRepository) readIncrementalCharacterStats(ctx context.Context, tx *sql.Tx, snapshot *contract.UIStatsSnapshot, watermark time.Time) (bool, error) {
	var (
		schemaVersion int
		prevWatermark sql.NullTime
		payload       []byte
	)
	err := tx.QueryRowContext(ctx, `
		SELECT schema_version, watermark_at, payload
		FROM ui_stats_snapshots
		WHERE snapshot_key = 'current'`).Scan(&schemaVersion, &prevWatermark, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load previous UI statistics snapshot: %w", err)
	}
	if schemaVersion != contract.UIStatsSchemaVersion || !prevWatermark.Valid || !prevWatermark.Time.Before(watermark) {
		return false, nil
	}
	var prev contract.UIStatsSnapshot
	if err := json.Unmarshal(payload, &prev); err != nil {
		return false, nil
	}

	totals := make(map[groupKey]int64, len(prev.Groups))
	for _, g := range prev.Groups {
		totals[groupKey{Scope: g.Scope, Dimension: g.Dimension, Key: g.Key}] = g.Total
	}

	delta, err := queryFineGrain(ctx, tx, `
		WITH delta AS (
			SELECT region, datacenter, world, race, tribe, gender, 1 AS sign
			FROM characters
			WHERE first_seen_at > $1 AND first_seen_at <= $2
			UNION ALL
			SELECT region, datacenter, world, race, tribe, gender, -1
			FROM characters
			WHERE deleted_at > $1 AND deleted_at <= $2
		)
		SELECT `+fineGrainKeyColumns+`,
		       SUM(sign) AS total, 0 AS active, 0 AS max_level
		FROM delta
		GROUP BY `+fineGrainGroupBy, prevWatermark.Time, watermark)
	if err != nil {
		return false, fmt.Errorf("query UI statistics delta: %w", err)
	}
	for key, counts := range delta.groups {
		totals[key] += counts.Total
	}
	summaryTotal := prev.Summary.Total + delta.summary.Total

	live, err := queryFineGrain(ctx, tx, `
		SELECT `+fineGrainKeyColumns+`,
		       COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE latest_achievement_at >= $1) AS active,
		       COUNT(*) FILTER (WHERE max_job_level >= $2) AS max_level
		FROM characters
		WHERE deleted_at IS NULL
		  AND (latest_achievement_at >= $1 OR max_job_level >= $2)
		GROUP BY `+fineGrainGroupBy, snapshot.ActivitySince, snapshot.MaxLevel)
	if err != nil {
		return false, fmt.Errorf("query UI active statistics: %w", err)
	}

	for key := range live.groups {
		if _, ok := totals[key]; !ok {
			totals[key] = 0 // an active group the totals do not know: invalid, forces a full rebuild
		}
	}
	for _, key := range sortGroupKeys(totals) {
		total, active := totals[key], live.groups[key].Active
		if total == 0 && active == 0 {
			continue
		}
		snapshot.Groups = append(snapshot.Groups, contract.ScopedGroupCount{
			Scope: key.Scope, Dimension: key.Dimension, Key: key.Key, Total: total, Active: active,
		})
	}
	snapshot.Summary = contract.StatsSummary{Total: summaryTotal, Active: live.summary.Active, MaxLevel: live.summary.MaxLevel}
	snapshot.SourceCharacters = summaryTotal

	if err := census.ValidateUIStatsSnapshot(snapshot); err != nil {
		return false, nil
	}
	return true, nil
}

func (r *UIStatsRepository) readMilestoneStats(ctx context.Context, tx *sql.Tx, snapshot *contract.UIStatsSnapshot) error {
	rows, err := tx.QueryContext(ctx, `
		WITH tracked AS MATERIALIZED (
			SELECT cm.character_id, cm.achievement_id, cm.achieved_at,
			       m.expansion, COALESCE(c.world, '') AS world
			FROM character_milestones cm
			JOIN milestone_achievements m ON m.achievement_id = cm.achievement_id
			JOIN characters c ON c.id = cm.character_id
			WHERE c.deleted_at IS NULL
			  AND (cm.achievement_id = 590 OR
			       ((m.kind = 'expansion_msq' OR m.kind = 'expansion') AND m.expansion IS NOT NULL))
		), expansion_stats AS (
			SELECT world, expansion, GROUPING(world) AS global_scope,
			       COUNT(DISTINCT character_id) AS count
			FROM tracked
			WHERE expansion IS NOT NULL
			GROUP BY GROUPING SETS ((expansion), (world, expansion))
		), daily_stats AS (
			SELECT world, TO_CHAR(achieved_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day,
			       GROUPING(world) AS global_scope,
			       COUNT(DISTINCT character_id) AS count
			FROM tracked
			WHERE achievement_id = 590 AND achieved_at >= $1
			GROUP BY GROUPING SETS (
				(TO_CHAR(achieved_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')),
				(world, TO_CHAR(achieved_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'))
			)
		)
		SELECT 'expansion' AS kind, world, expansion AS value, global_scope, count
		FROM expansion_stats
		UNION ALL
		SELECT 'daily' AS kind, world, day AS value, global_scope, count
		FROM daily_stats`,
		snapshot.GeneratedAt.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(newCharactersLookbackDays-1)))
	if err != nil {
		return fmt.Errorf("query UI milestone statistics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var world sql.NullString
		var value string
		var globalScope int
		var count int64
		if err := rows.Scan(&kind, &world, &value, &globalScope, &count); err != nil {
			return fmt.Errorf("scan UI milestone statistics: %w", err)
		}
		scope := contract.StatsScope{}
		if globalScope == 0 {
			if world.String == "" {
				continue
			}
			scope.World = world.String
		}
		switch kind {
		case "expansion":
			snapshot.Expansions = append(snapshot.Expansions, contract.ScopedExpansionCount{Scope: scope, Expansion: value, Count: count})
		case "daily":
			snapshot.NewCharacters = append(snapshot.NewCharacters, contract.ScopedDailyCount{Scope: scope, Day: value, Count: count})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate UI milestone statistics: %w", err)
	}
	return nil
}

var _ contract.UIStatsRepository = (*UIStatsRepository)(nil)
