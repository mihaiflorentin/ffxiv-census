package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// seedRankingSwitch stores two NA worlds whose order flips between the two
// ranking measures: Gilgamesh has more characters, Adamantoise more active
// players.
func seedRankingSwitch(t *testing.T, rig *testRig) {
	t.Helper()
	now := time.Now().UTC()
	recent := now.Add(-time.Hour)
	id := uint32(9100)
	add := func(world, dc string, active bool) {
		id++
		rec := contract.CharacterRecord{ID: id, Name: "Switch Tester", World: world, Datacenter: dc, Region: "NA", Race: "Hyur", Tribe: "Midlander", FirstSeenAt: recent}
		if active {
			rec.LatestAchievementAt = &recent
		}
		if err := rig.chars.Upsert(context.Background(), rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		add("Gilgamesh", "Aether", false)
	}
	for range 2 {
		add("Adamantoise", "Aether", true)
	}
}

func switchGet(t *testing.T, mux *http.ServeMux, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d", target, rec.Code)
	}
	return rec
}

// rankedWorlds returns the world names of the rankings table in row order.
func rankedWorlds(root *html.Node) []string {
	var out []string
	for _, row := range findAll(root, byAtom(atom.Tr)) {
		for _, a := range findAll(row, byAtom(atom.A)) {
			if href, _ := attr(a, "href"); strings.HasPrefix(href, "/ui/worlds/") {
				out = append(out, textOf(a))
			}
		}
	}
	return out
}

// switchOptions returns the links of the segmented switch named label.
func switchOptions(t *testing.T, root *html.Node, label string) []*html.Node {
	t.Helper()
	groups := findAll(root, func(n *html.Node) bool {
		role, _ := attr(n, "role")
		name, _ := attr(n, "aria-label")
		return role == "group" && name == label
	})
	if len(groups) != 1 {
		t.Fatalf("expected one switch group %q, got %d", label, len(groups))
	}
	return findAll(groups[0], byAtom(atom.A))
}

func currentOption(options []*html.Node) string {
	for _, a := range options {
		if v, _ := attr(a, "aria-current"); v == "true" {
			return textOf(a)
		}
	}
	return ""
}

// TestSwitchDegradesToPlainLinks verifies the segmented switch on the Worlds
// rankings: each option is an ordinary link that keeps the filters, so with
// no JavaScript a click loads the full page ranked the other way; the same
// link carries the HTMX swap of the rankings region with a pushed URL.
func TestSwitchDegradesToPlainLinks(t *testing.T) {
	rig := newTestRig(t)
	seedRankingSwitch(t, rig)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	doc := parseHTML(t, switchGet(t, mux, "/ui/worlds?region=NA", nil).Body.String())
	options := switchOptions(t, doc, "Rank worlds by")
	if len(options) != 2 {
		t.Fatalf("expected a link pair, got %d options", len(options))
	}
	if got := currentOption(options); got != "Characters" {
		t.Errorf("current option = %q, want Characters by default", got)
	}
	if got := rankedWorlds(doc); len(got) != 2 || got[0] != "Gilgamesh" {
		t.Errorf("default ranking %v, want Gilgamesh first", got)
	}

	var activeHref string
	for _, a := range options {
		href, _ := attr(a, "href")
		if !strings.Contains(href, "region=NA") {
			t.Errorf("switch option %q drops the region filter: %q", textOf(a), href)
		}
		if get, _ := attr(a, "hx-get"); get != href {
			t.Errorf("option %q hx-get %q, want its href %q", textOf(a), get, href)
		}
		if push, _ := attr(a, "hx-push-url"); push != "true" {
			t.Errorf("option %q must push its URL, got hx-push-url=%q", textOf(a), push)
		}
		target, _ := attr(a, "hx-target")
		if !strings.HasPrefix(target, "#") || byID(doc, strings.TrimPrefix(target, "#")) == nil {
			t.Errorf("option %q hx-target %q must name a region on the page", textOf(a), target)
		}
		if textOf(a) == "Active players" {
			activeHref = href
		}
	}
	if activeHref == "" {
		t.Fatal("no Active players option")
	}

	// No JavaScript: following the plain link renders the whole page.
	plain := parseHTML(t, switchGet(t, mux, activeHref, nil).Body.String())
	if len(findAll(plain, byAtom(atom.Header))) == 0 {
		t.Error("a plain request for the switch link must return the full page")
	}
	if got := currentOption(switchOptions(t, plain, "Rank worlds by")); got != "Active players" {
		t.Errorf("current option = %q after following the link", got)
	}
	if got := rankedWorlds(plain); len(got) != 2 || got[0] != "Adamantoise" {
		t.Errorf("active ranking %v, want Adamantoise first", got)
	}
}

