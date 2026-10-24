package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	census "github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"github.com/mihaiflorentin88/ffxiv-census/port/dto/response"
)

// testRig wires the real domain service on top of in-memory fakes, exactly as
// routes.go does, so handler tests exercise the full service -> repo path.
type testRig struct {
	svc   *census.Service
	chars *mockrepo.CharacterRepository
	ach   *mockrepo.AchievementRepository
	c     *CensusController
}

func newRig(t *testing.T) *testRig {
	t.Helper()
	chars := mockrepo.NewCharacterFake()
	ach := mockrepo.NewAchievementFake()
	ach.SetCharacterRepo(chars)
	svc := census.NewService(chars, ach, mockrepo.NewCensusRunFake())
	return &testRig{
		svc: svc, chars: chars, ach: ach,
		c: NewCensusController(svc),
	}
}

func (r *testRig) seed(t *testing.T, char *contract.CharacterProfile) {
	t.Helper()
	w, err := r.svc.PrepareCharacter(char)
	if err != nil {
		t.Fatalf("PrepareCharacter(%d): %v", char.ID, err)
	}
	if err := r.chars.StoreResult(context.Background(), contract.CensusResult{Characters: []contract.CharacterWrite{w}}); err != nil {
		t.Fatalf("StoreResult(%d): %v", char.ID, err)
	}
}

func doGET(t *testing.T, h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func assertError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantMsg string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, wantStatus, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q is not JSON: %v", rec.Body.String(), err)
	}
	if msg, ok := body["error"]; !ok || !strings.Contains(msg, wantMsg) {
		t.Errorf("error body = %q, want it to contain %q", body["error"], wantMsg)
	}
}

func TestCensusController_Latest(t *testing.T) {
	rig := newRig(t)
	rig.seed(t, &contract.CharacterProfile{
		ID: 1, Name: "Tataru", World: "Ultros", Datacenter: "Primal", Gender: 2, Race: "Lalafell",
		ClassJobs: []contract.ClassJobRecord{
			{ClassJobID: 1, Name: "Gladiator", Level: 100},
		},
	})
	rig.seed(t, &contract.CharacterProfile{
		ID: 2, Name: "Moen", World: "Ultros", Datacenter: "Primal", Race: "Lalafell",
		ClassJobs: []contract.ClassJobRecord{
			{ClassJobID: 1, Name: "Gladiator", Level: 90},
		},
	})

	var body response.CensusSummary
	decodeJSON(t, doGET(t, rig.c.Latest, "/api/v1/census/latest"), &body)
	if body.TotalCharacters != 2 {
		t.Errorf("total_characters = %d, want 2", body.TotalCharacters)
	}
	if body.ActiveCharacters != 0 {
		t.Errorf("active_characters = %d, want 0 (no achievements ingested)", body.ActiveCharacters)
	}
	if body.ActiveRatio != 0 {
		t.Errorf("active_ratio = %v, want 0", body.ActiveRatio)
	}
	if body.MaxLevelCharacters != 1 {
		t.Errorf("max_level_characters = %d, want 1", body.MaxLevelCharacters)
	}
	if body.ActivityWindowDays != 90 {
		t.Errorf("activity_window_days = %d, want the default 90", body.ActivityWindowDays)
	}
}

func TestCensusController_LatestUsesStatsSnapshot(t *testing.T) {
	generated := time.Now().UTC()
	statsRepo := mockrepo.NewUIStatsFake(&contract.UIStatsSnapshot{
		SchemaVersion:    contract.UIStatsSchemaVersion,
		GeneratedAt:      generated,
		ActivitySince:    generated.Add(-45 * 24 * time.Hour),
		MaxLevel:         100,
		SourceCharacters: 80,
		Summary:          contract.StatsSummary{Total: 80, Active: 20, MaxLevel: 10},
	})
	stats := census.NewUIStatsService(statsRepo, time.Minute, time.Hour)
	chars := mockrepo.NewCharacterFake()
	chars.CountErr = errors.New("raw aggregate must not run")
	controller := NewCensusController(census.NewService(chars, mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake()), stats)

	var body response.CensusSummary
	decodeJSON(t, doGET(t, controller.Latest, "/api/v1/census/latest"), &body)
	if body.TotalCharacters != 80 || body.ActiveCharacters != 20 || body.MaxLevelCharacters != 10 {
		t.Fatalf("summary = %#v", body)
	}
	// The snapshot was built with a 45-day activity window.
	if body.ActivityWindowDays != 45 {
		t.Fatalf("activity_window_days = %d, want 45", body.ActivityWindowDays)
	}
}

