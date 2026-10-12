package repository

import (
	"context"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

var fakeRetireRule = contract.RetirementRule{MinFailures: 10, MinAge: 14 * 24 * time.Hour}

func TestFakeClaimScans_BackgroundSkipsRetired(t *testing.T) {
	f, clock := newFakeRepo()
	live := mustFakeInsert(t, f, "10.0.0.1")
	retired := mustFakeInsert(t, f, "10.0.0.2")
	fakeMutate(f, retired, func(p *contract.ProxyRecord) { now := clock.Now(); p.RetiredAt = &now })

	leases := mustFakeClaim(t, f, contract.ScanBackground, 10)
	if len(leases) != 1 || leases[0].Record.ID != live {
		t.Fatalf("background claim = %d leases, want only proxy %d", len(leases), live)
	}
}

func TestFakeCompleteScanFailure_RetiresOnlyProxiesThatNeverWorked(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(now time.Time, p *contract.ProxyRecord)
		rule  contract.RetirementRule
		retir bool
	}{
		{"never worked, tenth failure, old", func(now time.Time, p *contract.ProxyRecord) {
			p.FailCount = 9
			p.FirstSeenAt = now.Add(-15 * 24 * time.Hour)
		}, fakeRetireRule, true},
		{"never worked, ninth failure", func(now time.Time, p *contract.ProxyRecord) {
			p.FailCount = 8
			p.FirstSeenAt = now.Add(-15 * 24 * time.Hour)
		}, fakeRetireRule, false},
		{"never worked, too young", func(now time.Time, p *contract.ProxyRecord) {
			p.FailCount = 30
			p.FirstSeenAt = now.Add(-13 * 24 * time.Hour)
		}, fakeRetireRule, false},
		{"worked once", func(now time.Time, p *contract.ProxyRecord) {
			p.FailCount = 30
			alive := now.Add(-20 * 24 * time.Hour)
			p.LastAliveAt = &alive
			p.FirstSeenAt = now.Add(-40 * 24 * time.Hour)
		}, fakeRetireRule, false},
		{"rule disabled", func(now time.Time, p *contract.ProxyRecord) {
			p.FailCount = 30
			p.FirstSeenAt = now.Add(-40 * 24 * time.Hour)
		}, contract.RetirementRule{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, clock := newFakeRepo()
			id := mustFakeInsert(t, f, "10.0.0.1")
			fakeMutate(f, id, func(p *contract.ProxyRecord) { tc.mut(clock.Now(), p) })
			leases := mustFakeClaim(t, f, contract.ScanBackground, 1)
			update := fakeUpdate(contract.ScanFailure, time.Minute, time.Second)
			update.Retirement = tc.rule
			if ok, err := f.CompleteScan(context.Background(), leases[0], update); err != nil || !ok {
				t.Fatalf("CompleteScan = %v, %v", ok, err)
			}
			got, _ := f.Get(context.Background(), id)
			if (got.RetiredAt != nil) != tc.retir {
				t.Fatalf("retired = %v, want %v", got.RetiredAt != nil, tc.retir)
			}
		})
	}
}

func TestFakeCompleteScanSuccess_UnretiresProxy(t *testing.T) {
	f, clock := newFakeRepo()
	id := mustFakeInsert(t, f, "10.0.0.1")
	leases := mustFakeClaim(t, f, contract.ScanBackground, 1)
	fakeMutate(f, id, func(p *contract.ProxyRecord) { now := clock.Now(); p.RetiredAt = &now })
	if ok, err := f.CompleteScan(context.Background(), leases[0], fakeUpdate(contract.ScanSuccess, time.Minute, time.Second)); err != nil || !ok {
		t.Fatalf("CompleteScan = %v, %v", ok, err)
	}
	if got, _ := f.Get(context.Background(), id); got.RetiredAt != nil {
		t.Fatal("proxy that just worked is still retired")
	}
}

func TestFakeCountByStatus_ReportsRetired(t *testing.T) {
	f, clock := newFakeRepo()
	dead := mustFakeInsert(t, f, "10.0.0.1")
	retired := mustFakeInsert(t, f, "10.0.0.2")
	fakeMutate(f, dead, func(p *contract.ProxyRecord) { p.Status = contract.ProxyStatusDead })
	fakeMutate(f, retired, func(p *contract.ProxyRecord) {
		now := clock.Now()
		p.Status = contract.ProxyStatusDead
		p.RetiredAt = &now
	})
	counts, err := f.CountByStatus(context.Background())
	if err != nil || counts["dead"] != 1 || counts["retired"] != 1 || len(counts) != 2 {
		t.Fatalf("CountByStatus = %v, %v; want one dead and one retired", counts, err)
	}
}

var testListingRule = contract.ListingRule{RewriteInterval: time.Hour, ReviveAfter: 48 * time.Hour}

