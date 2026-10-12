package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// CharacterRepository is an in-memory fake with error injection and call recording.
// It mirrors the SQLite implementation's semantics (first_seen_at preservation,
// deleted_at clearing, jobs replacement, stale ordering) so handler tests don't
// drift from production behavior.
type CharacterRepository struct {
	mu               sync.Mutex
	characters       map[uint32]contract.CharacterRecord
	jobs             map[uint32][]contract.ClassJobRecord
	gear             map[uint32][]contract.CharacterGearRecord
	UpsertErr        error
	UpsertBatchErr   error
	UpsertBatchCalls int
	rejectIDs        map[uint32]bool
	// OnUpsertBatch, when set, is called with the 1-based call number at the
	// start of every StoreResult (and so UpsertBatch); a non-nil result fails
	// that call.
	OnUpsertBatch func(call int) error
	// StoredAchievements records the achievement writes applied by StoreResult.
	StoredAchievements       []contract.AchievementWrite
	UpsertGearErr            error
	GetGearErr               error
	FindIDGapsAfterErr       error
	FillGapsCursorErr        error
	AdvanceFillGapsCursorErr error
	GetErr                   error
	MarkDeletedErr           error
	UpdateErr                error
	ListStaleErr             error
	ListErr                  error
	StreamErr                error
	CountErr                 error
	CountActiveErr           error
	BreakdownErr             error
	SummaryCountsErr         error
	MultiBreakdownErr        error
	DemographicBreakdownErr  error
	NewPerDayErr             error
	MaxIDErr                 error
	IDSweepCursorErr         error
	AdvanceIDSweepCursorErr  error
	idSweepCursor            uint32
	fillGapsCursor           uint32
	UpsertCalls              int
	UpsertGearCalls          int
}

func NewCharacterFake() *CharacterRepository {
	return &CharacterRepository{
		characters: map[uint32]contract.CharacterRecord{},
		jobs:       map[uint32][]contract.ClassJobRecord{},
		gear:       map[uint32][]contract.CharacterGearRecord{},
	}
}

func (f *CharacterRepository) Upsert(ctx context.Context, rec contract.CharacterRecord, jobs []contract.ClassJobRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.UpsertCalls++
	if f.UpsertErr != nil {
		return f.UpsertErr
	}
	// Mirror SQL ON CONFLICT semantics: preserve first_seen_at, clear deleted_at.
	if existing, ok := f.characters[rec.ID]; ok {
		rec.FirstSeenAt = existing.FirstSeenAt
	}
	rec.DeletedAt = nil
	f.characters[rec.ID] = cloneCharacter(rec)
	// Always replace the job set (SQL deletes then re-inserts; nil -> empty).
	f.jobs[rec.ID] = append([]contract.ClassJobRecord(nil), jobs...)
	return nil
}

// RejectID makes any UpsertBatch containing a write for id fail with
// contract.ErrInvalidData before anything is stored, like a database that
// rejects that row and rolls the transaction back.
func (f *CharacterRepository) RejectID(id uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rejectIDs == nil {
		f.rejectIDs = map[uint32]bool{}
	}
	f.rejectIDs[id] = true
}

// UpsertBatch applies every write; UpsertBatchErr fails the whole batch before
// anything is stored, like a rolled-back transaction.
func (f *CharacterRepository) UpsertBatch(ctx context.Context, writes []contract.CharacterWrite) error {
	return f.StoreResult(ctx, contract.CensusResult{Characters: writes})
}

