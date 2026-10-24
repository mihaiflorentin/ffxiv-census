package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	census "github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"github.com/mihaiflorentin88/ffxiv-census/port/dto/response"
)

// CensusController exposes the read-only census REST API. It depends only on
// the domain census service; all DTO mapping happens here.
type CensusController struct {
	svc   *census.Service
	stats *census.UIStatsService
}

func NewCensusController(svc *census.Service, stats ...*census.UIStatsService) *CensusController {
	controller := &CensusController{svc: svc}
	if len(stats) > 0 {
		controller.stats = stats[0]
	}
	return controller
}

func (c *CensusController) currentStats(w http.ResponseWriter, r *http.Request) (*contract.UIStatsSnapshot, bool) {
	if c.stats == nil {
		return nil, false
	}
	snapshot, _, err := c.stats.Current(r.Context())
	if err != nil {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusServiceUnavailable, "statistics temporarily unavailable")
		return nil, true
	}
	return snapshot, true
}

// Latest serves GET /api/v1/census/latest: totals plus the active ratio
// (active = latest achievement within the activity window).
func (c *CensusController) Latest(w http.ResponseWriter, r *http.Request) {
	if snapshot, handled := c.currentStats(w, r); handled {
		if snapshot == nil {
			return
		}
		total, active, maxLevelCount := snapshot.Summary.Total, snapshot.Summary.Active, snapshot.Summary.MaxLevel
		ratio := 0.0
		if total > 0 {
			ratio = float64(active) / float64(total)
		}
		writeJSON(w, http.StatusOK, response.CensusSummary{
			TotalCharacters: total, ActiveCharacters: active, ActiveRatio: ratio, MaxLevelCharacters: maxLevelCount,
			ActivityWindowDays: census.SnapshotActivityWindowDays(snapshot),
		})
		return
	}
	if c.svc == nil {
		writeError(w, http.StatusInternalServerError, "census service unavailable")
		return
	}
	total, active, maxLevelCount, err := c.svc.Summary(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ratio := 0.0
	if total > 0 {
		ratio = float64(active) / float64(total)
	}
	writeJSON(w, http.StatusOK, response.CensusSummary{
		TotalCharacters:    total,
		ActiveCharacters:   active,
		ActiveRatio:        ratio,
		MaxLevelCharacters: maxLevelCount,
		ActivityWindowDays: census.WindowDays(c.svc.ActivityWindow()),
	})
}

// List serves GET /api/v1/census/characters: one page of characters plus the
// total count. limit defaults to 100 and is clamped to 500; offset defaults
// to 0. Missing/empty parameters fall back to defaults; non-numeric or
// non-positive values (limit <= 0) are rejected with 400.
func (c *CensusController) List(w http.ResponseWriter, r *http.Request) {
	if c.svc == nil {
		writeError(w, http.StatusInternalServerError, "census service unavailable")
		return
	}
	query := r.URL.Query()

	limit := 100
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		if n > 500 {
			n = 500
		}
		limit = n
	}
	offset := 0
	if raw := query.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset")
			return
		}
		offset = n
	}

	activeOnly := false
	if raw := query.Get("active"); raw != "" {
		if b, err := strconv.ParseBool(raw); err == nil {
			activeOnly = b
		}
	}

	f := contract.CharacterFilter{
		World:         query.Get("world"),
		Datacenter:    query.Get("datacenter"),
		Region:        query.Get("region"),
		Race:          query.Get("race"),
		Name:          query.Get("name"),
		GrandCompany:  query.Get("grand_company"),
		FreeCompanyID: query.Get("free_company_id"),
		ActiveOnly:    activeOnly,
		SortBy:        query.Get("sort_by"),
		SortOrder:     query.Get("sort_order"),
	}
	chars, total, err := c.svc.ListCharacters(r.Context(), f, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]response.CharacterListItem, 0, len(chars))
	for i := range chars {
		items = append(items, toCharacterListItem(&chars[i], c.svc.ActivitySince()))
	}
	writeJSON(w, http.StatusOK, response.PaginatedCharacters{
		Items:  items,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// Get serves GET /api/v1/census/characters/{id}: full detail for one
// character. A non-numeric id is a 400; an unknown id is a 404.
func (c *CensusController) Get(w http.ResponseWriter, r *http.Request) {
	if c.svc == nil {
		writeError(w, http.StatusInternalServerError, "census service unavailable")
		return
	}
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 32)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid character id")
		return
	}
	detail, err := c.svc.CharacterDetail(r.Context(), uint32(id))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if detail == nil {
		writeError(w, http.StatusNotFound, "character not found")
		return
	}
	writeJSON(w, http.StatusOK, toCharacterDetail(detail, c.svc.ActivitySince()))
}

