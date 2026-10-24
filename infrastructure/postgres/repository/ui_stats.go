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
	if opts.NewCharactersWindowDays <= 0 {
		return nil, errors.New("UI statistics new-characters window is required")
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
		SchemaVersion:           contract.UIStatsSchemaVersion,
		GeneratedAt:             time.Now().UTC(),
		ActivitySince:           opts.ActivitySince.UTC(),
		NewCharactersWindowDays: opts.NewCharactersWindowDays,
		MaxLevel:                opts.MaxLevel,
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
	var freeCompanies *contract.StatsFreeCompanies
	var freeCompaniesDuration time.Duration
	if usedMode == contract.UIStatsRefreshFull {
		passStarted := time.Now()
		freeCompanies, err = readFreeCompanyBoards(ctx, tx, snapshot.ActivitySince, snapshot.GeneratedAt)
		if err != nil {
			return nil, err
		}
		freeCompaniesDuration = time.Since(passStarted)
		snapshot.FreeCompaniesBuiltAt = snapshot.GeneratedAt
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
	// The full refresh publishes the main snapshot and the Free Company
	// boards together; the incremental refresh leaves the boards' row alone.
	publish, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin UI statistics publish: %w", err)
	}
	defer func() { _ = publish.Rollback() }()
	if _, err := publish.ExecContext(ctx, `
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
	if freeCompanies != nil {
		if err := publishFreeCompanyBoards(ctx, publish, snapshot, freeCompanies, freeCompaniesDuration); err != nil {
			return nil, err
		}
	}
	if err := publish.Commit(); err != nil {
		return nil, fmt.Errorf("commit UI statistics publish: %w", err)
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
	// The job columns make each fine-grain group about 2 KB of aggregate
	// state (about 21 MB for the demo's 8.8k groups); give the hash
	// aggregate room to stay in memory instead of spilling to disk.
	if _, err := tx.ExecContext(ctx, `SET LOCAL work_mem = '64MB'`); err != nil {
		return fmt.Errorf("size UI statistics aggregate memory: %w", err)
	}
	jobs := newJobAccumulator()
	roll, err := queryFineGrainWithExtraColumns(ctx, tx, jobs, `
		SELECT `+fineGrainKeyColumns+`,
		       COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE latest_achievement_at >= $1) AS active,
		       COUNT(*) FILTER (WHERE max_job_level >= $2) AS max_level,
		       `+privacyCountColumns+`,
		       `+inFreeCompanyColumn+`,
		       `+jobFineColumns+`
		FROM characters
		WHERE deleted_at IS NULL
		GROUP BY `+fineGrainGroupBy, snapshot.ActivitySince, snapshot.MaxLevel)
	if err != nil {
		return fmt.Errorf("query UI character statistics: %w", err)
	}
	snapshot.Jobs = jobs.section(snapshot.GeneratedAt)
	snapshot.Summary = roll.summary.statsSummary()
	snapshot.SourceCharacters = roll.summary.Total
	snapshot.PrivacyBuiltAt = snapshot.GeneratedAt
	for _, key := range roll.sortedKeys() {
		snapshot.Groups = append(snapshot.Groups, scopedGroupCount(key, roll.groups[key]))
	}
	return nil
}

// queryFineGrain runs a fine-grain statistics query (the fineGrainKeyColumns
// followed by the fineCounts columns) and rolls its rows up.
func queryFineGrain(ctx context.Context, tx *sql.Tx, query string, args ...any) (*statsRollup, error) {
	return queryFineGrainWithExtraColumns(ctx, tx, nil, query, args...)
}

// queryFineGrainWithExtraColumns is queryFineGrain for a query whose rows carry extra
// columns after the fineCounts columns; extra scans and keeps them.
func queryFineGrainWithExtraColumns(ctx context.Context, tx *sql.Tx, extra extraColumnScanner, query string, args ...any) (*statsRollup, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roll := newStatsRollup()
	for rows.Next() {
		var key fineKey
		var counts fineCounts
		dest := []any{&key.Region, &key.Datacenter, &key.World, &key.Race, &key.Tribe, &key.Gender, &key.GrandCompany,
			&counts.Total, &counts.Active, &counts.MaxLevel, &counts.Private, &counts.Unchecked, &counts.InFreeCompany}
		if extra != nil {
			dest = append(dest, extra.dest()...)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		roll.add(key, counts)
		if extra != nil {
			extra.add(key, counts)
		}
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
// The privacy counts (private, unchecked) are carried forward from the
// previous snapshot and move by the same delta, read from each new or deleted
// character's current row; PrivacyBuiltAt keeps the full rebuild's time.
//
// Drift accepted until the next full rebuild: a character whose location,
// race, Grand Company or achievement privacy changes after it was counted, a soft delete that is later reverted,
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
	// A snapshot built with a different new-characters window needs a full
	// rebuild, so a changed window takes effect at once.
	if prev.NewCharactersWindowDays != snapshot.NewCharactersWindowDays {
		return false, nil
	}
	// Likewise a changed activity window: the Free Company boards' active
	// counts only move on a full rebuild.
	if census.SnapshotActivityWindowDays(&prev) != census.SnapshotActivityWindowDays(snapshot) {
		return false, nil
	}
	// The privacy counts can only be carried forward from a snapshot that has
	// them (one written before they existed has no build time).
	if prev.PrivacyBuiltAt.IsZero() {
		return false, nil
	}
	// The job section is carried forward unchanged, with its build time; a
	// previous snapshot without one needs a full rebuild.
	if prev.Jobs == nil {
		return false, nil
	}
	snapshot.Jobs = prev.Jobs
	// The Free Company boards (their own row) and the in a Free Company
	// counts wait for the nightly rebuild; a previous snapshot without them
	// needs a full rebuild.
	if prev.FreeCompaniesBuiltAt.IsZero() {
		return false, nil
	}
	snapshot.FreeCompaniesBuiltAt = prev.FreeCompaniesBuiltAt

	// carried holds the figures that move by the delta: every group's total
	// and the location groups' privacy counts. The in a Free Company counts
	// ride along unchanged (the delta adds none).
	carried := make(map[groupKey]fineCounts, len(prev.Groups))
	for _, g := range prev.Groups {
		carried[groupKey{Scope: g.Scope, Dimension: g.Dimension, Key: g.Key}] = fineCounts{Total: g.Total, Private: g.Private, Unchecked: g.Unchecked, InFreeCompany: g.InFreeCompany}
	}

	delta, err := queryFineGrain(ctx, tx, `
		WITH delta AS (
			SELECT region, datacenter, world, race, tribe, gender, grand_company,
			       achievements_private, latest_achievement_at, 1 AS sign
			FROM characters
			WHERE first_seen_at > $1 AND first_seen_at <= $2
			UNION ALL
			SELECT region, datacenter, world, race, tribe, gender, grand_company,
			       achievements_private, latest_achievement_at, -1
			FROM characters
			WHERE deleted_at > $1 AND deleted_at <= $2
		)
		SELECT `+fineGrainKeyColumns+`,
		       SUM(sign) AS total, 0 AS active, 0 AS max_level,
		       COALESCE(SUM(sign) FILTER (WHERE achievements_private = 1), 0) AS private,
		       COALESCE(SUM(sign) FILTER (WHERE achievements_private = 0 AND latest_achievement_at IS NULL), 0) AS unchecked,
		       0 AS in_free_company
		FROM delta
		GROUP BY `+fineGrainGroupBy, prevWatermark.Time, watermark)
	if err != nil {
		return false, fmt.Errorf("query UI statistics delta: %w", err)
	}
	for key, counts := range delta.groups {
		carried[key] = carried[key].plus(counts)
	}
	summary := fineCounts{Total: prev.Summary.Total, Private: prev.Summary.Private, Unchecked: prev.Summary.Unchecked, InFreeCompany: prev.Summary.InFreeCompany}.plus(delta.summary)

	live, err := queryFineGrain(ctx, tx, `
		SELECT `+fineGrainKeyColumns+`,
		       COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE latest_achievement_at >= $1) AS active,
		       COUNT(*) FILTER (WHERE max_job_level >= $2) AS max_level,
		       0 AS private, 0 AS unchecked, 0 AS in_free_company
		FROM characters
		WHERE deleted_at IS NULL
		  AND (latest_achievement_at >= $1 OR max_job_level >= $2)
		GROUP BY `+fineGrainGroupBy, snapshot.ActivitySince, snapshot.MaxLevel)
	if err != nil {
		return false, fmt.Errorf("query UI active statistics: %w", err)
	}

	for key := range live.groups {
		if _, ok := carried[key]; !ok {
			carried[key] = fineCounts{} // an active group the totals do not know: invalid, forces a full rebuild
		}
	}
	for _, key := range sortGroupKeys(carried) {
		counts := carried[key]
		counts.Active, counts.MaxLevel = live.groups[key].Active, live.groups[key].MaxLevel
		if counts.Total == 0 && counts.Active == 0 {
			continue
		}
		snapshot.Groups = append(snapshot.Groups, scopedGroupCount(key, counts))
	}
	summary.Active, summary.MaxLevel = live.summary.Active, live.summary.MaxLevel
	snapshot.Summary = summary.statsSummary()
	snapshot.SourceCharacters = summary.Total
	snapshot.PrivacyBuiltAt = prev.PrivacyBuiltAt

	if err := census.ValidateUIStatsSnapshot(snapshot); err != nil {
		return false, nil
	}
	return true, nil
}

// readMilestoneStats reads the MSQ completions (whole game and per world)
// and the new-character figures: the daily series over the current and the
// previous new-characters window at whole-game scope, and each world's
// totals for both windows on its world group. Both windows end with the last
// complete UTC day before the generation day.
func (r *UIStatsRepository) readMilestoneStats(ctx context.Context, tx *sql.Tx, snapshot *contract.UIStatsSnapshot) error {
	bounds := census.NewCharactersWindowBounds(snapshot.GeneratedAt, snapshot.NewCharactersWindowDays)
	rows, err := tx.QueryContext(ctx, `
		WITH tracked AS MATERIALIZED (
			SELECT cm.character_id, cm.achievement_id, cm.achieved_at,
			       m.expansion, COALESCE(c.world, '') AS world
			FROM character_milestones cm
			JOIN milestone_achievements m ON m.achievement_id = cm.achievement_id
			JOIN characters c ON c.id = cm.character_id
			WHERE c.deleted_at IS NULL
			  AND ((cm.achievement_id = 590 AND cm.achieved_at >= $1 AND cm.achieved_at < $3) OR
			       ((m.kind = 'expansion_msq' OR m.kind = 'expansion') AND m.expansion IS NOT NULL))
		), expansion_stats AS (
			SELECT world, expansion, GROUPING(world) AS global_scope,
			       COUNT(DISTINCT character_id) AS count
			FROM tracked
			WHERE expansion IS NOT NULL
			GROUP BY GROUPING SETS ((expansion), (world, expansion))
		), daily_stats AS (
			SELECT TO_CHAR(achieved_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day,
			       COUNT(DISTINCT character_id) AS count
			FROM tracked
			WHERE achievement_id = 590
			GROUP BY 1
		), world_window_stats AS (
			SELECT world,
			       COUNT(DISTINCT character_id) FILTER (WHERE achieved_at >= $2) AS count,
			       COUNT(DISTINCT character_id) FILTER (WHERE achieved_at < $2) AS previous
			FROM tracked
			WHERE achievement_id = 590
			GROUP BY world
		)
		SELECT 'expansion' AS kind, world, expansion AS value, global_scope, count, 0::bigint AS previous
		FROM expansion_stats
		UNION ALL
		SELECT 'daily', NULL, day, 1, count, 0
		FROM daily_stats
		UNION ALL
		SELECT 'world_window', world, '', 0, count, previous
		FROM world_window_stats`,
		bounds.PreviousStart, bounds.CurrentStart, bounds.End)
	if err != nil {
		return fmt.Errorf("query UI milestone statistics: %w", err)
	}
	defer rows.Close()
	worldGroups := make(map[string]int)
	for i, group := range snapshot.Groups {
		if group.Scope == (contract.StatsScope{}) && group.Dimension == "world" {
			worldGroups[group.Key] = i
			snapshot.Groups[i].NewCharacters, snapshot.Groups[i].NewCharactersPrevious = 0, 0
		}
	}
	for rows.Next() {
		var kind string
		var world sql.NullString
		var value string
		var globalScope int
		var count, previous int64
		if err := rows.Scan(&kind, &world, &value, &globalScope, &count, &previous); err != nil {
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
			snapshot.NewCharacters = append(snapshot.NewCharacters, contract.ScopedDailyCount{Day: value, Count: count})
		case "world_window":
			// A world without a group has no live characters to count.
			if i, ok := worldGroups[scope.World]; ok {
				snapshot.Groups[i].NewCharacters, snapshot.Groups[i].NewCharactersPrevious = count, previous
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate UI milestone statistics: %w", err)
	}
	return nil
}

var _ contract.UIStatsRepository = (*UIStatsRepository)(nil)
