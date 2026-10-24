package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestRacesHandler(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:                  2001,
		Name:                "Miqo'te Adventurer",
		World:               "Balmung",
		Datacenter:          "Crystal",
		Region:              "NA",
		Race:                "Miqo'te",
		Tribe:               "Seeker of the Sun",
		FirstSeenAt:         recent,
		LatestAchievementAt: &recent,
	}, nil)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          2002,
		Name:        "Lalafell Merchant",
		World:       "Ragnarok",
		Datacenter:  "Chaos",
		Region:      "EU",
		Race:        "Lalafell",
		Tribe:       "Dunesfolk",
		FirstSeenAt: recent,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/races", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.Races(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Miqo&#39;te") && !strings.Contains(body, "Miqo'te") {
		t.Errorf("expected body to contain Miqo'te, got:\n%s", body)
	}
	if !strings.Contains(body, "Lalafell") {
		t.Errorf("expected body to contain Lalafell, got:\n%s", body)
	}
	if !strings.Contains(strings.ToLower(body), "race distribution") {
		t.Errorf("expected body to contain 'Race distribution', got:\n%s", body)
	}

	// Filtered request: Datacenter Chaos (should exclude Miqo'te on Balmung)
	reqFiltered := httptest.NewRequest(http.MethodGet, "/ui/races?dc=Chaos", nil)
	recFiltered := httptest.NewRecorder()
	rig.ctrl.Races(recFiltered, reqFiltered)

	if recFiltered.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recFiltered.Code, recFiltered.Body.String())
	}
	bodyFiltered := recFiltered.Body.String()
	if !strings.Contains(bodyFiltered, "Lalafell") {
		t.Errorf("expected filtered body to contain Lalafell, got:\n%s", bodyFiltered)
	}
	if strings.Contains(bodyFiltered, "Miqo&#39;te") || strings.Contains(bodyFiltered, "Miqo'te") {
		t.Errorf("expected filtered body to exclude Miqo'te on Crystal DC")
	}
}

func TestRacesCascadingFilters(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          4001,
		Name:        "EU Player",
		World:       "Cerberus",
		Datacenter:  "Chaos",
		Region:      "EU",
		Race:        "Hyur",
		Tribe:       "Midlander",
		FirstSeenAt: recent,
	}, nil)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          4002,
		Name:        "NA Player",
		World:       "Gilgamesh",
		Datacenter:  "Aether",
		Region:      "NA",
		Race:        "Elezen",
		Tribe:       "Wildwood",
		FirstSeenAt: recent,
	}, nil)

	// region=EU → DC dropdown should contain only Chaos & Light, not Aether/Crystal
	reqEU := httptest.NewRequest(http.MethodGet, "/ui/races?region=EU", nil)
	recEU := httptest.NewRecorder()
	rig.ctrl.Races(recEU, reqEU)

	if recEU.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recEU.Code)
	}
	bodyEU := recEU.Body.String()
	if !strings.Contains(bodyEU, `<option value="Chaos"`) {
		t.Error("region=EU: expected DC dropdown to contain Chaos")
	}
	if !strings.Contains(bodyEU, `<option value="Light"`) {
		t.Error("region=EU: expected DC dropdown to contain Light")
	}
	if strings.Contains(bodyEU, `<option value="Aether"`) {
		t.Error("region=EU: DC dropdown should NOT contain Aether")
	}
	if strings.Contains(bodyEU, `<option value="Crystal"`) {
		t.Error("region=EU: DC dropdown should NOT contain Crystal")
	}

	// region=EU&dc=Chaos → World dropdown should contain Chaos worlds, not Light worlds
	reqChaos := httptest.NewRequest(http.MethodGet, "/ui/races?region=EU&dc=Chaos", nil)
	recChaos := httptest.NewRecorder()
	rig.ctrl.Races(recChaos, reqChaos)

	if recChaos.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recChaos.Code)
	}
	bodyChaos := recChaos.Body.String()
	if !strings.Contains(bodyChaos, `<option value="Cerberus"`) {
		t.Error("region=EU&dc=Chaos: expected World dropdown to contain Cerberus")
	}
	if strings.Contains(bodyChaos, `<option value="Alpha"`) {
		t.Error("region=EU&dc=Chaos: World dropdown should NOT contain Light world Alpha")
	}
	if strings.Contains(bodyChaos, `<option value="Lich"`) {
		t.Error("region=EU&dc=Chaos: World dropdown should NOT contain Light world Lich")
	}
}

func TestRacesHandler_DemographicPieCharts(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID: 7001, Name: "SunSeeker", World: "Balmung", Datacenter: "Crystal", Region: "NA",
		Race: "Miqo'te", Tribe: "Seekers of the Sun", Gender: 2,
		FirstSeenAt: recent, LatestAchievementAt: &recent,
	}, nil)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID: 7002, Name: "Highlander", World: "Louisoix", Datacenter: "Chaos", Region: "EU",
		Race: "Hyur", Tribe: "Highlander", Gender: 1,
		FirstSeenAt: recent, LatestAchievementAt: &recent,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/races", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.Races(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	if !strings.Contains(body, "tribePieChart") {
		t.Error("expected tribePieChart canvas in body")
	}
	if !strings.Contains(body, "Seekers of the Sun") {
		t.Error("expected tribe 'Seekers of the Sun' in body")
	}

	if !strings.Contains(body, "genderPieChart") {
		t.Error("expected genderPieChart canvas in body")
	}

	if !strings.Contains(body, "raceGenderPieChart") {
		t.Error("expected raceGenderPieChart canvas in body")
	}
}

