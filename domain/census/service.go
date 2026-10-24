package census

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// Service is the domain brain of the census. It converts Lodestone DTOs into
// persisted records and computes milestone/activity facts. It depends only on
// contracts, never on SQL or HTTP.
type Service struct {
	characters     contract.CharacterRepository
	achievements   contract.AchievementRepository
	censusRuns     contract.CensusRunRepository
	mu             sync.RWMutex
	activityWindow time.Duration
	// newCharactersWindow is how far back the new-character figures count.
	newCharactersWindow time.Duration
	// privateRecheckWindow is how long a private flag counts as verified.
	privateRecheckWindow time.Duration
	maxLevel             uint32
	expansions           []ExpansionConfig
	// milestoneCache holds the last-fetched milestone registry to avoid
	// re-querying the DB on every ProcessAchievements call.
	milestoneCache    []contract.MilestoneAchievement
	milestoneCacheAt  time.Time
	milestoneCacheTTL time.Duration
}

func NewService(
	characters contract.CharacterRepository,
	achievements contract.AchievementRepository,
	censusRuns contract.CensusRunRepository,
) *Service {
	return &Service{
		characters:           characters,
		achievements:         achievements,
		censusRuns:           censusRuns,
		activityWindow:       defaultActivityWindow,
		newCharactersWindow:  defaultNewCharactersWindow,
		privateRecheckWindow: defaultPrivateRecheckWindow,
		maxLevel:             100,
		expansions:           DefaultExpansions,
		milestoneCacheTTL:    5 * time.Minute,
	}
}

// ErrProfileHidden is returned by PrepareCharacter when a character
// carries no usable race data. On Lodestone this happens for private profiles
// (no race block at all), access-restricted profiles (HTTP 403), and
// suppressed demographics (race/clan rendered as "----"). Such characters
// exist but have no censusable demographics: callers skip them without
// storing and without failing the job.
var ErrProfileHidden = errors.New("character profile hidden: no race data")

// hiddenRace reports whether a profile's race is unusable for the census.
func hiddenRace(race string) bool {
	race = strings.TrimSpace(race)
	return race == "" || race == "----"
}

// SetConfig configures max level and expansion milestones.
func (s *Service) SetConfig(maxLevel uint32, expansions []ExpansionConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxLevel > 0 {
		s.maxLevel = maxLevel
	}
	if len(expansions) > 0 {
		s.expansions = make([]ExpansionConfig, len(expansions))
		copy(s.expansions, expansions)
	}
}

// MaxLevel returns the configured max level cap.
func (s *Service) MaxLevel() uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxLevel
}

// Expansions returns a copy of the configured expansions.
func (s *Service) Expansions() []ExpansionConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ExpansionConfig, len(s.expansions))
	copy(out, s.expansions)
	return out
}

// Milestones returns the full milestone achievement registry based on configured expansions.
func (s *Service) Milestones() []contract.MilestoneAchievement {
	return BuildMilestones(s.Expansions())
}

// SyncMilestones seeds the milestone registry into the DB (idempotent).
func (s *Service) SyncMilestones(ctx context.Context) error {
	if err := s.achievements.SyncMilestones(ctx, s.Milestones()); err != nil {
		return err
	}
	// Invalidate the cache so the next ProcessAchievements picks up the fresh registry.
	s.mu.Lock()
	s.milestoneCache = nil
	s.milestoneCacheAt = time.Time{}
	s.mu.Unlock()
	return nil
}

// cachedMilestones returns the milestone registry, using a short-lived cache
// to avoid querying the DB on every ProcessAchievements call.
func (s *Service) cachedMilestones(ctx context.Context) ([]contract.MilestoneAchievement, error) {
	s.mu.RLock()
	cached := s.milestoneCache
	cachedAt := s.milestoneCacheAt
	ttl := s.milestoneCacheTTL
	s.mu.RUnlock()

	if cached != nil && time.Since(cachedAt) < ttl {
		return cached, nil
	}

	fresh, err := s.achievements.ListMilestones(ctx)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.milestoneCache = fresh
	s.milestoneCacheAt = time.Now()
	s.mu.Unlock()

	return fresh, nil
}

