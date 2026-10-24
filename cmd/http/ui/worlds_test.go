package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestWorldsHandler(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:                  3001,
		Name:                "Gilgamesh Knight",
		World:               "Gilgamesh",
		Datacenter:          "Aether",
		Region:              "NA",
		Race:                "Hyur",
		Tribe:               "Midlander",
		FirstSeenAt:         recent,
		LatestAchievementAt: &recent,
	}, nil)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          3002,
		Name:        "Cerberus Mage",
		World:       "Cerberus",
		Datacenter:  "Chaos",
		Region:      "EU",
		Race:        "Elezen",
		Tribe:       "Duskwight",
		FirstSeenAt: recent,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/worlds", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.Worlds(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Gilgamesh") {
		t.Errorf("expected body to contain 'Gilgamesh', got:\n%s", body)
	}
	if !strings.Contains(body, "Cerberus") {
		t.Errorf("expected body to contain 'Cerberus', got:\n%s", body)
	}
	if !strings.Contains(body, "Aether") {
		t.Errorf("expected body to contain 'Aether', got:\n%s", body)
	}
}

func TestWorldsCascadingFilters(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          5001,
		Name:        "NA Player",
		World:       "Gilgamesh",
		Datacenter:  "Aether",
		Region:      "NA",
		Race:        "Hyur",
		Tribe:       "Midlander",
		FirstSeenAt: recent,
	}, nil)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          5002,
		Name:        "EU Player",
		World:       "Cerberus",
		Datacenter:  "Chaos",
		Region:      "EU",
		Race:        "Elezen",
		Tribe:       "Wildwood",
		FirstSeenAt: recent,
	}, nil)

	// region=NA → DC dropdown should contain Aether, not Chaos/Light
	reqNA := httptest.NewRequest(http.MethodGet, "/ui/worlds?region=NA", nil)
	recNA := httptest.NewRecorder()
	rig.ctrl.Worlds(recNA, reqNA)

	if recNA.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recNA.Code)
	}
	bodyNA := recNA.Body.String()
	if !strings.Contains(bodyNA, `<option value="Aether"`) {
		t.Error("region=NA: expected DC dropdown to contain Aether")
	}
	if strings.Contains(bodyNA, `<option value="Chaos"`) {
		t.Error("region=NA: DC dropdown should NOT contain Chaos")
	}
	if strings.Contains(bodyNA, `<option value="Light"`) {
		t.Error("region=NA: DC dropdown should NOT contain Light")
	}
}

// newCharactersDay renders a UTC day string offsetDays before today, matching
// the day format of the new-character daily series.
func newCharactersDay(offsetDays int) string {
	return time.Now().UTC().AddDate(0, 0, -offsetDays).Format("2006-01-02")
}

// regionWorldsForTest enumerates every known world belonging to the region.
func regionWorldsForTest(region string) []string {
	var worlds []string
	for _, dc := range DCsForRegion(region) {
		worlds = append(worlds, WorldsForDC(dc)...)
	}
	sort.Strings(worlds)
	return worlds
}

// seedWorldsCharacters inserts one NA and one EU character so the snapshot
// summary and world groups are populated.
func seedWorldsCharacters(t *testing.T, rig *testRig) {
	t.Helper()
	recent := time.Now().UTC().Add(-1 * time.Hour)
	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          7101,
		Name:        "Gilgamesh Scout",
		World:       "Gilgamesh",
		Datacenter:  "Aether",
		Region:      "NA",
		Race:        "Hyur",
		Tribe:       "Midlander",
		FirstSeenAt: recent,
	}, nil)
	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          7102,
		Name:        "Cerberus Scout",
		World:       "Cerberus",
		Datacenter:  "Chaos",
		Region:      "EU",
		Race:        "Elezen",
		Tribe:       "Duskwight",
		FirstSeenAt: recent,
	}, nil)
}

// rankingsRowFor returns the rankings table row segment rendered for a world.
func rankingsRowFor(body, world string) string {
	for _, row := range strings.Split(body, "<tr>") {
		if strings.Contains(row, ">"+world+"</a>") {
			return row
		}
	}
	return ""
}

