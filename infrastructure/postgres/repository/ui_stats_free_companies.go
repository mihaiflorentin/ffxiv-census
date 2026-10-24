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
// constant however many Free Companies exist. Names are read afterwards for
// the kept companies only.

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

// freeCompanyNamesQuery reads each kept company's name from its most recently
// censused member, so a rename shows once any member is censused again.
const freeCompanyNamesQuery = `
	SELECT DISTINCT ON (fc_id) fc_id, COALESCE(fc_name, '')
	FROM characters
	WHERE deleted_at IS NULL AND fc_id = ANY($1)
	ORDER BY fc_id, last_census_at DESC NULLS LAST, id DESC`

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
	// look cheap). This is the transaction's last query, so the settings
	// touch nothing else.
	for _, setting := range []string{"enable_hashagg", "enable_seqscan", "enable_bitmapscan"} {
		if _, err := tx.ExecContext(ctx, "SET LOCAL "+setting+" = off"); err != nil {
			return nil, fmt.Errorf("plan Free Company statistics: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, freeCompanyPassQuery, activitySince, census.FreeCompanyMinimumMembers)
	if err != nil {
		return nil, fmt.Errorf("query Free Company statistics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c fcCandidate
		var world sql.NullString
		if err := rows.Scan(&c.id, &world, &c.members, &c.active); err != nil {
			return nil, fmt.Errorf("scan Free Company statistics: %w", err)
		}
		if world.String == "" {
			continue
		}
		lists := worlds[world.String]
		if lists == nil {
			lists = &worldLists{
				members: &fcTopList{size: census.FreeCompanyBoardSize},
				active:  &fcTopList{size: census.FreeCompanyBoardSize, byActive: true},
			}
			worlds[world.String] = lists
		}
		lists.members.offer(c)
		lists.active.offer(c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Free Company statistics: %w", err)
	}
	rows.Close()

	kept := map[string]map[string]fcCandidate{}
	var ids []string
	for world, lists := range worlds {
		companies := map[string]fcCandidate{}
		for _, list := range []*fcTopList{lists.members, lists.active} {
			for _, c := range list.items {
				if _, ok := companies[c.id]; !ok {
					companies[c.id] = c
					ids = append(ids, c.id)
				}
			}
		}
		kept[world] = companies
	}

	names := make(map[string]string, len(ids))
	if len(ids) > 0 {
		nameRows, err := tx.QueryContext(ctx, freeCompanyNamesQuery, ids)
		if err != nil {
			return nil, fmt.Errorf("query Free Company names: %w", err)
		}
		defer nameRows.Close()
		for nameRows.Next() {
			var id, name string
			if err := nameRows.Scan(&id, &name); err != nil {
				return nil, fmt.Errorf("scan Free Company names: %w", err)
			}
			names[id] = name
		}
		if err := nameRows.Err(); err != nil {
			return nil, fmt.Errorf("iterate Free Company names: %w", err)
		}
	}

	boards := &contract.StatsFreeCompanies{BuiltAt: builtAt, ActivitySince: activitySince, Worlds: []contract.WorldFreeCompanies{}}
	for world, companies := range kept {
		w := contract.WorldFreeCompanies{World: world}
		for _, c := range companies {
			w.Companies = append(w.Companies, contract.FreeCompanyCount{ID: c.id, Name: names[c.id], Members: c.members, Active: c.active})
		}
		sort.Slice(w.Companies, func(i, j int) bool {
			a, b := w.Companies[i], w.Companies[j]
			return fcRanksAbove(fcCandidate{a.ID, a.Members, a.Active}, fcCandidate{b.ID, b.Members, b.Active}, false)
		})
		boards.Worlds = append(boards.Worlds, w)
	}
	sort.Slice(boards.Worlds, func(i, j int) bool { return boards.Worlds[i].World < boards.Worlds[j].World })
	return boards, nil
}

// freeCompanyCompanies counts the companies stored across every world.
func freeCompanyCompanies(boards *contract.StatsFreeCompanies) int64 {
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
		freeCompanyCompanies(boards), duration.Milliseconds(), payload); err != nil {
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