// TestSwitchSwapsRegionWithFocusAndAnnouncement verifies the HTMX path: a
// request from the switch gets only the region, whose heading takes focus
// and whose announcement the polite live region in the shell reads out.
func TestSwitchSwapsRegionWithFocusAndAnnouncement(t *testing.T) {
	rig := newTestRig(t)
	seedRankingSwitch(t, rig)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	page := parseHTML(t, switchGet(t, mux, "/ui/worlds?region=NA", nil).Body.String())
	live := findAll(page, func(n *html.Node) bool {
		v, _ := attr(n, "aria-live")
		return v == "polite"
	})
	if len(live) != 1 {
		t.Fatalf("shell needs one polite live region, got %d", len(live))
	}
	liveID, _ := attr(live[0], "id")
	options := switchOptions(t, page, "Rank worlds by")
	target, _ := attr(options[0], "hx-target")
	regionID := strings.TrimPrefix(target, "#")

	rec := switchGet(t, mux, "/ui/worlds?region=NA&sort=active", map[string]string{"HX-Request": "true", "HX-Target": regionID})
	if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "HX-Target") {
		t.Errorf("Vary = %q, want HX-Target so caches keep the fragment apart", vary)
	}
	body := rec.Body.String()
	frag := parseHTML(t, body)
	if len(findAll(frag, byAtom(atom.Header))) > 0 || strings.Contains(body, "<html") {
		t.Fatal("an HTMX switch request must get the region fragment, not the page")
	}
	region := byID(frag, regionID)
	if region == nil {
		t.Fatalf("fragment must be the region #%s itself", regionID)
	}
	if announce, _ := attr(region, "data-switch-announce"); !strings.Contains(announce, "active players") {
		t.Errorf("region announcement %q must say what changed", announce)
	}
	headings := findAll(region, func(n *html.Node) bool {
		_, ok := attr(n, "data-switch-heading")
		return ok
	})
	if len(headings) != 1 {
		t.Fatalf("region needs one data-switch-heading, got %d", len(headings))
	}
	if tab, _ := attr(headings[0], "tabindex"); tab != "-1" {
		t.Errorf("swapped heading tabindex %q, want -1 so it can take focus", tab)
	}
	if got := currentOption(switchOptions(t, frag, "Rank worlds by")); got != "Active players" {
		t.Errorf("swapped switch current option = %q", got)
	}
	if got := rankedWorlds(frag); len(got) != 2 || got[0] != "Adamantoise" {
		t.Errorf("swapped ranking %v, want Adamantoise first", got)
	}

	// A history-restore request (back button after a cache miss) needs the
	// full page even though HTMX marks it as an HTMX request.
	restore := switchGet(t, mux, "/ui/worlds?region=NA&sort=active", map[string]string{"HX-Request": "true", "HX-History-Restore-Request": "true"})
	if !strings.Contains(restore.Body.String(), "<header") {
		t.Error("a history-restore request must get the full page")
	}

	js := serveSiteScript(t, mux, page)
	for _, hook := range []string{"data-switch-heading", "data-switch-announce", liveID} {
		if !strings.Contains(js, hook) {
			t.Errorf("census.js must use %q to move focus and announce", hook)
		}
	}
}

// TestSwitchUnknownSortFallsBack verifies an unknown ranking value renders the
// default ranking rather than an error.
func TestSwitchUnknownSortFallsBack(t *testing.T) {
	rig := newTestRig(t)
	seedRankingSwitch(t, rig)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	doc := parseHTML(t, switchGet(t, mux, "/ui/worlds?sort=bogus", nil).Body.String())
	if got := currentOption(switchOptions(t, doc, "Rank worlds by")); got != "Characters" {
		t.Errorf("current option = %q, want Characters", got)
	}
}
