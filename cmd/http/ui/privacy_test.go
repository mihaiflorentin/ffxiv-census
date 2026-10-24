package ui

import (
	"net/http"
	"strings"
	"testing"
	"time"

	census "github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mockqueue "github.com/mihaiflorentin88/ffxiv-census/mock/queue"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"golang.org/x/net/html"
)

var privacyBuiltAt = time.Date(2026, 10, 23, 3, 0, 0, 0, time.UTC)

// privacySnapshot is a hand-built v3 snapshot: 1,000 characters, 15% private
// and 5% unchecked, across NA (Balmung, Coeurl), EU (Ragnarok, Lich) and a
// tiny Oceanian world.
func privacyPageSnapshot(version int) *contract.UIStatsSnapshot {
	generated := time.Now().UTC()
	loc := func(dim, key string, total, private, unchecked int64) contract.ScopedGroupCount {
		return contract.ScopedGroupCount{Dimension: dim, Key: key, Total: total, Active: total / 3, Private: private, Unchecked: unchecked}
	}
	s := &contract.UIStatsSnapshot{
		SchemaVersion:    version,
		GeneratedAt:      generated,
		ActivitySince:    generated.Add(-30 * 24 * time.Hour),
		MaxLevel:         100,
		SourceCharacters: 1000,
		Summary:          contract.StatsSummary{Total: 1000, Active: 333, MaxLevel: 250, Private: 150, Unchecked: 50},
		Groups: []contract.ScopedGroupCount{
			loc("region", "NA", 500, 100, 25),
			loc("region", "EU", 490, 45, 25),
			loc("region", "OCE", 10, 5, 0),
			loc("world", "Balmung", 300, 90, 15),
			loc("world", "Coeurl", 200, 10, 10),
			loc("world", "Ragnarok", 250, 25, 5),
			loc("world", "Lich", 240, 20, 20),
			loc("world", "Bismarck", 10, 5, 0),
		},
	}
	if version >= 3 {
		s.PrivacyBuiltAt = privacyBuiltAt
	} else {
		s.Summary.Private, s.Summary.Unchecked = 0, 0
		for i := range s.Groups {
			s.Groups[i].Private, s.Groups[i].Unchecked = 0, 0
		}
	}
	return s
}

func privacyMux(t *testing.T, snapshot *contract.UIStatsSnapshot) *http.ServeMux {
	t.Helper()
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(snapshot), time.Hour, 24*time.Hour)
	ctrl := NewUIController(svc, mockqueue.NewFake(), stats, testBaseURL)
	mux := http.NewServeMux()
	ctrl.RegisterRoutes(mux)
	return mux
}

func flatText(n *html.Node) string {
	return strings.Join(strings.Fields(textOf(n)), " ")
}

