package ui

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	census "github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var playableRaces = []string{"Hyur", "Elezen", "Lalafell", "Miqo'te", "Roegadyn", "Au Ra", "Hrothgar", "Viera"}

// seedFullDashboard fills every dashboard section: races, regions, an
// expansion completion and new characters in the 30-day window.
func seedFullDashboard(t *testing.T, rig *testRig) {
	t.Helper()
	now := time.Now().UTC()
	recent := now.Add(-time.Hour)
	for i, race := range playableRaces {
		if err := rig.chars.Upsert(context.Background(), contract.CharacterRecord{
			ID: uint32(30001 + i), Name: "Char " + race, World: "Balmung", Datacenter: "Crystal", Region: "NA",
			Race: race, FirstSeenAt: recent, LatestAchievementAt: &recent,
		}, nil); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_ = rig.ach.SyncMilestones(context.Background(), census.DefaultMilestones())
	_ = rig.ach.UpsertCharacterMilestones(context.Background(), 30001, []contract.CharacterMilestone{
		{CharacterID: 30001, AchievementID: 1129, AchievedAt: recent},
	})
	rig.ach.NewCharactersResponse = []contract.DailyCount{
		{Day: now.AddDate(0, 0, -2).Format("2006-01-02"), Count: 7},
	}
}

func renderDashboard(t *testing.T) *html.Node {
	t.Helper()
	rig := newTestRig(t)
	seedFullDashboard(t, rig)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)
	rec := serveMuxGet(t, mux, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	return parseHTML(t, rec.Body.String())
}

func TestDashboardHasNoInlineStylesOrEmoji(t *testing.T) {
	doc := renderDashboard(t)
	assertNoInlineStylesOrEmoji(t, doc)
	for _, h := range findAll(doc, func(n *html.Node) bool {
		switch n.DataAtom {
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
			return true
		}
		return false
	}) {
		if containsEmoji(textOf(h)) {
			t.Errorf("heading contains emoji: %q", textOf(h))
		}
	}
}

var colourLiteral = regexp.MustCompile(`#[0-9A-Fa-f]{3,8}\b|rgba?\(`)

func TestDashboardChartsUseSharedThemeAndHaveTextAlternatives(t *testing.T) {
	doc := renderDashboard(t)

	scripts := findAll(doc, byAtom(atom.Script))
	themeAt, chartLibAt, firstInlineAt := -1, -1, -1
	for i, s := range scripts {
		src, _ := attr(s, "src")
		src, _, _ = strings.Cut(src, "?") // drop the build version
		if typ, _ := attr(s, "type"); typ == "application/json" {
			continue // a data block, not a script
		}
		switch {
		case src == "/ui/assets/chart-theme.js":
			themeAt = i
		case src == "/ui/assets/chart.umd.min.js":
			chartLibAt = i
		case src == "" && firstInlineAt < 0:
			firstInlineAt = i
		}
		if src == "" {
			if lit := colourLiteral.FindString(textOf(s)); lit != "" {
				t.Errorf("inline chart script carries a colour literal %q; colours come from the shared theme", lit)
			}
		}
	}
	if themeAt < 0 || chartLibAt < 0 || themeAt < chartLibAt {
		t.Fatalf("chart theme must load after Chart.js (theme at %d, Chart.js at %d)", themeAt, chartLibAt)
	}
	if firstInlineAt >= 0 && firstInlineAt < themeAt {
		t.Error("an inline chart script runs before the shared theme loads")
	}

	canvases := findAll(doc, byAtom(atom.Canvas))
	if len(canvases) < 2 {
		t.Fatalf("expected the race and timeline charts, got %d canvases", len(canvases))
	}
	for _, c := range canvases {
		id, _ := attr(c, "id")
		if role, _ := attr(c, "role"); role != "img" {
			t.Errorf("canvas %q role = %q, want img", id, role)
		}
		if label, _ := attr(c, "aria-label"); strings.TrimSpace(label) == "" {
			t.Errorf("canvas %q has no accessible name", id)
		}
		ref, _ := attr(c, "aria-describedby")
		alts := findAll(doc, func(n *html.Node) bool { v, _ := attr(n, "id"); return ref != "" && v == ref })
		if len(alts) != 1 {
			t.Errorf("canvas %q aria-describedby %q must reference one text alternative", id, ref)
			continue
		}
		if len(findAll(alts[0], byAtom(atom.Table))) == 0 && textOf(alts[0]) == "" {
			t.Errorf("canvas %q text alternative is empty", id)
		}
	}
}

func TestDashboardRaceTextAlternativeListsEveryRace(t *testing.T) {
	doc := renderDashboard(t)
	var raceTable *html.Node
	for _, tb := range findAll(doc, byAtom(atom.Table)) {
		for _, c := range findAll(tb, byAtom(atom.Caption)) {
			if strings.Contains(textOf(c), "Characters by race") {
				raceTable = tb
			}
		}
	}
	if raceTable == nil {
		t.Fatal("expected a table captioned 'Characters by race'")
	}
	got := map[string]bool{}
	for _, row := range tableBodyRows(raceTable) {
		got[strings.Join(row, "|")] = true
	}
	for _, race := range playableRaces {
		if !got[race+"|1|12.5%"] {
			t.Errorf("race table missing row %q, got %v", race+"|1|12.5%", got)
		}
	}
}

func TestChartThemeAssetAndPalette(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	rec := serveMuxGet(t, mux, "/ui/assets/chart-theme.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("chart theme asset: status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("chart theme content type %q, want javascript", ct)
	}
	theme := rec.Body.String()

	tokens := colorTokens(servedStylesheet(t))
	seen := map[string]string{}
	for i := 1; i <= 8; i++ {
		name := fmt.Sprintf("chart-%d", i)
		hex, ok := tokens[name]
		if !ok {
			t.Errorf("stylesheet missing --color-%s", name)
			continue
		}
		if prev, dup := seen[strings.ToUpper(hex)]; dup {
			t.Errorf("--color-%s duplicates --color-%s (%s)", name, prev, hex)
		}
		seen[strings.ToUpper(hex)] = name
		if !strings.Contains(theme, "--color-"+name) {
			t.Errorf("chart theme does not read --color-%s", name)
		}
	}
	// Each race's fixed slot comes from the page's taxonomy block (see
	// TestPagesCarryTheCensusTaxonomyForCharts), not a copy in the theme.
	if !strings.Contains(theme, "census-taxonomy") {
		t.Error("chart theme does not read the race order from #census-taxonomy")
	}
}