func TestWorldsHandler_NewCharactersCard(t *testing.T) {
	chaosCount := len(WorldsForDC("Chaos"))
	euCount := len(regionWorldsForTest("EU"))

	scenarios := []struct {
		name      string
		days      []contract.DailyCount
		denyTrend bool
		wantTrend map[string]string
	}{
		{
			name: "growth vs previous window",
			days: []contract.DailyCount{
				{Day: newCharactersDay(1), Count: 20},
				{Day: newCharactersDay(5), Count: 10},
				{Day: newCharactersDay(100), Count: 4},
				{Day: newCharactersDay(120), Count: 6},
			},
			wantTrend: map[string]string{
				"":                    "Up +20 (+200.0%)",
				"?dc=Chaos":           fmt.Sprintf("Up +%s (+200.0%%)", formatNumber(int64(20*chaosCount))),
				"?region=EU&dc=Chaos": fmt.Sprintf("Up +%s (+200.0%%)", formatNumber(int64(20*chaosCount))),
				"?region=EU":          fmt.Sprintf("Up +%s (+200.0%%)", formatNumber(int64(20*euCount))),
				"?region=eu":          fmt.Sprintf("Up +%s (+200.0%%)", formatNumber(int64(20*euCount))),
			},
		},
		{
			name: "no previous window data",
			days: []contract.DailyCount{
				{Day: newCharactersDay(3), Count: 12},
			},
			wantTrend: map[string]string{
				"":                    "Up +12 on the previous 90 days",
				"?dc=Chaos":           fmt.Sprintf("Up +%s on the previous 90 days", formatNumber(int64(12*chaosCount))),
				"?region=EU&dc=Chaos": fmt.Sprintf("Up +%s on the previous 90 days", formatNumber(int64(12*chaosCount))),
				"?region=EU":          fmt.Sprintf("Up +%s on the previous 90 days", formatNumber(int64(12*euCount))),
				"?region=eu":          fmt.Sprintf("Up +%s on the previous 90 days", formatNumber(int64(12*euCount))),
			},
		},
		{
			name:      "no new characters at all",
			days:      nil,
			denyTrend: true,
			wantTrend: map[string]string{
				"":                    "No new characters recorded",
				"?dc=Chaos":           "No new characters recorded",
				"?region=EU&dc=Chaos": "No new characters recorded",
				"?region=EU":          "No new characters recorded",
				"?region=eu":          "No new characters recorded",
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			rig := newTestRig(t)
			rig.stats.everyWorld = true // the overridden series reaches every world
			rig.ach.NewCharactersResponse = scenario.days
			seedWorldsCharacters(t, rig)

			for query, want := range scenario.wantTrend {
				req := httptest.NewRequest(http.MethodGet, "/ui/worlds"+query, nil)
				rec := httptest.NewRecorder()
				rig.ctrl.Worlds(rec, req)

				if rec.Code != http.StatusOK {
					t.Fatalf("%s: expected status 200, got %d: %s", query, rec.Code, rec.Body.String())
				}
				body := textOf(parseHTML(t, rec.Body.String()))
				if !strings.Contains(body, want) {
					t.Errorf("%s: expected card subtext %q in body:\n%s", query, want, body)
				}
				if scenario.denyTrend && strings.Contains(body, "on the previous 90 days") {
					t.Errorf("%s: expected no trend subtext, got:\n%s", query, body)
				}
			}
		})
	}
}

func TestWorldsWorldRankings_NewColumn(t *testing.T) {
	rig := newTestRig(t)
	rig.ach.NewCharactersResponse = []contract.DailyCount{
		{Day: newCharactersDay(1), Count: 77000},
	}
	seedWorldsCharacters(t, rig)

	req := httptest.NewRequest(http.MethodGet, "/ui/worlds?region=EU&dc=Chaos", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.Worlds(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, `>New, 90 days</th>`) {
		t.Errorf("expected New, 90 days table header in body:\n%s", body)
	}

	row := rankingsRowFor(body, "Cerberus")
	if row == "" {
		t.Fatal("expected a rankings row for Cerberus")
	}
	if !strings.Contains(row, "77,000</td>") {
		t.Errorf("expected Cerberus row 'New, 90 days' cell '77,000', got row:\n%s", row)
	}
}