func TestPrivacyPageDrawsTheVeil(t *testing.T) {
	rec := serveMuxGet(t, privacyMux(t, privacyPageSnapshot(3)), "/ui/privacy")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	doc := parseHTML(t, rec.Body.String())
	assertNoInlineStylesOrEmoji(t, doc)

	veils := findAll(doc, byClass("veil"))
	if len(veils) != 1 {
		t.Fatalf("want one veil band, got %d", len(veils))
	}
	if _, ok := attr(veils[0], "data-entrance"); !ok {
		t.Error("the veil is the page's one entrance")
	}
	segments := map[string]*html.Node{}
	for _, seg := range findAll(veils[0], byClass("veil-segment")) {
		for _, kind := range []string{"public", "private", "unchecked"} {
			if hasClass(seg, "veil-"+kind) {
				segments[kind] = seg
			}
		}
	}
	for kind, width := range map[string]string{"public": "80.000%", "private": "15.000%", "unchecked": "5.000%"} {
		seg := segments[kind]
		if seg == nil {
			t.Fatalf("veil has no %s segment", kind)
		}
		if got, _ := attr(seg, "width"); got != width {
			t.Errorf("%s segment width = %q, want %q", kind, got, width)
		}
	}
	if fill, _ := attr(segments["private"], "fill"); !strings.HasPrefix(fill, "url(#") {
		t.Errorf("the private segment is hatched with a pattern, got fill %q", fill)
	}

	key := findAll(doc, byClass("veil-key"))
	if len(key) != 1 {
		t.Fatalf("want one veil key, got %d", len(key))
	}
	keyText := flatText(key[0])
	for _, want := range []string{"Public", "80.0%", "800", "Private", "15.0%", "150", "Not yet checked", "5.0%", "50"} {
		if !strings.Contains(keyText, want) {
			t.Errorf("veil key %q is missing %q", keyText, want)
		}
	}

	fresh := findAll(doc, byClass("freshness"))
	if len(fresh) != 1 || flatText(fresh[0]) != "Rebuilt nightly, last on 23 October 2026" {
		t.Errorf("want the nightly freshness line with the privacy build date, got %d", len(fresh))
	}
	main := renderedMain(t, doc)
	for _, want := range []string{
		"can’t count as active",
		"lower bound",
		"Show the numbers",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("page is missing %q", want)
		}
	}

	regions := tableCaptioned(t, doc, "Achievement privacy by region")
	assertDataTable(t, doc, regions, "Characters", "Public", "Private", "Not yet checked")
	rows := tableBodyRows(regions)
	if len(rows) != 3 || rows[0][0] != "North America" || rows[0][3] != "20.0%" {
		t.Errorf("region rows = %v, want NA first with 20.0%% private", rows)
	}

	most := flatText(byID(doc, "most-private"))
	least := flatText(byID(doc, "least-private"))
	if !strings.Contains(most, "Balmung") || !strings.Contains(most, "30.0%") || strings.Contains(most, "Bismarck") {
		t.Errorf("most private = %q, want Balmung at 30.0%% and no tiny world", most)
	}
	if !strings.Contains(least, "Coeurl") || !strings.Contains(least, "5.0%") {
		t.Errorf("least private = %q, want Coeurl at 5.0%%", least)
	}
	if !strings.Contains(main, "1 world with fewer than 60 characters is left out") {
		t.Errorf("page must say how many small worlds are left out")
	}
	if !linksTo(doc, "/ui/worlds/Balmung") || !linksTo(doc, "/ui/methodology#achievement-privacy") {
		t.Error("worlds link to their pages and the page links to the methodology")
	}

	if got := extractHeadAttribute(t, rec.Body.String(), `<link rel="canonical" href="([^"]*)">`); got != testBaseURL+"/ui/privacy" {
		t.Errorf("canonical = %q", got)
	}
	if got := extractHeadAttribute(t, rec.Body.String(), `<meta property="og:url" content="([^"]*)">`); got != testBaseURL+"/ui/privacy" {
		t.Errorf("og:url = %q", got)
	}
	for _, link := range findAll(doc, byClass("primary-nav")) {
		if linksTo(link, "/ui/privacy") {
			t.Error("Private achievements is linked from Methodology, not from the main nav")
		}
	}
}

func TestPrivacyPageOnAVersion2SnapshotWaitsForTheNextRefresh(t *testing.T) {
	rec := serveMuxGet(t, privacyMux(t, privacyPageSnapshot(2)), "/ui/privacy")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	doc := parseHTML(t, rec.Body.String())
	main := renderedMain(t, doc)
	if !strings.Contains(main, "Privacy figures arrive with the next refresh.") {
		t.Errorf("v2 snapshot must show the next-refresh state, got %q", main)
	}
	if len(findAll(doc, byClass("veil"))) != 0 {
		t.Error("no veil without privacy figures")
	}
}

func TestPrivacyNotesOnTheDashboardAndExpansions(t *testing.T) {
	mux := privacyMux(t, privacyPageSnapshot(3))
	for path, want := range map[string]string{
		"/":              "15.0% of characters hide their achievements, so they can’t count as active.",
		"/ui/expansions": "15.0% of characters hide their achievements, so these completions are a lower bound.",
	} {
		doc := parseHTML(t, serveMuxGet(t, mux, path).Body.String())
		notes := findAll(doc, byClass("privacy-note"))
		if len(notes) != 1 {
			t.Fatalf("%s: want one privacy note, got %d", path, len(notes))
		}
		if got := flatText(notes[0]); got != want {
			t.Errorf("%s note = %q, want %q", path, got, want)
		}
		if !linksTo(notes[0], "/ui/privacy") {
			t.Errorf("%s note must link to /ui/privacy", path)
		}
	}

	v2 := privacyMux(t, privacyPageSnapshot(2))
	for _, path := range []string{"/", "/ui/expansions"} {
		doc := parseHTML(t, serveMuxGet(t, v2, path).Body.String())
		if n := len(findAll(doc, byClass("privacy-note"))); n != 0 {
			t.Errorf("%s: no privacy note before the privacy figures exist, got %d", path, n)
		}
	}
}

func TestMethodologyExplainsAchievementPrivacy(t *testing.T) {
	doc := parseHTML(t, serveMuxGet(t, privacyMux(t, privacyPageSnapshot(3)), "/ui/methodology").Body.String())
	section := byID(doc, "achievement-privacy")
	if section == nil {
		t.Fatal("methodology needs an achievement-privacy section")
	}
	text := renderedMain(t, doc)
	for _, want := range []string{"Not yet checked", "lower bound", "nightly"} {
		if !strings.Contains(text, want) {
			t.Errorf("methodology is missing %q", want)
		}
	}
	if !linksTo(doc, "/ui/privacy") {
		t.Error("methodology links to the Private achievements page")
	}
}
