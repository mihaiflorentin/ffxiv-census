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

func TestExpansionsHandler(t *testing.T) {
	rig := newTestRig(t)
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour)

	_ = rig.chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:                  4001,
		Name:                "Warrior of Light",
		World:               "Balmung",
		Datacenter:          "Crystal",
		Region:              "NA",
		Race:                "Hyur",
		Tribe:               "Midlander",
		FirstSeenAt:         recent,
		LatestAchievementAt: &recent,
	}, nil)

	// Sync milestones to fake repo
	_ = rig.ach.SyncMilestones(context.Background(), []contract.MilestoneAchievement{
		{AchievementID: 1139, Kind: contract.MilestoneKindExpansion, Expansion: stringPtr("Heavensward"), Detail: "Looking Up"},
		{AchievementID: 1794, Kind: contract.MilestoneKindExpansion, Expansion: stringPtr("Stormblood"), Detail: "The Measure of His Reach"},
		{AchievementID: 2298, Kind: contract.MilestoneKindExpansion, Expansion: stringPtr("Shadowbringers"), Detail: "Shadowbringers"},
		{AchievementID: 2958, Kind: contract.MilestoneKindExpansion, Expansion: stringPtr("Endwalker"), Detail: "That Its Chorus Might Ring for All"},
		{AchievementID: 3496, Kind: contract.MilestoneKindExpansion, Expansion: stringPtr("Dawntrail"), Detail: "In the Glow of a New Dawn"},
	})

	_ = rig.ach.UpsertCharacterMilestones(context.Background(), 4001, []contract.CharacterMilestone{
		{CharacterID: 4001, AchievementID: 1139, AchievedAt: recent},
		{CharacterID: 4001, AchievementID: 1794, AchievedAt: recent},
		{CharacterID: 4001, AchievementID: 2298, AchievedAt: recent},
		{CharacterID: 4001, AchievementID: 2958, AchievedAt: recent},
		{CharacterID: 4001, AchievementID: 3496, AchievedAt: recent},
	})

	req := httptest.NewRequest(http.MethodGet, "/ui/expansions", nil)
	rec := httptest.NewRecorder()
	rig.ctrl.Expansions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Expansion progression") {
		t.Errorf("expected body to contain 'Expansion progression', got:\n%s", body)
	}
	if !strings.Contains(body, "Dawntrail") {
		t.Errorf("expected body to contain Dawntrail, got:\n%s", body)
	}
	if !strings.Contains(body, "Endwalker") {
		t.Errorf("expected body to contain Endwalker, got:\n%s", body)
	}
}

func stringPtr(s string) *string {
	return &s
}
