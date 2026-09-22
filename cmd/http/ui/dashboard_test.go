package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	census "github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mockqueue "github.com/mihaiflorentin88/ffxiv-census/mock/queue"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

type testRig struct {
	svc   *census.Service
	chars *mockrepo.CharacterRepository
	ach   *mockrepo.AchievementRepository
	q     *mockqueue.Fake
	ctrl  *UIController
}

type testStatsRepository struct {
	svc *census.Service
}

func (r *testStatsRepository) LoadCurrent(ctx context.Context) (*contract.UIStatsSnapshot, error) {
	generated := time.Now().UTC()
	total, active, maxLevel, err := r.svc.SummaryCounts(ctx)
	if err != nil {
		return nil, err
	}
	snapshot := &contract.UIStatsSnapshot{
		SchemaVersion:    contract.UIStatsSchemaVersion,
		GeneratedAt:      generated,
		ActivitySince:    r.svc.ActivitySince(),
		MaxLevel:         r.svc.MaxLevel(),
		SourceCharacters: total,
		Summary:          contract.StatsSummary{Total: total, Active: active, MaxLevel: maxLevel},
	}
	for _, dimension := range []string{"region", "world", "race"} {
		groups, groupErr := r.svc.Breakdown(ctx, dimension)
		if groupErr != nil {
			return nil, groupErr
		}
		for _, group := range groups {
			snapshot.Groups = append(snapshot.Groups, contract.ScopedGroupCount{Dimension: dimension, Key: group.Key, Total: group.Total, Active: group.Active})
		}
	}

	filters := []struct {
		scope  contract.StatsScope
		filter contract.CharacterFilter
	}{{}}
	for _, region := range []string{"NA", "EU", "JP", "OCE"} {
		filters = append(filters, struct {
			scope  contract.StatsScope
			filter contract.CharacterFilter
		}{scope: contract.StatsScope{Region: region}, filter: contract.CharacterFilter{Region: region}})
	}
	for _, dc := range census.Datacenters() {
		filters = append(filters, struct {
			scope  contract.StatsScope
			filter contract.CharacterFilter
		}{scope: contract.StatsScope{Datacenter: dc}, filter: contract.CharacterFilter{Datacenter: dc}})
	}
	for _, world := range census.Worlds() {
		filters = append(filters, struct {
			scope  contract.StatsScope
			filter contract.CharacterFilter
		}{scope: contract.StatsScope{World: world}, filter: contract.CharacterFilter{World: world}})
	}
	for _, item := range filters {
		races, groupErr := r.svc.Breakdown(ctx, "race", item.filter)
		if groupErr != nil {
			return nil, groupErr
		}
		for _, group := range races {
			if item.scope == (contract.StatsScope{}) {
				continue // global race rows were added above
			}
			snapshot.Groups = append(snapshot.Groups, contract.ScopedGroupCount{Scope: item.scope, Dimension: "race", Key: group.Key, Total: group.Total, Active: group.Active})
		}
		demo, demoErr := r.svc.DemographicBreakdown(ctx, item.filter)
		if demoErr != nil {
			return nil, demoErr
		}
		for dimension, groups := range map[string][]contract.GroupCount{"tribe": demo.Tribes, "gender": demo.Genders, "race_gender": demo.RaceGenders} {
			for _, group := range groups {
				snapshot.Groups = append(snapshot.Groups, contract.ScopedGroupCount{Scope: item.scope, Dimension: dimension, Key: group.Key, Total: group.Total, Active: group.Active})
			}
		}
	}
	for _, expansion := range mustExpansions(ctx, r.svc) {
		snapshot.Expansions = append(snapshot.Expansions, contract.ScopedExpansionCount{Expansion: expansion.Expansion, Count: expansion.Count})
	}
	days, err := r.svc.NewCharacters(ctx, snapshot.ActivitySince.AddDate(0, 0, -30), generated.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	for _, day := range days {
		snapshot.NewCharacters = append(snapshot.NewCharacters, contract.ScopedDailyCount{Day: day.Day, Count: day.Count})
	}
	for _, world := range census.Worlds() {
		detail, detailErr := r.svc.WorldDetail(ctx, world)
		if detailErr != nil {
			return nil, detailErr
		}
		scope := contract.StatsScope{World: world}
		for _, expansion := range detail.MSQCompletions {
			snapshot.Expansions = append(snapshot.Expansions, contract.ScopedExpansionCount{Scope: scope, Expansion: expansion.Expansion, Count: expansion.Count})
		}
		for _, day := range detail.NewCharactersTimeline {
			snapshot.NewCharacters = append(snapshot.NewCharacters, contract.ScopedDailyCount{Scope: scope, Day: day.Day, Count: day.Count})
		}
	}
	return snapshot, nil
}

func mustExpansions(ctx context.Context, svc *census.Service) []contract.ExpansionCount {
	items, _ := svc.ExpansionCompletions(ctx)
	return items
}

func (r *testStatsRepository) Refresh(context.Context, contract.UIStatsRefreshOptions) (*contract.UIStatsRefreshResult, error) {
	snapshot, err := r.LoadCurrent(context.Background())
	return &contract.UIStatsRefreshResult{Snapshot: snapshot}, err
}

func newTestRig(t *testing.T) *testRig {
	t.Helper()
	chars := mockrepo.NewCharacterFake()
	ach := mockrepo.NewAchievementFake()
	runs := mockrepo.NewCensusRunFake()
	svc := census.NewService(chars, ach, runs)
	q := mockqueue.NewFake()
	stats := census.NewUIStatsService(&testStatsRepository{svc: svc}, time.Nanosecond, time.Hour)
	ctrl := NewUIController(svc, q, stats, testBaseURL)
	return &testRig{
		svc:   svc,
		chars: chars,
		ach:   ach,
		q:     q,
		ctrl:  ctrl,
	}
}

func TestDashboardHandler(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	// Seed test characters
	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:                  1001,
		Name:                "Tataru Taru",
		World:               "Balmung",
		Datacenter:          "Crystal",
		Region:              "NA",
		Race:                "Lalafell",
		Tribe:               "Plainsfolk",
		FirstSeenAt:         recent,
		LatestAchievementAt: &recent,
	}, []contract.ClassJobRecord{
		{CharacterID: 1001, Level: 100, Name: "Paladin"},
	})

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          1002,
		Name:        "Alphinaud Leveilleur",
		World:       "Ragnarok",
		Datacenter:  "Chaos",
		Region:      "EU",
		Race:        "Elezen",
		Tribe:       "Wildwood",
		FirstSeenAt: recent,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.Dashboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Total Population") {
		t.Errorf("expected body to contain 'Total Population', got:\n%s", body)
	}
	if !strings.Contains(body, "Active Players") {
		t.Errorf("expected body to contain 'Active Players', got:\n%s", body)
	}
	if !strings.Contains(body, "Max Level (Lv. 100)") {
		t.Errorf("expected body to contain 'Max Level (Lv. 100)', got:\n%s", body)
	}
	if !strings.Contains(body, "Characters at Cap") {
		t.Errorf("expected body to contain 'Characters at Cap', got:\n%s", body)
	}
	if !strings.Contains(body, "Crystal") && !strings.Contains(body, "NA") {
		t.Errorf("expected body to contain region stats, got:\n%s", body)
	}
}

