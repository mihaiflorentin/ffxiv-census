package repository_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
)

func TestCooldownDestination_CountsConsecutiveFailuresUntilReset(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.40.0.1", 9001)
	healthyViaCompletion(t, driver, repo, id)

	for want := 1; want <= 3; want++ {
		rec, err := repo.ClaimProxy(ctx, "owner", time.Minute)
		if err != nil || rec == nil {
			t.Fatalf("claim %d: %v, %v", want, rec, err)
		}
		if err := repo.CooldownDestination(ctx, id, "owner", time.Minute, time.Second); err != nil {
			t.Fatalf("CooldownDestination: %v", err)
		}
		if got := mustGetProxy(t, repo, id).DestinationFailures; got != want {
			t.Fatalf("after failure %d: DestinationFailures = %d", want, got)
		}
		// Let the 1 s cooldown lapse so the next claim succeeds.
		seedScanState(t, driver, id, `destination_cooldown_until = NULL`)
	}

	rec, err := repo.ClaimProxy(ctx, "owner", time.Minute)
	if err != nil || rec == nil {
		t.Fatalf("claim: %v, %v", rec, err)
	}
	if rec.DestinationFailures != 3 {
		t.Errorf("claimed record carries %d failures, want 3", rec.DestinationFailures)
	}

	// A foreign owner cannot reset the counter.
	if err := repo.ResetDestinationFailures(ctx, id, "intruder"); err != nil {
		t.Fatalf("ResetDestinationFailures intruder: %v", err)
	}
	if got := mustGetProxy(t, repo, id).DestinationFailures; got != 3 {
		t.Errorf("intruder reset the counter to %d", got)
	}
	if err := repo.ResetDestinationFailures(ctx, id, "owner"); err != nil {
		t.Fatalf("ResetDestinationFailures: %v", err)
	}
	p := mustGetProxy(t, repo, id)
	if p.DestinationFailures != 0 {
		t.Errorf("DestinationFailures = %d after reset, want 0", p.DestinationFailures)
	}
	if p.LockedBy == nil || *p.LockedBy != "owner" {
		t.Error("reset must keep the consumer lock: the proxy is still in use")
	}
}

func TestClaimProxy_PrefersProxiesWithoutRecentDestinationFailures(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	best := mustInsertProxy(t, repo, "10.41.0.1", 9001)
	other := mustInsertProxy(t, repo, "10.41.0.2", 9002)
	healthyViaCompletion(t, driver, repo, best)
	healthyViaCompletion(t, driver, repo, other)
	// The nominally better proxy just came back from a cooldown.
	seedScanState(t, driver, best, `uptime_percent = 99, destination_failures = 2`)
	seedScanState(t, driver, other, `uptime_percent = 10`)

	rec, err := repo.ClaimProxy(ctx, "owner", time.Minute)
	if err != nil || rec == nil {
		t.Fatalf("ClaimProxy: %v, %v", rec, err)
	}
	if rec.ID != other {
		t.Errorf("claimed proxy %d, want %d: a proxy on probation must rank behind clean ones", rec.ID, other)
	}
}

func TestClaimProxy_ConcurrentClaimersNeverGetCooledOrSharedProxies(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	const total, cooled = 8, 3
	cooledIDs := map[int64]bool{}
	for i := 0; i < total; i++ {
		id := mustInsertProxy(t, repo, "10.42.0."+string(rune('1'+i)), 9000+i)
		healthyViaCompletion(t, driver, repo, id)
		if i < cooled {
			seedScanState(t, driver, id, `destination_cooldown_until = statement_timestamp() + interval '1 hour', destination_failures = 1`)
			cooledIDs[id] = true
		}
	}

	var mu sync.Mutex
	got := map[int64]int{}
	var wg sync.WaitGroup
	for i := 0; i < total*2; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			rec, err := repo.ClaimProxy(ctx, "owner-"+string(rune('a'+n)), time.Minute)
			if err != nil {
				t.Errorf("ClaimProxy: %v", err)
				return
			}
			if rec == nil {
				return
			}
			mu.Lock()
			got[rec.ID]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if len(got) != total-cooled {
		t.Errorf("claimed %d distinct proxies, want %d", len(got), total-cooled)
	}
	for id, n := range got {
		if cooledIDs[id] {
			t.Errorf("cooled proxy %d was handed out", id)
		}
		if n != 1 {
			t.Errorf("proxy %d handed out %d times", id, n)
		}
	}
}
