package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestCharacterDetailHandler(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)
	fcID := "9232332145678901234"
	fcName := "Scions of the Seventh Dawn"

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:                  36796000,
		Name:                "Tataru Taru",
		World:               "Balmung",
		Datacenter:          "Crystal",
		Region:              "NA",
		Race:                "Lalafell",
		Tribe:               "Plainsfolk",
		Gender:              2, // Female
		GrandCompany:        "Immortal Flames",
		FreeCompanyID:       &fcID,
		FreeCompanyName:     &fcName,
		FirstSeenAt:         recent,
		LatestAchievementAt: &recent,
	}, []contract.ClassJobRecord{
		{CharacterID: 36796000, Name: "Paladin", Level: 100},
		{CharacterID: 36796000, Name: "White Mage", Level: 100},
		{CharacterID: 36796000, Name: "Weaver", Level: 100},
		{CharacterID: 36796000, Name: "Miner", Level: 90},
	})

	// Test 200 OK for existing character
	req := httptest.NewRequest(http.MethodGet, "/ui/characters/36796000", nil)
	req.SetPathValue("id", "36796000")
	rec := httptest.NewRecorder()
	rig.ctrl.CharacterDetail(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Tataru Taru") {
		t.Errorf("expected body to contain 'Tataru Taru', got:\n%s", body)
	}
	if !strings.Contains(body, "Paladin") {
		t.Errorf("expected body to contain Paladin job, got:\n%s", body)
	}
	if !strings.Contains(body, "Active, last 90 days") {
		t.Errorf("expected the Active badge to state the service's 90-day window, got:\n%s", body)
	}

	// Test 404 for unknown character
	reqNotFound := httptest.NewRequest(http.MethodGet, "/ui/characters/99999999", nil)
	reqNotFound.SetPathValue("id", "99999999")
	recNotFound := httptest.NewRecorder()
	rig.ctrl.CharacterDetail(recNotFound, reqNotFound)

	if recNotFound.Code != http.StatusNotFound {
		t.Fatalf("expected status 404 for unknown character, got %d", recNotFound.Code)
	}
}

func TestCharacterSearchHandler(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          36796000,
		Name:        "Tataru Taru",
		World:       "Balmung",
		Datacenter:  "Crystal",
		Region:      "NA",
		Race:        "Lalafell",
		Tribe:       "Plainsfolk",
		FirstSeenAt: recent,
	}, nil)

	// Numeric search -> redirect
	reqNum := httptest.NewRequest(http.MethodGet, "/ui/characters/search?q=36796000", nil)
	recNum := httptest.NewRecorder()
	rig.ctrl.CharacterSearch(recNum, reqNum)

	if recNum.Code != http.StatusFound && recNum.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect for numeric search, got %d", recNum.Code)
	}
	if loc := recNum.Header().Get("Location"); loc != "/ui/characters/36796000" {
		t.Errorf("expected redirect location /ui/characters/36796000, got %q", loc)
	}

	// Text search -> renders list/results
	reqText := httptest.NewRequest(http.MethodGet, "/ui/characters/search?q=Tataru", nil)
	recText := httptest.NewRecorder()
	rig.ctrl.CharacterSearch(recText, reqText)

	if recText.Code != http.StatusOK {
		t.Fatalf("expected status 200 for text search, got %d", recText.Code)
	}
	if !strings.Contains(recText.Body.String(), "Tataru Taru") {
		t.Errorf("expected search result to contain 'Tataru Taru', got:\n%s", recText.Body.String())
	}
}

func TestCharacterListHandler(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:          36796000,
		Name:        "Tataru Taru",
		World:       "Balmung",
		Datacenter:  "Crystal",
		Region:      "NA",
		Race:        "Lalafell",
		Tribe:       "Plainsfolk",
		FirstSeenAt: recent,
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/ui/characters", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.CharacterList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if !strings.Contains(rec.Body.String(), "Character Directory") {
		t.Errorf("expected body to contain 'Character Directory', got:\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Tataru Taru") {
		t.Errorf("expected body to contain 'Tataru Taru', got:\n%s", rec.Body.String())
	}
}