func TestFakeMarkListed_StampsRevivesAndRateLimits(t *testing.T) {
	f, clock := newFakeRepo()
	ctx := context.Background()
	id := mustFakeInsert(t, f, "10.0.0.1")
	ep := []contract.ProxyEndpoint{
		{Protocol: "http", IP: "10.0.0.1", Port: 9000},
		{Protocol: "http", IP: "10.0.0.1", Port: 9000}, // duplicate in one list
		{Protocol: "http", IP: "10.9.9.9", Port: 9000}, // unknown, never inserted
	}

	res, err := f.MarkListed(ctx, ep, testListingRule)
	if err != nil || res.Marked != 1 || res.Revived != 0 {
		t.Fatalf("first MarkListed = %+v, %v; want 1 marked", res, err)
	}
	if n, _ := f.Count(ctx); n != 1 {
		t.Fatalf("Count = %d, listing an unknown endpoint must not insert", n)
	}

	// Within the rewrite interval the listing is rate limited.
	fakeMutate(f, id, func(p *contract.ProxyRecord) {
		retired := clock.Now().Add(-72 * time.Hour)
		p.RetiredAt = &retired
		p.FailCount = 14
	})
	clock.advance(59 * time.Minute)
	if res, _ := f.MarkListed(ctx, ep, testListingRule); res.Marked != 0 {
		t.Fatalf("MarkListed within the interval = %+v, want a no-op", res)
	}
	if got, _ := f.Get(ctx, id); got.RetiredAt == nil {
		t.Fatal("revived inside the rewrite window")
	}

	clock.advance(2 * time.Minute)
	res, err = f.MarkListed(ctx, ep, testListingRule)
	if err != nil || res.Marked != 1 || res.Revived != 1 {
		t.Fatalf("MarkListed after the interval = %+v, %v; want marked and revived", res, err)
	}
	got, _ := f.Get(ctx, id)
	if got.RetiredAt != nil || got.LastListedAt == nil || got.FailCount != 0 {
		t.Fatalf("after revival: retired_at=%v last_listed_at=%v fail_count=%d", got.RetiredAt, got.LastListedAt, got.FailCount)
	}
}

func TestFakeMarkListed_DoesNotReviveBeforeReviveAfter(t *testing.T) {
	f, clock := newFakeRepo()
	ctx := context.Background()
	id := mustFakeInsert(t, f, "10.0.0.2")
	ep := []contract.ProxyEndpoint{{Protocol: "http", IP: "10.0.0.2", Port: 9000}}
	fakeMutate(f, id, func(p *contract.ProxyRecord) {
		retired := clock.Now().Add(-24 * time.Hour)
		p.RetiredAt = &retired
		p.FailCount = 14
	})

	res, err := f.MarkListed(ctx, ep, testListingRule)
	if err != nil || res.Marked != 1 || res.Revived != 0 {
		t.Fatalf("MarkListed = %+v, %v; want stamped but not revived", res, err)
	}
	got, _ := f.Get(ctx, id)
	if got.RetiredAt == nil || got.FailCount != 14 || got.LastListedAt == nil {
		t.Fatalf("young retirement changed: retired_at=%v fail_count=%d last_listed_at=%v", got.RetiredAt, got.FailCount, got.LastListedAt)
	}
}

func TestFakeRetireStale_WalksInBatchesAndIsIdempotent(t *testing.T) {
	f, clock := newFakeRepo()
	ctx := context.Background()
	for i := range 25 {
		id := mustFakeInsert(t, f, "10.0.1."+string(rune('a'+i)))
		fakeMutate(f, id, func(p *contract.ProxyRecord) {
			p.FailCount = 11
			p.FirstSeenAt = clock.Now().Add(-30 * 24 * time.Hour)
		})
	}
	young := mustFakeInsert(t, f, "10.0.2.1")
	fakeMutate(f, young, func(p *contract.ProxyRecord) { p.FailCount = 11; p.FirstSeenAt = clock.Now() })

	sweep := func() (total int64) {
		var after int64
		for {
			res, err := f.RetireStale(ctx, fakeRetireRule, after, 10)
			if err != nil {
				t.Fatalf("RetireStale: %v", err)
			}
			if res.Retired > 10 {
				t.Fatalf("batch retired %d rows, limit 10", res.Retired)
			}
			total += res.Retired
			if res.Done {
				return total
			}
			if res.NextID <= after {
				t.Fatalf("cursor stuck at %d", after)
			}
			after = res.NextID
		}
	}
	if got := sweep(); got != 25 {
		t.Fatalf("first sweep retired %d, want 25", got)
	}
	if got := sweep(); got != 0 {
		t.Fatalf("second sweep retired %d, want 0", got)
	}
	if p, _ := f.Get(ctx, young); p.RetiredAt != nil {
		t.Fatal("a proxy younger than the age threshold was retired")
	}
	if _, err := f.RetireStale(ctx, fakeRetireRule, 0, 0); err == nil {
		t.Fatal("batch 0 accepted")
	}
}

func TestFakeRevivedProxyNeedsFreshFailuresToBeRetiredAgain(t *testing.T) {
	f, clock := newFakeRepo()
	ctx := context.Background()
	id := mustFakeInsert(t, f, "10.0.3.1")
	ep := []contract.ProxyEndpoint{{Protocol: "http", IP: "10.0.3.1", Port: 9000}}
	fakeMutate(f, id, func(p *contract.ProxyRecord) {
		retired := clock.Now().Add(-72 * time.Hour)
		p.RetiredAt = &retired
		p.FailCount = 14
		p.FirstSeenAt = clock.Now().Add(-30 * 24 * time.Hour)
	})
	if res, err := f.MarkListed(ctx, ep, testListingRule); err != nil || res.Revived != 1 {
		t.Fatalf("MarkListed = %+v, %v; want revived", res, err)
	}
	if res, err := f.RetireStale(ctx, fakeRetireRule, 0, 100); err != nil || res.Retired != 0 {
		t.Fatalf("sweep right after revival retired %d, %v; want 0", res.Retired, err)
	}
}
