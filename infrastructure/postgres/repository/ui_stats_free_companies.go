package repository

import (
	"container/heap"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// The Free Company boards of the full refresh. One streaming, index-only
// grouped pass over idx_characters_fc_live (fc_id, including world and
// latest_achievement_at, live members only) yields one row per Free Company;
// Go keeps two bounded top lists per world, one per measure, so memory stays
// constant however many Free Companies exist. Each list also keeps the
// entries tied with its 100th on both counts (at most
// freeCompanyTieAllowance more), because the board breaks such ties by name
// and names are not known during the pass. Names are read afterwards, one
// index lookup per kept company, and each world is then trimmed to the
// board's own top 100 per measure (census.TrimFreeCompanyWorld).

const freeCompaniesSnapshotKey = "free_companies"

// freeCompanyPassQuery groups live Free Company members by company. A
// company's world is the smallest world among its members: a Free Company
// belongs to one world, so they only differ while a transferred member's row
// is stale.
const freeCompanyPassQuery = `
	SELECT fc_id, MIN(world) AS world, COUNT(*) AS members,
	       COUNT(*) FILTER (WHERE latest_achievement_at >= $1) AS active
	FROM characters
	WHERE deleted_at IS NULL AND fc_id IS NOT NULL
	GROUP BY fc_id
	HAVING COUNT(*) >= $2`

// freeCompanyNamesQuery reads each kept company's name from any one live
// member that has one: a single idx_characters_fc_live lookup per company,
// never a pass over all of its members. Members re-censused after a rename
// carry the new name, so a rename shows once the member read is one of them.
const freeCompanyNamesQuery = `
	SELECT f.id, COALESCE(n.fc_name, '')
	FROM unnest($1::text[]) AS f(id)
	LEFT JOIN LATERAL (
		SELECT c.fc_name
		FROM characters c
		WHERE c.deleted_at IS NULL AND c.fc_id = f.id AND c.fc_name <> ''
		LIMIT 1
	) n ON TRUE`

// freeCompanyTieAllowance bounds how many entries tied with a world list's
// 100th (on both counts) the pass keeps beyond the 100, to break the tie by
// name once names are known.
const freeCompanyTieAllowance = 20

// freeCompanyPlanSettings are the planner switches the pass turns off.
var freeCompanyPlanSettings = []string{"enable_hashagg", "enable_seqscan", "enable_bitmapscan"}

// fcCandidate is one Free Company of the grouped pass.
type fcCandidate struct {
	id              string
	members, active int64
}

// fcRanksAbove reports whether a ranks above b on a world's board by members
// (or by active members): the measure, then the other measure, then the
// lower Lodestone id. Names are not known during the pass, so ties on both
// counts at the cut-off keep the lower id.
func fcRanksAbove(a, b fcCandidate, byActive bool) bool {
	first, second := [2]int64{a.members, a.active}, [2]int64{b.members, b.active}
	if byActive {
		first, second = [2]int64{a.active, a.members}, [2]int64{b.active, b.members}
	}
	if first[0] != second[0] {
		return first[0] > second[0]
	}
	if first[1] != second[1] {
		return first[1] > second[1]
	}
	return lodestoneIDLess(a.id, b.id)
}

// lodestoneIDLess orders numeric Lodestone ids numerically.
func lodestoneIDLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// fcTopList keeps the best size candidates by one measure: a min-heap whose
// root is the lowest-ranked candidate kept.
type fcTopList struct {
	items    []fcCandidate
	byActive bool
	size     int
}

func (l *fcTopList) Len() int           { return len(l.items) }
func (l *fcTopList) Less(i, j int) bool { return fcRanksAbove(l.items[j], l.items[i], l.byActive) }
func (l *fcTopList) Swap(i, j int)      { l.items[i], l.items[j] = l.items[j], l.items[i] }
func (l *fcTopList) Push(x any)         { l.items = append(l.items, x.(fcCandidate)) }
func (l *fcTopList) Pop() any {
	last := l.items[len(l.items)-1]
	l.items = l.items[:len(l.items)-1]
	return last
}

// withCutOffTies returns the list's top n, plus the entries tied with the
// n-th on both counts (the list holds at most freeCompanyTieAllowance of
// them beyond n), best first.
func (l *fcTopList) withCutOffTies(n int) []fcCandidate {
	sorted := append([]fcCandidate(nil), l.items...)
	sort.Slice(sorted, func(i, j int) bool { return fcRanksAbove(sorted[i], sorted[j], l.byActive) })
	if len(sorted) <= n {
		return sorted
	}
	cut := sorted[n-1]
	end := n
	for end < len(sorted) && sorted[end].members == cut.members && sorted[end].active == cut.active {
		end++
	}
	return sorted[:end]
}

func (l *fcTopList) offer(c fcCandidate) {
	if len(l.items) < l.size {
		heap.Push(l, c)
		return
	}
	if fcRanksAbove(c, l.items[0], l.byActive) {
		l.items[0] = c
		heap.Fix(l, 0)
	}
}

// readFreeCompanyBoards runs the Free Company pass inside the refresh's read
// transaction and returns the boards built at builtAt.
func readFreeCompanyBoards(ctx context.Context, tx *sql.Tx, activitySince, builtAt time.Time) (*contract.StatsFreeCompanies, error) {
	type worldLists struct{ members, active *fcTopList }
	worlds := map[string]*worldLists{}

	// Pin the plan to the streaming index-only scan: a hashed aggregate over
	// a sequential scan would hold every Free Company in memory (and the
	// refresh raised work_mem for the fine-grain pass, which makes hashing
	// look cheap). The settings are put back right after the pass, so the
	// names query plans normally.
	restore, err := pinFreeCompanyPlan(ctx, tx)
	if err != nil {
		return nil, err
	}
	err = scanFreeCompanyPass(ctx, tx, activitySince, func(world string, c fcCandidate) {
		lists := worlds[world]
		if lists == nil {
			size := census.FreeCompanyBoardSize + freeCompanyTieAllowance
			lists = &worldLists{members: &fcTopList{size: size}, active: &fcTopList{size: size, byActive: true}}
			worlds[world] = lists
		}
		lists.members.offer(c)
		lists.active.offer(c)
	})
	if restoreErr := restore(); err == nil {
		err = restoreErr
	}
	if err != nil {
		return nil, err
	}

	kept := map[string]map[string]fcCandidate{}
	var ids []string
	for world, lists := range worlds {
		companies := map[string]fcCandidate{}
		for _, list := range []*fcTopList{lists.members, lists.active} {
			for _, c := range list.withCutOffTies(census.FreeCompanyBoardSize) {
				if _, ok := companies[c.id]; !ok {
					companies[c.id] = c
					ids = append(ids, c.id)
				}
			}
		}
		kept[world] = companies
	}

	names, err := readFreeCompanyNames(ctx, tx, ids)
	if err != nil {
		return nil, err
	}

	boards := &contract.StatsFreeCompanies{BuiltAt: builtAt, ActivitySince: activitySince, Worlds: []contract.WorldFreeCompanies{}}
	for world, companies := range kept {
		var all []contract.FreeCompanyCount
		for _, c := range companies {
			all = append(all, contract.FreeCompanyCount{ID: c.id, Name: names[c.id], Members: c.members, Active: c.active})
		}
		boards.Worlds = append(boards.Worlds, contract.WorldFreeCompanies{World: world, Companies: census.TrimFreeCompanyWorld(all, census.FreeCompanyBoardSize)})
	}
	sort.Slice(boards.Worlds, func(i, j int) bool { return boards.Worlds[i].World < boards.Worlds[j].World })
	return boards, nil
}

// pinFreeCompanyPlan turns the freeCompanyPlanSettings off for the rest of
// the transaction and returns the function that puts back the values they
// had.
func pinFreeCompanyPlan(ctx context.Context, tx *sql.Tx) (func() error, error) {
	previous := make([]string, len(freeCompanyPlanSettings))
	for i, setting := range freeCompanyPlanSettings {
		if err := tx.QueryRowContext(ctx, `SELECT current_setting($1)`, setting).Scan(&previous[i]); err != nil {
			return nil, fmt.Errorf("plan Free Company statistics: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `SELECT set_config($1, 'off', true)`, setting); err != nil {
			return nil, fmt.Errorf("plan Free Company statistics: %w", err)
		}
	}
	return func() error {
		for i, setting := range freeCompanyPlanSettings {
			if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, setting, previous[i]); err != nil {
				return fmt.Errorf("restore planner settings after the Free Company pass: %w", err)
			}
		}
		return nil
	}, nil
}

// scanFreeCompanyPass streams the grouped pass, handing each company with a
// world to offer.
func scanFreeCompanyPass(ctx context.Context, tx *sql.Tx, activitySince time.Time, offer func(world string, c fcCandidate)) error {
	rows, err := tx.QueryContext(ctx, freeCompanyPassQuery, activitySince, census.FreeCompanyMinimumMembers)
	if err != nil {
		return fmt.Errorf("query Free Company statistics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c fcCandidate
		var world sql.NullString
		if err := rows.Scan(&c.id, &world, &c.members, &c.active); err != nil {
			return fmt.Errorf("scan Free Company statistics: %w", err)
		}
		if world.String != "" {
			offer(world.String, c)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate Free Company statistics: %w", err)
	}
	return nil
}

// readFreeCompanyNames reads the names of the Free Companies ids.
func readFreeCompanyNames(ctx context.Context, tx *sql.Tx, ids []string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	rows, err := tx.QueryContext(ctx, freeCompanyNamesQuery, ids)
	if err != nil {
		return nil, fmt.Errorf("query Free Company names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("scan Free Company names: %w", err)
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Free Company names: %w", err)
	}
	return names, nil
}

// storedFreeCompanyCount is how many Free Companies the boards store across every world.
func storedFreeCompanyCount(boards *contract.StatsFreeCompanies) int64 {
	var n int64
	for _, w := range boards.Worlds {
		n += int64(len(w.Companies))
	}
	return n
}

// publishFreeCompanyBoards writes the Free Company row inside the publishing
// transaction of a full refresh.
func publishFreeCompanyBoards(ctx context.Context, tx *sql.Tx, snapshot *contract.UIStatsSnapshot, boards *contract.StatsFreeCompanies, duration time.Duration) error {
	payload, err := json.Marshal(boards)
	if err != nil {
		return fmt.Errorf("encode Free Company boards: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ui_stats_snapshots (
			snapshot_key, schema_version, generated_at, activity_since, max_level,
			source_character_count, refresh_duration_ms, payload
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (snapshot_key) DO UPDATE SET
			schema_version = excluded.schema_version,
			generated_at = excluded.generated_at,
			activity_since = excluded.activity_since,
			max_level = excluded.max_level,
			source_character_count = excluded.source_character_count,
			refresh_duration_ms = excluded.refresh_duration_ms,
			payload = excluded.payload`,
		freeCompaniesSnapshotKey, snapshot.SchemaVersion, boards.BuiltAt, boards.ActivitySince, snapshot.MaxLevel,
		storedFreeCompanyCount(boards), duration.Milliseconds(), payload); err != nil {
		return fmt.Errorf("publish Free Company boards: %w", err)
	}
	return nil
}

func (r *UIStatsRepository) LoadFreeCompanies(ctx context.Context) (*contract.StatsFreeCompanies, error) {
	row, err := r.driver.FetchOne(ctx, `SELECT payload FROM ui_stats_snapshots WHERE snapshot_key = $1`, freeCompaniesSnapshotKey)
	if err != nil {
		return nil, err
	}
	var payload []byte
	if err := row.Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, census.ErrUIStatsUnavailable
		}
		return nil, err
	}
	var boards contract.StatsFreeCompanies
	if err := json.Unmarshal(payload, &boards); err != nil {
		return nil, fmt.Errorf("decode Free Company boards: %w", err)
	}
	// BuiltAt keeps the payload's full precision, so it matches the main
	// snapshot's FreeCompaniesBuiltAt exactly.
	boards.BuiltAt = boards.BuiltAt.UTC()
	boards.ActivitySince = boards.ActivitySince.UTC()
	return &boards, nil
}