// StoreResult applies the characters, then each achievement write's summary
// (milestones are only recorded in StoredAchievements: this fake does not own
// the milestone table). UpsertBatchErr fails the whole result before anything
// is stored.
func (f *CharacterRepository) StoreResult(ctx context.Context, result contract.CensusResult) error {
	f.mu.Lock()
	f.UpsertBatchCalls++
	err := f.UpsertBatchErr
	if err == nil && f.OnUpsertBatch != nil {
		err = f.OnUpsertBatch(f.UpsertBatchCalls)
	}
	for _, w := range result.Characters {
		if err == nil && f.rejectIDs[w.Record.ID] {
			err = fmt.Errorf("character %d rejected: %w", w.Record.ID, contract.ErrInvalidData)
		}
	}
	for _, a := range result.Achievements {
		if err == nil && f.rejectIDs[a.CharacterID] {
			err = fmt.Errorf("character %d rejected: %w", a.CharacterID, contract.ErrInvalidData)
		}
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	for _, w := range result.Characters {
		if err := f.Upsert(ctx, w.Record, w.Jobs); err != nil {
			return err
		}
	}
	for _, a := range result.Achievements {
		var err error
		if a.Private {
			err = f.SetAchievementsPrivate(ctx, a.CharacterID, true)
		} else {
			err = f.UpdateAchievementSummary(ctx, a.CharacterID, false, a.LatestID, a.LatestAt)
		}
		if err != nil {
			return err
		}
		f.mu.Lock()
		f.StoredAchievements = append(f.StoredAchievements, a)
		f.mu.Unlock()
	}
	return nil
}

func (f *CharacterRepository) UpsertGear(ctx context.Context, charID uint32, gear []contract.CharacterGearRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.UpsertGearCalls++
	if f.UpsertGearErr != nil {
		return f.UpsertGearErr
	}
	cloned := make([]contract.CharacterGearRecord, len(gear))
	for i, g := range gear {
		cloned[i] = cloneGear(g)
	}
	f.gear[charID] = cloned
	return nil
}

func (f *CharacterRepository) GetGear(ctx context.Context, id uint32) ([]contract.CharacterGearRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GetGearErr != nil {
		return nil, f.GetGearErr
	}
	items := f.gear[id]
	cloned := make([]contract.CharacterGearRecord, len(items))
	for i, g := range items {
		cloned[i] = cloneGear(g)
	}
	return cloned, nil
}

func (f *CharacterRepository) FindIDGapsAfter(ctx context.Context, afterID, maxID uint32, limit int) ([][2]uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FindIDGapsAfterErr != nil {
		return nil, f.FindIDGapsAfterErr
	}
	if maxID == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	validIDs := []uint32{0}
	for id := range f.characters {
		if id <= maxID {
			validIDs = append(validIDs, id)
		}
	}
	sort.Slice(validIDs, func(i, j int) bool { return validIDs[i] < validIDs[j] })

	var gaps [][2]uint32
	minStart := uint64(afterID) + 1
	for i := range validIDs {
		next := uint64(maxID + 1)
		if i+1 < len(validIDs) {
			next = uint64(validIDs[i+1])
		}
		if next <= uint64(validIDs[i])+1 {
			continue
		}
		start := uint64(validIDs[i]) + 1
		end := next - 1
		if start < minStart {
			start = minStart
		}
		if start > end {
			continue
		}
		gaps = append(gaps, [2]uint32{uint32(start), uint32(end)})
		if len(gaps) >= limit {
			break
		}
	}
	return gaps, nil
}

func (f *CharacterRepository) FillGapsCursor(ctx context.Context) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FillGapsCursorErr != nil {
		return 0, f.FillGapsCursorErr
	}
	return f.fillGapsCursor, nil
}

func (f *CharacterRepository) AdvanceFillGapsCursor(ctx context.Context, expected, next uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.AdvanceFillGapsCursorErr != nil {
		return f.AdvanceFillGapsCursorErr
	}
	if f.fillGapsCursor == expected {
		f.fillGapsCursor = next
		return nil
	}
	if f.fillGapsCursor >= next {
		return nil
	}
	return fmt.Errorf("fill gaps cursor changed concurrently: expected %d, current %d, next %d", expected, f.fillGapsCursor, next)
}

func (f *CharacterRepository) Get(ctx context.Context, id uint32) (*contract.CharacterRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GetErr != nil {
		return nil, f.GetErr
	}
	rec, ok := f.characters[id]
	if !ok {
		return nil, nil
	}
	cp := cloneCharacter(rec)
	return &cp, nil
}

func (f *CharacterRepository) GetJobs(ctx context.Context, id uint32) ([]contract.ClassJobRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.ClassJobRecord(nil), f.jobs[id]...), nil
}

