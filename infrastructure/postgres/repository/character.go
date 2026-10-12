package repository

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// CharacterRepository is a PostgreSQL implementation of contract.CharacterRepository.
type CharacterRepository struct {
	driver contract.DatabaseDriver
}

func NewCharacterRepository(driver contract.DatabaseDriver) contract.CharacterRepository {
	return &CharacterRepository{driver: driver}
}

const characterColumns = `id, name, world, datacenter, region, race, tribe, gender, grand_company,
		        fc_id, fc_name, bio, active_job, item_level,
		        achievements_private, achievements_checked_at, latest_achievement_id, latest_achievement_at,
		        first_seen_at, last_census_at, deleted_at`

// Upsert stores one character and its jobs through StoreResult, the only write
// path for characters.
func (r *CharacterRepository) Upsert(ctx context.Context, rec contract.CharacterRecord, jobs []contract.ClassJobRecord) error {
	return r.StoreResult(ctx, contract.CensusResult{Characters: []contract.CharacterWrite{{Record: rec, Jobs: jobs}}})
}

// StoreResult stores the characters, jobs, milestones and achievement
// summaries of result in ONE transaction. An empty result is a no-op.
func (r *CharacterRepository) StoreResult(ctx context.Context, result contract.CensusResult) error {
	if result.Size() == 0 {
		return nil
	}
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("character upsert begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Census data may lose its last few commits in a Postgres crash (the next
	// census pass rewrites them) in exchange for not waiting on a WAL flush.
	// SET LOCAL scopes this to the transaction, so stats, leases and
	// migrations on the same pooled connection stay synchronous.
	if _, err := tx.ExecContext(ctx, `SET LOCAL synchronous_commit = off`); err != nil {
		return fmt.Errorf("character upsert set async commit: %w", err)
	}

	if err := storeBatch(ctx, tx, result); err != nil {
		return markInvalidData(err)
	}
	return markInvalidData(tx.Commit())
}

// storeBatch writes result inside tx with a fixed number of statements, however
// many characters it holds: one multi-row characters upsert, one jobs delete
// and one jobs upsert for all characters, then the achievements.
//
// Two pods storing overlapping IDs must take row locks in the same global
// order, or they deadlock. The character statement inserts its rows in
// ascending ID order; when achievements follow, the rows of every character
// the batch touches are locked first, in ascending ID order.
func storeBatch(ctx context.Context, tx *sql.Tx, result contract.CensusResult) error {
	achievements := slices.Clone(result.Achievements)
	slices.SortStableFunc(achievements, func(a, b contract.AchievementWrite) int {
		return cmp.Compare(a.CharacterID, b.CharacterID)
	})
	writes := lastPerCharacter(result.Characters)

	if len(achievements) > 0 {
		if err := lockCharacters(ctx, tx, writes, achievements); err != nil {
			return err
		}
	}
	if len(writes) > 0 {
		if err := upsertCharactersTx(ctx, tx, writes); err != nil {
			return err
		}
		if err := replaceJobsTx(ctx, tx, writes); err != nil {
			return err
		}
	}
	for _, a := range achievements {
		if err := storeAchievementTx(ctx, tx, a); err != nil {
			return err
		}
	}
	return nil
}

// lastPerCharacter returns the writes sorted by ascending character ID, with
// only the last write of a repeated ID: one INSERT ... ON CONFLICT statement
// cannot update the same row twice.
func lastPerCharacter(writes []contract.CharacterWrite) []contract.CharacterWrite {
	byID := make(map[uint32]contract.CharacterWrite, len(writes))
	for _, w := range writes {
		byID[w.Record.ID] = w
	}
	out := make([]contract.CharacterWrite, 0, len(byID))
	for _, w := range byID {
		out = append(out, w)
	}
	slices.SortFunc(out, func(a, b contract.CharacterWrite) int { return cmp.Compare(a.Record.ID, b.Record.ID) })
	return out
}

// lockCharacters locks the existing rows of every character the batch touches,
// in ascending ID order.
func lockCharacters(ctx context.Context, tx *sql.Tx, writes []contract.CharacterWrite, achievements []contract.AchievementWrite) error {
	ids := make([]int64, 0, len(writes)+len(achievements))
	for _, w := range writes {
		ids = append(ids, int64(w.Record.ID))
	}
	for _, a := range achievements {
		ids = append(ids, int64(a.CharacterID))
	}
	if _, err := tx.ExecContext(ctx,
		`SELECT id FROM characters WHERE id = ANY($1::bigint[]) ORDER BY id FOR UPDATE`, ids); err != nil {
		return fmt.Errorf("character lock rows: %w", err)
	}
	return nil
}

// markInvalidData joins contract.ErrInvalidData to errors that Postgres
// raised because of the written values: SQLSTATE class 22 (data exception,
// e.g. invalid byte sequence, value too long, out of range) and class 23
// (integrity constraint violation). Everything else (connection loss,
// cancelled or timed-out context, serialization failure, shutdown) is left
// untouched so batch writers treat it as transient.
func markInvalidData(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23")) {
		return errors.Join(contract.ErrInvalidData, err)
	}
	return err
}

