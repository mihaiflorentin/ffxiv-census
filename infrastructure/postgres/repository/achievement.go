package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// AchievementRepository is a PostgreSQL implementation of contract.AchievementRepository.
type AchievementRepository struct {
	driver contract.DatabaseDriver
}

func NewAchievementRepository(driver contract.DatabaseDriver) contract.AchievementRepository {
	return &AchievementRepository{driver: driver}
}

func (r *AchievementRepository) SyncMilestones(ctx context.Context, registry []contract.MilestoneAchievement) error {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sync milestones begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, m := range registry {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO milestone_achievements (achievement_id, kind, expansion, detail)
			 VALUES ($1, $2, $3, $4)
			 ON CONFLICT (achievement_id) DO UPDATE SET
				kind = excluded.kind,
				expansion = excluded.expansion,
				detail = excluded.detail`,
			m.AchievementID, m.Kind, nullableString(m.Expansion), m.Detail); err != nil {
			return fmt.Errorf("sync milestone %d: %w", m.AchievementID, err)
		}
	}
	return tx.Commit()
}

func (r *AchievementRepository) ListMilestones(ctx context.Context) ([]contract.MilestoneAchievement, error) {
	rows, err := r.driver.FetchMany(ctx,
		`SELECT achievement_id, kind, expansion, detail
		   FROM milestone_achievements
		  ORDER BY achievement_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []contract.MilestoneAchievement
	for rows.Next() {
		var m contract.MilestoneAchievement
		var exp sql.NullString
		if err := rows.Scan(&m.AchievementID, &m.Kind, &exp, &m.Detail); err != nil {
			return nil, err
		}
		m.Expansion = sqlStringPtr(exp)
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpsertCharacterMilestones additively inserts or updates a character's earned
// milestones in one batch. The ON CONFLICT clause makes it idempotent.
func (r *AchievementRepository) UpsertCharacterMilestones(ctx context.Context, characterID uint32, milestones []contract.CharacterMilestone) error {
	query, args := milestoneUpsert(milestones)
	if query == "" {
		return nil
	}

	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("milestones batch upsert: %w", err)
	}
	return nil
}

// milestoneUpsert builds the single multi-row INSERT that additively stores
// milestones; the ON CONFLICT clause makes it idempotent. It returns an empty
// query when there is nothing to store.
func milestoneUpsert(milestones []contract.CharacterMilestone) (string, []any) {
	if len(milestones) == 0 {
		return "", nil
	}
	var buf strings.Builder
	buf.WriteString(`INSERT INTO character_milestones (character_id, achievement_id, achieved_at) VALUES `)
	args := make([]any, 0, len(milestones)*3)
	for i, m := range milestones {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, "($%d,$%d,$%d)", i*3+1, i*3+2, i*3+3)
		args = append(args, m.CharacterID, m.AchievementID, m.AchievedAt)
	}
	buf.WriteString(` ON CONFLICT (character_id, achievement_id) DO UPDATE SET achieved_at = excluded.achieved_at`)
	return buf.String(), args
}

func (r *AchievementRepository) ListCharacterMilestones(ctx context.Context, characterID uint32) ([]contract.CharacterMilestone, error) {
	rows, err := r.driver.FetchMany(ctx,
		`SELECT character_id, achievement_id, achieved_at
		   FROM character_milestones
		  WHERE character_id = $1
		  ORDER BY achieved_at DESC`, characterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []contract.CharacterMilestone
	for rows.Next() {
		var m contract.CharacterMilestone
		var at time.Time
		if err := rows.Scan(&m.CharacterID, &m.AchievementID, &at); err != nil {
			return nil, err
		}
		m.AchievedAt = at
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *AchievementRepository) CountExpansions(ctx context.Context) ([]contract.ExpansionCount, error) {
	return r.CountExpansionsFiltered(ctx, contract.CharacterFilter{})
}

func (r *AchievementRepository) CountExpansionsFiltered(ctx context.Context, filter contract.CharacterFilter) ([]contract.ExpansionCount, error) {
	where, args := characterJoinFilterWhere(filter, "c", 1)

	query := fmt.Sprintf(`
		SELECT m.expansion, COUNT(DISTINCT cm.character_id) AS count
		  FROM milestone_achievements m
		  JOIN character_milestones cm ON cm.achievement_id = m.achievement_id
		  JOIN characters c ON c.id = cm.character_id
		 WHERE (m.kind = 'expansion_msq' OR m.kind = 'expansion')
		   AND m.expansion IS NOT NULL
		   AND c.deleted_at IS NULL
		   %s
		 GROUP BY m.expansion
		 ORDER BY m.expansion ASC`, where)

	rows, err := r.driver.FetchMany(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []contract.ExpansionCount
	for rows.Next() {
		var e contract.ExpansionCount
		if err := rows.Scan(&e.Expansion, &e.Count); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *AchievementRepository) NewCharactersPerDay(ctx context.Context, since, until time.Time, filter contract.CharacterFilter) ([]contract.DailyCount, error) {
	where, filterArgs := characterJoinFilterWhere(filter, "c", 3)

	args := []any{since, until}
	args = append(args, filterArgs...)

	query := fmt.Sprintf(`
		SELECT TO_CHAR(cm.achieved_at, 'YYYY-MM-DD') AS day,
		       COUNT(DISTINCT cm.character_id) AS count
		  FROM character_milestones cm
		  JOIN characters c ON c.id = cm.character_id
		 WHERE cm.achievement_id = 590
		   AND cm.achieved_at >= $1
		   AND cm.achieved_at < $2
		   AND c.deleted_at IS NULL
		   %s
		 GROUP BY day
		 ORDER BY day ASC`, where)

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

func (r *AchievementRepository) CountChocoboMilestones(ctx context.Context, since time.Time, filter contract.CharacterFilter) (int64, error) {
	where, filterArgs := characterJoinFilterWhere(filter, "c", 2)

	args := []any{since}
	args = append(args, filterArgs...)

	query := fmt.Sprintf(`
		SELECT COUNT(DISTINCT cm.character_id)
		  FROM character_milestones cm
		  JOIN characters c ON c.id = cm.character_id
		 WHERE cm.achievement_id = 590
		   AND cm.achieved_at >= $1
		   AND c.deleted_at IS NULL
		   %s`, where)

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

func characterJoinFilterWhere(f contract.CharacterFilter, prefix string, startIdx int) (string, []any) {
	var where []string
	var args []any

	addParam := func(clauseTpl string, val any) {
		paramIdx := startIdx + len(args)
		args = append(args, val)
		where = append(where, fmt.Sprintf(clauseTpl, paramIdx))
	}

	p := prefix + "."
	if f.World != "" {
		addParam(p+"world = $%d", f.World)
	}
	if f.Datacenter != "" {
		addParam(p+"datacenter = $%d", f.Datacenter)
	}
	if f.Region != "" {
		addParam(p+"region = $%d", f.Region)
	}
	if f.Race != "" {
		addParam(p+"race = $%d", f.Race)
	}
	if f.GrandCompany != "" {
		addParam(p+"grand_company = $%d", f.GrandCompany)
	}
	if f.FreeCompanyID != "" {
		addParam(p+"fc_id = $%d", f.FreeCompanyID)
	}
	if f.Name != "" {
		addParam(p+"name ILIKE $%d", "%"+f.Name+"%")
	}
	if len(where) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(where, " AND "), args
}
