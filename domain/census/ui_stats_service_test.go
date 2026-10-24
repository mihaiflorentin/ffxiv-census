package census

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

type statsRepoStub struct {
	mu       sync.Mutex
	snapshot *contract.UIStatsSnapshot
	// refreshSnapshot, when set, is what Refresh returns instead of snapshot.
	refreshSnapshot *contract.UIStatsSnapshot
	loadErr         error
	loadCalls       int
	block           chan struct{}

	freeCompanies      *contract.StatsFreeCompanies
	freeCompaniesErr   error
	freeCompaniesCalls int
	// freeCompaniesBlock, when set, holds every LoadFreeCompanies until closed.
	freeCompaniesBlock chan struct{}
}

func (s *statsRepoStub) LoadFreeCompanies(context.Context) (*contract.StatsFreeCompanies, error) {
	s.mu.Lock()
	s.freeCompaniesCalls++
	block := s.freeCompaniesBlock
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.freeCompaniesErr != nil {
		return nil, s.freeCompaniesErr
	}
	if s.freeCompanies == nil {
		return nil, ErrUIStatsUnavailable
	}
	clone := *s.freeCompanies
	return &clone, nil
}

func (s *statsRepoStub) fcCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.freeCompaniesCalls
}

func (s *statsRepoStub) LoadCurrent(context.Context) (*contract.UIStatsSnapshot, error) {
	s.mu.Lock()
	s.loadCalls++
	block := s.block
	snapshot, err := s.snapshot, s.loadErr
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	return cloneUIStatsSnapshot(snapshot), err
}

func (s *statsRepoStub) Refresh(context.Context, contract.UIStatsRefreshOptions) (*contract.UIStatsRefreshResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.snapshot
	if s.refreshSnapshot != nil {
		snapshot = s.refreshSnapshot
	}
	return &contract.UIStatsRefreshResult{Snapshot: cloneUIStatsSnapshot(snapshot)}, nil
}

func (s *statsRepoStub) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadCalls
}

func TestUIStatsServiceCachesAndDefensivelyCopies(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	repo := &statsRepoStub{snapshot: validUIStatsSnapshot()}
	svc := NewUIStatsService(repo, time.Minute, 12*time.Hour)
	svc.now = func() time.Time { return now }

	first, state, err := svc.Current(context.Background())
	if err != nil || state.Stale {
		t.Fatalf("Current() = state %#v, err %v", state, err)
	}
	first.Summary.Total = 999
	second, _, err := svc.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Summary.Total != 3 {
		t.Fatalf("cached snapshot mutated: total = %d", second.Summary.Total)
	}
	if repo.calls() != 1 {
		t.Fatalf("LoadCurrent calls = %d, want 1", repo.calls())
	}
}

func TestUIStatsServiceStateCarriesSnapshotActivityWindow(t *testing.T) {
	snapshot := validUIStatsSnapshot()
	snapshot.ActivitySince = snapshot.GeneratedAt.Add(-45 * 24 * time.Hour)
	svc := NewUIStatsService(&statsRepoStub{snapshot: snapshot}, time.Minute, 12*time.Hour)

	_, state, err := svc.Current(context.Background())
	if err != nil || state.ActivityWindowDays != 45 {
		t.Fatalf("Current() state = %#v, err %v; want ActivityWindowDays 45", state, err)
	}
	if cached, ok := svc.Cached(); !ok || cached.ActivityWindowDays != 45 {
		t.Fatalf("Cached() = %#v, %v; want ActivityWindowDays 45", cached, ok)
	}
}

func TestUIStatsServiceSingleflightsExpiredReload(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	repo := &statsRepoStub{snapshot: validUIStatsSnapshot()}
	svc := NewUIStatsService(repo, time.Minute, 12*time.Hour)
	svc.now = func() time.Time { return now }
	if _, _, err := svc.Current(context.Background()); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Minute)
	repo.block = make(chan struct{})
	const callers = 20
	start := make(chan struct{})
	done := make(chan error, callers)
	for range callers {
		go func() {
			<-start
			_, _, err := svc.Current(context.Background())
			done <- err
		}()
	}
	close(start)
	deadline := time.Now().Add(time.Second)
	for repo.calls() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(repo.block)
	for range callers {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if repo.calls() != 2 {
		t.Fatalf("LoadCurrent calls = %d, want 2", repo.calls())
	}
}