func TestDashboardHandler_RaceChartLayout(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	// Seed characters with distinct races so the chart renders.
	races := []string{"Hyur", "Elezen", "Lalafell", "Miqo'te", "Roegadyn", "Au Ra", "Hrothgar", "Viera"}
	for i, race := range races {
		_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
			ID:                  uint32(5001 + i),
			Name:                "Char " + race,
			World:               "Balmung",
			Datacenter:          "Crystal",
			Region:              "NA",
			Race:                race,
			FirstSeenAt:         recent,
			LatestAchievementAt: &recent,
		}, nil)
	}

	req := httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.Dashboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()

	// Responsive grid replaces fixed 1fr 1fr.
	if !strings.Contains(body, "repeat(auto-fit, minmax(360px, 1fr)") {
		t.Error("expected responsive grid with repeat(auto-fit, minmax(360px, 1fr))")
	}
	// Race chart container must be 340px.
	if !strings.Contains(body, "height: 340px") {
		t.Error("expected race chart container height 340px")
	}
	// Chart.js options: maintainAspectRatio must be false.
	if !strings.Contains(body, "maintainAspectRatio: false") {
		t.Error("expected maintainAspectRatio: false in race chart options")
	}
	// Chart.js options: cutout must be 65%.
	if !strings.Contains(body, `cutout: "65%"`) && !strings.Contains(body, `cutout:'65%'`) {
		t.Error(`expected cutout: "65%" in race chart options`)
	}
	// Legend must be at bottom, not right.
	if strings.Contains(body, `position: "right"`) || strings.Contains(body, `position:"right"`) {
		t.Error("race chart legend should not be position right")
	}
	if !strings.Contains(body, `position: "bottom"`) && !strings.Contains(body, `position:"bottom"`) {
		t.Error(`expected legend position: "bottom"`)
	}
	// Legend must be centered.
	if !strings.Contains(body, `align: "center"`) && !strings.Contains(body, `align:"center"`) {
		t.Error(`expected legend align: "center"`)
	}
	// Legend must use circular point style markers, not stretched rectangles.
	if !strings.Contains(body, `usePointStyle: true`) {
		t.Error("expected usePointStyle: true in race chart legend")
	}
	if !strings.Contains(body, `pointStyle: "circle"`) {
		t.Error(`expected pointStyle: "circle" in race chart legend`)
	}
	// pointStyleWidth forces a fixed 10px marker that stretches differently
	// from the font-derived height; it must be absent.
	if strings.Contains(body, `pointStyleWidth`) {
		t.Error("race chart legend must not contain pointStyleWidth")
	}
}

func TestWorldDrilldownHandler(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:                  1001,
		Name:                "Tataru Taru",
		World:               "Balmung",
		Datacenter:          "Crystal",
		Region:              "NA",
		Race:                "Lalafell",
		Tribe:               "Plainsfolk",
		FirstSeenAt:         recent,
		LatestAchievementAt: &recent,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/partials/world-breakdown?region=NA", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.WorldDrilldown(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Balmung") {
		t.Errorf("expected body to contain 'Balmung', got:\n%s", body)
	}
}

