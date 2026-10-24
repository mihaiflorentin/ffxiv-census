package ui

import (
	"errors"
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

// snapshotPages lists every page whose content depends on the statistics
// snapshot and therefore answers 503 while no snapshot exists.
var snapshotPages = []string{
	"/",
	"/ui/dashboard",
	"/ui/races",
	"/ui/worlds",
	"/ui/worlds/Adamantoise",
	"/ui/expansions",
}

// assertErrorShell checks an error page renders inside the page shell: one
// main landmark, one h1 containing wantHeading, the footer, and a primary nav
// with no item marked as the current page.
func assertErrorShell(t *testing.T, body, wantHeading string) *html.Node {
	t.Helper()
	doc := parseHTML(t, body)
	mains := findAll(doc, byAtom(atom.Main))
	if len(mains) != 1 {
		t.Fatalf("expected one main landmark, got %d", len(mains))
	}
	h1s := findAll(doc, byAtom(atom.H1))
	if len(h1s) != 1 {
		t.Fatalf("expected exactly one h1, got %d", len(h1s))
	}
	if got := textOf(h1s[0]); !strings.Contains(got, wantHeading) {
		t.Errorf("h1 = %q, want it to contain %q", got, wantHeading)
	}
	if len(findAll(doc, byAtom(atom.Footer))) != 1 {
		t.Error("error page must render the shell footer")
	}
	navs := findAll(doc, func(n *html.Node) bool {
		label, _ := attr(n, "aria-label")
		return n.DataAtom == atom.Nav && label == "Primary"
	})
	if len(navs) != 1 {
		t.Fatalf("expected the primary nav, got %d", len(navs))
	}
	for _, a := range findAll(navs[0], byAtom(atom.A)) {
		if _, ok := attr(a, "aria-current"); ok {
			href, _ := attr(a, "href")
			t.Errorf("error page marks nav item %q as current", href)
		}
	}
	if styled := findAll(doc, func(n *html.Node) bool { _, ok := attr(n, "style"); return ok }); len(styled) > 0 {
		t.Errorf("error page contains %d inline style attributes", len(styled))
	}
	return mains[0]
}

func linksTo(root *html.Node, href string) bool {
	return len(findAll(root, func(n *html.Node) bool {
		h, _ := attr(n, "href")
		return n.DataAtom == atom.A && h == href
	})) > 0
}

func TestNoSnapshotRendersStyledBeingPreparedPage(t *testing.T) {
	failing := mockrepo.NewUIStatsFake(nil)
	failing.LoadErr = errors.New("no snapshot")
	controllers := map[string]*UIController{
		"no stats service": NewUIController(nil, mockqueue.NewFake(), nil, testBaseURL),
		"no snapshot yet":  NewUIController(nil, mockqueue.NewFake(), census.NewUIStatsService(failing, time.Minute, time.Hour), testBaseURL),
	}
	for name, ctrl := range controllers {
		mux := http.NewServeMux()
		ctrl.RegisterRoutes(mux)
		for _, path := range snapshotPages {
			t.Run(name+" "+path, func(t *testing.T) {
				rec := serveMuxGet(t, mux, path)
				if rec.Code != http.StatusServiceUnavailable {
					t.Fatalf("status = %d, want 503", rec.Code)
				}
				if got := rec.Header().Get("Retry-After"); got != "60" {
					t.Errorf("Retry-After = %q, want 60", got)
				}
				if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
					t.Errorf("Content-Type = %q, want text/html", ct)
				}
				main := assertErrorShell(t, rec.Body.String(), "being prepared")
				if text := strings.ToLower(textOf(main)); !strings.Contains(text, "try again") {
					t.Errorf("503 page must tell the visitor to try again shortly, got %q", textOf(main))
				}
				if !linksTo(main, "/ui/methodology") {
					t.Error("503 page should point to the methodology page, which works without a snapshot")
				}
			})
		}
	}
}

