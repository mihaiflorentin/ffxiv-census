package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

var _ contract.JobMaskBackfillRepository = (*CharacterRepository)(nil)

// BackfillJobMasksBatch implements contract.JobMaskBackfillRepository. The
// cursor row is locked for the batch, so two backfills never walk the same
// range; the masks are computed with the same taxonomy as the write path.
func (r *CharacterRepository) BackfillJobMasksBatch(ctx context.Context, limit int) (contract.JobMaskBackfillBatch, error) {
	var batch contract.JobMaskBackfillBatch
	if limit <= 0 {
		return batch, fmt.Errorf("job mask backfill: batch size must be positive, got %d", limit)
	}
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return batch, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return batch, fmt.Errorf("job mask backfill begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO job_mask_backfill_state (singleton, last_id) VALUES (TRUE, 0)
		 ON CONFLICT (singleton) DO NOTHING`); err != nil {
		return batch, fmt.Errorf("job mask backfill init cursor: %w", err)
	}
	var cursor int64
	if err := tx.QueryRowContext(ctx,
		`SELECT last_id FROM job_mask_backfill_state WHERE singleton FOR UPDATE`).Scan(&cursor); err != nil {
		return batch, fmt.Errorf("job mask backfill read cursor: %w", err)
	}

	walked, pending, err := walkCharacters(ctx, tx, cursor, limit)
	if err != nil {
		return batch, err
	}
	batch.Scanned = len(walked)
	batch.Done = len(walked) < limit
	if len(walked) > 0 {
		cursor = walked[len(walked)-1]
	}
	batch.Cursor = uint32(cursor)

	if len(pending) > 0 {
		filled, err := fillJobMasks(ctx, tx, pending, r.maxLevel)
		if err != nil {
			return batch, err
		}
		batch.Filled = filled
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE job_mask_backfill_state SET last_id = $1, updated_at = NOW() WHERE singleton`, cursor); err != nil {
		return batch, fmt.Errorf("job mask backfill advance cursor: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return batch, fmt.Errorf("job mask backfill commit: %w", err)
	}
	return batch, nil
}

// walkCharacters returns the next limit character IDs after cursor and,
// among them, the IDs whose masks are still NULL.
func walkCharacters(ctx context.Context, tx *sql.Tx, cursor int64, limit int) (walked, pending []int64, err error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, jobs_levelled_mask IS NULL OR jobs_at_cap_mask IS NULL
		   FROM characters WHERE id > $1 ORDER BY id LIMIT $2`, cursor, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("job mask backfill walk: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var missing bool
		if err := rows.Scan(&id, &missing); err != nil {
			return nil, nil, fmt.Errorf("job mask backfill scan: %w", err)
		}
		walked = append(walked, id)
		if missing {
			pending = append(pending, id)
		}
	}
	return walked, pending, rows.Err()
}

// fillJobMasks computes the masks of ids from their stored jobs and writes
// them where they are still NULL, so a character re-censused meanwhile keeps
// the masks its write set. It returns how many characters it filled.
func fillJobMasks(ctx context.Context, tx *sql.Tx, ids []int64, maxLevel uint32) (int, error) {
	jobs := make(map[int64][]contract.ClassJobRecord, len(ids))
	rows, err := tx.QueryContext(ctx,
		`SELECT character_id, class_job_id, level FROM character_jobs WHERE character_id = ANY($1::bigint[])`, ids)
	if err != nil {
		return 0, fmt.Errorf("job mask backfill read jobs: %w", err)
	}
	for rows.Next() {
		var charID int64
		var job contract.ClassJobRecord
		if err := rows.Scan(&charID, &job.ClassJobID, &job.Level); err != nil {
			rows.Close()
			return 0, fmt.Errorf("job mask backfill scan job: %w", err)
		}
		jobs[charID] = append(jobs[charID], job)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("job mask backfill read jobs: %w", err)
	}

	levelledMasks, atCapMasks := make([]int64, len(ids)), make([]int64, len(ids))
	for i, id := range ids {
		levelled, atCap := census.JobMasks(jobs[id], maxLevel)
		levelledMasks[i], atCapMasks[i] = int64(levelled), int64(atCap)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE characters c
		    SET jobs_levelled_mask = t.levelled, jobs_at_cap_mask = t.at_cap
		   FROM unnest($1::bigint[], $2::bigint[], $3::bigint[]) AS t(id, levelled, at_cap)
		  WHERE c.id = t.id AND (c.jobs_levelled_mask IS NULL OR c.jobs_at_cap_mask IS NULL)`,
		ids, levelledMasks, atCapMasks)
	if err != nil {
		return 0, fmt.Errorf("job mask backfill write: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("job mask backfill rows affected: %w", err)
	}
	return int(n), nil
}