// MilestoneIDs returns the set of tracked milestone achievement IDs from the
// cached registry. Used by handlers to pre-filter achievements before processing.
func (s *Service) MilestoneIDs(ctx context.Context) (map[uint32]bool, error) {
	registry, err := s.cachedMilestones(ctx)
	if err != nil {
		return nil, err
	}
	ids := make(map[uint32]bool, len(registry))
	for _, m := range registry {
		ids[m.AchievementID] = true
	}
	return ids, nil
}

// ListCharacterMilestones returns the milestones already earned by a character.
// Used by handlers to pre-seed the stop function and skip scraping when all
// milestones are known.
func (s *Service) ListCharacterMilestones(ctx context.Context, charID uint32) ([]contract.CharacterMilestone, error) {
	return s.achievements.ListCharacterMilestones(ctx, charID)
}

// GetCharacter returns a character record by ID, or nil if not found.
// Used by handlers that need to check staleness or other character fields.
func (s *Service) GetCharacter(ctx context.Context, charID uint32) (*contract.CharacterRecord, error) {
	return s.characters.Get(ctx, charID)
}

// PrepareCharacter validates a Lodestone character and converts it into the
// write that stores it, without storing anything. Handlers hand the result to
// a contract.ResultWriter. It returns ErrProfileHidden for a profile without
// usable race data.
func (s *Service) PrepareCharacter(char *contract.CharacterProfile) (contract.CharacterWrite, error) {
	if char == nil {
		return contract.CharacterWrite{}, errors.New("cannot upsert nil character")
	}
	if hiddenRace(char.Race) {
		return contract.CharacterWrite{}, ErrProfileHidden
	}
	if strings.TrimSpace(char.Name) == "" {
		return contract.CharacterWrite{}, fmt.Errorf("cannot upsert character %d: name is empty", char.ID)
	}
	return contract.CharacterWrite{Record: profileToRecord(char), Jobs: profileToJobs(char)}, nil
}

// MaxCharacterID returns the maximum character ID known to the census.
func (s *Service) MaxCharacterID(ctx context.Context) (uint32, error) {
	return s.characters.MaxID(ctx)
}

func profileToRecord(char *contract.CharacterProfile) contract.CharacterRecord {
	now := time.Now().UTC()
	rec := contract.CharacterRecord{
		ID:           char.ID,
		Name:         char.Name,
		World:        char.World,
		Datacenter:   char.Datacenter,
		Region:       RegionForDatacenter(char.Datacenter),
		Gender:       char.Gender,
		Race:         char.Race,
		Tribe:        char.Tribe,
		GrandCompany: char.GrandCompany,
		Bio:          char.Bio,
		ActiveJob:    char.ActiveJob,
		FirstSeenAt:  now,
		LastCensusAt: &now,
	}
	if char.FreeCompanyID != "" {
		rec.FreeCompanyID = &char.FreeCompanyID
	}
	if char.FreeCompanyName != "" {
		rec.FreeCompanyName = &char.FreeCompanyName
	}
	return rec
}

func profileToJobs(char *contract.CharacterProfile) []contract.ClassJobRecord {
	jobs := make([]contract.ClassJobRecord, 0, len(char.ClassJobs))
	for _, j := range char.ClassJobs {
		jobs = append(jobs, contract.ClassJobRecord{
			CharacterID: char.ID,
			ClassJobID:  j.ClassJobID,
			Name:        j.Name,
			Level:       j.Level,
			ExpLevel:    j.ExpLevel,
		})
	}
	return jobs
}

