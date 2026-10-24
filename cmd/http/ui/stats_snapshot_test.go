package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mockqueue "github.com/mihaiflorentin88/ffxiv-census/mock/queue"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestDashboardUsesSnapshotWhenRawAggregateRepositoriesFail(t *testing.T) {
	generated := time.Now().UTC().Truncate(time.Second)
	snapshot := &contract.UIStatsSnapshot{
		SchemaVersion:    contract.UIStatsSchemaVersion,
		GeneratedAt:      generated,
		ActivitySince:    generated.Add(-30 * 24 * time.Hour),
		MaxLevel:         100,
		SourceCharacters: 42,
		Summary:          contract.StatsSummary{Total: 42, Active: 12, MaxLevel: 7},
	}
	statsRepo := mockrepo.NewUIStatsFake(snapshot)
	stats := census.NewUIStatsService(statsRepo, time.Minute, time.Hour)
	chars := mockrepo.NewCharacterFake()
	chars.SummaryCountsErr = errSnapshotOnly
	chars.BreakdownErr = errSnapshotOnly
	chars.MultiBreakdownErr = errSnapshotOnly
	ach := mockrepo.NewAchievementFake()
	ach.CountExpansionsErr = errSnapshotOnly
	ach.NewCharactersPerDayErr = errSnapshotOnly
	svc := census.NewService(chars, ach, mockrepo.NewCensusRunFake())
	ctrl := NewUIController(svc, mockqueue.NewFake(), stats, testBaseURL)

	rec := httptest.NewRecorder()
	ctrl.Dashboard(rec, httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "42") {
		t.Fatalf("dashboard does not contain snapshot total: %s", rec.Body.String())
	}
	if statsRepo.LoadCalls != 1 {
		t.Fatalf("snapshot loads = %d, want 1", statsRepo.LoadCalls)
	}
}

func TestUIControllerPrecompilesTemplates(t *testing.T) {
	rig := newTestRig(t)
	first := rig.ctrl.pageTemplates["templates/dashboard.html"]
	if first == nil {
		t.Fatal("dashboard template was not precompiled")
	}

	for range 2 {
		rec := httptest.NewRecorder()
		rig.ctrl.Dashboard(rec, httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if rig.ctrl.pageTemplates["templates/dashboard.html"] != first {
		t.Fatal("dashboard template pointer changed between renders")
	}
}

func TestDashboardETagAndSnapshotFreshness(t *testing.T) {
	generated := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	snapshot := &contract.UIStatsSnapshot{
		SchemaVersion:    contract.UIStatsSchemaVersion,
		GeneratedAt:      generated,
		ActivitySince:    generated.Add(-30 * 24 * time.Hour),
		MaxLevel:         100,
		SourceCharacters: 1,
		Summary:          contract.StatsSummary{Total: 1},
	}
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(snapshot), time.Minute, 365*24*time.Hour)
	ctrl := NewUIController(census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake()), mockqueue.NewFake(), stats, testBaseURL)

	first := httptest.NewRecorder()
	ctrl.Dashboard(first, httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}
	etag := first.Header().Get("ETag")
	if etag == "" || !strings.Contains(first.Body.String(), "Statistics updated 2026-08-24 12:00 UTC") {
		t.Fatalf("etag/body = %q / %s", etag, first.Body.String())
	}
	if vary := first.Header().Get("Vary"); !strings.Contains(vary, "HX-Request") {
		t.Fatalf("Vary = %q", vary)
	}

	request := httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil)
	request.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	ctrl.Dashboard(second, request)
	if second.Code != http.StatusNotModified || second.Body.Len() != 0 {
		t.Fatalf("conditional response = %d body=%q", second.Code, second.Body.String())
	}
}

func TestDashboardMarksOldSnapshotStale(t *testing.T) {
	generated := time.Now().UTC().Add(-13 * time.Hour)
	snapshot := &contract.UIStatsSnapshot{
		SchemaVersion: contract.UIStatsSchemaVersion,
		GeneratedAt:   generated,
		ActivitySince: generated.Add(-30 * 24 * time.Hour),
		MaxLevel:      100,
	}
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(snapshot), time.Minute, 12*time.Hour)
	ctrl := NewUIController(nil, mockqueue.NewFake(), stats, testBaseURL)
	rec := httptest.NewRecorder()
	ctrl.Dashboard(rec, httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	notice := staleNotice(t, rec.Body.String())
	if notice == "" {
		t.Fatalf("stale snapshot must render the stale notice: %s", rec.Body.String())
	}
	if !strings.Contains(notice, "may be behind") || !strings.Contains(notice, generated.Format("2006-01-02 15:04 UTC")) {
		t.Fatalf("stale notice = %q, want it to say the figures may be behind and name the snapshot time", notice)
	}
}