// storeAchievementTx writes one achievement census inside tx, with the same
// effect as AchievementRepository.UpsertCharacterMilestones followed by the
// summary update (a private profile only flags the character private and
// records when it was verified; a zero CheckedAt keeps the recorded time).
func storeAchievementTx(ctx context.Context, tx *sql.Tx, a contract.AchievementWrite) error {
	if a.Private {
		var checkedAt *time.Time
		if !a.CheckedAt.IsZero() {
			checkedAt = &a.CheckedAt
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE characters
			    SET achievements_private = $1,
			        achievements_checked_at = COALESCE($2::timestamptz, achievements_checked_at)
			  WHERE id = $3`,
			boolInt(true), nullableTime(checkedAt), a.CharacterID); err != nil {
			return fmt.Errorf("character set achievements private: %w", err)
		}
		return nil
	}
	if query, args := milestoneUpsert(a.Milestones); query != "" {
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("milestones batch upsert: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE characters
		    SET achievements_private = $1,
		        latest_achievement_id = $2,
		        latest_achievement_at = $3
		  WHERE id = $4`,
		boolInt(false), nullableUint32(a.LatestID), nullableTime(a.LatestAt), a.CharacterID); err != nil {
		return fmt.Errorf("character update achievement summary: %w", err)
	}
	return nil
}

// characterColumnArrays holds the character rows of a batch as parallel column
// arrays, the shape unnest turns back into rows. Nullable columns are pointer
// slices so a nil element binds as SQL NULL.
type characterColumnArrays struct {
	ids, itemLevels                                    []int64
	genders, private, maxLevels                        []int32
	names, worlds, datacenters, regions, races, tribes []string
	companies, bios, activeJobs                        []string
	fcIDs, fcNames                                     []*string
	latestIDs                                          []*int64
	latestAts, lastCensuses, deleteds                  []*time.Time
	firstSeens                                         []time.Time
}

func toCharacterColumns(writes []contract.CharacterWrite) characterColumnArrays {
	var c characterColumnArrays
	for _, w := range writes {
		rec := w.Record
		c.ids = append(c.ids, int64(rec.ID))
		c.names = append(c.names, rec.Name)
		c.worlds = append(c.worlds, rec.World)
		c.datacenters = append(c.datacenters, rec.Datacenter)
		c.regions = append(c.regions, rec.Region)
		c.races = append(c.races, rec.Race)
		c.tribes = append(c.tribes, rec.Tribe)
		c.genders = append(c.genders, int32(rec.Gender))
		c.companies = append(c.companies, rec.GrandCompany)
		c.fcIDs = append(c.fcIDs, rec.FreeCompanyID)
		c.fcNames = append(c.fcNames, rec.FreeCompanyName)
		c.bios = append(c.bios, rec.Bio)
		c.activeJobs = append(c.activeJobs, rec.ActiveJob)
		c.itemLevels = append(c.itemLevels, int64(rec.ItemLevel))
		c.private = append(c.private, int32(boolInt(rec.AchievementsPrivate)))
		var latestID *int64
		if rec.LatestAchievementID != nil {
			v := int64(*rec.LatestAchievementID)
			latestID = &v
		}
		c.latestIDs = append(c.latestIDs, latestID)
		c.latestAts = append(c.latestAts, rec.LatestAchievementAt)
		c.firstSeens = append(c.firstSeens, rec.FirstSeenAt)
		c.lastCensuses = append(c.lastCensuses, rec.LastCensusAt)
		c.deleteds = append(c.deleteds, rec.DeletedAt)
		c.maxLevels = append(c.maxLevels, int32(maxJobLevel(w.Jobs)))
	}
	return c
}

// upsertCharactersTx writes the character rows of writes (unique IDs) as one
// multi-row upsert, inserted in the order given.
func upsertCharactersTx(ctx context.Context, tx *sql.Tx, writes []contract.CharacterWrite) error {
	c := toCharacterColumns(writes)
	const query = `INSERT INTO characters (
			id, name, world, datacenter, region, race, tribe, gender, grand_company,
			fc_id, fc_name, bio, active_job, item_level,
			achievements_private, latest_achievement_id, latest_achievement_at,
			first_seen_at, last_census_at, deleted_at, max_job_level
		)
		SELECT id, name, world, datacenter, region, race, tribe, gender, grand_company,
			fc_id, fc_name, bio, active_job, item_level,
			achievements_private, latest_achievement_id, latest_achievement_at,
			first_seen_at, last_census_at, deleted_at, max_job_level
		  FROM unnest($1::bigint[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[],
			$8::smallint[], $9::text[], $10::text[], $11::text[], $12::text[], $13::text[], $14::integer[],
			$15::smallint[], $16::integer[], $17::timestamptz[], $18::timestamptz[], $19::timestamptz[],
			$20::timestamptz[], $21::smallint[])
		       AS t(id, name, world, datacenter, region, race, tribe, gender, grand_company,
			fc_id, fc_name, bio, active_job, item_level,
			achievements_private, latest_achievement_id, latest_achievement_at,
			first_seen_at, last_census_at, deleted_at, max_job_level)
		 ORDER BY id
		ON CONFLICT (id) DO UPDATE SET
			name = excluded.name,
			world = excluded.world,
			datacenter = excluded.datacenter,
			region = excluded.region,
			race = excluded.race,
			tribe = excluded.tribe,
			gender = excluded.gender,
			grand_company = excluded.grand_company,
			fc_id = excluded.fc_id,
			fc_name = excluded.fc_name,
			bio = excluded.bio,
			active_job = excluded.active_job,
			item_level = excluded.item_level,
			achievements_private = CASE WHEN excluded.achievements_private = 1 THEN 1 ELSE characters.achievements_private END,
			latest_achievement_id = COALESCE(excluded.latest_achievement_id, characters.latest_achievement_id),
			latest_achievement_at = COALESCE(excluded.latest_achievement_at, characters.latest_achievement_at),
			last_census_at = excluded.last_census_at,
			deleted_at = NULL,
			max_job_level = excluded.max_job_level`

	if _, err := tx.ExecContext(ctx, query,
		c.ids, c.names, c.worlds, c.datacenters, c.regions, c.races, c.tribes, c.genders, c.companies,
		c.fcIDs, c.fcNames, c.bios, c.activeJobs, c.itemLevels,
		c.private, c.latestIDs, c.latestAts, c.firstSeens, c.lastCensuses, c.deleteds, c.maxLevels); err != nil {
		return fmt.Errorf("character upsert: %w", err)
	}
	return nil
}

// replaceJobsTx makes the stored jobs of every character in writes (unique
// IDs) match its job list: one statement removes the jobs a character no
// longer has, one upserts the rest in place.
func replaceJobsTx(ctx context.Context, tx *sql.Tx, writes []contract.CharacterWrite) error {
	// Empty, not nil: a nil slice binds as SQL NULL, which matches nothing.
	charIDs, jobChars := make([]int64, 0, len(writes)), []int64{}
	jobIDs, levels, names, exps := []int32{}, []int32{}, []string{}, []int64{}
	for _, w := range writes {
		charIDs = append(charIDs, int64(w.Record.ID))
		ids, ns, ls, es := jobColumns(w.Jobs)
		for range ids {
			jobChars = append(jobChars, int64(w.Record.ID))
		}
		jobIDs, names, levels, exps = append(jobIDs, ids...), append(names, ns...), append(levels, ls...), append(exps, es...)
	}

	// Remove only the jobs a character no longer has; the rest are updated in place below.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM character_jobs cj
		  WHERE cj.character_id = ANY($1::bigint[])
		    AND NOT EXISTS (SELECT 1 FROM unnest($2::bigint[], $3::integer[]) AS k(character_id, class_job_id)
		                     WHERE k.character_id = cj.character_id AND k.class_job_id = cj.class_job_id)`,
		charIDs, jobChars, jobIDs); err != nil {
		return fmt.Errorf("character upsert delete jobs: %w", err)
	}
	if len(jobChars) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO character_jobs (character_id, class_job_id, name, level, exp_level)
		 SELECT t.character_id, t.class_job_id, t.name, t.level, t.exp_level
		   FROM unnest($1::bigint[], $2::integer[], $3::text[], $4::integer[], $5::bigint[])
		        AS t(character_id, class_job_id, name, level, exp_level)
		  ORDER BY t.character_id, t.class_job_id
		 ON CONFLICT (character_id, class_job_id) DO UPDATE SET
			name = excluded.name,
			level = excluded.level,
			exp_level = excluded.exp_level`,
		jobChars, jobIDs, names, levels, exps); err != nil {
		return fmt.Errorf("character upsert insert jobs: %w", err)
	}
	return nil
}

