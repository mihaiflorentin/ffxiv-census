package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestWorldDetailHandler(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:                  3001,
		Name:                "Louisoix Warrior",
		World:               "Louisoix",
		Datacenter:          "Chaos",
		Region:              "EU",
		Race:                "Hyur",
		FirstSeenAt:         recent,
		LatestAchievementAt: &recent,
	}, nil)

	rig.ach.ChocoboCountResponse = 1
	rig.ach.ExpansionsResponse = []contract.ExpansionCount{{Expansion: "Dawntrail", Count: 1}}
	rig.ach.NewCharactersResponse = []contract.DailyCount{{Day: "2026-08-17", Count: 1}}

	req := httptest.NewRequest(http.MethodGet, "/ui/worlds/Louisoix", nil)
	req.SetPathValue("world", "Louisoix")
	rec := httptest.NewRecorder()
	rig.ctrl.WorldDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Louisoix") {
		t.Errorf("expected body to contain Louisoix, got:\n%s", body)
	}
	if !strings.Contains(body, "Chaos") {
		t.Errorf("expected body to contain Chaos datacenter, got:\n%s", body)
	}
	if !strings.Contains(body, "Dawntrail") {
		t.Errorf("expected body to contain Dawntrail expansion, got:\n%s", body)
	}
}

func TestWorldDetailHandler_EmptyWorldRedirect(t *testing.T) {
	rig := newTestRig(t)
	req := httptest.NewRequest(http.MethodGet, "/ui/worlds/", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.WorldDetail(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected status 302 redirect, got %d", rec.Code)
	}
}

func TestWorldDetailHandler_NewCharactersTrend(t *testing.T) {
	now := time.Now().UTC()
	day := func(offsetDays int) string {
		return now.AddDate(0, 0, -offsetDays).Format("2006-01-02")
	}
	cases := []struct {
		name       string
		days       []contract.DailyCount
		wantTrend  string
		wantAbsent string
	}{
		{
			name:      "growth shows positive trend",
			days:      []contract.DailyCount{{Day: day(1), Count: 20}, {Day: day(5), Count: 10}, {Day: day(100), Count: 4}, {Day: day(120), Count: 6}},
			wantTrend: "Up +20 (+200.0%) on the previous 90 days",
		},
		{
			name:      "decline shows negative trend",
			days:      []contract.DailyCount{{Day: day(2), Count: 5}, {Day: day(100), Count: 15}, {Day: day(110), Count: 10}},
			wantTrend: "Down −20 (−80.0%) on the previous 90 days",
		},
		{
			name:      "no previous window shows absolute only",
			days:      []contract.DailyCount{{Day: day(3), Count: 12}},
			wantTrend: "Up +12 on the previous 90 days",
		},
		{
			name:       "no data omits trend line",
			days:       nil,
			wantAbsent: "on the previous 90 days",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRig(t)
			rig.stats.everyWorld = true // the overridden series reaches every world
			rig.ach.NewCharactersResponse = tc.days

			req := httptest.NewRequest(http.MethodGet, "/ui/worlds/Louisoix", nil)
			req.SetPathValue("world", "Louisoix")
			rec := httptest.NewRecorder()
			rig.ctrl.WorldDetail(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
			}
			body := textOf(parseHTML(t, rec.Body.String()))
			if tc.wantTrend != "" && !strings.Contains(body, tc.wantTrend) {
				t.Errorf("expected body to contain trend %q, got:\n%s", tc.wantTrend, body)
			}
			if tc.wantAbsent != "" && strings.Contains(body, tc.wantAbsent) {
				t.Errorf("expected body to NOT contain %q, got:\n%s", tc.wantAbsent, body)
			}
		})
	}
}
