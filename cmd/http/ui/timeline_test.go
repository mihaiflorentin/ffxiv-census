package ui

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mockqueue "github.com/mihaiflorentin88/ffxiv-census/mock/queue"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// timelineSnapshot is generated mid-morning on Oct 24, so Oct 24 is a
// partial day. With a 90-day window the complete days are Jul 26 to Oct 23,
// and the 13 weeks Jul 25 to Oct 23.
func timelineSnapshot(windowDays int) *contract.UIStatsSnapshot {
	generated := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	snapshot := &contract.UIStatsSnapshot{
		SchemaVersion:           contract.UIStatsSchemaVersion,
		GeneratedAt:             generated,
		ActivitySince:           generated.Add(-90 * 24 * time.Hour),
		NewCharactersWindowDays: windowDays,
		MaxLevel:                100,
	}
	for day, count := range map[string]int64{
		"2026-10-24": 50, // partial current day: left out
		"2026-10-23": 5,
		"2026-09-24": 3,
		"2026-07-26": 4,
		"2026-07-25": 7, // before the window
	} {
		snapshot.NewCharacters = append(snapshot.NewCharacters, contract.ScopedDailyCount{Day: day, Count: count})
	}
	return snapshot
}

// TestDashboardChartsTheWindowPerWeek renders the dashboard's
// new-character chart: 7-day weeks ending with the last complete UTC day,
// enough to cover the window, and a summary of them. A snapshot without a
// recorded window has no weekly series, so no chart.
func TestDashboardChartsTheWindowPerWeek(t *testing.T) {
	serve := func(windowDays int) *html.Node {
		stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(timelineSnapshot(windowDays)), time.Minute, 24*365*time.Hour)
		mux := http.NewServeMux()
		NewUIController(nil, mockqueue.NewFake(), stats, testBaseURL).RegisterRoutes(mux)
		rec := serveMuxGet(t, mux, "/")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		return parseHTML(t, rec.Body.String())
	}

	doc := serve(90)
	if got, want := flatText(byID(doc, "weekly-summary")), "19 new characters in the last 13 weeks, peaking at 11 in the week of Jul 25."; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	rows := tableBodyRows(tableCaptioned(t, doc, "New characters per week"))
	if len(rows) != 13 || rows[0][0] != "Jul 25 – Jul 31" || rows[0][1] != "11" || rows[12][0] != "Oct 17 – Oct 23" || rows[12][1] != "5" {
		t.Fatalf("weekly rows = %v, want 13 weeks from Jul 25 (11) to Oct 23 (5)", rows)
	}
	times := findAll(doc, byAtom(atom.Time))
	if len(times) == 0 {
		t.Fatal("each week should be a <time> element")
	}
	if dt, _ := attr(times[len(times)-1], "datetime"); dt != "2026-10-17" {
		t.Errorf("last week datetime = %q, want 2026-10-17", dt)
	}

	if byID(serve(0), "weekly-summary") != nil {
		t.Error("a snapshot without a recorded window has no weekly chart")
	}
}

// TestLineChartsDoNotOvershoot guards the shared line style: curves must not
// dip below zero between sparse daily counts.
func TestLineChartsDoNotOvershoot(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)
	theme := serveMuxGet(t, mux, "/ui/assets/chart-theme.js").Body.String()
	if !strings.Contains(theme, `d.elements.line.cubicInterpolationMode = "monotone";`) {
		t.Error("line charts must use monotone cubic interpolation, which never overshoots the data")
	}
}
