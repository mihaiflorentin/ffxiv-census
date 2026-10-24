package census

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestUIStatsServiceFreeCompanyBoardReloadsOnlyWhenTheBuildTimeChanges(t *testing.T) {
	ctx := context.Background()
	boards := fcBoardsFixture()
	repo := &statsRepoStub{freeCompanies: boards}
	svc := NewUIStatsService(repo, time.Minute, 12*time.Hour)
	snapshot := validUIStatsSnapshot()
	snapshot.FreeCompaniesBuiltAt = boards.BuiltAt
	query := FreeCompanyQuery{Measure: FreeCompanyMembers, Scope: contract.StatsScope{World: "Balmung"}}

	for i := 0; i < 3; i++ {
		got, err := svc.FreeCompanyBoard(ctx, snapshot, query)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Ready || !got.BuiltAt.Equal(boards.BuiltAt) || len(got.Rows) != 3 || got.Rows[0].Name != "Alpha" {
			t.Fatalf("board = %+v", got)
		}
	}
	if repo.fcCalls() != 1 {
		t.Errorf("LoadFreeCompanies calls = %d, want 1", repo.fcCalls())
	}

	// The nightly rebuild moves the build time: the next request reloads.
	rebuilt := *boards
	rebuilt.BuiltAt = boards.BuiltAt.Add(24 * time.Hour)
	rebuilt.Worlds = []contract.WorldFreeCompanies{{World: "Balmung", Companies: []contract.FreeCompanyCount{fc("9", "Zeta", 90, 1)}}}
	repo.mu.Lock()
	repo.freeCompanies = &rebuilt
	repo.mu.Unlock()
	snapshot.FreeCompaniesBuiltAt = rebuilt.BuiltAt
	got, err := svc.FreeCompanyBoard(ctx, snapshot, query)
	if err != nil {
		t.Fatal(err)
	}
	if repo.fcCalls() != 2 || len(got.Rows) != 1 || got.Rows[0].Name != "Zeta" {
		t.Errorf("after the rebuild: calls %d board %+v", repo.fcCalls(), got.Rows)
	}
}

func TestUIStatsServiceFreeCompanyBoardIsPendingWithoutBoards(t *testing.T) {
	ctx := context.Background()
	query := FreeCompanyQuery{Measure: FreeCompanyMembers}

	// A snapshot built before the boards existed (or a version 2 one).
	repo := &statsRepoStub{freeCompanies: fcBoardsFixture()}
	svc := NewUIStatsService(repo, time.Minute, 12*time.Hour)
	got, err := svc.FreeCompanyBoard(ctx, validUIStatsSnapshot(), query)
	if err != nil || got.Ready || repo.fcCalls() != 0 {
		t.Errorf("snapshot without boards: %+v, err %v, loads %d; want pending without a load", got, err, repo.fcCalls())
	}
	v2 := validUIStatsSnapshot()
	v2.SchemaVersion = 2
	v2.FreeCompaniesBuiltAt = v2.GeneratedAt
	if got, err := svc.FreeCompanyBoard(ctx, v2, query); err != nil || got.Ready {
		t.Errorf("version 2 snapshot: %+v, err %v; want pending", got, err)
	}

	// The snapshot names a build but the row is missing.
	repo = &statsRepoStub{}
	svc = NewUIStatsService(repo, time.Minute, 12*time.Hour)
	snapshot := validUIStatsSnapshot()
	snapshot.FreeCompaniesBuiltAt = snapshot.GeneratedAt
	got, err = svc.FreeCompanyBoard(ctx, snapshot, query)
	if err != nil || got.Ready {
		t.Errorf("missing row: %+v, err %v; want pending", got, err)
	}

	// Any other failure is an error.
	repo = &statsRepoStub{freeCompaniesErr: errors.New("connection reset")}
	svc = NewUIStatsService(repo, time.Minute, 12*time.Hour)
	if _, err := svc.FreeCompanyBoard(ctx, snapshot, query); err == nil {
		t.Error("load failure: want an error")
	}
}

func TestValidateUIStatsSnapshotRejectsMoreFreeCompanyMembersThanCharacters(t *testing.T) {
	snapshot := validUIStatsSnapshot()
	snapshot.Groups = append(snapshot.Groups, contract.ScopedGroupCount{Dimension: "world", Key: "Balmung", Total: 2, InFreeCompany: 3})
	if err := ValidateUIStatsSnapshot(snapshot); err == nil {
		t.Error("want an error for a group with more Free Company members than characters")
	}
	snapshot = validUIStatsSnapshot()
	snapshot.Summary.InFreeCompany = -1
	if err := ValidateUIStatsSnapshot(snapshot); err == nil {
		t.Error("want an error for a negative summary count")
	}
}