// jobColumns splits jobs into parallel column arrays for unnest. A repeated
// class job ID keeps its last entry, because one INSERT ... ON CONFLICT
// statement cannot update the same row twice. The slices are never nil: a nil
// slice binds as SQL NULL, and "<> ALL(NULL)" would delete nothing.
func jobColumns(jobs []contract.ClassJobRecord) (ids []int32, names []string, levels []int32, exps []int64) {
	ids, names = make([]int32, 0, len(jobs)), make([]string, 0, len(jobs))
	levels, exps = make([]int32, 0, len(jobs)), make([]int64, 0, len(jobs))
	pos := make(map[uint8]int, len(jobs))
	for _, j := range jobs {
		i, seen := pos[j.ClassJobID]
		if !seen {
			i = len(ids)
			pos[j.ClassJobID] = i
			ids = append(ids, int32(j.ClassJobID))
			names = append(names, "")
			levels = append(levels, 0)
			exps = append(exps, 0)
		}
		names[i] = j.Name
		levels[i] = int32(j.Level)
		exps[i] = int64(j.ExpLevel)
	}
	return ids, names, levels, exps
}

func maxJobLevel(jobs []contract.ClassJobRecord) uint8 {
	var maxLevel uint8
	for _, job := range jobs {
		if job.Level > maxLevel {
			maxLevel = job.Level
		}
	}
	return maxLevel
}