func TestCensusController_Latest_NilService(t *testing.T) {
	c := NewCensusController(nil)
	assertError(t, doGET(t, c.Latest, "/api/v1/census/latest"), http.StatusInternalServerError, "census service unavailable")
}

func TestCensusController_List(t *testing.T) {
	rig := newRig(t)
	for _, id := range []uint32{1, 2, 3} {
		rig.seed(t, &contract.CharacterProfile{ID: id, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur"})
	}

	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantItems  int
		wantTotal  int64
		wantLimit  int
		wantOffset int
	}{
		{name: "defaults", query: "", wantStatus: 200, wantItems: 3, wantTotal: 3, wantLimit: 100, wantOffset: 0},
		{name: "page one", query: "limit=2&offset=0", wantStatus: 200, wantItems: 2, wantTotal: 3, wantLimit: 2, wantOffset: 0},
		{name: "page two", query: "limit=2&offset=2", wantStatus: 200, wantItems: 1, wantTotal: 3, wantLimit: 2, wantOffset: 2},
		{name: "limit clamped", query: "limit=1000", wantStatus: 200, wantItems: 3, wantTotal: 3, wantLimit: 500, wantOffset: 0},
		{name: "invalid limit", query: "limit=abc", wantStatus: 400},
		{name: "zero limit", query: "limit=0", wantStatus: 400},
		{name: "negative limit", query: "limit=-1", wantStatus: 400},
		{name: "negative offset", query: "offset=-5", wantStatus: 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doGET(t, rig.c.List, "/api/v1/census/characters"+querySuffix(tt.query))
			if tt.wantStatus != http.StatusOK {
				wantMsg := "limit"
				if tt.name == "negative offset" {
					wantMsg = "offset"
				}
				assertError(t, rec, tt.wantStatus, wantMsg)
				return
			}
			var body response.PaginatedCharacters
			decodeJSON(t, rec, &body)
			if len(body.Items) != tt.wantItems {
				t.Errorf("items = %d, want %d", len(body.Items), tt.wantItems)
			}
			if body.Total != tt.wantTotal {
				t.Errorf("total = %d, want %d", body.Total, tt.wantTotal)
			}
			if body.Limit != tt.wantLimit {
				t.Errorf("limit = %d, want %d", body.Limit, tt.wantLimit)
			}
			if body.Offset != tt.wantOffset {
				t.Errorf("offset = %d, want %d", body.Offset, tt.wantOffset)
			}
			// DTO field pass-through on the first item.
			if len(body.Items) > 0 {
				first := body.Items[0]
				if first.Name != "Char" || first.World != "Ultros" || first.Datacenter != "Primal" || first.Region != "NA" {
					t.Errorf("first item fields = %+v, want name Char/world Ultros/datacenter Primal/region NA", first)
				}
			}
		})
	}
}

func TestCensusController_List_Filters(t *testing.T) {
	rig := newRig(t)
	rig.seed(t, &contract.CharacterProfile{ID: 1, Name: "Feed How", World: "Louisoix", Datacenter: "Chaos", Race: "Au Ra"})
	rig.seed(t, &contract.CharacterProfile{ID: 2, Name: "Ninto Thegen", World: "Louisoix", Datacenter: "Chaos", Race: "Miqo'te"})
	rig.seed(t, &contract.CharacterProfile{ID: 3, Name: "Ahribella White", World: "Zodiark", Datacenter: "Light", Race: "Miqo'te"})
	rig.seed(t, &contract.CharacterProfile{ID: 4, Name: "Alpha Test", World: "Ultros", Datacenter: "Primal", Race: "Hyur"})

	tests := []struct {
		name      string
		query     string
		wantIDs   []uint32
		wantTotal int64
	}{
		{name: "filter by world", query: "world=Louisoix", wantIDs: []uint32{1, 2}, wantTotal: 2},
		{name: "filter by datacenter", query: "datacenter=Chaos", wantIDs: []uint32{1, 2}, wantTotal: 2},
		{name: "filter by region", query: "region=EU", wantIDs: []uint32{1, 2, 3}, wantTotal: 3},
		{name: "filter by race", query: "race=Miqo%27te", wantIDs: []uint32{2, 3}, wantTotal: 2},
		{name: "filter by name substring", query: "name=feed", wantIDs: []uint32{1}, wantTotal: 1},
		{name: "combined filters", query: "world=Louisoix&race=Miqo%27te", wantIDs: []uint32{2}, wantTotal: 1},
		{name: "no match", query: "world=Balmung", wantIDs: nil, wantTotal: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doGET(t, rig.c.List, "/api/v1/census/characters"+querySuffix(tt.query))
			var body response.PaginatedCharacters
			decodeJSON(t, rec, &body)
			if body.Total != tt.wantTotal {
				t.Errorf("total = %d, want %d", body.Total, tt.wantTotal)
			}
			var gotIDs []uint32
			for _, item := range body.Items {
				gotIDs = append(gotIDs, item.ID)
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("got ids %v, want %v", gotIDs, tt.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Fatalf("got ids %v, want %v", gotIDs, tt.wantIDs)
				}
			}
		})
	}
}

