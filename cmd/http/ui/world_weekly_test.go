package ui

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	census "github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mockqueue "github.com/mihaiflorentin88/ffxiv-census/mock/queue"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// weeklyWorldMux serves a schema 4 snapshot generated on Oct 24 with a
// 14-day window (two weeks) in which Balmung has a weekly series.
func weeklyWorldMux(t *testing.T, weekly []int64) *http.ServeMux {
	t.Helper()
	snapshot := rankingsSnapshot(contract.UIStatsSchemaVersion)
	snapshot.GeneratedAt = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	snapshot.ActivitySince = snapshot.GeneratedAt.Add(-90 * 24 * time.Hour)
	snapshot.NewCharactersWindowDays = 14
	for i, g := range snapshot.Groups {
		if g.Scope == (contract.StatsScope{}) && g.Dimension == "world" && g.Key == "Balmung" {
			snapshot.Groups[i].NewCharactersWeekly = weekly
		}
	}
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(snapshot), time.Hour, 24*time.Hour)
	mux := http.NewServeMux()
	NewUIController(svc, mockqueue.NewFake(), stats, testBaseURL).RegisterRoutes(mux)
	return mux
}

// The world page charts its new characters per week, with the numbers in a
// table: each week's days and count, oldest first.
func TestWorldDetailChartsNewCharactersPerWeek(t *testing.T) {
	rec := serveMuxGet(t, weeklyWorldMux(t, []int64{3, 7}), "/ui/worlds/Balmung")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "New characters per week") || !strings.Contains(body, `id="weeklyNewCharactersChart"`) {
		t.Fatal("world page must chart new characters per week")
	}
	doc := parseHTML(t, body)
	got := tableBodyRows(tableCaptioned(t, doc, "New characters per week on Balmung"))
	want := [][]string{{"Oct 10 – Oct 16", "3"}, {"Oct 17 – Oct 23", "7"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("weekly table = %v, want %v", got, want)
	}
	if !strings.Contains(body, "10 new characters on Balmung in the last 2 weeks") {
		t.Error("chart summary must total the weeks")
	}
}