func TestRacesHandler_DemographicChartsFiltered(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID: 8001, Name: "NAChar", World: "Balmung", Datacenter: "Crystal", Region: "NA",
		Race: "Miqo'te", Tribe: "Seekers of the Sun", Gender: 2,
		FirstSeenAt: recent, LatestAchievementAt: &recent,
	}, nil)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID: 8002, Name: "EUChar", World: "Louisoix", Datacenter: "Chaos", Region: "EU",
		Race: "Hyur", Tribe: "Highlander", Gender: 1,
		FirstSeenAt: recent, LatestAchievementAt: &recent,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/races?region=NA", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.Races(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Seekers of the Sun") {
		t.Error("expected filtered body to contain NA tribe")
	}
	if strings.Contains(body, "Highlander") {
		t.Error("expected filtered body to exclude EU tribe")
	}
}

// newCharactersDaysFor builds an override daily new-character series with
// counts keyed by day offset relative to now: offsets 1-29 fall in the
// current 30-day window, offsets 30-59 in the previous one.
func newCharactersDaysFor(offsets map[int]int64) []contract.DailyCount {
	now := time.Now().UTC()
	days := make([]contract.DailyCount, 0, len(offsets))
	for offset, count := range offsets {
		days = append(days, contract.DailyCount{
			Day:   now.AddDate(0, 0, -offset).Format("2006-01-02"),
			Count: count,
		})
	}
	return days
}

func TestRacesHandler_NewCharactersCard(t *testing.T) {
	// The rig's NewCharactersResponse override is replicated verbatim into the
	// global series AND into every world-scoped series, so a filter resolving
	// to N worlds scales each per-window sum by N. Expected trends therefore
	// use the same world mappings the handler resolves per scope.
	regionWorlds := func(region string) []string {
		var worlds []string
		for _, dc := range DCsForRegion(region) {
			worlds = append(worlds, WorldsForDC(dc)...)
		}
		return worlds
	}
	growthTrend := func(worldCount int) string {
		// Current 30 (20+10) vs previous 10 (4+6) -> delta 20, 200.0%.
		return fmt.Sprintf("Up +%s (+200.0%%)", formatNumber(int64(20*worldCount)))
	}
	crystalWorlds := len(WorldsForDC("Crystal"))
	naWorlds := len(regionWorlds("NA"))
	growthDays := func() []contract.DailyCount {
		return newCharactersDaysFor(map[int]int64{1: 20, 5: 10, 40: 4, 50: 6})
	}
	tests := []struct {
		name          string
		query         string
		newCharacters []contract.DailyCount
		wantContains  []string
		wantAbsent    []string
	}{
		{
			name:          "no selection sums the global window",
			query:         "/ui/races",
			newCharacters: growthDays(),
			wantContains:  []string{growthTrend(1)},
		},
		{
			name:          "region filter sums member worlds",
			query:         "/ui/races?region=NA",
			newCharacters: growthDays(),
			wantContains:  []string{growthTrend(naWorlds)},
		},
		{
			name:          "lowercase region still scopes the card",
			query:         "/ui/races?region=na",
			newCharacters: growthDays(),
			wantContains:  []string{growthTrend(naWorlds)},
		},
		{
			name:          "datacenter filter sums dc worlds",
			query:         "/ui/races?dc=Crystal",
			newCharacters: growthDays(),
			wantContains:  []string{growthTrend(crystalWorlds)},
		},
		{
			name:          "world filter sums that world",
			query:         "/ui/races?world=Balmung",
			newCharacters: growthDays(),
			wantContains:  []string{growthTrend(1)},
		},
		{
			name:          "zero previous window renders absolute delta",
			query:         "/ui/races?dc=Crystal",
			newCharacters: newCharactersDaysFor(map[int]int64{3: 12}),
			wantContains:  []string{fmt.Sprintf("Up +%s on the previous 30 days", formatNumber(int64(12*crystalWorlds)))},
		},
		{
			name:         "both windows zero renders fallback",
			query:        "/ui/races",
			wantContains: []string{"No new characters recorded"},
			wantAbsent:   []string{"on the previous 30 days"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newTestRig(t)
			rig.ach.NewCharactersResponse = tt.newCharacters

			req := httptest.NewRequest(http.MethodGet, tt.query, nil)
			rec := httptest.NewRecorder()
			rig.ctrl.Races(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
			}
			body := textOf(parseHTML(t, rec.Body.String()))
			for _, want := range tt.wantContains {
				if !strings.Contains(body, want) {
					t.Errorf("expected body to contain %q, got:\n%s", want, body)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(body, absent) {
					t.Errorf("expected body NOT to contain %q, got:\n%s", absent, body)
				}
			}
		})
	}
}