func (f *CharacterRepository) MarkDeleted(ctx context.Context, id uint32, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MarkDeletedErr != nil {
		return f.MarkDeletedErr
	}
	rec, ok := f.characters[id]
	if !ok {
		return nil // SQL UPDATE affects 0 rows; no phantom record
	}
	rec.DeletedAt = cloneTime(&at)
	f.characters[id] = rec
	return nil
}

func (f *CharacterRepository) UpdateAchievementSummary(ctx context.Context, id uint32, private bool, latestID *uint32, latestAt *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.UpdateErr != nil {
		return f.UpdateErr
	}
	rec, ok := f.characters[id]
	if !ok {
		return nil // SQL UPDATE affects 0 rows
	}
	rec.AchievementsPrivate = private
	rec.LatestAchievementID = cloneUint32(latestID)
	rec.LatestAchievementAt = cloneTime(latestAt)
	f.characters[id] = rec
	return nil
}

func (f *CharacterRepository) SetAchievementsPrivate(ctx context.Context, id uint32, private bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.characters[id]
	if !ok {
		return nil // SQL UPDATE affects 0 rows
	}
	rec.AchievementsPrivate = private
	f.characters[id] = rec
	return nil
}

func (f *CharacterRepository) ListStale(ctx context.Context, cutoff time.Time, limit int) ([]contract.CharacterRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListStaleErr != nil {
		return nil, f.ListStaleErr
	}
	var stale []contract.CharacterRecord
	for _, rec := range f.characters {
		if rec.DeletedAt != nil {
			continue
		}
		if cutoff.IsZero() || rec.LastCensusAt == nil || rec.LastCensusAt.Before(cutoff) {
			stale = append(stale, cloneCharacter(rec))
		}
	}
	// Order oldest-first (NULL last_census_at sorts first, like SQLite ASC).
	sort.Slice(stale, func(i, j int) bool {
		li, lj := stale[i].LastCensusAt, stale[j].LastCensusAt
		if li == nil && lj == nil {
			return stale[i].ID < stale[j].ID
		}
		if li == nil {
			return true
		}
		if lj == nil {
			return false
		}
		if li.Equal(*lj) {
			return stale[i].ID < stale[j].ID
		}
		return li.Before(*lj)
	})
	if limit > 0 && len(stale) > limit {
		stale = stale[:limit]
	}
	return stale, nil
}

func matchesFilter(rec contract.CharacterRecord, jobs []contract.ClassJobRecord, f contract.CharacterFilter) bool {
	if f.World != "" && rec.World != f.World {
		return false
	}
	if f.Datacenter != "" && rec.Datacenter != f.Datacenter {
		return false
	}
	if f.Region != "" && rec.Region != f.Region {
		return false
	}
	if f.Race != "" && rec.Race != f.Race {
		return false
	}
	if f.Name != "" && !strings.Contains(strings.ToLower(rec.Name), strings.ToLower(f.Name)) {
		return false
	}
	if f.GrandCompany != "" && rec.GrandCompany != f.GrandCompany {
		return false
	}
	if f.FreeCompanyID != "" && (rec.FreeCompanyID == nil || *rec.FreeCompanyID != f.FreeCompanyID) {
		return false
	}
	if f.ActiveOnly && rec.LatestAchievementAt == nil {
		return false
	}
	if f.Since != nil {
		if rec.LatestAchievementAt == nil || rec.LatestAchievementAt.Before(*f.Since) {
			return false
		}
	}
	if f.MinLevel > 0 {
		hasLevel := false
		for _, j := range jobs {
			if uint32(j.Level) >= f.MinLevel {
				hasLevel = true
				break
			}
		}
		if !hasLevel {
			return false
		}
	}
	return true
}