// staleNotice returns the text of the status notice inside main, or "".
func staleNotice(t *testing.T, body string) string {
	t.Helper()
	doc := parseHTML(t, body)
	for _, main := range findAll(doc, byAtom(atom.Main)) {
		for _, n := range findAll(main, func(n *html.Node) bool { role, _ := attr(n, "role"); return role == "status" }) {
			if text := textOf(n); strings.Contains(strings.ToLower(text), "behind") {
				return text
			}
		}
	}
	return ""
}

func TestDashboardOmitsStaleNoticeDuringRoutineReload(t *testing.T) {
	generated := time.Now().UTC().Add(-5 * time.Minute)
	snapshot := &contract.UIStatsSnapshot{
		SchemaVersion: contract.UIStatsSchemaVersion,
		GeneratedAt:   generated,
		ActivitySince: generated.Add(-30 * 24 * time.Hour),
		MaxLevel:      100,
	}
	// A nanosecond cache TTL makes every request after the first serve the
	// warm copy while a background reload runs.
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(snapshot), time.Nanosecond, 12*time.Hour)
	ctrl := NewUIController(nil, mockqueue.NewFake(), stats, testBaseURL)
	for i := range 3 {
		rec := httptest.NewRecorder()
		ctrl.Dashboard(rec, httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d", i, rec.Code)
		}
		if notice := staleNotice(t, rec.Body.String()); notice != "" {
			t.Fatalf("request %d rendered stale notice %q for a five-minute-old snapshot", i, notice)
		}
	}
}

func TestDashboardETagIncludesNormalizedQueryAndRepresentation(t *testing.T) {
	generated := time.Now().UTC()
	snapshot := &contract.UIStatsSnapshot{
		SchemaVersion: contract.UIStatsSchemaVersion,
		GeneratedAt:   generated,
		ActivitySince: generated.Add(-30 * 24 * time.Hour),
		MaxLevel:      100,
	}
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(snapshot), time.Minute, 12*time.Hour)
	ctrl := NewUIController(nil, mockqueue.NewFake(), stats, testBaseURL)

	etag := func(target string, htmx bool) string {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if htmx {
			req.Header.Set("HX-Request", "true")
		}
		rec := httptest.NewRecorder()
		ctrl.Dashboard(rec, req)
		return rec.Header().Get("ETag")
	}
	plain := etag("/ui/dashboard?a=1&b=2", false)
	if reordered := etag("/ui/dashboard?b=2&a=1", false); reordered != plain {
		t.Fatalf("query order changed ETag: %q != %q", reordered, plain)
	}
	if filtered := etag("/ui/dashboard?a=2&b=2", false); filtered == plain {
		t.Fatalf("different query reused ETag %q", plain)
	}
	if partial := etag("/ui/dashboard?a=1&b=2", true); partial == plain {
		t.Fatalf("HTMX representation reused ETag %q", plain)
	}
}

func TestAnalyticsRouteWithoutSnapshotFailsFast(t *testing.T) {
	ctrl := NewUIController(nil, mockqueue.NewFake(), nil, testBaseURL)
	rec := httptest.NewRecorder()
	ctrl.Dashboard(rec, httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("response = %d Retry-After=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

var errSnapshotOnly = &snapshotOnlyError{}

type snapshotOnlyError struct{}

func (*snapshotOnlyError) Error() string { return "raw aggregate called" }

// TestETagChangesWithTheBuild stops a browser from keeping a page from the
// previous release: after a deploy the snapshot can be unchanged, so the
// ETag must also carry the build, or a conditional request answers 304 and
// the old markup (old labels, old switches) stays on screen.
func TestETagChangesWithTheBuild(t *testing.T) {
	generated := time.Now().UTC()
	snapshot := &contract.UIStatsSnapshot{
		SchemaVersion: contract.UIStatsSchemaVersion,
		GeneratedAt:   generated,
		ActivitySince: generated.Add(-30 * 24 * time.Hour),
		MaxLevel:      100,
	}
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(snapshot), time.Minute, 12*time.Hour)
	oldBuild := NewUIController(nil, mockqueue.NewFake(), stats, testBaseURL)
	oldBuild.buildID = "old"
	newBuild := NewUIController(nil, mockqueue.NewFake(), stats, testBaseURL)
	newBuild.buildID = "new"

	first := httptest.NewRecorder()
	oldBuild.Dashboard(first, httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil))
	etag := first.Header().Get("ETag")

	request := httptest.NewRequest(http.MethodGet, "/ui/dashboard", nil)
	request.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	newBuild.Dashboard(second, request)
	if second.Code != http.StatusOK {
		t.Fatalf("a new build answered %d to the old build's ETag; want 200 with fresh markup", second.Code)
	}
}

// TestBuildIDIsStableWithinAProcess keeps ETags reusable between requests
// served by the same binary.
func TestBuildIDIsStableWithinAProcess(t *testing.T) {
	if a, b := uiBuildID(), uiBuildID(); a == "" || a != b {
		t.Fatalf("build id = %q then %q; want one non-empty value", a, b)
	}
}
