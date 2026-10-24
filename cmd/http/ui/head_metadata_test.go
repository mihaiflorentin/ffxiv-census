package ui

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// testBaseURL mirrors the default app.base_url configuration value so the
// indexable page head assertions match what production renders by default.
const testBaseURL = "https://census.ffxivbard.com"

func extractHeadAttribute(t *testing.T, body, pattern string) string {
	t.Helper()
	match := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("head pattern %q not found in rendered page:\n%s", pattern, body)
	}
	return match[1]
}

func serveMuxGet(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestIndexablePagesHeadMetadata verifies every indexable page serves complete
// HTML with absolute canonical URL, meta description, and Open Graph head tags.
func TestIndexablePagesHeadMetadata(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	tests := []struct {
		name          string
		path          string
		canonicalPath string
	}{
		{"Dashboard At Root", "/", "/"},
		{"Dashboard", "/ui/dashboard", "/"},
		{"Races", "/ui/races", "/ui/races"},
		{"Worlds", "/ui/worlds", "/ui/worlds"},
		{"World Detail", "/ui/worlds/Adamantoise", "/ui/worlds/Adamantoise"},
		{"Expansions", "/ui/expansions", "/ui/expansions"},
		{"Methodology", "/ui/methodology", "/ui/methodology"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveMuxGet(t, mux, tc.path)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()

			wantCanonical := testBaseURL + tc.canonicalPath
			if got := extractHeadAttribute(t, body, `<link rel="canonical" href="([^"]*)">`); got != wantCanonical {
				t.Errorf("canonical link got %q, want %q", got, wantCanonical)
			}

			description := extractHeadAttribute(t, body, `<meta name="description" content="([^"]*)">`)
			if strings.TrimSpace(description) == "" {
				t.Error("meta description must be non-empty")
			}

			if got := extractHeadAttribute(t, body, `<meta property="og:site_name" content="([^"]*)">`); got != "FFXIV Census" {
				t.Errorf("og:site_name got %q, want %q", got, "FFXIV Census")
			}
			if got := extractHeadAttribute(t, body, `<meta property="og:url" content="([^"]*)">`); got != wantCanonical {
				t.Errorf("og:url got %q, want %q", got, wantCanonical)
			}
			if got := extractHeadAttribute(t, body, `<meta property="og:type" content="([^"]*)">`); got != "website" {
				t.Errorf("og:type got %q, want %q", got, "website")
			}
			if got := extractHeadAttribute(t, body, `<meta property="og:title" content="([^"]*)">`); !strings.Contains(got, "FFXIV Census") {
				t.Errorf("og:title got %q, want it to end with the site name", got)
			}
			if got := extractHeadAttribute(t, body, `<meta property="og:description" content="([^"]*)">`); got != description {
				t.Errorf("og:description got %q, want meta description %q", got, description)
			}
		})
	}
}

// TestRootServesDashboardContent verifies the root URL renders the dashboard
// page directly instead of redirecting to /ui/dashboard.
func TestRootServesDashboardContent(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	rec := serveMuxGet(t, mux, "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / expected status 200, got %d (redirect to /ui/dashboard is not allowed)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Characters in the census") {
		t.Errorf("GET / expected dashboard content marker 'Characters in the census', got:\n%s", rec.Body.String())
	}
}

// TestWorldsFilteredCanonicalizesToCleanPath verifies filter query parameters
// never leak into the canonical URL of the worlds page.
func TestWorldsFilteredCanonicalizesToCleanPath(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	rec := serveMuxGet(t, mux, "/ui/worlds?region=NA&dc=Aether")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	wantCanonical := testBaseURL + "/ui/worlds"
	if got := extractHeadAttribute(t, rec.Body.String(), `<link rel="canonical" href="([^"]*)">`); got != wantCanonical {
		t.Errorf("filtered worlds canonical got %q, want clean path %q", got, wantCanonical)
	}
}
