package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mockqueue "github.com/mihaiflorentin88/ffxiv-census/mock/queue"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"golang.org/x/net/html/atom"
)

func footerText(t *testing.T, body string) string {
	t.Helper()
	footers := findAll(parseHTML(t, body), byAtom(atom.Footer))
	if len(footers) != 1 {
		t.Fatalf("expected one footer, got %d", len(footers))
	}
	return textOf(footers[0])
}

// TestSnapshotFreePagesNeverLoadTheSnapshot checks the 404 and Methodology
// pages read only the snapshot already held in memory: with nothing cached
// they render without freshness and never hit the database, and once a page
// has loaded the snapshot they show its freshness.
func TestSnapshotFreePagesNeverLoadTheSnapshot(t *testing.T) {
	generated := time.Now().UTC().Add(-time.Hour)
	repo := mockrepo.NewUIStatsFake(&contract.UIStatsSnapshot{
		SchemaVersion: contract.UIStatsSchemaVersion,
		GeneratedAt:   generated,
		ActivitySince: generated.Add(-30 * 24 * time.Hour),
		MaxLevel:      100,
	})
	stats := census.NewUIStatsService(repo, time.Minute, 12*time.Hour)
	mux := http.NewServeMux()
	NewUIController(nil, mockqueue.NewFake(), stats, testBaseURL).RegisterRoutes(mux)

	for _, path := range []string{"/ui/does-not-exist", "/ui/methodology"} {
		rec := serveMuxGet(t, mux, path)
		if got := footerText(t, rec.Body.String()); !strings.Contains(got, "Statistics are being prepared") {
			t.Errorf("%s footer with nothing cached = %q, want no freshness", path, got)
		}
	}
	if repo.LoadCalls != 0 {
		t.Fatalf("LoadCurrent calls = %d, want 0: snapshot-free pages must not load the snapshot", repo.LoadCalls)
	}

	if rec := serveMuxGet(t, mux, "/ui/dashboard"); rec.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d", rec.Code)
	}
	for _, path := range []string{"/ui/does-not-exist", "/ui/methodology"} {
		rec := serveMuxGet(t, mux, path)
		if got := footerText(t, rec.Body.String()); !strings.Contains(got, "Statistics updated "+generated.Format("2006-01-02")) {
			t.Errorf("%s footer once cached = %q, want snapshot freshness", path, got)
		}
	}
	if repo.LoadCalls != 1 {
		t.Fatalf("LoadCurrent calls = %d, want 1 (the dashboard only)", repo.LoadCalls)
	}
}

// TestUnmatchedPathsGetStyledNotFound mounts the UI next to stand-ins for the
// server's API, health, metrics and docs routes, and checks the not-found
// fallback styles only paths no route matches, outside the API.
func TestUnmatchedPathsGetStyledNotFound(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	text := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	mux.HandleFunc("/health", text(http.StatusOK, `{"status":"ok"}`))
	mux.HandleFunc("/metrics", text(http.StatusOK, "metrics"))
	mux.HandleFunc("GET /api/v1/census/latest", text(http.StatusOK, `{"latest":true}`))
	mux.HandleFunc("GET /api/v1/census/characters/{id}", text(http.StatusNotFound, `{"error":"character not found"}`))
	mux.Handle("/docs/", http.NotFoundHandler())
	rig.ctrl.RegisterRoutes(mux)
	handler := rig.ctrl.NotFoundFallback(mux)

	cases := []struct {
		name, method, path string
		wantStatus         int
		wantStyled         bool
		wantBody           string
	}{
		{"unknown top-level path", http.MethodGet, "/nope", http.StatusNotFound, true, ""},
		{"unknown nested path", http.MethodGet, "/some/deep/path", http.StatusNotFound, true, ""},
		{"unknown ui path", http.MethodGet, "/ui/nope", http.StatusNotFound, true, ""},
		{"head of unknown path", http.MethodHead, "/nope", http.StatusNotFound, false, ""},
		{"dashboard", http.MethodGet, "/", http.StatusOK, false, ""},
		{"races", http.MethodGet, "/ui/races", http.StatusOK, false, ""},
		{"health", http.MethodGet, "/health", http.StatusOK, false, `{"status":"ok"}`},
		{"metrics", http.MethodGet, "/metrics", http.StatusOK, false, "metrics"},
		{"api route", http.MethodGet, "/api/v1/census/latest", http.StatusOK, false, `{"latest":true}`},
		{"api handler's own 404", http.MethodGet, "/api/v1/census/characters/9", http.StatusNotFound, false, `{"error":"character not found"}`},
		{"unknown api path", http.MethodGet, "/api/v1/nope", http.StatusNotFound, false, "404 page not found\n"},
		{"api method not allowed", http.MethodPost, "/api/v1/census/latest", http.StatusMethodNotAllowed, false, ""},
		{"docs file server 404", http.MethodGet, "/docs/missing.json", http.StatusNotFound, false, "404 page not found\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			styled := strings.Contains(rec.Body.String(), "Page not found")
			if tc.method == http.MethodGet && styled != tc.wantStyled {
				t.Fatalf("styled 404 = %v, want %v; body %q", styled, tc.wantStyled, rec.Body.String())
			}
			if tc.wantStyled {
				assertErrorShell(t, rec.Body.String(), "Page not found")
			}
			if tc.method == http.MethodHead && tc.wantStatus == http.StatusNotFound {
				if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
					t.Errorf("HEAD Content-Type = %q, want the styled page's text/html", ct)
				}
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Errorf("body = %q, want %q unchanged", rec.Body.String(), tc.wantBody)
			}
		})
	}
}
