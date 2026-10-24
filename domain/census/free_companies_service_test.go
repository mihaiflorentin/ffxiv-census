package census

import (
	"context"
	"errors"
	"sync"
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

func TestUIStatsServiceFreeCompanyBoardRemembersOnlyKnownScopes(t *testing.T) {
	ctx := context.Background()
	boards := fcBoardsFixture()
	svc := NewUIStatsService(&statsRepoStub{freeCompanies: boards}, time.Minute, 12*time.Hour)
	snapshot := validUIStatsSnapshot()
	snapshot.FreeCompaniesBuiltAt = boards.BuiltAt

	for i := 0; i < 50; i++ {
		scope := contract.StatsScope{World: "Nowhere" + string(rune('A'+i))}
		if _, err := svc.FreeCompanyBoard(ctx, snapshot, FreeCompanyQuery{Measure: FreeCompanyMembers, Scope: scope}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.FreeCompanyBoard(ctx, snapshot, FreeCompanyQuery{Measure: FreeCompanyMeasure("bogus")}); err != nil {
		t.Fatal(err)
	}
	if n := svc.freeCompanies.memoised(); n != 0 {
		t.Errorf("unknown filters memoised %d boards, want 0", n)
	}
	got, err := svc.FreeCompanyBoard(ctx, snapshot, FreeCompanyQuery{Measure: FreeCompanyMembers, Scope: contract.StatsScope{World: "Balmung"}})
	if err != nil || len(got.Rows) != 3 {
		t.Fatalf("known world: %+v, %v", got, err)
	}
	if n := svc.freeCompanies.memoised(); n != 1 {
		t.Errorf("after a known world: %d boards memoised, want 1", n)
	}
}

func TestUIStatsServiceFreeCompanyBoardLoadsOnceWithoutHoldingCallersBehindIt(t *testing.T) {
	boards := fcBoardsFixture()
	block := make(chan struct{})
	repo := &statsRepoStub{freeCompanies: boards, freeCompaniesBlock: block}
	svc := NewUIStatsService(repo, time.Minute, 12*time.Hour)
	snapshot := validUIStatsSnapshot()
	snapshot.FreeCompaniesBuiltAt = boards.BuiltAt
	query := FreeCompanyQuery{Measure: FreeCompanyMembers, Scope: contract.StatsScope{World: "Balmung"}}

	var wg sync.WaitGroup
	results := make(chan FreeCompanyBoardResult, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := svc.FreeCompanyBoard(context.Background(), snapshot, query)
			if err != nil {
				t.Error(err)
			}
			results <- got
		}()
	}
	waitFor(t, func() bool { return repo.fcCalls() == 1 })

	// A caller that gives up returns at once, while the load is still running.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := svc.FreeCompanyBoard(cancelled, snapshot, query)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a cancelled caller waiting on the load: want an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled caller is stuck behind the running load")
	}

	close(block)
	wg.Wait()
	close(results)
	for got := range results {
		if !got.Ready || len(got.Rows) != 3 {
			t.Errorf("shared load result = %+v", got)
		}
	}
	if repo.fcCalls() != 1 {
		t.Errorf("LoadFreeCompanies calls = %d, want 1 shared load", repo.fcCalls())
	}
}

func TestUIStatsServiceFreeCompanyBoardRemembersAMissingRowForAWhile(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	repo := &statsRepoStub{}
	svc := NewUIStatsService(repo, time.Minute, 12*time.Hour)
	svc.now = func() time.Time { return now }
	snapshot := validUIStatsSnapshot()
	snapshot.FreeCompaniesBuiltAt = snapshot.GeneratedAt
	query := FreeCompanyQuery{Measure: FreeCompanyMembers}

	for i := 0; i < 5; i++ {
		got, err := svc.FreeCompanyBoard(ctx, snapshot, query)
		if err != nil || got.Ready {
			t.Fatalf("missing row: %+v, %v; want pending", got, err)
		}
	}
	if repo.fcCalls() != 1 {
		t.Errorf("missing row: %d loads, want 1 until the retry interval passes", repo.fcCalls())
	}

	now = now.Add(freeCompanyMissingRetry + time.Second)
	if _, err := svc.FreeCompanyBoard(ctx, snapshot, query); err != nil {
		t.Fatal(err)
	}
	if repo.fcCalls() != 2 {
		t.Errorf("after the retry interval: %d loads, want 2", repo.fcCalls())
	}

	// A new build time retries at once.
	snapshot.FreeCompaniesBuiltAt = snapshot.FreeCompaniesBuiltAt.Add(time.Hour)
	repo.mu.Lock()
	repo.freeCompanies = fcBoardsFixture()
	repo.mu.Unlock()
	got, err := svc.FreeCompanyBoard(ctx, snapshot, query)
	if err != nil || !got.Ready || repo.fcCalls() != 3 {
		t.Errorf("new build: %+v, %v, loads %d; want ready after a third load", got, err, repo.fcCalls())
	}
}

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}
