package census

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

type UIStatsState struct {
	GeneratedAt time.Time
	Age         time.Duration
	Stale       bool
}

const uiStatsReloadTimeout = 30 * time.Second

// UIStatsService keeps the immutable analytics read model hot in each process.
type UIStatsService struct {
	repo         contract.UIStatsRepository
	cacheTTL     time.Duration
	staleWarning time.Duration
	now          func() time.Time
	observer     contract.UIStatsObserver

	mu       sync.Mutex
	snapshot *contract.UIStatsSnapshot
	loadedAt time.Time
	loading  bool
	wait     chan struct{}
	// reloadFailed records that the most recent reload attempt failed while a
	// warm snapshot was held. Only then (or once the snapshot outlives
	// staleWarning) is the served snapshot reported stale; a routine
	// stale-while-revalidate reload after cacheTTL is not.
	reloadFailed bool
}

func (s *UIStatsService) SetObserver(observer contract.UIStatsObserver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observer = observer
}

func (s *UIStatsService) observeCache(result string, snapshot *contract.UIStatsSnapshot) {
	s.mu.Lock()
	observer := s.observer
	s.mu.Unlock()
	if observer != nil {
		observer.ObserveUIStatsCache(result, snapshot)
	}
}

func NewUIStatsService(repo contract.UIStatsRepository, cacheTTL, staleWarning time.Duration) *UIStatsService {
	if cacheTTL <= 0 {
		cacheTTL = time.Minute
	}
	if staleWarning <= 0 {
		staleWarning = 12 * time.Hour
	}
	return &UIStatsService{
		repo:         repo,
		cacheTTL:     cacheTTL,
		staleWarning: staleWarning,
		now:          func() time.Time { return time.Now().UTC() },
	}
}

func (s *UIStatsService) Current(ctx context.Context) (*contract.UIStatsSnapshot, UIStatsState, error) {
	if s == nil || s.repo == nil {
		return nil, UIStatsState{}, ErrUIStatsUnavailable
	}
	now := s.now().UTC()

	s.mu.Lock()
	if s.snapshot != nil && now.Sub(s.loadedAt) < s.cacheTTL {
		snapshot := cloneUIStatsSnapshot(s.snapshot)
		failed := s.reloadFailed
		s.mu.Unlock()
		s.observeCache("hit", snapshot)
		return snapshot, s.state(snapshot, now, failed), nil
	}
	if s.loading {
		if s.snapshot != nil {
			snapshot := cloneUIStatsSnapshot(s.snapshot)
			failed := s.reloadFailed
			s.mu.Unlock()
			s.observeCache("stale_served", snapshot)
			return snapshot, s.state(snapshot, now, failed), nil
		}
		wait := s.wait
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, UIStatsState{}, fmt.Errorf("%w: %v", ErrUIStatsUnavailable, ctx.Err())
		case <-wait:
			return s.Current(ctx)
		}
	}
	s.loading = true
	s.wait = make(chan struct{})
	previous := cloneUIStatsSnapshot(s.snapshot)
	failed := s.reloadFailed
	s.mu.Unlock()
	if previous != nil {
		go s.reload(context.WithoutCancel(ctx))
		s.observeCache("stale_served", previous)
		return previous, s.state(previous, now, failed), nil
	}

	loaded, err := s.repo.LoadCurrent(ctx)
	if err == nil {
		err = ValidateUIStatsSnapshot(loaded)
	}
	current := s.finishReload(loaded, err)

	if err != nil {
		s.observeCache("error", nil)
		return nil, UIStatsState{}, fmt.Errorf("%w: %v", ErrUIStatsUnavailable, err)
	}
	s.observeCache("reload", current)
	return current, s.state(current, now, false), nil
}

func (s *UIStatsService) reload(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, uiStatsReloadTimeout)
	defer cancel()
	loaded, err := s.repo.LoadCurrent(ctx)
	if err == nil {
		err = ValidateUIStatsSnapshot(loaded)
	}
	current := s.finishReload(loaded, err)
	if err != nil {
		s.observeCache("error", current)
		return
	}
	s.observeCache("reload", current)
}

func (s *UIStatsService) finishReload(loaded *contract.UIStatsSnapshot, err error) *contract.UIStatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.snapshot = cloneUIStatsSnapshot(loaded)
	}
	s.reloadFailed = err != nil && s.snapshot != nil
	// Apply the cache TTL to both success and warm failure so a database outage
	// cannot turn request traffic into a continuous sequence of reload attempts.
	if err == nil || s.snapshot != nil {
		s.loadedAt = s.now().UTC()
	}
	s.loading = false
	close(s.wait)
	s.wait = nil
	return cloneUIStatsSnapshot(s.snapshot)
}

func (s *UIStatsService) Refresh(ctx context.Context, opts contract.UIStatsRefreshOptions) (*contract.UIStatsRefreshResult, error) {
	if s == nil || s.repo == nil {
		return nil, ErrUIStatsUnavailable
	}
	started := s.now()
	result, err := s.repo.Refresh(ctx, opts)
	s.mu.Lock()
	observer := s.observer
	s.mu.Unlock()
	if observer != nil {
		outcome := "success"
		payloadBytes := int64(0)
		if err != nil {
			outcome = "error"
		} else if result != nil && result.Skipped {
			outcome = "skipped"
		} else if result != nil {
			payloadBytes = result.PayloadBytes
		}
		observer.ObserveUIStatsRefresh(outcome, s.now().Sub(started), payloadBytes)
	}
	if err != nil || result == nil || result.Skipped {
		return result, err
	}
	if err := ValidateUIStatsSnapshot(result.Snapshot); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.snapshot = cloneUIStatsSnapshot(result.Snapshot)
	s.loadedAt = s.now().UTC()
	s.reloadFailed = false
	s.mu.Unlock()
	return result, nil
}

func (s *UIStatsService) state(snapshot *contract.UIStatsSnapshot, now time.Time, reloadFailed bool) UIStatsState {
	age := now.Sub(snapshot.GeneratedAt)
	if age < 0 {
		age = 0
	}
	return UIStatsState{
		GeneratedAt: snapshot.GeneratedAt,
		Age:         age,
		Stale:       reloadFailed || age >= s.staleWarning,
	}
}

func cloneUIStatsSnapshot(snapshot *contract.UIStatsSnapshot) *contract.UIStatsSnapshot {
	if snapshot == nil {
		return nil
	}
	clone := *snapshot
	clone.Groups = append([]contract.ScopedGroupCount(nil), snapshot.Groups...)
	clone.Expansions = append([]contract.ScopedExpansionCount(nil), snapshot.Expansions...)
	clone.NewCharacters = append([]contract.ScopedDailyCount(nil), snapshot.NewCharacters...)
	return &clone
}