func TestUIStatsServiceReturnsWarmSnapshotWhileReloadRuns(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	repo := &statsRepoStub{snapshot: validUIStatsSnapshot()}
	svc := NewUIStatsService(repo, time.Minute, 12*time.Hour)
	svc.now = func() time.Time { return now }
	if _, _, err := svc.Current(context.Background()); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Minute)
	repo.mu.Lock()
	repo.block = make(chan struct{})
	block := repo.block
	repo.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})

	type response struct {
		snapshot *contract.UIStatsSnapshot
		state    UIStatsState
		err      error
	}
	done := make(chan response, 1)
	go func() {
		snapshot, state, err := svc.Current(context.Background())
		done <- response{snapshot: snapshot, state: state, err: err}
	}()

	select {
	case got := <-done:
		// A routine stale-while-revalidate reload is not a delayed refresh:
		// the snapshot is minutes old, so it must not be flagged stale.
		if got.err != nil || got.snapshot == nil || got.state.Stale {
			t.Fatalf("Current() = snapshot %#v, state %#v, err %v", got.snapshot, got.state, got.err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("warm Current blocked on snapshot reload")
	}
}

func TestUIStatsServiceServesLastGoodSnapshotOnReloadError(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	repo := &statsRepoStub{snapshot: validUIStatsSnapshot()}
	svc := NewUIStatsService(repo, time.Minute, time.Hour)
	svc.now = func() time.Time { return now }
	if _, _, err := svc.Current(context.Background()); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Hour)
	repo.loadErr = errors.New("database down")
	got, state, err := svc.Current(context.Background())
	if err != nil {
		t.Fatalf("Current() error = %v, want stale snapshot", err)
	}
	if got == nil || !state.Stale {
		t.Fatalf("Current() = %#v, state %#v", got, state)
	}
}

func TestUIStatsServiceColdLoadError(t *testing.T) {
	repo := &statsRepoStub{loadErr: errors.New("database down")}
	svc := NewUIStatsService(repo, time.Minute, time.Hour)
	if _, _, err := svc.Current(context.Background()); !errors.Is(err, ErrUIStatsUnavailable) {
		t.Fatalf("Current() error = %v, want ErrUIStatsUnavailable", err)
	}
}