// Breakdown serves GET /api/v1/stats/breakdown?by=race|world|datacenter|region.
// A missing or unknown dimension is a 400.
func (c *CensusController) Breakdown(w http.ResponseWriter, r *http.Request) {
	if c.svc == nil {
		writeError(w, http.StatusInternalServerError, "census service unavailable")
		return
	}
	by := r.URL.Query().Get("by")
	if by == "" {
		writeError(w, http.StatusBadRequest, "missing by parameter")
		return
	}
	if by != "race" && by != "world" && by != "datacenter" && by != "region" {
		writeError(w, http.StatusBadRequest, census.ErrInvalidDimension.Error())
		return
	}
	if snapshot, handled := c.currentStats(w, r); handled {
		if snapshot == nil {
			return
		}
		groups := census.SnapshotGroups(snapshot, by, contract.StatsScope{})
		items := make([]response.BreakdownGroup, 0, len(groups))
		for _, group := range groups {
			items = append(items, response.BreakdownGroup{Key: group.Key, Total: group.Total, Active: group.Active})
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	groups, err := c.svc.Breakdown(r.Context(), by)
	if errors.Is(err, census.ErrInvalidDimension) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]response.BreakdownGroup, 0, len(groups))
	for _, g := range groups {
		items = append(items, response.BreakdownGroup{Key: g.Key, Total: g.Total, Active: g.Active})
	}
	writeJSON(w, http.StatusOK, items)
}

// NewCharacters serves GET /api/v1/stats/new-characters[?since=YYYY-MM-DD&until=YYYY-MM-DD]:
// characters who earned the Chocobo milestone (achievement 590) per UTC day
// in [since, until). Without since it serves the new-characters window: its
// complete UTC days before today (or before the snapshot's generation day).
// Snapshot-backed requests must stay inside the snapshot's two windows, the
// current one and the previous one.
func (c *CensusController) NewCharacters(w http.ResponseWriter, r *http.Request) {
	if c.svc == nil {
		writeError(w, http.StatusInternalServerError, "census service unavailable")
		return
	}
	query := r.URL.Query()

	var since, until time.Time
	if sinceRaw := query.Get("since"); sinceRaw != "" {
		parsed, err := time.Parse("2006-01-02", sinceRaw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since parameter")
			return
		}
		since = parsed
	}
	if untilRaw := query.Get("until"); untilRaw != "" {
		parsed, err := time.Parse("2006-01-02", untilRaw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid until parameter")
			return
		}
		until = parsed
	}
	if snapshot, handled := c.currentStats(w, r); handled {
		if snapshot == nil {
			return
		}
		windowDays := census.SnapshotNewCharactersWindowDays(snapshot)
		end := snapshot.GeneratedAt.UTC().Truncate(24 * time.Hour)
		if since.IsZero() {
			since = end.AddDate(0, 0, -windowDays)
		}
		if until.IsZero() {
			until = end
		}
		maxUntil := end.Add(24 * time.Hour)
		if since.Before(end.AddDate(0, 0, -2*windowDays)) || until.After(maxUntil) || !until.After(since) {
			writeError(w, http.StatusBadRequest, "requested range is outside the available statistics snapshot")
			return
		}
		days := census.SnapshotDaily(snapshot, contract.StatsScope{})
		items := make([]response.NewCharactersDay, 0, len(days))
		for _, day := range days {
			parsed, parseErr := time.Parse("2006-01-02", day.Day)
			if parseErr == nil && !parsed.Before(since) && parsed.Before(until) {
				items = append(items, response.NewCharactersDay{Day: day.Day, Count: day.Count})
			}
		}
		writeJSON(w, http.StatusOK, items)
		return
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	if since.IsZero() {
		since = today.AddDate(0, 0, -census.WindowDays(c.svc.NewCharactersWindow()))
		if until.IsZero() {
			until = today
		}
	}
	if until.IsZero() {
		until = time.Now().UTC()
	}
	days, err := c.svc.NewCharacters(r.Context(), since, until)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]response.NewCharactersDay, 0, len(days))
	for _, d := range days {
		items = append(items, response.NewCharactersDay{Day: d.Day, Count: d.Count})
	}
	writeJSON(w, http.StatusOK, items)
}

