package ui

import (
	"context"
	"net/http"
	"testing"
	"time"

	census "github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// seedExpansionFunnel adds four characters who finished the main scenario
// up to different expansions: 4 A Realm Reborn, 3 Heavensward, 2 Stormblood,
// 2 Shadowbringers, 1 Endwalker, 1 Dawntrail.
func seedExpansionFunnel(t *testing.T, rig *testRig) {
	t.Helper()
	recent := time.Now().UTC().Add(-time.Hour)
	_ = rig.ach.SyncMilestones(context.Background(), census.DefaultMilestones())
	reached := []int{6, 2, 4, 1} // expansions finished, in release order
	for i, n := range reached {
		id := uint32(50001 + i)
		if err := rig.chars.Upsert(context.Background(), contract.CharacterRecord{
			ID: id, Name: "Scion", World: "Balmung", Datacenter: "Crystal", Region: "NA",
			Race: "Hyur", FirstSeenAt: recent, LatestAchievementAt: &recent,
		}, nil); err != nil {
			t.Fatalf("seed: %v", err)
		}
		var milestones []contract.CharacterMilestone
		for _, exp := range census.DefaultExpansions[:n] {
			milestones = append(milestones, contract.CharacterMilestone{CharacterID: id, AchievementID: exp.AchievementID, AchievedAt: recent})
		}
		_ = rig.ach.UpsertCharacterMilestones(context.Background(), id, milestones)
	}
}

// definitions maps each <dt> text under root to its <dd> text.
func definitions(root *html.Node) map[string]string {
	out := map[string]string{}
	for _, dt := range findAll(root, byAtom(atom.Dt)) {
		for dd := dt.NextSibling; dd != nil; dd = dd.NextSibling {
			if dd.DataAtom == atom.Dd {
				out[textOf(dt)] = textOf(dd)
				break
			}
		}
	}
	return out
}

func TestExpansionsShowsOneLabelledStepPerExpansion(t *testing.T) {
	rig := newTestRig(t)
	seedExpansionFunnel(t, rig)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)
	rec := serveMuxGet(t, mux, "/ui/expansions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	doc := parseHTML(t, rec.Body.String())

	heading := byID(doc, "progression-heading")
	if heading == nil || textOf(heading) != "Main scenario progression" {
		t.Fatalf("expected a 'Main scenario progression' heading with id progression-heading")
	}
	steps := findAll(doc, func(n *html.Node) bool {
		if n.DataAtom != atom.Li || n.Parent == nil || n.Parent.DataAtom != atom.Ol {
			return false
		}
		for a := n.Parent; a != nil; a = a.Parent {
			if v, _ := attr(a, "aria-labelledby"); v == "progression-heading" {
				return true
			}
		}
		return false
	})
	want := []struct {
		name string
		defs map[string]string
	}{
		{"A Realm Reborn", map[string]string{"Completed": "4", "Share of total": "100.0%", "Retention": "Starting point"}},
		{"Heavensward", map[string]string{"Completed": "3", "Share of total": "75.0%", "Retention from A Realm Reborn": "75.0%"}},
		{"Stormblood", map[string]string{"Completed": "2", "Share of total": "50.0%", "Retention from Heavensward": "66.7%"}},
		{"Shadowbringers", map[string]string{"Completed": "2", "Share of total": "50.0%", "Retention from Stormblood": "100.0%"}},
		{"Endwalker", map[string]string{"Completed": "1", "Share of total": "25.0%", "Retention from Shadowbringers": "50.0%"}},
		{"Dawntrail", map[string]string{"Completed": "1", "Share of total": "25.0%", "Retention from Endwalker": "100.0%"}},
	}
	if len(steps) != len(want) {
		t.Fatalf("expected %d progression steps, got %d", len(want), len(steps))
	}
	for i, w := range want {
		hs := findAll(steps[i], byAtom(atom.H3))
		if len(hs) != 1 || textOf(hs[0]) != w.name {
			t.Errorf("step %d heading = %v, want %q", i, len(hs), w.name)
		}
		got := definitions(steps[i])
		for term, value := range w.defs {
			if got[term] != value {
				t.Errorf("%s: %q = %q, want %q (all: %v)", w.name, term, got[term], value, got)
			}
		}
	}
}

func TestExpansionsRetentionIsNotMeasuredAfterAnEmptyStep(t *testing.T) {
	doc := renderPage(t, "/ui/expansions")
	// The fixture has one A Realm Reborn completion and none after it.
	found := false
	for _, li := range findAll(doc, byAtom(atom.Li)) {
		defs := definitions(li)
		v, ok := defs["Retention from Heavensward"]
		found = found || ok
		if ok && v != "Not measured" {
			t.Errorf("retention after an expansion nobody finished = %q, want Not measured", v)
		}
		if v, ok := defs["Retention from A Realm Reborn"]; ok && v != "0.0%" {
			t.Errorf("Heavensward retention = %q, want 0.0%%", v)
		}
	}
	if !found {
		t.Error("no Stormblood step labelled with retention from Heavensward")
	}
}