func TestUnknownRoutesRenderStyledNotFoundPage(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	cases := []struct {
		name, path, wantHeading, wantText string
	}{
		{"unknown world", "/ui/worlds/Nowhereland", "World not found", "Nowhereland"},
		{"unknown ui route", "/ui/does-not-exist", "Page not found", ""},
		{"disabled characters route", "/ui/characters", "Page not found", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveMuxGet(t, mux, tc.path)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q, want text/html", ct)
			}
			main := assertErrorShell(t, rec.Body.String(), tc.wantHeading)
			if !linksTo(main, "/") {
				t.Error("404 page must link back to the dashboard")
			}
			if tc.wantText != "" && !strings.Contains(textOf(main), tc.wantText) {
				t.Errorf("404 page should name %q, got %q", tc.wantText, textOf(main))
			}
			doc := parseHTML(t, rec.Body.String())
			robots := findAll(doc, func(n *html.Node) bool {
				name, _ := attr(n, "name")
				return n.DataAtom == atom.Meta && name == "robots"
			})
			if len(robots) != 1 {
				t.Fatal("404 page must carry a robots meta tag")
			}
			if v, _ := attr(robots[0], "content"); !strings.Contains(v, "noindex") {
				t.Errorf("robots = %q, want noindex", v)
			}
		})
	}
}

// TestEmptyStatesGiveDirection renders every page against an empty census and
// checks each missing data block says what is missing and what happens next,
// instead of a bare "no data" line.
func TestEmptyStatesGiveDirection(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	const nextRefresh = "Statistics appear after the next census refresh."
	cases := []struct {
		path, title, hint, href string
	}{
		{"/", "No race data yet.", nextRefresh, ""},
		{"/", "No regional data yet.", nextRefresh, ""},
		{"/ui/races", "No race data yet.", nextRefresh, ""},
		{"/ui/races", "No tribe data yet.", nextRefresh, ""},
		{"/ui/races", "No gender data yet.", nextRefresh, ""},
		{"/ui/races", "No race and gender data yet.", nextRefresh, ""},
		{"/ui/races?region=EU", "No race data for this selection.", "Try a wider selection", "/ui/races"},
		{"/ui/worlds?region=Nowhere", "No worlds match these filters.", "Try another region or datacenter", "/ui/worlds"},
		{"/ui/worlds/Adamantoise", "No race data for this world yet.", nextRefresh, ""},
		{"/ui/partials/world-breakdown?region=EU", "No worlds recorded for EU yet.", nextRefresh, ""},
	}
	for _, tc := range cases {
		t.Run(tc.path+" "+tc.title, func(t *testing.T) {
			rec := serveMuxGet(t, mux, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			doc := parseHTML(t, rec.Body.String())
			var found *html.Node
			for _, p := range findAll(doc, byAtom(atom.P)) {
				if strings.TrimSpace(textOf(p)) == tc.title {
					found = p.Parent
					break
				}
			}
			if found == nil {
				t.Fatalf("empty state %q not rendered", tc.title)
			}
			if text := textOf(found); !strings.Contains(text, tc.hint) {
				t.Errorf("empty state %q hint = %q, want %q", tc.title, text, tc.hint)
			}
			if tc.href != "" && !linksTo(found, tc.href) {
				t.Errorf("empty state %q should link to %q", tc.title, tc.href)
			}
		})
	}
}

func TestNotFoundPageOmitsStaleNotice(t *testing.T) {
	generated := time.Now().UTC().Add(-13 * time.Hour)
	snapshot := &contract.UIStatsSnapshot{
		SchemaVersion: contract.UIStatsSchemaVersion,
		GeneratedAt:   generated,
		ActivitySince: generated.Add(-30 * 24 * time.Hour),
		MaxLevel:      100,
	}
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(snapshot), time.Minute, 12*time.Hour)
	mux := http.NewServeMux()
	NewUIController(nil, mockqueue.NewFake(), stats, testBaseURL).RegisterRoutes(mux)
	rec := serveMuxGet(t, mux, "/ui/does-not-exist")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if notice := staleNotice(t, rec.Body.String()); notice != "" {
		t.Fatalf("404 page shows no figures but rendered stale notice %q", notice)
	}
}

func TestKnownWorldIsMatchedCaseInsensitively(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)
	if rec := serveMuxGet(t, mux, "/ui/worlds/adamantoise"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a known world in lower case", rec.Code)
	}
}