func TestDashboardHandler_SyncNotice(t *testing.T) {
	rig := newTestRig(t)

	req := httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.Dashboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Data sync in progress") {
		t.Errorf("expected body to contain 'Data sync in progress', got:\n%s", body)
	}
	if !strings.Contains(body, "January 2027") {
		t.Errorf("expected body to contain 'January 2027', got:\n%s", body)
	}
	if !strings.Contains(body, `class="alert alert-info"`) {
		t.Errorf("expected body to contain 'alert alert-info', got:\n%s", body)
	}
}

func TestDashboardHandler_ExpansionSortOrder(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID: 5001, Name: "TestChar", World: "Balmung", Datacenter: "Crystal", Region: "NA",
		Race: "Hyur", FirstSeenAt: recent, LatestAchievementAt: &recent,
	}, nil)

	_ = rig.ach.SyncMilestones(context.Background(), census.DefaultMilestones())
	_ = rig.ach.UpsertCharacterMilestones(context.Background(), 5001, []contract.CharacterMilestone{
		{CharacterID: 5001, AchievementID: 1129, AchievedAt: recent},
		{CharacterID: 5001, AchievementID: 3496, AchievedAt: recent},
	})

	req := httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.Dashboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	arIdx := strings.Index(body, "A Realm Reborn")
	dtIdx := strings.Index(body, "Dawntrail")
	if arIdx < 0 {
		t.Fatal("expected 'A Realm Reborn' in body")
	}
	if dtIdx < 0 {
		t.Fatal("expected 'Dawntrail' in body")
	}
	if arIdx > dtIdx {
		t.Errorf("expansion sort order wrong: A Realm Reborn (idx %d) should appear before Dawntrail (idx %d)", arIdx, dtIdx)
	}
}

func TestDashboardHandler_NewCharactersCard(t *testing.T) {
	tests := []struct {
		name        string
		days        map[int]int64
		wantTrend   string
		wantNoTrend bool
	}{
		{
			name:      "growth vs previous window",
			days:      map[int]int64{1: 20, 5: 10, 40: 4, 50: 6},
			wantTrend: "▲ 20 (200.0%) vs previous 30d",
		},
		{
			name:      "zero previous window",
			days:      map[int]int64{3: 12},
			wantTrend: "▲ 12 vs previous 30d",
		},
		{
			name:        "both windows zero",
			days:        nil,
			wantTrend:   "",
			wantNoTrend: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newTestRig(t)
			now := time.Now().UTC()
			recent := now.Add(-1 * time.Hour)

			_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
				ID: 7001, Name: "Card Tester", World: "Balmung", Datacenter: "Crystal", Region: "NA",
				Race: "Hyur", FirstSeenAt: recent, LatestAchievementAt: &recent,
			}, nil)

			if tt.days != nil {
				series := make([]contract.DailyCount, 0, len(tt.days))
				for offset, count := range tt.days {
					series = append(series, contract.DailyCount{Day: now.AddDate(0, 0, -offset).Format("2006-01-02"), Count: count})
				}
				rig.ach.NewCharactersResponse = series
			}

			req := httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil)
			rec := httptest.NewRecorder()
			rig.ctrl.Dashboard(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
			}

			body := rec.Body.String()
			if !strings.Contains(body, "New Characters (30d)") {
				t.Errorf("expected card label 'New Characters (30d)' in body:\n%s", body)
			}
			if tt.wantNoTrend {
				if !strings.Contains(body, "No new characters recorded") {
					t.Errorf("expected fallback subtext 'No new characters recorded' in body:\n%s", body)
				}
				if strings.Contains(body, "vs previous 30d") {
					t.Errorf("did not expect 'vs previous 30d' when both windows are zero:\n%s", body)
				}
				return
			}
			if !strings.Contains(body, tt.wantTrend) {
				t.Errorf("expected trend %q in body:\n%s", tt.wantTrend, body)
			}
		})
	}
}

func TestDashboardWorldDrilldown_NewColumn(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID: 7101, Name: "Drill Tester", World: "Balmung", Datacenter: "Crystal", Region: "NA",
		Race: "Hyur", FirstSeenAt: recent, LatestAchievementAt: &recent,
	}, nil)

	rig.ach.NewCharactersResponse = []contract.DailyCount{
		{Day: now.AddDate(0, 0, -2).Format("2006-01-02"), Count: 7},
		{Day: now.AddDate(0, 0, -6).Format("2006-01-02"), Count: 5},
		{Day: now.AddDate(0, 0, -35).Format("2006-01-02"), Count: 3},
	}

	req := httptest.NewRequest(http.MethodGet, "/ui/partials/world-breakdown?region=NA", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.WorldDrilldown(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "New (30d)") {
		t.Errorf("expected 'New (30d)' column header in body:\n%s", body)
	}
	if !strings.Contains(body, "<td class=\"text-right\">12</td>") {
		t.Errorf("expected per-world current-window cell '12' in body:\n%s", body)
	}
}
