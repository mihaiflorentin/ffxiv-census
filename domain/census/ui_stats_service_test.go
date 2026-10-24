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
	mu        sync.Mutex
	snapshot  *contract.UIStatsSnapshot
	loadErr   error
	loadCalls int
	block     chan struct{}
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
	return &contract.UIStatsRefreshResult{Snapshot: cloneUIStatsSnapshot(s.snapshot)}, nil
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
