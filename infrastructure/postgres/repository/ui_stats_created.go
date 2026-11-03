package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

const (
	// creationBandWidth is how many character IDs one calibration band holds.
	creationBandWidth = 100_000
	// creationCalibrationStep is how many IDs below the exact cut each round
	// of the calibration reads, and creationCalibrationRounds how many rounds
	// it reads at most before the curve must reach the series' first day.
	creationCalibrationStep   = 2_000_000
	creationCalibrationRounds = 10
)

// readCreatedStats counts the created characters (UIStatsSnapshot.Created):
// every live character by the UTC day its ID was handed out, over the
// new-characters windows. From exactFromID on, a character is dated by its
// first sighting; below it by the calibration curve of the company chocobo
// dates (census.CreationAnchors), read in rounds down from the cut until it
// reaches the series' first day. Each world group gets its window totals and
// weekly series. Without a cut, or without a character at or above it,
// there are no created figures.
func readCreatedStats(ctx context.Context, tx *sql.Tx, snapshot *contract.UIStatsSnapshot, exactFromID, minSamples int64) error {
	snapshot.Created, snapshot.CreatedExactFrom, snapshot.CreatedExactFromID, snapshot.CreatedBuiltAt = nil, "", 0, time.Time{}
	worldGroups := make(map[string]int)
	for i, group := range snapshot.Groups {
		if group.Scope == (contract.StatsScope{}) && group.Dimension == "world" {
			worldGroups[group.Key] = i
			snapshot.Groups[i].Created, snapshot.Groups[i].CreatedPrevious, snapshot.Groups[i].CreatedWeekly = 0, 0, nil
		}
	}
	if exactFromID <= 0 {
		return nil
	}
	if minSamples <= 0 {
		minSamples = census.DefaultCreationMinSamples
	}
	var firstSeen time.Time
	err := tx.QueryRowContext(ctx, `SELECT first_seen_at FROM characters WHERE id >= $1 ORDER BY id LIMIT 1`, exactFromID).Scan(&firstSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read created-characters cut: %w", err)
	}
	exact := census.CreationAnchor{ID: exactFromID, At: firstSeen.UTC().Truncate(24 * time.Hour)}
	bounds := census.NewCharactersWindowBounds(snapshot.GeneratedAt, snapshot.NewCharactersWindowDays)

	var bands []census.CreationBand
	for upper, round := exactFromID, 0; upper > 0 && round < creationCalibrationRounds; round++ {
		lower := max(upper-creationCalibrationStep, 0) / creationBandWidth * creationBandWidth
		more, err := readCreationBands(ctx, tx, lower, upper)
		if err != nil {
			return err
		}
		bands = append(more, bands...)
		if anchors := census.CreationAnchors(bands, minSamples, exact); len(anchors) > 1 && !anchors[0].At.After(bounds.PreviousStart) {
			break
		}
		upper = lower
	}
	first, boundaries := census.CreationBoundaries(census.CreationAnchors(bands, minSamples, exact), bounds.PreviousStart, exact.At)
	lowest := exactFromID
	if len(boundaries) > 0 {
		lowest = boundaries[0]
	} else {
		boundaries = []int64{exactFromID}
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT COALESCE(world, ''),
		       CASE WHEN id >= $1 THEN TO_CHAR(first_seen_at AT TIME ZONE 'UTC', 'YYYY-MM-DD') ELSE '' END,
		       CASE WHEN id < $1 THEN width_bucket(id, $2::bigint[]) ELSE 0 END,
		       COUNT(*)
		FROM characters
		WHERE deleted_at IS NULL AND id >= $3
		  AND (id < $1 OR (first_seen_at >= $4 AND first_seen_at < $5))
		GROUP BY 1, 2, 3`,
		exactFromID, boundaries, lowest, bounds.PreviousStart, bounds.End)
	if err != nil {
		return fmt.Errorf("query created characters: %w", err)
	}
	defer rows.Close()
	daily := make(map[string]int64)
	for rows.Next() {
		var world, value string
		var bucket int
		var count int64
		if err := rows.Scan(&world, &value, &bucket, &count); err != nil {
			return fmt.Errorf("scan created characters: %w", err)
		}
		day := first.AddDate(0, 0, bucket-1)
		if bucket == 0 {
			if day, err = time.Parse("2006-01-02", value); err != nil {
				continue
			}
		}
		daily[day.Format("2006-01-02")] += count
		i, ok := worldGroups[world]
		if !ok {
			continue
		}
		group := &snapshot.Groups[i]
		switch {
		case bounds.InCurrent(day):
			group.Created += count
		case bounds.InPrevious(day):
			group.CreatedPrevious += count
		}
		if week := bounds.Week(day); week >= 0 {
			if group.CreatedWeekly == nil {
				group.CreatedWeekly = make([]int64, bounds.Weeks())
			}
			group.CreatedWeekly[week] += count
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate created characters: %w", err)
	}
	for _, day := range slices.Sorted(maps.Keys(daily)) {
		snapshot.Created = append(snapshot.Created, contract.ScopedDailyCount{Day: day, Count: daily[day]})
	}
	snapshot.CreatedExactFrom = exact.At.Format("2006-01-02")
	snapshot.CreatedExactFromID = exactFromID
	snapshot.CreatedBuiltAt = snapshot.GeneratedAt
	return nil
}

// readCreationBands reads the calibration bands of the character IDs
// [lower, upper): each band's company chocobo count and early (10th
// percentile) date.
func readCreationBands(ctx context.Context, tx *sql.Tx, lower, upper int64) ([]census.CreationBand, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT character_id / $3 AS band, COUNT(*),
		       percentile_disc(0.1) WITHIN GROUP (ORDER BY achieved_at)
		FROM character_milestones
		WHERE achievement_id = 590 AND character_id >= $1 AND character_id < $2
		GROUP BY 1
		ORDER BY 1`, lower, upper, creationBandWidth)
	if err != nil {
		return nil, fmt.Errorf("query creation calibration: %w", err)
	}
	defer rows.Close()
	var bands []census.CreationBand
	for rows.Next() {
		var band, samples int64
		var early time.Time
		if err := rows.Scan(&band, &samples, &early); err != nil {
			return nil, fmt.Errorf("scan creation calibration: %w", err)
		}
		bands = append(bands, census.CreationBand{FirstID: band * creationBandWidth, EndID: (band + 1) * creationBandWidth, Samples: samples, Early: early.UTC()})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate creation calibration: %w", err)
	}
	return bands, nil
}