// defaultActivityWindow is how far back a latest public achievement counts as
// active. A full census pass takes months, so the window is wide.
const defaultActivityWindow = 90 * 24 * time.Hour

// defaultNewCharactersWindow is how far back the new-character figures
// count (a first company chocobo earned), compared with the same length
// before it. The census sees a new chocobo only when it revisits the
// character, so the window is wide.
const defaultNewCharactersWindow = DefaultNewCharactersWindowDays * 24 * time.Hour

// defaultPrivateRecheckWindow is how long a character found private is left
// alone before its achievement census runs again.
const defaultPrivateRecheckWindow = 30 * 24 * time.Hour

// AchievementCheck is what an achievement census job should do for a
// character.
type AchievementCheck int

const (
	// AchievementCheckDue: ask the Lodestone.
	AchievementCheckDue AchievementCheck = iota
	// AchievementCheckSkip: a private flag verified within the recheck window;
	// do nothing.
	AchievementCheckSkip
	// AchievementCheckStamp: flagged private but never verified (every flag
	// stored before migration 00022). Trust the flag, record the time without
	// asking the Lodestone, and recheck after the window.
	AchievementCheckStamp
)

// AchievementCensusCheck decides what an achievement census job does for a
// character. Only a character flagged private is ever skipped: one whose flag
// was verified within the private recheck window is skipped, one never
// verified is stamped (see AchievementCheckStamp). Anything else is due: a
// character that is not private, or that the census has no row for. On a
// lookup error it returns AchievementCheckDue with the error, so a failing
// read never hides a character from the census.
func (s *Service) AchievementCensusCheck(ctx context.Context, charID uint32) (AchievementCheck, error) {
	rec, err := s.characters.Get(ctx, charID)
	if err != nil {
		return AchievementCheckDue, err
	}
	if rec == nil || !rec.AchievementsPrivate {
		return AchievementCheckDue, nil
	}
	if rec.AchievementsCheckedAt == nil {
		return AchievementCheckStamp, nil
	}
	s.mu.RLock()
	window := s.privateRecheckWindow
	s.mu.RUnlock()
	if time.Since(*rec.AchievementsCheckedAt) >= window {
		return AchievementCheckDue, nil
	}
	return AchievementCheckSkip, nil
}

// PrepareMilestoneResults filters a fetched achievement summary against the
// milestone registry and returns the write that stores it, without storing
// anything. Handlers hand the result to a contract.ResultWriter. A private
// profile hides its achievements, so its write only flags the character
// private, records when that was verified and preserves any prior milestones
// and latest achievement.
func (s *Service) PrepareMilestoneResults(ctx context.Context, charID uint32, summary *contract.AchievementSummary) (contract.AchievementWrite, error) {
	if summary == nil {
		return contract.AchievementWrite{}, errors.New("nil achievement summary")
	}
	if summary.Private {
		return contract.AchievementWrite{CharacterID: charID, Private: true, CheckedAt: time.Now().UTC()}, nil
	}

	registry, err := s.cachedMilestones(ctx)
	if err != nil {
		return contract.AchievementWrite{}, err
	}
	if len(registry) == 0 {
		return contract.AchievementWrite{}, errors.New("milestone registry is empty; run SyncMilestones before processing achievements")
	}
	byID := make(map[uint32]contract.MilestoneAchievement, len(registry))
	for _, m := range registry {
		byID[m.AchievementID] = m
	}

	w := contract.AchievementWrite{CharacterID: charID}
	for _, r := range summary.Milestones {
		if !r.Earned {
			continue
		}
		if _, ok := byID[r.AchievementID]; ok {
			w.Milestones = append(w.Milestones, contract.CharacterMilestone{
				CharacterID:   charID,
				AchievementID: r.AchievementID,
				AchievedAt:    r.EarnedAt,
			})
		}
	}
	if summary.LatestAchievement != nil {
		id := summary.LatestAchievement.AchievementID
		at := summary.LatestAchievement.EarnedAt
		w.LatestID = &id
		w.LatestAt = &at
	}
	return w, nil
}