func TestUIStatsServiceFlagsStaleAfterBackgroundReloadFails(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	repo := &statsRepoStub{snapshot: validUIStatsSnapshot()}
	svc := NewUIStatsService(repo, time.Minute, 12*time.Hour)
	svc.now = func() time.Time { return now }
	if _, _, err := svc.Current(context.Background()); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Minute)
	repo.mu.Lock()
	repo.loadErr = errors.New("database down")
	repo.mu.Unlock()
	if _, _, err := svc.Current(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForReloadToFinish(t, svc)

	_, state, err := svc.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Stale {
		t.Fatalf("state after failed reload = %#v, want Stale", state)
	}

	now = now.Add(2 * time.Minute)
	repo.mu.Lock()
	repo.loadErr = nil
	repo.mu.Unlock()
	if _, _, err := svc.Current(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForReloadToFinish(t, svc)
	_, state, err = svc.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Stale {
		t.Fatalf("state after recovered reload = %#v, want not Stale", state)
	}
}

func TestUIStatsServiceFlagsStaleByAge(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC).Add(12 * time.Hour)
	repo := &statsRepoStub{snapshot: validUIStatsSnapshot()}
	svc := NewUIStatsService(repo, time.Minute, 12*time.Hour)
	svc.now = func() time.Time { return now }
	_, state, err := svc.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Stale {
		t.Fatalf("state at age == stale_warning = %#v, want Stale", state)
	}
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// startBlockedBackgroundReload expires the cache and triggers a background
// reload that blocks inside LoadCurrent until the returned release is called.
// The reload then returns the given snapshot and error.
func startBlockedBackgroundReload(t *testing.T, svc *UIStatsService, repo *statsRepoStub, clock *testClock, snapshot *contract.UIStatsSnapshot, loadErr error) func() {
	t.Helper()
	clock.Advance(2 * time.Minute)
	block := make(chan struct{})
	repo.mu.Lock()
	repo.block = block
	repo.snapshot = snapshot
	repo.loadErr = loadErr
	calls := repo.loadCalls
	repo.mu.Unlock()
	if _, _, err := svc.Current(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for repo.calls() == calls && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if repo.calls() == calls {
		t.Fatal("background reload did not start")
	}
	var once sync.Once
	return func() { once.Do(func() { close(block) }) }
}

func refreshWithNewerSnapshot(t *testing.T, svc *UIStatsService, repo *statsRepoStub) {
	t.Helper()
	newer := validUIStatsSnapshot()
	newer.GeneratedAt = newer.GeneratedAt.Add(time.Minute)
	newer.Summary.Total = 7
	repo.mu.Lock()
	repo.refreshSnapshot = newer
	repo.mu.Unlock()
	if _, err := svc.Refresh(context.Background(), contract.UIStatsRefreshOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestUIStatsServiceIgnoresFailedReloadOvertakenByRefresh(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)}
	repo := &statsRepoStub{snapshot: validUIStatsSnapshot()}
	svc := NewUIStatsService(repo, time.Minute, 12*time.Hour)
	svc.now = clock.Now
	if _, _, err := svc.Current(context.Background()); err != nil {
		t.Fatal(err)
	}

	release := startBlockedBackgroundReload(t, svc, repo, clock, nil, errors.New("database down"))
	t.Cleanup(release)
	refreshWithNewerSnapshot(t, svc, repo)
	calls := repo.calls()

	clock.Advance(30 * time.Second)
	release()
	waitForReloadToFinish(t, svc)

	got, state, err := svc.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Stale {
		t.Fatalf("state = %#v: an overtaken reload failure must not flag the newer snapshot stale", state)
	}
	if got.Summary.Total != 7 {
		t.Fatalf("total = %d, want the refreshed snapshot (7)", got.Summary.Total)
	}
	// The refresh happened 30s ago and the TTL is 1m, so the cache is still
	// fresh only if the failed reload did not reset loadedAt.
	clock.Advance(45 * time.Second)
	if _, _, err := svc.Current(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForReloadToFinish(t, svc)
	if repo.calls() != calls+1 {
		t.Fatalf("LoadCurrent calls = %d, want %d: loadedAt must stay at the refresh time", repo.calls(), calls+1)
	}
}

func TestUIStatsServiceIgnoresOlderReloadOvertakenByRefresh(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)}
	repo := &statsRepoStub{snapshot: validUIStatsSnapshot()}
	svc := NewUIStatsService(repo, time.Minute, 12*time.Hour)
	svc.now = clock.Now
	if _, _, err := svc.Current(context.Background()); err != nil {
		t.Fatal(err)
	}

	older := validUIStatsSnapshot()
	older.Summary.Total = 5
	release := startBlockedBackgroundReload(t, svc, repo, clock, older, nil)
	t.Cleanup(release)
	refreshWithNewerSnapshot(t, svc, repo)

	release()
	waitForReloadToFinish(t, svc)

	got, _, err := svc.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Total != 7 {
		t.Fatalf("total = %d, want the refreshed snapshot (7), not the overtaken reload", got.Summary.Total)
	}
}

func TestUIStatsServiceCachedNeverLoads(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	repo := &statsRepoStub{snapshot: validUIStatsSnapshot()}
	svc := NewUIStatsService(repo, time.Minute, 12*time.Hour)
	svc.now = func() time.Time { return now }

	if _, ok := svc.Cached(); ok {
		t.Fatal("Cached() reported a snapshot before any load")
	}
	if repo.calls() != 0 {
		t.Fatalf("Cached() loaded the snapshot: calls = %d", repo.calls())
	}
	if _, _, err := svc.Current(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Past the cache TTL, Cached still answers from memory without reloading.
	now = now.Add(time.Hour)
	state, ok := svc.Cached()
	if !ok || !state.GeneratedAt.Equal(validUIStatsSnapshot().GeneratedAt) || state.Age != 2*time.Hour || state.Stale {
		t.Fatalf("Cached() = %#v, %v", state, ok)
	}
	if repo.calls() != 1 {
		t.Fatalf("LoadCurrent calls = %d, want 1", repo.calls())
	}
}

func waitForReloadToFinish(t *testing.T, svc *UIStatsService) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		loading := svc.loading
		svc.mu.Unlock()
		if !loading {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background reload did not finish")
}