func TestCensusController_Get(t *testing.T) {
	rig := newRig(t)
	now := time.Now().UTC()
	rig.seed(t, &contract.CharacterProfile{
		ID:              1,
		Name:            "Tataru Taru",
		World:           "Ultros",
		Datacenter:      "Primal",
		Gender:          2,
		Race:            "Lalafell",
		FreeCompanyID:   "9234567890123456789",
		FreeCompanyName: "The Scions",
		ClassJobs: []contract.ClassJobRecord{
			{ClassJobID: 19, Name: "Paladin", Level: 90, ExpLevel: 12345},
		},
	})
	_ = rig.ach.UpsertCharacterMilestones(context.Background(), 1, []contract.CharacterMilestone{
		{CharacterID: 1, AchievementID: 590, AchievedAt: now},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/census/characters/1", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	rig.c.Get(rec, req)

	var body response.CharacterDetail
	decodeJSON(t, rec, &body)
	if body.Character.ID != 1 || body.Character.Name != "Tataru Taru" {
		t.Errorf("character = %+v, want id 1 / Tataru Taru", body.Character)
	}
	if body.Character.FreeCompanyID == nil || *body.Character.FreeCompanyID != "9234567890123456789" {
		t.Errorf("free_company_id = %v, want passthrough", body.Character.FreeCompanyID)
	}
	if len(body.Jobs) != 1 || body.Jobs[0].Name != "Paladin" || body.Jobs[0].Level != 90 || body.Jobs[0].ExpLevel != 12345 {
		t.Errorf("jobs = %+v, want one Paladin 90", body.Jobs)
	}
	if len(body.Milestones) != 1 || body.Milestones[0].AchievementID != 590 {
		t.Errorf("milestones = %+v, want one 590", body.Milestones)
	}
}

func TestCensusController_Get_NotFound(t *testing.T) {
	rig := newRig(t)
	rig.seed(t, &contract.CharacterProfile{ID: 1, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/census/characters/999", nil)
	req.SetPathValue("id", "999")
	rec := httptest.NewRecorder()
	rig.c.Get(rec, req)
	assertError(t, rec, http.StatusNotFound, "character not found")
}

func TestCensusController_Get_InvalidID(t *testing.T) {
	rig := newRig(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/census/characters/abc", nil)
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	rig.c.Get(rec, req)
	assertError(t, rec, http.StatusBadRequest, "invalid character id")
}

func TestCensusController_Breakdown(t *testing.T) {
	rig := newRig(t)
	rig.seed(t, &contract.CharacterProfile{ID: 1, Name: "A", World: "Ultros", Datacenter: "Primal", Race: "Hyur"})
	rig.seed(t, &contract.CharacterProfile{ID: 2, Name: "B", World: "Ultros", Datacenter: "Primal", Race: "Hyur"})
	rig.seed(t, &contract.CharacterProfile{ID: 3, Name: "C", World: "Moogle", Datacenter: "Chaos", Race: "Hyur"})

	var groups []response.BreakdownGroup
	decodeJSON(t, doGET(t, rig.c.Breakdown, "/api/v1/stats/breakdown?by=world"), &groups)
	if len(groups) != 2 {
		t.Fatalf("groups = %+v, want 2 worlds", groups)
	}
	if groups[0].Key != "Ultros" || groups[0].Total != 2 {
		t.Errorf("groups[0] = %+v, want Ultros total 2", groups[0])
	}
	if groups[1].Key != "Moogle" || groups[1].Total != 1 {
		t.Errorf("groups[1] = %+v, want Moogle total 1", groups[1])
	}
}

func TestCensusController_Breakdown_MissingBy(t *testing.T) {
	rig := newRig(t)
	assertError(t, doGET(t, rig.c.Breakdown, "/api/v1/stats/breakdown"), http.StatusBadRequest, "by")
}

func TestCensusController_Breakdown_InvalidDimension(t *testing.T) {
	rig := newRig(t)
	assertError(t, doGET(t, rig.c.Breakdown, "/api/v1/stats/breakdown?by=bogus"), http.StatusBadRequest, "invalid breakdown dimension")
}

func TestCensusController_NewCharacters(t *testing.T) {
	rig := newRig(t)
	ctx := context.Background()
	// Seed a character and its chocobo milestone (achievement 590) so the
	// service path (NewCharacters -> achievements.NewCharactersPerDay) is
	// exercised against real milestone data, not first_seen_at.
	if err := rig.chars.Upsert(ctx, contract.CharacterRecord{
		ID: 1, Name: "A", World: "Ultros", Datacenter: "Primal", Region: "NA",
		FirstSeenAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	}, nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := rig.ach.UpsertCharacterMilestones(ctx, 1, []contract.CharacterMilestone{
		{CharacterID: 1, AchievementID: 590, AchievedAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("UpsertCharacterMilestones: %v", err)
	}

	var days []response.NewCharactersDay
	decodeJSON(t, doGET(t, rig.c.NewCharacters, "/api/v1/stats/new-characters?since=2026-08-01&until=2026-09-01"), &days)
	if len(days) != 1 {
		t.Fatalf("days = %+v, want exactly one bucket", days)
	}
	if days[0].Day != "2026-08-10" || days[0].Count != 1 {
		t.Errorf("days[0] = %+v, want day 2026-08-10 count 1", days[0])
	}
}

func TestCensusController_NewCharacters_MissingSince(t *testing.T) {
	rig := newRig(t)
	assertError(t, doGET(t, rig.c.NewCharacters, "/api/v1/stats/new-characters"), http.StatusBadRequest, "since")
}

func TestCensusController_NewCharacters_InvalidSince(t *testing.T) {
	rig := newRig(t)
	assertError(t, doGET(t, rig.c.NewCharacters, "/api/v1/stats/new-characters?since=notadate"), http.StatusBadRequest, "since")
}

func TestCensusController_Expansion(t *testing.T) {
	rig := newRig(t)
	ctx := context.Background()
	if err := rig.svc.SyncMilestones(ctx); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}
	now := time.Now().UTC()
	_ = rig.ach.UpsertCharacterMilestones(ctx, 1, []contract.CharacterMilestone{
		{CharacterID: 1, AchievementID: 1139, AchievedAt: now}, // Heavensward
	})
	_ = rig.ach.UpsertCharacterMilestones(ctx, 2, []contract.CharacterMilestone{
		{CharacterID: 2, AchievementID: 1794, AchievedAt: now}, // Stormblood
	})

	var all []response.ExpansionStat
	decodeJSON(t, doGET(t, rig.c.Expansion, "/api/v1/stats/expansion"), &all)
	if len(all) != 2 {
		t.Fatalf("expansion = %+v, want 2 entries", all)
	}
	if all[0].Expansion != "Heavensward" || all[0].Count != 1 {
		t.Errorf("all[0] = %+v, want Heavensward 1", all[0])
	}
	if all[1].Expansion != "Stormblood" || all[1].Count != 1 {
		t.Errorf("all[1] = %+v, want Stormblood 1", all[1])
	}
}

func TestCensusController_Expansion_NameFilter(t *testing.T) {
	rig := newRig(t)
	ctx := context.Background()
	if err := rig.svc.SyncMilestones(ctx); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}
	now := time.Now().UTC()
	_ = rig.ach.UpsertCharacterMilestones(ctx, 1, []contract.CharacterMilestone{
		{CharacterID: 1, AchievementID: 1139, AchievedAt: now}, // Heavensward
	})

	var filtered []response.ExpansionStat
	decodeJSON(t, doGET(t, rig.c.Expansion, "/api/v1/stats/expansion?name=Heavensward"), &filtered)
	if len(filtered) != 1 || filtered[0].Expansion != "Heavensward" {
		t.Errorf("filtered = %+v, want only Heavensward", filtered)
	}

	// A name with no matches returns an empty list, not 404.
	var none []response.ExpansionStat
	decodeJSON(t, doGET(t, rig.c.Expansion, "/api/v1/stats/expansion?name=DoesNotExist"), &none)
	if len(none) != 0 {
		t.Errorf("none = %+v, want empty list", none)
	}
}

func querySuffix(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}
