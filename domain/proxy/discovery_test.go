package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	proxydomain "github.com/mihaiflorentin88/ffxiv-census/domain/proxy"
	mockproxy "github.com/mihaiflorentin88/ffxiv-census/mock/proxy"
	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func discardLogger() contract.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// listing builds n distinct http records named after the provider, so two
// providers never list the same endpoint unless a test says so.
func listing(provider string, n int) []contract.ProxyRecord {
	recs := make([]contract.ProxyRecord, n)
	for i := range recs {
		recs[i] = contract.ProxyRecord{
			Protocol: "http", IP: fmt.Sprintf("%s-%d", provider, i), Port: 8080, Source: provider,
		}
	}
	return recs
}

func newDiscoverer(repo *repository.FakeProxyRepository, providers ...contract.ProxyProvider) *proxydomain.Discoverer {
	return proxydomain.NewDiscoverer(providers, repo, discardLogger())
}

func TestDiscoverer_InsertsNewProxiesInBulkWithoutPerLineReads(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	d := newDiscoverer(repo, mockproxy.NewFakeProvider("alpha", listing("alpha", 250)))

	res, err := d.Run(context.Background(), 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Inserted != 250 {
		t.Fatalf("Inserted = %d, want 250", res.Inserted)
	}
	if n, _ := repo.Count(context.Background()); n != 250 {
		t.Fatalf("Count = %d, want 250", n)
	}
	if repo.ExistsCalls != 0 || repo.InsertCalls != 0 {
		t.Errorf("per-line reads: Exists=%d InsertIfAbsent=%d, want none", repo.ExistsCalls, repo.InsertCalls)
	}
	if repo.InsertNewCalls != 1 {
		t.Errorf("InsertNew calls = %d, want 1 bulk statement for 250 lines", repo.InsertNewCalls)
	}
}

func TestDiscoverer_RelistedRetiredProxyIsRevivedNotReinserted(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	retiredAt := time.Now().Add(-72 * time.Hour)
	retiredID := repo.Seed(contract.ProxyRecord{
		Protocol: "http", IP: "alpha-0", Port: 8080, Source: "old", FailCount: 14, RetiredAt: &retiredAt,
	})
	d := newDiscoverer(repo, mockproxy.NewFakeProvider("alpha", listing("alpha", 2)))

	res, err := d.Run(context.Background(), 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("Inserted = %d, want only the unknown alpha-1", res.Inserted)
	}
	if n, _ := repo.Count(context.Background()); n != 2 {
		t.Fatalf("Count = %d, want 2: the retired endpoint must not be inserted again", n)
	}
	p, _ := repo.Get(context.Background(), retiredID)
	if p.RetiredAt != nil || p.LastListedAt == nil {
		t.Fatalf("retired_at = %v, last_listed_at = %v; want revived and stamped", p.RetiredAt, p.LastListedAt)
	}
	if p.FailCount != 14 {
		t.Errorf("fail_count = %d, want it kept so one failed scan retires it again", p.FailCount)
	}
}

func TestDiscoverer_EveryListedEndpointGoesThroughMarkListed(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	repo.Seed(contract.ProxyRecord{Protocol: "http", IP: "alpha-0", Port: 8080, Source: "old"})
	d := newDiscoverer(repo, mockproxy.NewFakeProvider("alpha", listing("alpha", 3)))

	if _, err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(repo.MarkListedEndpoints); got != 3 {
		t.Fatalf("MarkListed saw %d endpoints, want all 3 listed (new and existing)", got)
	}
}

func TestDiscoverer_FailingProviderDoesNotStopTheOthers(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	bad := mockproxy.NewFakeProvider("bad", nil)
	bad.SetError(errors.New("fetch failed"))
	d := newDiscoverer(repo, bad, mockproxy.NewFakeProvider("good", listing("good", 2))).
		WithShuffle(func([]contract.ProxyProvider) {})

	res, err := d.Run(context.Background(), 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Inserted != 2 {
		t.Fatalf("Inserted = %d, want 2 from the healthy provider", res.Inserted)
	}
	byName := providerResults(res)
	if byName["bad"].Err == nil || byName["good"].Err != nil {
		t.Fatalf("results = %+v, want an error for bad only", res.Providers)
	}
}

func TestDiscoverer_FailsWhenEveryProviderFails(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	one := mockproxy.NewFakeProvider("one", nil)
	one.SetError(errors.New("down"))
	two := mockproxy.NewFakeProvider("two", nil)
	two.SetError(errors.New("down"))

	if _, err := newDiscoverer(repo, one, two).Run(context.Background(), 0); err == nil {
		t.Fatal("Run succeeded although every provider failed")
	}
}

func TestDiscoverer_WorksInChunksSoHugeListsStayBounded(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	d := newDiscoverer(repo, mockproxy.NewFakeProvider("alpha", listing("alpha", 250))).WithChunkSize(100)

	res, err := d.Run(context.Background(), 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Inserted != 250 {
		t.Fatalf("Inserted = %d, want 250", res.Inserted)
	}
	if repo.InsertNewCalls != 3 || repo.MarkListedCalls != 3 {
		t.Errorf("InsertNew calls = %d, MarkListed calls = %d, want 3 chunks each", repo.InsertNewCalls, repo.MarkListedCalls)
	}
}

func insertedBy(res proxydomain.DiscoveryResult) map[string]int {
	out := map[string]int{}
	for _, p := range res.Providers {
		out[p.Name] += p.Inserted
	}
	return out
}

func inOrder([]contract.ProxyProvider) {}

func TestDiscoverer_RespectsThePerRunLimit(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	d := newDiscoverer(repo, mockproxy.NewFakeProvider("alpha", listing("alpha", 100)))

	res, err := d.Run(context.Background(), 10)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n, _ := repo.Count(context.Background()); res.Inserted != 10 || n != 10 {
		t.Fatalf("Inserted = %d, stored = %d, want 10", res.Inserted, n)
	}
}

func TestDiscoverer_LimitIsSplitFairlyAcrossProviders(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	d := newDiscoverer(repo,
		mockproxy.NewFakeProvider("alpha", listing("alpha", 100)),
		mockproxy.NewFakeProvider("beta", listing("beta", 100)),
		mockproxy.NewFakeProvider("gamma", listing("gamma", 100)),
	).WithShuffle(inOrder)

	res, err := d.Run(context.Background(), 30)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := insertedBy(res)
	if got["alpha"] != 10 || got["beta"] != 10 || got["gamma"] != 10 {
		t.Fatalf("inserted per provider = %v, want 10 each (a first provider must not starve the rest)", got)
	}
}

func TestDiscoverer_UnusedShareIsRedistributed(t *testing.T) {
	// Worst case for a single pass: the small provider comes last, so by the
	// time its unused share is known the big ones have already been capped.
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	d := newDiscoverer(repo,
		mockproxy.NewFakeProvider("big1", listing("big1", 100)),
		mockproxy.NewFakeProvider("big2", listing("big2", 100)),
		mockproxy.NewFakeProvider("small", listing("small", 2)),
	).WithShuffle(inOrder)

	res, err := d.Run(context.Background(), 30)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := insertedBy(res)
	if got["small"] != 2 || got["big1"] != 14 || got["big2"] != 14 || res.Inserted != 30 {
		t.Fatalf("inserted per provider = %v (total %d), want small=2 big1=14 big2=14", got, res.Inserted)
	}
}

func TestDiscoverer_FailedProviderShareGoesToTheOthers(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	bad := mockproxy.NewFakeProvider("bad", nil)
	bad.SetError(errors.New("down"))
	d := newDiscoverer(repo, bad, mockproxy.NewFakeProvider("good", listing("good", 100))).WithShuffle(inOrder)

	res, err := d.Run(context.Background(), 20)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Inserted != 20 {
		t.Fatalf("Inserted = %d, want the full 20 for the surviving provider", res.Inserted)
	}
}

func TestDiscoverer_NoLimitTakesEverything(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	d := newDiscoverer(repo,
		mockproxy.NewFakeProvider("alpha", listing("alpha", 700)),
		mockproxy.NewFakeProvider("beta", listing("beta", 700)),
	)
	res, err := d.Run(context.Background(), 0)
	if err != nil || res.Inserted != 1400 {
		t.Fatalf("Inserted = %d, %v; want 1400", res.Inserted, err)
	}
}

func TestDiscoverer_ProviderOrderChangesBetweenRuns(t *testing.T) {
	first := map[string]bool{}
	for i := 0; i < 64; i++ {
		repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
		res, err := newDiscoverer(repo,
			mockproxy.NewFakeProvider("alpha", listing("alpha", 1)),
			mockproxy.NewFakeProvider("beta", listing("beta", 1)),
			mockproxy.NewFakeProvider("gamma", listing("gamma", 1)),
		).Run(context.Background(), 0)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		first[res.Providers[0].Name] = true
	}
	if len(first) < 2 {
		t.Fatalf("the same provider was read first in 64 runs: %v", first)
	}
}

func TestDiscoverer_SkipsRecordsThatCannotBeProxies(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	recs := []contract.ProxyRecord{
		{Protocol: "http", IP: "", Port: 80, Source: "alpha"},
		{Protocol: "http", IP: "1.1.1.1", Port: 0, Source: "alpha"},
		{Protocol: "http", IP: "1.1.1.2", Port: 70000, Source: "alpha"},
		{Protocol: "", IP: "1.1.1.3", Port: 80, Source: "alpha"},
		{Protocol: "http", IP: "1.1.1.4", Port: 80, Source: "alpha"},
	}
	res, err := newDiscoverer(repo, mockproxy.NewFakeProvider("alpha", recs)).Run(context.Background(), 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Inserted != 1 || res.Providers[0].Listed != 1 {
		t.Fatalf("Inserted = %d, Listed = %d, want 1 and 1", res.Inserted, res.Providers[0].Listed)
	}
}

func twoLists(name string) *mockproxy.FakeListProvider {
	return mockproxy.NewFakeListProvider(name,
		mockproxy.FakeList{URL: name + "/http.txt", ETag: `"v1"`, Records: listing(name+"-http", 5)},
		mockproxy.FakeList{URL: name + "/socks.txt", ETag: `"v1"`, Records: listing(name+"-socks", 5)},
	)
}

func TestDiscoverer_UnchangedListsAreSkippedOnTheNextRun(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	cache := mockproxy.NewFakeListCache()
	prov := twoLists("alpha")
	d := newDiscoverer(repo, prov).WithListCache(cache)

	first, err := d.Run(context.Background(), 0)
	if err != nil || first.Inserted != 10 {
		t.Fatalf("first run: Inserted = %d, %v; want 10", first.Inserted, err)
	}
	if v, ok := cache.Get("alpha/http.txt"); !ok || v.ETag != `"v1"` {
		t.Fatalf("validators after a complete read = %v, %v", v, ok)
	}

	// A new process: nothing is kept in memory, only the cache survives.
	d = newDiscoverer(repo, prov).WithListCache(cache)
	listCalls := repo.MarkListedCalls
	second, err := d.Run(context.Background(), 0)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(prov.Read) != 2 || len(prov.Skipped) != 2 {
		t.Fatalf("read %v, skipped %v; want the unchanged lists skipped on run two", prov.Read, prov.Skipped)
	}
	if second.Inserted != 0 || repo.MarkListedCalls != listCalls {
		t.Errorf("skipped lists still hit the database: inserted %d, extra MarkListed calls %d", second.Inserted, repo.MarkListedCalls-listCalls)
	}
}

func TestDiscoverer_ChangedListIsReadAgain(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	cache := mockproxy.NewFakeListCache()
	prov := twoLists("alpha")
	if _, err := newDiscoverer(repo, prov).WithListCache(cache).Run(context.Background(), 0); err != nil {
		t.Fatalf("first run: %v", err)
	}

	prov.SetETag("alpha/socks.txt", `"v2"`)
	prov.Read, prov.Skipped = nil, nil
	if _, err := newDiscoverer(repo, prov).WithListCache(cache).Run(context.Background(), 0); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(prov.Read) != 1 || prov.Read[0] != "alpha/socks.txt" || len(prov.Skipped) != 1 {
		t.Fatalf("read %v, skipped %v; want only the changed list read", prov.Read, prov.Skipped)
	}
	if v, _ := cache.Get("alpha/socks.txt"); v.ETag != `"v2"` {
		t.Errorf("validators = %v, want the new version stored", v)
	}
}

func TestDiscoverer_ListCutShortByTheBudgetIsNotRemembered(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	cache := mockproxy.NewFakeListCache()
	prov := twoLists("alpha")

	if _, err := newDiscoverer(repo, prov).WithListCache(cache).Run(context.Background(), 3); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := cache.Get("alpha/http.txt"); ok {
		t.Fatal("a list that was not read to the end was remembered as unchanged; its remaining proxies would never be read")
	}
}

func TestDiscoverer_ListIsNotRememberedWhenItsRecordsWereNotStored(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	repo.InsertNewErr = errors.New("db down")
	cache := mockproxy.NewFakeListCache()

	_, err := newDiscoverer(repo, twoLists("alpha")).WithListCache(cache).Run(context.Background(), 0)
	if err == nil {
		t.Fatal("Run succeeded although nothing could be stored")
	}
	if cache.Saves != 0 {
		t.Fatalf("validators saved %d times although the insert failed", cache.Saves)
	}
}

func TestDiscoverer_CacheFailuresFallBackToReadingEverything(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	cache := mockproxy.NewFakeListCache()
	cache.LoadErr = errors.New("table missing")
	cache.SaveErr = errors.New("read only")

	res, err := newDiscoverer(repo, twoLists("alpha")).WithListCache(cache).Run(context.Background(), 0)
	if err != nil || res.Inserted != 10 {
		t.Fatalf("Inserted = %d, %v; a broken cache must not stop discovery", res.Inserted, err)
	}
}

func providerResults(res proxydomain.DiscoveryResult) map[string]proxydomain.ProviderResult {
	out := make(map[string]proxydomain.ProviderResult, len(res.Providers))
	for _, p := range res.Providers {
		out[p.Name] = p
	}
	return out
}

// New proxies carry no evidence: the consumer claim path never hands them out
// and they enter the background scan queue.
func TestDiscoverer_NewProxyEntersBackgroundScanOnly(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	d := newDiscoverer(repo, mockproxy.NewFakeProvider("alpha", listing("alpha", 1)))
	if _, err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("Run: %v", err)
	}

	p, err := repo.Get(context.Background(), 1)
	if err != nil || p == nil {
		t.Fatalf("Get: %v, %v", p, err)
	}
	if p.GeneralHealthy || p.LastVerifiedAt != nil {
		t.Fatalf("new proxy has fresh evidence: healthy=%v verified_at=%v", p.GeneralHealthy, p.LastVerifiedAt)
	}
	if rec, err := repo.ClaimProxy(context.Background(), "owner", time.Minute); err != nil || rec != nil {
		t.Fatalf("unverified new proxy was handed to a consumer: %v, %v", rec, err)
	}
	leases, err := repo.ClaimScans(context.Background(), contract.ScanBackground, 10)
	if err != nil || len(leases) != 1 || leases[0].Record.ID != p.ID {
		t.Fatalf("new proxy not in the background scan queue: %d leases, %v", len(leases), err)
	}
}
