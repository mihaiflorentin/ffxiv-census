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
// partial day. With a 90-day window the complete days are Jul 26 to Oct 23;
// a snapshot without a recorded window (windowDays 0) used 30 days, Sep 24
// to Oct 23.
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

func timelineTableRows(t *testing.T, doc *html.Node) []*html.Node {
	t.Helper()
	for _, table := range findAll(doc, byAtom(atom.Table)) {
		captions := findAll(table, byAtom(atom.Caption))
		if len(captions) == 1 && strings.HasPrefix(textOf(captions[0]), "New characters per day") {
			return findAll(table, func(n *html.Node) bool {
				scope, _ := attr(n, "scope")
				return n.DataAtom == atom.Th && scope == "row"
			})
		}
	}
	t.Fatal("no 'New characters per day' table")
	return nil
}

// TestTimelineSpansOneWindowEndingOnTheLastCompleteDay renders the
// dashboard's new-character chart: it covers the snapshot's window, one
// row per complete UTC day, and its summary states the window.
func TestTimelineSpansOneWindowEndingOnTheLastCompleteDay(t *testing.T) {
	cases := []struct {
		name        string
		windowDays  int
		wantDays    int
		wantFirst   string
		wantSummary string
	}{
		{"90-day window", 90, 90, "Jul 26", "12 new characters in the last 90 days, peaking at 5 on Oct 23."},
		{"snapshot without a window", 0, 30, "Sep 24", "8 new characters in the last 30 days, peaking at 5 on Oct 23."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(timelineSnapshot(tc.windowDays)), time.Minute, 24*365*time.Hour)
			mux := http.NewServeMux()
			NewUIController(nil, mockqueue.NewFake(), stats, testBaseURL).RegisterRoutes(mux)
			rec := serveMuxGet(t, mux, "/")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			doc := parseHTML(t, rec.Body.String())

			summary := byID(doc, "timeline-summary")
			if summary == nil || textOf(summary) != tc.wantSummary {
				t.Errorf("summary = %q, want %q", textOf(summary), tc.wantSummary)
			}

			rows := timelineTableRows(t, doc)
			if len(rows) != tc.wantDays {
				t.Fatalf("timeline has %d days, want %d", len(rows), tc.wantDays)
			}
			first, last := rows[0], rows[len(rows)-1]
			if textOf(first) != tc.wantFirst || textOf(last) != "Oct 23" {
				t.Errorf("timeline runs %q to %q, want %s to Oct 23", textOf(first), textOf(last), tc.wantFirst)
			}
			times := findAll(last, byAtom(atom.Time))
			if len(times) != 1 {
				t.Fatal("each day should be a <time> element")
			}
			if dt, _ := attr(times[0], "datetime"); dt != "2026-10-23" {
				t.Errorf("last day datetime = %q, want 2026-10-23", dt)
			}
		})
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