// Expansion serves GET /api/v1/stats/expansion[?name=Expansion]: how many
// distinct characters completed each expansion's MSQ. An optional name filter
// narrows the list; no match returns an empty list, not a 404.
func (c *CensusController) Expansion(w http.ResponseWriter, r *http.Request) {
	if c.svc == nil {
		writeError(w, http.StatusInternalServerError, "census service unavailable")
		return
	}
	name := r.URL.Query().Get("name")
	if snapshot, handled := c.currentStats(w, r); handled {
		if snapshot == nil {
			return
		}
		stats := census.SnapshotExpansions(snapshot, contract.StatsScope{})
		items := make([]response.ExpansionStat, 0, len(stats))
		for _, stat := range stats {
			if name == "" || stat.Expansion == name {
				items = append(items, response.ExpansionStat{Expansion: stat.Expansion, Count: stat.Count})
			}
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	stats, err := c.svc.ExpansionCompletions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]response.ExpansionStat, 0, len(stats))
	for _, s := range stats {
		if name != "" && s.Expansion != name {
			continue
		}
		items = append(items, response.ExpansionStat{Expansion: s.Expansion, Count: s.Count})
	}
	writeJSON(w, http.StatusOK, items)
}

// toCharacterListItem maps a persisted character record to the API list item.
// Every field the DTO declares is copied; pointer fields pass through as-is.
// A character is active when its latest achievement is at or after
// activeSince, the start of the census activity window.
func toCharacterListItem(rec *contract.CharacterRecord, activeSince time.Time) response.CharacterListItem {
	isActive := rec.LatestAchievementAt != nil && !rec.LatestAchievementAt.Before(activeSince)
	return response.CharacterListItem{
		ID:                  rec.ID,
		Name:                rec.Name,
		World:               rec.World,
		Datacenter:          rec.Datacenter,
		Region:              rec.Region,
		Race:                rec.Race,
		Gender:              rec.Gender,
		FreeCompanyID:       rec.FreeCompanyID,
		FreeCompanyName:     rec.FreeCompanyName,
		AchievementsPrivate: rec.AchievementsPrivate,
		LatestAchievementID: rec.LatestAchievementID,
		IsActive:            isActive,
		FirstSeenAt:         rec.FirstSeenAt,
		LastCensusAt:        rec.LastCensusAt,
	}
}

// toCharacterDetail maps the domain aggregate to the API detail payload.
func toCharacterDetail(d *census.CharacterDetail, activeSince time.Time) response.CharacterDetail {
	out := response.CharacterDetail{
		Character:  toCharacterListItem(&d.Character, activeSince),
		Jobs:       make([]response.CharacterJobDetail, 0, len(d.Jobs)),
		Milestones: make([]response.CharacterMilestoneDetail, 0, len(d.Milestones)),
	}
	for _, j := range d.Jobs {
		out.Jobs = append(out.Jobs, response.CharacterJobDetail{
			ClassJobID: j.ClassJobID,
			Name:       j.Name,
			Level:      j.Level,
			ExpLevel:   j.ExpLevel,
		})
	}
	for _, m := range d.Milestones {
		out.Milestones = append(out.Milestones, response.CharacterMilestoneDetail{
			AchievementID: m.AchievementID,
			AchievedAt:    m.AchievedAt,
		})
	}
	return out
}
