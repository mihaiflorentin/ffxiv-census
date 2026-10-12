package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// TestHub_CooldownGrowsPerConsecutiveFailureAndResetsOnSuccess walks one proxy
// through repeated destination failures: each cooldown doubles up to the cap,
// the proxy is not handed out while cooled, and a success clears the streak.
func TestHub_CooldownGrowsPerConsecutiveFailureAndResetsOnSuccess(t *testing.T) {
	repo, clock := newClockRepo()
	id := seedFreshProxy(t, repo, "1.2.3.4")
	hub := NewProxyHub(repo, 5*time.Minute, nil, 10*time.Second, consumerPolicy).
		WithDestinationCooldownCap(30 * time.Second)
	ctx := context.Background()

	// 10 s, 20 s, then capped at 30 s, 30 s.
	for i, want := range []time.Duration{10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second} {
		p, err := hub.NewProxy(ctx, "owner")
		if err != nil || p == nil {
			t.Fatalf("failure %d: expected a proxy, got %v, %v", i+1, p, err)
		}
		delay := hub.CooldownFor(p, 0)
		if delay != want {
			t.Fatalf("failure %d: cooldown %s, want %s", i+1, delay, want)
		}
		if err := p.CooldownDestination(ctx, "owner", hub.LockTTL(), delay); err != nil {
			t.Fatalf("CooldownDestination: %v", err)
		}

		// Cooled: nothing to hand out until the cooldown ends.
		if again, err := hub.NewProxy(ctx, "other"); err != nil || again != nil {
			t.Fatalf("failure %d: cooled proxy handed out (%v, %v)", i+1, again, err)
		}
		*clock = clock.Add(want - time.Second)
		if again, _ := hub.NewProxy(ctx, "other"); again != nil {
			t.Fatalf("failure %d: handed out one second before the cooldown ended", i+1)
		}
		*clock = clock.Add(time.Second)
		// Keep the evidence fresh across the simulated minutes.
		repo.SeedSuccess(id, 50)
	}

	p, err := hub.NewProxy(ctx, "owner")
	if err != nil || p == nil {
		t.Fatalf("expected the proxy back after the last cooldown, got %v, %v", p, err)
	}
	if got := p.Record().DestinationFailures; got != 4 {
		t.Fatalf("claimed record carries %d failures, want 4", got)
	}
	if err := p.ResetDestinationFailures(ctx, "owner"); err != nil {
		t.Fatalf("ResetDestinationFailures: %v", err)
	}
	if got := p.Record().DestinationFailures; got != 0 {
		t.Errorf("in-memory record keeps %d failures after reset", got)
	}
	stored, _ := repo.Get(ctx, id)
	if stored.DestinationFailures != 0 {
		t.Errorf("stored failures %d after reset, want 0", stored.DestinationFailures)
	}
	if got := hub.CooldownFor(p, 0); got != 10*time.Second {
		t.Errorf("cooldown after a success restarts at the floor, got %s", got)
	}
}

// TestHub_HandoutRejectionGrowsCooldown proves the hand-out check shares the
// growth rule with the in-job failure path.
func TestHub_HandoutRejectionGrowsCooldown(t *testing.T) {
	repo, clock := newClockRepo()
	id := seedFreshProxy(t, repo, "1.2.3.4")
	hub := NewProxyHub(repo, 5*time.Minute, fakeChecker(func(context.Context, string, string, int) (int, error) {
		return 0, &contract.ProxyCheckError{Kind: contract.CheckTarget, Reason: "status 503", Err: errors.New("503")}
	}), 10*time.Second, consumerPolicy).WithDestinationCooldownCap(time.Minute)
	ctx := context.Background()

	for i, want := range []time.Duration{10 * time.Second, 20 * time.Second} {
		if p, err := hub.NewProxy(ctx, "owner"); err != nil || p != nil {
			t.Fatalf("rejection %d: %v, %v", i+1, p, err)
		}
		rec, _ := repo.Get(ctx, id)
		if got := rec.DestinationCooldownUntil.Sub(*clock); got != want {
			t.Fatalf("rejection %d: cooldown %s, want %s", i+1, got, want)
		}
		*clock = clock.Add(want)
		repo.SeedSuccess(id, 50)
	}
}