// IsActive reports whether a latest-achievement timestamp falls within the
// census activity window (default 90 days, overridable via SetActivityWindow).
func (s *Service) IsActive(latestAt time.Time) bool {
	s.mu.RLock()
	window := s.activityWindow
	s.mu.RUnlock()
	return !latestAt.IsZero() && time.Since(latestAt) <= window
}

// SetActivityWindow overrides the activity window used by IsActive, Summary,
// and Breakdown. Non-positive durations are ignored, keeping the current
// window.
func (s *Service) SetActivityWindow(d time.Duration) {
	if d > 0 {
		s.mu.Lock()
		s.activityWindow = d
		s.mu.Unlock()
	}
}

// SetNewCharactersWindow overrides the new-characters window. Non-positive
// durations are ignored, keeping the current window.
func (s *Service) SetNewCharactersWindow(d time.Duration) {
	if d > 0 {
		s.mu.Lock()
		s.newCharactersWindow = d
		s.mu.Unlock()
	}
}

// NewCharactersWindow returns how far back the new-character figures count.
// The comparison window is the same length, just before it.
func (s *Service) NewCharactersWindow() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.newCharactersWindow
}

// SetPrivateRecheckWindow overrides how long a character found private is left
// alone before its achievement census runs again. Non-positive durations are
// ignored, keeping the current window.
func (s *Service) SetPrivateRecheckWindow(d time.Duration) {
	if d > 0 {
		s.mu.Lock()
		s.privateRecheckWindow = d
		s.mu.Unlock()
	}
}

// ActivityWindow returns how far back a latest public achievement counts as
// active.
func (s *Service) ActivityWindow() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activityWindow
}

// ActivitySince returns the UTC instant marking the start of the activity
// window. Exported for use by handlers that need to construct filters.
func (s *Service) ActivitySince() time.Time {
	return s.activitySince()
}

// activitySince returns the UTC instant marking the start of the activity
// window: anything at or after it counts as active.
func (s *Service) activitySince() time.Time {
	s.mu.RLock()
	window := s.activityWindow
	s.mu.RUnlock()
	return time.Now().UTC().Add(-window)
}

// MarkCharacterDeleted records that a character no longer exists on Lodestone.
func (s *Service) MarkCharacterDeleted(ctx context.Context, id uint32, at time.Time) error {
	return s.characters.MarkDeleted(ctx, id, at)
}

// ErrInvalidDimension is returned by Breakdown when by is not one of
// race|world|datacenter|region.
var ErrInvalidDimension = errors.New("invalid breakdown dimension: want race|world|datacenter|region")

// CharacterDetail aggregates a character's persisted profile, jobs, and milestones.
type CharacterDetail struct {
	Character  contract.CharacterRecord
	Jobs       []contract.ClassJobRecord
	Gear       []contract.CharacterGearRecord
	Milestones []contract.CharacterMilestone
}

// Summary returns the total number of non-deleted characters and how many of
// them are active (latest achievement within the activity window).
func (s *Service) Summary(ctx context.Context) (total, active, maxLevelCount int64, err error) {
	since := s.activitySince()
	maxLvl := s.MaxLevel()

	var wg sync.WaitGroup
	var totalErr, activeErr, maxErr error

	wg.Add(3)
	go func() {
		defer wg.Done()
		total, totalErr = s.characters.Count(ctx, contract.CharacterFilter{})
	}()
	go func() {
		defer wg.Done()
		active, activeErr = s.characters.CountActive(ctx, since)
	}()
	go func() {
		defer wg.Done()
		maxLevelCount, maxErr = s.characters.Count(ctx, contract.CharacterFilter{MinLevel: maxLvl})
	}()
	wg.Wait()

	// Deterministic error precedence: total, active, max-level.
	if totalErr != nil {
		return 0, 0, 0, totalErr
	}
	if activeErr != nil {
		return 0, 0, 0, activeErr
	}
	if maxErr != nil {
		return 0, 0, 0, maxErr
	}
	return total, active, maxLevelCount, nil
}

