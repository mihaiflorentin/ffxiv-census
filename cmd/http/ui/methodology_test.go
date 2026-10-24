package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html/atom"
)

func TestMethodologyHandler(t *testing.T) {
	rig := newTestRig(t)

	req := httptest.NewRequest(http.MethodGet, "/ui/methodology", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.Methodology(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	doc := parseHTML(t, rec.Body.String())
	if h1 := findAll(doc, byAtom(atom.H1)); len(h1) != 1 || textOf(h1[0]) != "How the census works" {
		t.Errorf("expected one h1 'How the census works'")
	}
	var sections []string
	for _, h := range findAll(doc, byAtom(atom.H2)) {
		sections = append(sections, textOf(h))
	}
	wantSections := []string{"Data collection", "Active players", "New characters", "Main scenario progression", "Grand Companies", "Free Companies", "Jobs", "Achievement privacy"}
	if strings.Join(sections, "|") != strings.Join(wantSections, "|") {
		t.Errorf("section headings = %q, want %q", sections, wantSections)
	}

	body := textOf(doc)
	for _, want := range []string{
		"My Little Chocobo",
		"Achievement ID 590",
		"globally latest public achievement",
		"members seen by the census",
		"top 100",
		"at least 2 members",
		"renamed",
		// No snapshot is loaded, so the window is the service's default.
		"earned in the last 90 days",
		// Why the window is wide, and what it can't fix.
		"Why 90 days",
		"A full census pass takes months",
		"still undercounts",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q", want)
		}
	}

	rows := map[string]bool{}
	for _, row := range tableBodyRows(tableCaptioned(t, doc, "Capstone achievement for each expansion")) {
		rows[strings.Join(row, "|")] = true
	}
	for _, want := range []string{
		"A Realm Reborn|Before the Dawn|Patch 2.55|50",
		"Dawntrail|In the Glow of a New Dawn|Patch 7.0|100",
	} {
		if !rows[want] {
			t.Errorf("milestone rows %v missing %q", rows, want)
		}
	}
}
