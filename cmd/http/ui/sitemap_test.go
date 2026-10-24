package ui

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	census "github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mockqueue "github.com/mihaiflorentin88/ffxiv-census/mock/queue"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// parsedSitemapURLSet and parsedSitemapURL mirror the sitemap-protocol XML
// document so the response body can be parsed with encoding/xml.
type parsedSitemapURLSet struct {
	XMLName xml.Name           `xml:"urlset"`
	URLs    []parsedSitemapURL `xml:"url"`
}

type parsedSitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod"`
}

func newSitemapTestController(t *testing.T) (*UIController, *census.UIStatsService, *mockrepo.CharacterRepository) {
	t.Helper()
	chars := mockrepo.NewCharacterFake()
	ach := mockrepo.NewAchievementFake()
	runs := mockrepo.NewCensusRunFake()
	svc := census.NewService(chars, ach, runs)
	// Long cache TTL pins the snapshot so the handler and the test observe the
	// same GeneratedAt deterministically.
	stats := census.NewUIStatsService(&testStatsRepository{svc: svc}, time.Hour, time.Hour)
	ctrl := NewUIController(svc, mockqueue.NewFake(), stats, testBaseURL)
	return ctrl, stats, chars
}

func serveSitemap(t *testing.T, ctrl *UIController) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	ctrl.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSitemapHandler(t *testing.T) {
	base := testBaseURL
	ctrl, stats, chars := newSitemapTestController(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          4001,
		Name:        "Sitemap Balmung",
		World:       "Balmung",
		Datacenter:  "Crystal",
		Region:      "NA",
		Race:        "Lalafell",
		FirstSeenAt: recent,
	}, nil)
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          4002,
		Name:        "Sitemap Ragnarok",
		World:       "Ragnarok",
		Datacenter:  "Chaos",
		Region:      "EU",
		Race:        "Elezen",
		FirstSeenAt: recent,
	}, nil)

	snapshot, _, err := stats.Current(context.Background())
	if err != nil {
		t.Fatalf("expected statistics snapshot, got error: %v", err)
	}
	lastmod := snapshot.GeneratedAt.UTC().Format(time.RFC3339)

	rec := serveSitemap(t, ctrl)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/xml; charset=utf-8" {
		t.Errorf("expected Content-Type application/xml; charset=utf-8, got %q", got)
	}

	var set parsedSitemapURLSet
	if err := xml.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatalf("expected parseable sitemap XML, got error: %v\nbody:\n%s", err, rec.Body.String())
	}

	want := []string{
		base + "/",
		base + "/ui/races",
		base + "/ui/grand-companies",
		base + "/ui/worlds",
		base + "/ui/expansions",
		base + "/ui/methodology",
		base + "/ui/privacy",
		base + "/ui/worlds/Balmung",
		base + "/ui/worlds/Ragnarok",
	}
	if len(set.URLs) != len(want) {
		t.Fatalf("expected %d URLs, got %d: %+v", len(want), len(set.URLs), set.URLs)
	}
	for i, got := range set.URLs {
		if got.Loc != want[i] {
			t.Errorf("URL %d: expected %q, got %q", i, want[i], got.Loc)
		}
		if got.LastMod != lastmod {
			t.Errorf("URL %d (%s): expected lastmod %q, got %q", i, want[i], lastmod, got.LastMod)
		}
	}
}

type testErrorStatsRepository struct{}

func (r *testErrorStatsRepository) LoadCurrent(context.Context) (*contract.UIStatsSnapshot, error) {
	return nil, errors.New("snapshot unavailable")
}

func (r *testErrorStatsRepository) Refresh(context.Context, contract.UIStatsRefreshOptions) (*contract.UIStatsRefreshResult, error) {
	return nil, errors.New("snapshot unavailable")
}

func TestSitemapHandler_DegradedStillServesStaticURLs(t *testing.T) {
	base := testBaseURL
	want := []string{
		base + "/",
		base + "/ui/races",
		base + "/ui/grand-companies",
		base + "/ui/worlds",
		base + "/ui/expansions",
		base + "/ui/methodology",
		base + "/ui/privacy",
	}

	// Degraded mode must still answer 200: nil stats service...
	rec := serveSitemap(t, NewUIController(nil, mockqueue.NewFake(), nil, testBaseURL))
	assertDegradedSitemap(t, rec, want)

	// ...and a stats service whose snapshot cannot load.
	stats := census.NewUIStatsService(&testErrorStatsRepository{}, time.Hour, time.Hour)
	rec = serveSitemap(t, NewUIController(nil, mockqueue.NewFake(), stats, testBaseURL))
	assertDegradedSitemap(t, rec, want)
}

func assertDegradedSitemap(t *testing.T, rec *httptest.ResponseRecorder, want []string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/xml; charset=utf-8" {
		t.Errorf("expected Content-Type application/xml; charset=utf-8, got %q", got)
	}

	var set parsedSitemapURLSet
	if err := xml.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatalf("expected parseable sitemap XML, got error: %v\nbody:\n%s", err, rec.Body.String())
	}
	if len(set.URLs) != len(want) {
		t.Fatalf("expected %d URLs, got %d: %+v", len(want), len(set.URLs), set.URLs)
	}
	for i, got := range set.URLs {
		if got.Loc != want[i] {
			t.Errorf("URL %d: expected %q, got %q", i, want[i], got.Loc)
		}
		if got.LastMod != "" {
			t.Errorf("URL %d (%s): expected no lastmod in degraded mode, got %q", i, want[i], got.LastMod)
		}
	}
}