// ListCharacters returns one page of non-deleted characters matching filter
// (ordered by id, limited/offset) plus the matching count for pagination.
func (s *Service) ListCharacters(ctx context.Context, f contract.CharacterFilter, limit, offset int) ([]contract.CharacterRecord, int64, error) {
	chars, err := s.characters.List(ctx, f, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.characters.Count(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	return chars, total, nil
}

// StreamCharacters streams non-deleted characters matching filter in ID order, invoking fn for each record.
func (s *Service) StreamCharacters(ctx context.Context, f contract.CharacterFilter, fn func(rec contract.CharacterRecord) error) error {
	return s.characters.Stream(ctx, f, fn)
}

// CharacterDetail returns the full profile for one character, or nil (no
// error) when the id is unknown.
func (s *Service) CharacterDetail(ctx context.Context, id uint32) (*CharacterDetail, error) {
	rec, err := s.characters.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, nil
	}
	jobs, err := s.characters.GetJobs(ctx, id)
	if err != nil {
		return nil, err
	}
	gear, err := s.characters.GetGear(ctx, id)
	if err != nil {
		return nil, err
	}
	milestones, err := s.achievements.ListCharacterMilestones(ctx, id)
	if err != nil {
		return nil, err
	}
	detail := &CharacterDetail{Character: *rec, Jobs: jobs, Gear: gear, Milestones: milestones}
	return detail, nil
}

// WorldDetailStats aggregates high-level census facts for a specific world.
type WorldDetailStats struct {
	World            string
	Datacenter       string
	Region           string
	TotalCharacters  int64
	ActiveCharacters int64
	// NewCharacters counts the characters who earned their first company
	// chocobo in the new-characters window: the complete UTC days before
	// today. NewCharactersPrevious counts the window of the same length
	// before it.
	NewCharacters         int64
	NewCharactersPrevious int64
	Races                 []contract.GroupCount
	MSQCompletions        []contract.ExpansionCount
}

// WorldDetail returns full census stats for a specific world.
func (s *Service) WorldDetail(ctx context.Context, worldName string) (*WorldDetailStats, error) {
	filter := contract.CharacterFilter{World: worldName}
	since := s.ActivitySince()
	filterActive := contract.CharacterFilter{World: worldName, Since: &since}
	// Both windows end with the last complete UTC day; today is still in
	// progress.
	end := time.Now().UTC().Truncate(24 * time.Hour)
	windowDays := WindowDays(s.NewCharactersWindow())
	currentStart := end.AddDate(0, 0, -windowDays)
	previousStart := currentStart.AddDate(0, 0, -windowDays)

	var total, active int64
	var races []contract.GroupCount
	var msq []contract.ExpansionCount
	var daily []contract.DailyCount
	var chars []contract.CharacterRecord
	var totalErr, activeErr, racesErr, msqErr, dailyErr, listErr error

	var wg sync.WaitGroup
	wg.Add(6)
	go func() {
		defer wg.Done()
		total, totalErr = s.characters.Count(ctx, filter)
	}()
	go func() {
		defer wg.Done()
		active, activeErr = s.characters.Count(ctx, filterActive)
	}()
	go func() {
		defer wg.Done()
		races, racesErr = s.characters.Breakdown(ctx, "race", since, filter)
	}()
	go func() {
		defer wg.Done()
		msq, msqErr = s.achievements.CountExpansionsFiltered(ctx, filter)
	}()
	go func() {
		defer wg.Done()
		daily, dailyErr = s.achievements.NewCharactersPerDay(ctx, previousStart, end, filter)
	}()
	go func() {
		defer wg.Done()
		chars, listErr = s.characters.List(ctx, filter, 1, 0)
	}()
	wg.Wait()

	// Deterministic error precedence for the first five calls.
	if totalErr != nil {
		return nil, totalErr
	}
	if activeErr != nil {
		return nil, activeErr
	}
	if racesErr != nil {
		return nil, racesErr
	}
	if msqErr != nil {
		return nil, msqErr
	}
	if dailyErr != nil {
		return nil, dailyErr
	}
	var current, previous int64
	for _, day := range daily {
		parsed, err := time.Parse("2006-01-02", day.Day)
		if err != nil || !parsed.Before(end) {
			continue
		}
		switch {
		case !parsed.Before(currentStart):
			current += day.Count
		case !parsed.Before(previousStart):
			previous += day.Count
		}
	}

	dc := ""
	region := ""
	// Metadata lookup is non-fatal.
	if listErr == nil && len(chars) > 0 {
		dc = chars[0].Datacenter
		region = chars[0].Region
	}

	return &WorldDetailStats{
		World:                 worldName,
		Datacenter:            dc,
		Region:                region,
		TotalCharacters:       total,
		ActiveCharacters:      active,
		NewCharacters:         current,
		NewCharactersPrevious: previous,
		Races:                 races,
		MSQCompletions:        msq,
	}, nil
}

// Breakdown groups non-deleted characters by by (race|world|datacenter|region)
// with total and activity-window counts per group. Unknown dimensions return
// ErrInvalidDimension without touching the repository.
func (s *Service) Breakdown(ctx context.Context, by string, filter ...contract.CharacterFilter) ([]contract.GroupCount, error) {
	switch by {
	case "race", "world", "datacenter", "region":
	default:
		return nil, ErrInvalidDimension
	}
	f := contract.CharacterFilter{}
	if len(filter) > 0 {
		f = filter[0]
	}
	return s.characters.Breakdown(ctx, by, s.activitySince(), f)
}

// NewCharacters returns daily counts of new characters in [since, until),
// using the early-game Chocobo milestone (achievement ID 590).
func (s *Service) NewCharacters(ctx context.Context, since, until time.Time, filter ...contract.CharacterFilter) ([]contract.DailyCount, error) {
	f := contract.CharacterFilter{}
	if len(filter) > 0 {
		f = filter[0]
	}
	return s.achievements.NewCharactersPerDay(ctx, since, until, f)
}

// ExpansionCompletions returns how many distinct characters completed each
// expansion's MSQ.
func (s *Service) ExpansionCompletions(ctx context.Context) ([]contract.ExpansionCount, error) {
	return s.achievements.CountExpansions(ctx)
}

// SummaryCounts returns total, active, and max-level character counts in a
// single repository query. More efficient than Summary() which uses 3 queries.
func (s *Service) SummaryCounts(ctx context.Context) (total, active, maxLevelCount int64, err error) {
	return s.characters.SummaryCounts(ctx, s.activitySince(), s.MaxLevel())
}

// MultiBreakdown returns group-by counts for multiple columns in a single
// query. More efficient than calling Breakdown() multiple times.
func (s *Service) MultiBreakdown(ctx context.Context, columns []string, filter ...contract.CharacterFilter) (map[string][]contract.GroupCount, error) {
	f := contract.CharacterFilter{}
	if len(filter) > 0 {
		f = filter[0]
	}
	return s.characters.MultiBreakdown(ctx, columns, s.activitySince(), f)
}

// DemographicBreakdown returns tribe, gender, and race×gender counts in a
// single query. Used by the races page for demographic pie charts.
func (s *Service) DemographicBreakdown(ctx context.Context, filter ...contract.CharacterFilter) (*contract.DemographicCounts, error) {
	f := contract.CharacterFilter{}
	if len(filter) > 0 {
		f = filter[0]
	}
	return s.characters.DemographicBreakdown(ctx, s.activitySince(), f)
}