// List mirrors the SQL: non-deleted rows matching filter ordered by requested sort or id, limited/offset.
// SQLite LIMIT semantics: LIMIT 0 -> zero rows (offset is irrelevant),
// negative LIMIT -> unlimited, positive -> cap. Negative OFFSET is treated
// as 0 by SQLite.
func (f *CharacterRepository) List(ctx context.Context, filter contract.CharacterFilter, limit, offset int) ([]contract.CharacterRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	if limit == 0 {
		return nil, nil
	}
	var out []contract.CharacterRecord
	for _, rec := range f.characters {
		if rec.DeletedAt != nil || !matchesFilter(rec, f.jobs[rec.ID], filter) {
			continue
		}
		out = append(out, cloneCharacter(rec))
	}

	desc := strings.EqualFold(filter.SortOrder, "desc")
	switch strings.ToLower(filter.SortBy) {
	case "name":
		sort.Slice(out, func(i, j int) bool {
			if strings.EqualFold(out[i].Name, out[j].Name) {
				return out[i].ID < out[j].ID
			}
			if desc {
				return strings.ToLower(out[i].Name) > strings.ToLower(out[j].Name)
			}
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		})
	case "world":
		sort.Slice(out, func(i, j int) bool {
			if out[i].World == out[j].World {
				return out[i].ID < out[j].ID
			}
			if desc {
				return out[i].World > out[j].World
			}
			return out[i].World < out[j].World
		})
	case "created_at", "first_seen_at":
		sort.Slice(out, func(i, j int) bool {
			if out[i].FirstSeenAt.Equal(out[j].FirstSeenAt) {
				return out[i].ID < out[j].ID
			}
			if desc {
				return out[i].FirstSeenAt.After(out[j].FirstSeenAt)
			}
			return out[i].FirstSeenAt.Before(out[j].FirstSeenAt)
		})
	case "updated_at", "last_census_at":
		sort.Slice(out, func(i, j int) bool {
			li, lj := out[i].LastCensusAt, out[j].LastCensusAt
			if li == nil && lj == nil {
				return out[i].ID < out[j].ID
			}
			if li == nil {
				return !desc
			}
			if lj == nil {
				return desc
			}
			if li.Equal(*lj) {
				return out[i].ID < out[j].ID
			}
			if desc {
				return li.After(*lj)
			}
			return li.Before(*lj)
		})
	default:
		sort.Slice(out, func(i, j int) bool {
			if desc {
				return out[i].ID > out[j].ID
			}
			return out[i].ID < out[j].ID
		})
	}

	if offset < 0 {
		offset = 0
	}
	if offset >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Stream mirrors the streaming behavior: iterates non-deleted characters matching filter in ID order, invoking fn.
func (f *CharacterRepository) Stream(ctx context.Context, filter contract.CharacterFilter, fn func(rec contract.CharacterRecord) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StreamErr != nil {
		return f.StreamErr
	}
	var out []contract.CharacterRecord
	for _, rec := range f.characters {
		if rec.DeletedAt != nil || !matchesFilter(rec, f.jobs[rec.ID], filter) {
			continue
		}
		out = append(out, cloneCharacter(rec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	for _, rec := range out {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
	return nil
}

// Count mirrors SELECT COUNT(*) WHERE deleted_at IS NULL AND filter.
func (f *CharacterRepository) Count(ctx context.Context, filter contract.CharacterFilter) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CountErr != nil {
		return 0, f.CountErr
	}
	var n int64
	for _, rec := range f.characters {
		if rec.DeletedAt == nil && matchesFilter(rec, f.jobs[rec.ID], filter) {
			n++
		}
	}
	return n, nil
}

// CountActive mirrors the SQL: non-deleted rows with latest_achievement_at >= since.
func (f *CharacterRepository) CountActive(ctx context.Context, since time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CountActiveErr != nil {
		return 0, f.CountActiveErr
	}
	var n int64
	for _, rec := range f.characters {
		if rec.DeletedAt != nil || rec.LatestAchievementAt == nil {
			continue
		}
		if !rec.LatestAchievementAt.Before(since) {
			n++
		}
	}
	return n, nil
}

// breakdownColumns mirrors the SQLite repository's whitelist.
var breakdownColumns = map[string]bool{"race": true, "world": true, "datacenter": true, "region": true}

// Breakdown mirrors the SQL group-by: non-deleted rows grouped by the record
// field, total and active counts, ordered by total desc then key.
func (f *CharacterRepository) Breakdown(ctx context.Context, column string, since time.Time, filter contract.CharacterFilter) ([]contract.GroupCount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BreakdownErr != nil {
		return nil, f.BreakdownErr
	}
	if column != "race" && column != "world" && column != "datacenter" && column != "region" {
		return nil, fmt.Errorf("invalid breakdown column %q", column)
	}
	counts := map[string]*contract.GroupCount{}
	for _, rec := range f.characters {
		if rec.DeletedAt != nil || !matchesFilter(rec, f.jobs[rec.ID], filter) {
			continue
		}
		var key string
		switch column {
		case "race":
			key = rec.Race
		case "world":
			key = rec.World
		case "datacenter":
			key = rec.Datacenter
		case "region":
			key = rec.Region
		}
		g := counts[key]
		if g == nil {
			g = &contract.GroupCount{Key: key}
			counts[key] = g
		}
		g.Total++
		if rec.LatestAchievementAt != nil && !rec.LatestAchievementAt.Before(since) {
			g.Active++
		}
	}
	out := make([]contract.GroupCount, 0, len(counts))
	for _, g := range counts {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// SummaryCounts mirrors the SQL: total, active, and max-level counts in a single query.
func (f *CharacterRepository) SummaryCounts(ctx context.Context, since time.Time, maxLevel uint32) (total, active, maxLevelCount int64, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SummaryCountsErr != nil {
		return 0, 0, 0, f.SummaryCountsErr
	}
	for _, rec := range f.characters {
		if rec.DeletedAt != nil {
			continue
		}
		total++
		if rec.LatestAchievementAt != nil && !rec.LatestAchievementAt.Before(since) {
			active++
		}
		for _, j := range f.jobs[rec.ID] {
			if uint32(j.Level) >= maxLevel {
				maxLevelCount++
				break
			}
		}
	}
	return total, active, maxLevelCount, nil
}

// MultiBreakdown mirrors the SQL UNION ALL group-by for multiple columns.
func (f *CharacterRepository) MultiBreakdown(ctx context.Context, columns []string, since time.Time, filter contract.CharacterFilter) (map[string][]contract.GroupCount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MultiBreakdownErr != nil {
		return nil, f.MultiBreakdownErr
	}
	out := make(map[string][]contract.GroupCount, len(columns))
	for _, col := range columns {
		counts := map[string]*contract.GroupCount{}
		for _, rec := range f.characters {
			if rec.DeletedAt != nil || !matchesFilter(rec, f.jobs[rec.ID], filter) {
				continue
			}
			var key string
			switch col {
			case "race":
				key = rec.Race
			case "world":
				key = rec.World
			case "datacenter":
				key = rec.Datacenter
			case "region":
				key = rec.Region
			}
			g := counts[key]
			if g == nil {
				g = &contract.GroupCount{Key: key}
				counts[key] = g
			}
			g.Total++
			if rec.LatestAchievementAt != nil && !rec.LatestAchievementAt.Before(since) {
				g.Active++
			}
		}
		var list []contract.GroupCount
		for _, g := range counts {
			list = append(list, *g)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Total != list[j].Total {
				return list[i].Total > list[j].Total
			}
			return list[i].Key < list[j].Key
		})
		out[col] = list
	}
	return out, nil
}

// DemographicBreakdown mirrors the SQL: tribe, gender, and race×gender counts.
func (f *CharacterRepository) DemographicBreakdown(ctx context.Context, since time.Time, filter contract.CharacterFilter) (*contract.DemographicCounts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DemographicBreakdownErr != nil {
		return nil, f.DemographicBreakdownErr
	}
	tribeCounts := map[string]*contract.GroupCount{}
	genderCounts := map[string]*contract.GroupCount{}
	rgCounts := map[string]*contract.GroupCount{}

	for _, rec := range f.characters {
		if rec.DeletedAt != nil || !matchesFilter(rec, f.jobs[rec.ID], filter) {
			continue
		}
		active := rec.LatestAchievementAt != nil && !rec.LatestAchievementAt.Before(since)

		// Tribe breakdown
		if rec.Tribe != "" {
			g := tribeCounts[rec.Tribe]
			if g == nil {
				g = &contract.GroupCount{Key: rec.Tribe}
				tribeCounts[rec.Tribe] = g
			}
			g.Total++
			if active {
				g.Active++
			}
		}

		// Gender breakdown
		var genderKey string
		switch rec.Gender {
		case 1:
			genderKey = "Male"
		case 2:
			genderKey = "Female"
		default:
			genderKey = "Unknown"
		}
		g := genderCounts[genderKey]
		if g == nil {
			g = &contract.GroupCount{Key: genderKey}
			genderCounts[genderKey] = g
		}
		g.Total++
		if active {
			g.Active++
		}

		// Race×Gender breakdown
		if rec.Race != "" {
			rgKey := rec.Race + "|" + genderKey
			rg := rgCounts[rgKey]
			if rg == nil {
				rg = &contract.GroupCount{Key: rgKey}
				rgCounts[rgKey] = rg
			}
			rg.Total++
			if active {
				rg.Active++
			}
		}
	}

	out := &contract.DemographicCounts{}
	for _, g := range tribeCounts {
		out.Tribes = append(out.Tribes, *g)
	}
	for _, g := range genderCounts {
		out.Genders = append(out.Genders, *g)
	}
	for _, g := range rgCounts {
		out.RaceGenders = append(out.RaceGenders, *g)
	}
	return out, nil
}

// NewPerDay mirrors the SQL: non-deleted rows with first_seen_at in
// [since, until), counted per UTC day, ordered ascending by day.
func (f *CharacterRepository) NewPerDay(ctx context.Context, since, until time.Time, filter contract.CharacterFilter) ([]contract.DailyCount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.NewPerDayErr != nil {
		return nil, f.NewPerDayErr
	}
	counts := map[string]int64{}
	for _, rec := range f.characters {
		if rec.DeletedAt != nil || !matchesFilter(rec, f.jobs[rec.ID], filter) {
			continue
		}
		if rec.FirstSeenAt.Before(since) || !rec.FirstSeenAt.Before(until) {
			continue
		}
		counts[rec.FirstSeenAt.UTC().Format("2006-01-02")]++
	}
	out := make([]contract.DailyCount, 0, len(counts))
	for day, c := range counts {
		out = append(out, contract.DailyCount{Day: day, Count: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out, nil
}

func (f *CharacterRepository) MaxID(ctx context.Context) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.MaxIDErr != nil {
		return 0, f.MaxIDErr
	}
	var maxID uint32
	for id, rec := range f.characters {
		if rec.DeletedAt != nil {
			continue
		}
		if id > maxID {
			maxID = id
		}
	}
	return maxID, nil
}

func (f *CharacterRepository) IDSweepCursor(ctx context.Context) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.IDSweepCursorErr != nil {
		return 0, f.IDSweepCursorErr
	}
	if f.idSweepCursor == 0 {
		var maxID uint32
		for id := range f.characters {
			if id > maxID {
				maxID = id
			}
		}
		if maxID == ^uint32(0) {
			return 0, fmt.Errorf("id sweep cursor overflow after %d", maxID)
		}
		f.idSweepCursor = maxID + 1
	}
	return f.idSweepCursor, nil
}

func (f *CharacterRepository) AdvanceIDSweepCursor(ctx context.Context, expected, next uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.AdvanceIDSweepCursorErr != nil {
		return f.AdvanceIDSweepCursorErr
	}
	if expected == 0 || next <= expected {
		return fmt.Errorf("invalid id sweep cursor advance: %d -> %d", expected, next)
	}
	if f.idSweepCursor == 0 {
		return fmt.Errorf("id sweep cursor not initialized")
	}
	if f.idSweepCursor == expected {
		f.idSweepCursor = next
		return nil
	}
	if f.idSweepCursor >= next {
		return nil
	}
	return fmt.Errorf("id sweep cursor changed concurrently: expected %d, current %d, next %d", expected, f.idSweepCursor, next)
}

var _ contract.CharacterRepository = (*CharacterRepository)(nil)