func (r *CharacterRepository) UpsertGear(ctx context.Context, charID uint32, gear []contract.CharacterGearRecord) error {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("gear upsert begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM character_gear WHERE character_id = $1`, charID); err != nil {
		return fmt.Errorf("gear upsert delete: %w", err)
	}

	now := time.Now().UTC()
	for _, g := range gear {
		upAt := g.UpdatedAt
		if upAt.IsZero() {
			upAt = now
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO character_gear (character_id, slot, item_id, name, item_level, dye, materia, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 ON CONFLICT (character_id, slot) DO UPDATE SET
				item_id = excluded.item_id,
				name = excluded.name,
				item_level = excluded.item_level,
				dye = excluded.dye,
				materia = excluded.materia,
				updated_at = excluded.updated_at`,
			charID, g.Slot, g.ItemID, g.Name, g.ItemLevel, nullableString(g.Dye), strings.Join(g.Materia, ","), upAt); err != nil {
			return fmt.Errorf("gear upsert insert: %w", err)
		}
	}
	return tx.Commit()
}

func (r *CharacterRepository) GetGear(ctx context.Context, id uint32) ([]contract.CharacterGearRecord, error) {
	rows, err := r.driver.FetchMany(ctx,
		`SELECT character_id, slot, item_id, name, item_level, dye, materia, updated_at
		   FROM character_gear WHERE character_id = $1 ORDER BY slot`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []contract.CharacterGearRecord
	for rows.Next() {
		var g contract.CharacterGearRecord
		var dye, materia sql.NullString
		var upAt time.Time
		if err := rows.Scan(&g.CharacterID, &g.Slot, &g.ItemID, &g.Name, &g.ItemLevel, &dye, &materia, &upAt); err != nil {
			return nil, fmt.Errorf("gear scan: %w", err)
		}
		g.Dye = sqlStringPtr(dye)
		if materia.Valid && materia.String != "" {
			g.Materia = strings.Split(materia.String, ",")
		} else {
			g.Materia = []string{}
		}
		g.UpdatedAt = upAt
		out = append(out, g)
	}
	return out, rows.Err()
}

// FindIDGapsAfter returns missing/unscanned ID ranges strictly after afterID
// and up to maxID, ordered ascending. Gaps-and-islands over the stored IDs
// (seeded at 0 so the region below the lowest character is scannable); a gap
// straddling afterID is trimmed to its portion above afterID.
func (r *CharacterRepository) FindIDGapsAfter(ctx context.Context, afterID, maxID uint32, limit int) ([][2]uint32, error) {
	if maxID == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}

	// The anchor is the highest stored ID at or below the cursor (0 when none),
	// followed by stored IDs above it, so the scan starts at the cursor and
	// stops after limit gaps instead of walking every stored character.
	query := `
		WITH anchored AS (
			SELECT COALESCE(MAX(id), 0) AS id FROM characters WHERE id <= $2
			UNION ALL
			(SELECT id FROM characters WHERE id > $2 AND id <= $1 ORDER BY id)
		), gaps AS (
			SELECT id + 1 AS gap_start,
			       LEAST(COALESCE(LEAD(id) OVER (ORDER BY id) - 1, $1), $1) AS gap_end
			  FROM anchored
		)
		SELECT GREATEST(gap_start, $2 + 1), gap_end
		  FROM gaps
		 WHERE GREATEST(gap_start, $2 + 1) <= gap_end
		 LIMIT $3`

	rows, err := r.driver.FetchMany(ctx, query, maxID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("find id gaps after %d: %w", afterID, err)
	}
	defer rows.Close()

	minStart := uint64(afterID) + 1
	var gaps [][2]uint32
	for rows.Next() {
		var start, end int64
		if err := rows.Scan(&start, &end); err != nil {
			return nil, fmt.Errorf("scan id gap: %w", err)
		}
		s := uint64(start)
		if s < minStart {
			s = minStart
		}
		if s > uint64(end) {
			continue // empty gap, or empty after cursor trim
		}
		gaps = append(gaps, [2]uint32{uint32(s), uint32(end)})
		if len(gaps) >= limit {
			break
		}
	}
	return gaps, rows.Err()
}

// FillGapsCursor returns the last ID queued by the fill-gaps scanner. On
// first use the state row is created with 0 (nothing scanned yet).
func (r *CharacterRepository) FillGapsCursor(ctx context.Context) (uint32, error) {
	if _, err := r.driver.Execute(ctx, `
		INSERT INTO fill_gaps_state (singleton, last_queued_id)
		SELECT TRUE, 0
		ON CONFLICT (singleton) DO NOTHING`); err != nil {
		return 0, fmt.Errorf("initialize fill gaps cursor: %w", err)
	}
	row, err := r.driver.FetchOne(ctx, `SELECT last_queued_id FROM fill_gaps_state WHERE singleton = TRUE`)
	if err != nil {
		return 0, fmt.Errorf("fetch fill gaps cursor: %w", err)
	}
	var last int64
	if err := row.Scan(&last); err != nil {
		return 0, fmt.Errorf("scan fill gaps cursor: %w", err)
	}
	if last < 0 || last > int64(^uint32(0)) {
		return 0, fmt.Errorf("fill gaps cursor out of range: %d", last)
	}
	return uint32(last), nil
}

// AdvanceFillGapsCursor swaps the cursor from expected to next via a
// compare-and-set. A stale advance is idempotent when the stored cursor is
// already at or beyond next (never rewinds), and errors otherwise. next = 0
// is the legitimate wrap-around reset.
func (r *CharacterRepository) AdvanceFillGapsCursor(ctx context.Context, expected, next uint32) error {
	result, err := r.driver.Execute(ctx, `
		UPDATE fill_gaps_state
		SET last_queued_id = $1, updated_at = NOW()
		WHERE singleton = TRUE AND last_queued_id = $2`, next, expected)
	if err != nil {
		return fmt.Errorf("advance fill gaps cursor: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("advance fill gaps cursor rows affected: %w", err)
	}
	if rows == 1 {
		return nil
	}
	current, err := r.FillGapsCursor(ctx)
	if err != nil {
		return err
	}
	if current == expected || current >= next {
		return nil
	}
	return fmt.Errorf("fill gaps cursor changed concurrently: expected %d, current %d, next %d", expected, current, next)
}

func (r *CharacterRepository) Get(ctx context.Context, id uint32) (*contract.CharacterRecord, error) {
	row, err := r.driver.FetchOne(ctx,
		fmt.Sprintf(`SELECT %s FROM characters WHERE id = $1`, characterColumns), id)
	if err != nil {
		return nil, err
	}
	rec, err := scanCharacter(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return rec, nil
}

func (r *CharacterRepository) GetJobs(ctx context.Context, id uint32) ([]contract.ClassJobRecord, error) {
	rows, err := r.driver.FetchMany(ctx,
		`SELECT character_id, class_job_id, name, level, exp_level
		   FROM character_jobs WHERE character_id = $1 ORDER BY class_job_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []contract.ClassJobRecord
	for rows.Next() {
		var j contract.ClassJobRecord
		if err := rows.Scan(&j.CharacterID, &j.ClassJobID, &j.Name, &j.Level, &j.ExpLevel); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (r *CharacterRepository) MarkDeleted(ctx context.Context, id uint32, at time.Time) error {
	_, err := r.driver.Execute(ctx,
		`UPDATE characters SET deleted_at = $1 WHERE id = $2`, at, id)
	if err != nil {
		return fmt.Errorf("character mark deleted: %w", err)
	}
	return nil
}

// ListStaleIDs selects live characters through idx_characters_live_census, a
// partial index matching this ORDER BY, so the oldest rows come straight off
// the index instead of a sort of the whole table. Only ids are read.
func (r *CharacterRepository) ListStaleIDs(ctx context.Context, cutoff time.Time, limit int) ([]uint32, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	if cutoff.IsZero() {
		rows, err = r.driver.FetchMany(ctx,
			`SELECT id FROM characters
			  WHERE deleted_at IS NULL
			  ORDER BY last_census_at ASC NULLS FIRST, id ASC
			  LIMIT $1`, limit)
	} else {
		rows, err = r.driver.FetchMany(ctx,
			`SELECT id FROM characters
			  WHERE deleted_at IS NULL
			    AND (last_census_at < $1 OR last_census_at IS NULL)
			  ORDER BY last_census_at ASC NULLS FIRST, id ASC
			  LIMIT $2`, cutoff, limit)
	}
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

// ListDeletedIDs selects the re-probe batch through idx_characters_deleted_at,
// a partial index over deleted rows only. Only ids are read.
func (r *CharacterRepository) ListDeletedIDs(ctx context.Context, deletedBefore time.Time, limit int) ([]uint32, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.driver.FetchMany(ctx,
		`SELECT id FROM characters
		  WHERE deleted_at IS NOT NULL AND deleted_at < $1
		  ORDER BY deleted_at ASC, id ASC
		  LIMIT $2`, deletedBefore, limit)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

func scanIDs(rows *sql.Rows) ([]uint32, error) {
	defer rows.Close()
	var out []uint32
	for rows.Next() {
		var id uint32
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

var breakdownColumns = map[string]bool{"race": true, "world": true, "datacenter": true, "region": true}

const summaryCountsQuery = `SELECT
	COUNT(*) AS total,
	COUNT(*) FILTER (WHERE latest_achievement_at >= $1) AS active,
	COUNT(*) FILTER (WHERE max_job_level >= $2) AS max_level
FROM characters
WHERE deleted_at IS NULL`

func characterFilterWhere(f contract.CharacterFilter) (string, []any) {
	return characterFilterWhereWithStart(f, 1)
}

func characterFilterWhereWithStart(f contract.CharacterFilter, startIdx int) (string, []any) {
	var where []string
	var args []any

	addParam := func(clauseTpl string, val any) {
		paramIdx := startIdx + len(args)
		args = append(args, val)
		where = append(where, fmt.Sprintf(clauseTpl, paramIdx))
	}

	if f.World != "" {
		addParam("world = $%d", f.World)
	}
	if f.Datacenter != "" {
		addParam("datacenter = $%d", f.Datacenter)
	}
	if f.Region != "" {
		addParam("region = $%d", f.Region)
	}
	if f.Race != "" {
		addParam("race = $%d", f.Race)
	}
	if f.GrandCompany != "" {
		addParam("grand_company = $%d", f.GrandCompany)
	}
	if f.FreeCompanyID != "" {
		addParam("fc_id = $%d", f.FreeCompanyID)
	}
	if f.Name != "" {
		addParam("name ILIKE $%d", "%"+f.Name+"%")
	}
	if f.Since != nil {
		addParam("latest_achievement_at >= $%d", *f.Since)
	}
	if f.ActiveOnly {
		where = append(where, "deleted_at IS NULL")
	}
	if f.MinLevel > 0 {
		addParam("id IN (SELECT character_id FROM character_jobs WHERE level >= $%d)", f.MinLevel)
	}
	if len(where) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(where, " AND "), args
}

func characterOrderBy(sortBy, sortOrder string) string {
	var col string
	switch strings.ToLower(sortBy) {
	case "name":
		col = "name"
	case "world":
		col = "world"
	case "created_at":
		col = "first_seen_at"
	case "updated_at":
		col = "last_census_at"
	default:
		col = "id"
	}

	var dir string
	if strings.ToLower(sortOrder) == "desc" {
		dir = "DESC"
	} else {
		dir = "ASC"
	}
	return fmt.Sprintf("%s %s", col, dir)
}

func (r *CharacterRepository) List(ctx context.Context, f contract.CharacterFilter, limit, offset int) ([]contract.CharacterRecord, error) {
	where, args := characterFilterWhere(f)
	orderBy := characterOrderBy(f.SortBy, f.SortOrder)

	args = append(args, limit, offset)
	limitPos := len(args) - 1
	offsetPos := len(args)

	query := fmt.Sprintf(`SELECT %s FROM characters WHERE deleted_at IS NULL %s ORDER BY %s LIMIT $%d OFFSET $%d`,
		characterColumns, where, orderBy, limitPos, offsetPos)

	rows, err := r.driver.FetchMany(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []contract.CharacterRecord
	for rows.Next() {
		rec, err := scanCharacter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

func (r *CharacterRepository) Stream(ctx context.Context, f contract.CharacterFilter, fn func(rec contract.CharacterRecord) error) error {
	where, args := characterFilterWhere(f)
	query := fmt.Sprintf(`SELECT %s FROM characters WHERE deleted_at IS NULL %s ORDER BY id ASC`,
		characterColumns, where)

	rows, err := r.driver.FetchMany(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		rec, err := scanCharacter(rows)
		if err != nil {
			return err
		}
		if err := fn(*rec); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (r *CharacterRepository) Count(ctx context.Context, f contract.CharacterFilter) (int64, error) {
	where, args := characterFilterWhere(f)
	query := fmt.Sprintf(`SELECT COUNT(*) FROM characters WHERE deleted_at IS NULL %s`, where)
	row, err := r.driver.FetchOne(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *CharacterRepository) CountActive(ctx context.Context, since time.Time) (int64, error) {
	row, err := r.driver.FetchOne(ctx,
		`SELECT COUNT(*) FROM characters
		  WHERE latest_achievement_at >= $1 AND deleted_at IS NULL`, since)
	if err != nil {
		return 0, err
	}
	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (r *CharacterRepository) SummaryCounts(ctx context.Context, since time.Time, maxLevel uint32) (total, active, maxLevelCount int64, err error) {
	row, err := r.driver.FetchOne(ctx, summaryCountsQuery, since, maxLevel)
	if err != nil {
		return 0, 0, 0, err
	}
	if err := row.Scan(&total, &active, &maxLevelCount); err != nil {
		return 0, 0, 0, err
	}
	return total, active, maxLevelCount, nil
}

func (r *CharacterRepository) Breakdown(ctx context.Context, column string, since time.Time, f contract.CharacterFilter) ([]contract.GroupCount, error) {
	if !breakdownColumns[column] {
		return nil, fmt.Errorf("invalid breakdown column %q", column)
	}
	filterWhere, filterArgs := characterFilterWhereWithStart(f, 2)

	args := []any{since}
	args = append(args, filterArgs...)
	// In PostgreSQL, COUNT(*) FILTER (WHERE ...) is native and fast
	query := fmt.Sprintf(`SELECT %s AS key,
	                             COUNT(*) AS total,
	                             COUNT(*) FILTER (WHERE latest_achievement_at >= $1) AS active
	                        FROM characters
	                       WHERE deleted_at IS NULL %s
	                       GROUP BY %s
	                       ORDER BY total DESC`, column, filterWhere, column)

	rows, err := r.driver.FetchMany(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []contract.GroupCount
	for rows.Next() {
		var g contract.GroupCount
		var key sql.NullString
		if err := rows.Scan(&key, &g.Total, &g.Active); err != nil {
			return nil, err
		}
		if key.Valid {
			g.Key = key.String
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (r *CharacterRepository) MultiBreakdown(ctx context.Context, columns []string, since time.Time, f contract.CharacterFilter) (map[string][]contract.GroupCount, error) {
	if len(columns) == 0 {
		return map[string][]contract.GroupCount{}, nil
	}
	for _, col := range columns {
		if !breakdownColumns[col] {
			return nil, fmt.Errorf("invalid breakdown column %q", col)
		}
	}

	filterWhere, filterArgs := characterFilterWhereWithStart(f, 2)
	args := []any{since}
	args = append(args, filterArgs...)

	var unions []string
	for _, col := range columns {
		unions = append(unions, fmt.Sprintf(
			`SELECT '%s' AS dimension, %s AS key, COUNT(*) AS total,
			        COUNT(*) FILTER (WHERE latest_achievement_at >= $1) AS active
			   FROM characters WHERE deleted_at IS NULL %s
			  GROUP BY %s`, col, col, filterWhere, col,
		))
	}
	query := strings.Join(unions, " UNION ALL ")

	rows, err := r.driver.FetchMany(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string][]contract.GroupCount)
	for rows.Next() {
		var dimension string
		var g contract.GroupCount
		var key sql.NullString
		if err := rows.Scan(&dimension, &key, &g.Total, &g.Active); err != nil {
			return nil, err
		}
		if key.Valid {
			g.Key = key.String
		}
		out[dimension] = append(out[dimension], g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *CharacterRepository) DemographicBreakdown(ctx context.Context, since time.Time, f contract.CharacterFilter) (*contract.DemographicCounts, error) {
	// Build filter clauses with unique parameter indices for each UNION branch.
	// Branch 1 (tribe): $1 = since, $2..N = filter params
	filterWhere1, filterArgs1 := characterFilterWhereWithStart(f, 2)
	// Branch 2 (gender): params continue after branch 1
	offset2 := 2 + len(filterArgs1)
	filterWhere2, filterArgs2 := characterFilterWhereWithStart(f, offset2)
	// Branch 3 (race_gender): params continue after branch 2
	offset3 := offset2 + len(filterArgs2)
	filterWhere3, filterArgs3 := characterFilterWhereWithStart(f, offset3)

	args := []any{since}
	args = append(args, filterArgs1...)
	args = append(args, filterArgs2...)
	args = append(args, filterArgs3...)

	query := fmt.Sprintf(`
		SELECT 'tribe' AS dimension, tribe AS key, COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE latest_achievement_at >= $1) AS active
		  FROM characters WHERE deleted_at IS NULL AND tribe != '' %s
		  GROUP BY tribe
		UNION ALL
		SELECT 'gender' AS dimension,
		       CASE gender WHEN 1 THEN 'Male' WHEN 2 THEN 'Female' ELSE 'Unknown' END AS key,
		       COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE latest_achievement_at >= $1) AS active
		  FROM characters WHERE deleted_at IS NULL %s
		  GROUP BY gender
		UNION ALL
		SELECT 'race_gender' AS dimension,
		       race || '|' || CASE gender WHEN 1 THEN 'Male' WHEN 2 THEN 'Female' ELSE 'Unknown' END AS key,
		       COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE latest_achievement_at >= $1) AS active
		  FROM characters WHERE deleted_at IS NULL AND race != '' %s
		  GROUP BY race, gender`, filterWhere1, filterWhere2, filterWhere3)

	rows, err := r.driver.FetchMany(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := &contract.DemographicCounts{}
	for rows.Next() {
		var dimension string
		var g contract.GroupCount
		var key sql.NullString
		if err := rows.Scan(&dimension, &key, &g.Total, &g.Active); err != nil {
			return nil, err
		}
		if key.Valid {
			g.Key = key.String
		}
		switch dimension {
		case "tribe":
			out.Tribes = append(out.Tribes, g)
		case "gender":
			out.Genders = append(out.Genders, g)
		case "race_gender":
			out.RaceGenders = append(out.RaceGenders, g)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *CharacterRepository) NewPerDay(ctx context.Context, since, until time.Time, f contract.CharacterFilter) ([]contract.DailyCount, error) {
	filterWhere, filterArgs := characterFilterWhereWithStart(f, 3)

	args := []any{since, until}
	args = append(args, filterArgs...)
	query := fmt.Sprintf(`SELECT TO_CHAR(first_seen_at, 'YYYY-MM-DD') AS day,
	                             COUNT(*) AS count
	                        FROM characters
	                       WHERE first_seen_at >= $1 AND first_seen_at < $2
	                         AND deleted_at IS NULL %s
	                       GROUP BY day
	                       ORDER BY day ASC`, filterWhere)

	rows, err := r.driver.FetchMany(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []contract.DailyCount
	for rows.Next() {
		var d contract.DailyCount
		if err := rows.Scan(&d.Day, &d.Count); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *CharacterRepository) MaxID(ctx context.Context) (uint32, error) {
	row, err := r.driver.FetchOne(ctx, `SELECT COALESCE(MAX(id), 0) FROM characters`)
	if err != nil {
		return 0, err
	}
	var maxID int64
	if err := row.Scan(&maxID); err != nil {
		return 0, err
	}
	return uint32(maxID), nil
}

func (r *CharacterRepository) IDSweepCursor(ctx context.Context) (uint32, error) {
	if _, err := r.driver.Execute(ctx, `
		INSERT INTO id_sweep_state (singleton, next_id)
		SELECT TRUE, COALESCE(MAX(id), 0) + 1 FROM characters
		ON CONFLICT (singleton) DO NOTHING`); err != nil {
		return 0, fmt.Errorf("initialize id sweep cursor: %w", err)
	}
	row, err := r.driver.FetchOne(ctx, `SELECT next_id FROM id_sweep_state WHERE singleton = TRUE`)
	if err != nil {
		return 0, fmt.Errorf("fetch id sweep cursor: %w", err)
	}
	var nextID int64
	if err := row.Scan(&nextID); err != nil {
		return 0, fmt.Errorf("scan id sweep cursor: %w", err)
	}
	if nextID < 1 || nextID > int64(^uint32(0)) {
		return 0, fmt.Errorf("id sweep cursor out of range: %d", nextID)
	}
	return uint32(nextID), nil
}

func (r *CharacterRepository) AdvanceIDSweepCursor(ctx context.Context, expected, next uint32) error {
	if expected == 0 || next <= expected {
		return fmt.Errorf("invalid id sweep cursor advance: %d -> %d", expected, next)
	}
	result, err := r.driver.Execute(ctx, `
		UPDATE id_sweep_state
		SET next_id = $1, updated_at = NOW()
		WHERE singleton = TRUE AND next_id = $2`, next, expected)
	if err != nil {
		return fmt.Errorf("advance id sweep cursor: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("advance id sweep cursor rows affected: %w", err)
	}
	if rows == 1 {
		return nil
	}
	current, err := r.IDSweepCursor(ctx)
	if err != nil {
		return err
	}
	if current >= next {
		return nil
	}
	return fmt.Errorf("id sweep cursor changed concurrently: expected %d, current %d, next %d", expected, current, next)
}

func scanCharacter(row rowScanner) (*contract.CharacterRecord, error) {
	var c contract.CharacterRecord
	var race, tribe, grandCompany, fcID, fcName sql.NullString
	var bio, activeJob sql.NullString
	var itemLevel sql.NullInt64
	var achievementsPrivate int
	var latestAchievementID sql.NullInt64
	var latestAchievementAt, achievementsCheckedAt, lastCensusAt, deletedAt sql.NullTime
	var firstSeenAt time.Time

	err := row.Scan(
		&c.ID, &c.Name, &c.World, &c.Datacenter, &c.Region,
		&race, &tribe, &c.Gender, &grandCompany,
		&fcID, &fcName, &bio, &activeJob, &itemLevel,
		&achievementsPrivate, &achievementsCheckedAt, &latestAchievementID, &latestAchievementAt,
		&firstSeenAt, &lastCensusAt, &deletedAt,
	)
	if err != nil {
		return nil, err
	}

	c.Race = race.String
	c.Tribe = tribe.String
	c.GrandCompany = grandCompany.String
	c.FreeCompanyID = sqlStringPtr(fcID)
	c.FreeCompanyName = sqlStringPtr(fcName)
	c.Bio = bio.String
	c.ActiveJob = activeJob.String
	if itemLevel.Valid {
		c.ItemLevel = int(itemLevel.Int64)
	}
	c.AchievementsPrivate = achievementsPrivate == 1
	c.AchievementsCheckedAt = sqlTimePtr(achievementsCheckedAt)
	c.LatestAchievementID = sqlUint32Ptr(latestAchievementID)
	c.LatestAchievementAt = sqlTimePtr(latestAchievementAt)
	c.FirstSeenAt = firstSeenAt
	c.LastCensusAt = sqlTimePtr(lastCensusAt)
	c.DeletedAt = sqlTimePtr(deletedAt)

	return &c, nil
}
