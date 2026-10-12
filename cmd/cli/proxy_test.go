package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// fakeProvider implements contract.ProxyProvider for tests.
type fakeProvider struct {
	name    string
	records []contract.ProxyRecord
	err     error
	// fetchCalls counts how many times FetchProxies was invoked.
	fetchCalls int
	// emittedRecords counts how many records were successfully emitted
	// (i.e. emit returned nil) before FetchProxies returned.
	emittedRecords int
}

func (p *fakeProvider) Name() string { return p.name }
func (p *fakeProvider) FetchProxies(_ context.Context, emit func(contract.ProxyRecord) error) error {
	p.fetchCalls++
	if p.err != nil {
		return p.err
	}
	for _, rec := range p.records {
		p.emittedRecords++
		if err := emit(rec); err != nil {
			return err
		}
	}
	return nil
}

func TestPublishDiscoveredProxies_EmptyProviderSet(t *testing.T) {
	q := &errorQueue{failOn: 0} // never fails
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})

	published, err := publishDiscoveredProxies(context.Background(), q, repo, logger, nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if published != 0 {
		t.Errorf("published = %d, want 0", published)
	}
}

func TestPublishDiscoveredProxies_PartialProviderFailure(t *testing.T) {
	q := &errorQueue{failOn: 0} // never fails
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})

	providers := []contract.ProxyProvider{
		&fakeProvider{name: "bad", err: errors.New("fetch failed")},
		&fakeProvider{name: "good", records: []contract.ProxyRecord{
			{Protocol: "http", IP: "1.2.3.4", Port: 8080},
		}},
	}

	published, err := publishDiscoveredProxies(context.Background(), q, repo, logger, providers, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if published != 1 {
		t.Errorf("published = %d, want 1", published)
	}
}

func TestPublishDiscoveredProxies_AllProvidersFail(t *testing.T) {
	q := &errorQueue{failOn: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})

	providers := []contract.ProxyProvider{
		&fakeProvider{name: "bad1", err: errors.New("fail1")},
		&fakeProvider{name: "bad2", err: errors.New("fail2")},
	}

	_, err := publishDiscoveredProxies(context.Background(), q, repo, logger, providers, 0)
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
}

func TestPublishDiscoveredProxies_QueuePublishFailure(t *testing.T) {
	q := &errorQueue{failOn: 1} // fail on first publish
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})

	providers := []contract.ProxyProvider{
		&fakeProvider{name: "ok", records: []contract.ProxyRecord{
			{Protocol: "http", IP: "1.2.3.4", Port: 8080},
		}},
	}

	published, err := publishDiscoveredProxies(context.Background(), q, repo, logger, providers, 0)
	// When publish fails, publishDiscoveredProxies reports all providers failed
	// because totalPublished == 0 and totalErrors > 0.
	if err == nil {
		t.Fatal("expected error when queue publish fails")
	}
	if published != 0 {
		t.Errorf("published = %d, want 0", published)
	}
}

func TestPublishDiscoveredProxies_SkipsExisting(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	// Seed the repo with http/1.2.3.4/8080.
	if _, _, err := repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "1.2.3.4", Port: 8080, Source: "seed",
	}); err != nil {
		t.Fatal(err)
	}
	// Reset counters after seeding so we only count the publish call.
	repo.ExistsCalls = 0

	q := &errorQueue{failOn: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	providers := []contract.ProxyProvider{
		&fakeProvider{name: "test", records: []contract.ProxyRecord{
			{Protocol: "http", IP: "1.2.3.4", Port: 8080},
		}},
	}

	published, err := publishDiscoveredProxies(context.Background(), q, repo, logger, providers, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if published != 0 {
		t.Errorf("published = %d, want 0", published)
	}
	if repo.ExistsCalls != 1 {
		t.Errorf("ExistsCalls = %d, want 1", repo.ExistsCalls)
	}
	if q.calls != 0 {
		t.Errorf("queue Publish calls = %d, want 0", q.calls)
	}
}

func TestPublishDiscoveredProxies_PublishesNew(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	// Seed with http/1.2.3.4/8080.
	if _, _, err := repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "1.2.3.4", Port: 8080, Source: "seed",
	}); err != nil {
		t.Fatal(err)
	}
	repo.ExistsCalls = 0

	q := &errorQueue{failOn: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Different protocol (socks5) from seeded (http) — distinct tuple.
	providers := []contract.ProxyProvider{
		&fakeProvider{name: "test", records: []contract.ProxyRecord{
			{Protocol: "socks5", IP: "1.2.3.4", Port: 8080},
		}},
	}

	published, err := publishDiscoveredProxies(context.Background(), q, repo, logger, providers, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if published != 1 {
		t.Errorf("published = %d, want 1", published)
	}
}

func TestPublishDiscoveredProxies_LookupFailureFailsClosed(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	repo.ExistsErr = errors.New("connection refused")

	q := &errorQueue{failOn: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	providers := []contract.ProxyProvider{
		&fakeProvider{name: "test", records: []contract.ProxyRecord{
			{Protocol: "http", IP: "1.2.3.4", Port: 8080},
		}},
	}

	published, err := publishDiscoveredProxies(context.Background(), q, repo, logger, providers, 0)
	if err == nil {
		t.Fatal("expected error when lookup fails")
	}
	if !strings.Contains(err.Error(), "proxy lookup failed") && !strings.Contains(err.Error(), "all providers failed") {
		t.Errorf("error %q does not contain expected substring", err.Error())
	}
	if published != 0 {
		t.Errorf("published = %d, want 0", published)
	}
}

func TestPublishDiscoveredProxies_AllExistingSucceeds(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	if _, _, err := repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "1.2.3.4", Port: 8080, Source: "seed",
	}); err != nil {
		t.Fatal(err)
	}
	repo.ExistsCalls = 0

	q := &errorQueue{failOn: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Provider has only existing tuples.
	providers := []contract.ProxyProvider{
		&fakeProvider{name: "test", records: []contract.ProxyRecord{
			{Protocol: "http", IP: "1.2.3.4", Port: 8080},
		}},
	}

	published, err := publishDiscoveredProxies(context.Background(), q, repo, logger, providers, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v, want nil (all-existing is a successful provider)", err)
	}
	if published != 0 {
		t.Errorf("published = %d, want 0", published)
	}
}

func TestPublishDiscoveredProxies_LimitCountsPublishedAfterDeduplication(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	// Seed one existing tuple — it must not consume the limit.
	if _, _, err := repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "1.2.3.4", Port: 8080, Source: "seed",
	}); err != nil {
		t.Fatal(err)
	}
	repo.ExistsCalls = 0

	q := &errorQueue{failOn: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Two providers: first has one existing + one new, second has two new.
	// Limit is 2 — only two successful publishes allowed.
	providers := []contract.ProxyProvider{
		&fakeProvider{name: "p1", records: []contract.ProxyRecord{
			{Protocol: "http", IP: "1.2.3.4", Port: 8080},  // existing — skipped, does not consume limit
			{Protocol: "http", IP: "10.0.0.1", Port: 3128}, // new — published, consumes 1
		}},
		&fakeProvider{name: "p2", records: []contract.ProxyRecord{
			{Protocol: "http", IP: "10.0.0.2", Port: 3128}, // new — published, consumes 2 → limit reached
			{Protocol: "http", IP: "10.0.0.3", Port: 3128}, // never fetched — limit already reached
		}},
	}

	published, err := publishDiscoveredProxies(context.Background(), q, repo, logger, providers, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if published != 2 {
		t.Errorf("published = %d, want 2", published)
	}
	if q.calls != 2 {
		t.Errorf("queue Publish calls = %d, want 2", q.calls)
	}
}

func TestPublishDiscoveredProxies_ZeroLimitIsUnlimited(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	q := &errorQueue{failOn: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	providers := []contract.ProxyProvider{
		&fakeProvider{name: "p1", records: []contract.ProxyRecord{
			{Protocol: "http", IP: "10.0.0.1", Port: 3128},
			{Protocol: "http", IP: "10.0.0.2", Port: 3128},
			{Protocol: "http", IP: "10.0.0.3", Port: 3128},
		}},
	}

	published, err := publishDiscoveredProxies(context.Background(), q, repo, logger, providers, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if published != 3 {
		t.Errorf("published = %d, want 3 (unlimited)", published)
	}
}

func TestPublishDiscoveredProxies_LimitStopsProviderEarly(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	q := &errorQueue{failOn: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Provider 1 has three new tuples; limit is 2.
	// Provider 1 must stop after emitting/publishing the second tuple.
	// Provider 2 must never be invoked.
	p1 := &fakeProvider{name: "p1", records: []contract.ProxyRecord{
		{Protocol: "http", IP: "10.0.0.1", Port: 3128},
		{Protocol: "http", IP: "10.0.0.2", Port: 3128},
		{Protocol: "http", IP: "10.0.0.3", Port: 3128}, // never reached
	}}
	p2 := &fakeProvider{name: "p2", records: []contract.ProxyRecord{
		{Protocol: "http", IP: "10.0.0.4", Port: 3128},
	}}

	published, err := publishDiscoveredProxies(context.Background(), q, repo, logger, []contract.ProxyProvider{p1, p2}, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if published != 2 {
		t.Errorf("published = %d, want 2", published)
	}
	if q.calls != 2 {
		t.Errorf("queue Publish calls = %d, want 2", q.calls)
	}
	if p1.fetchCalls != 1 {
		t.Errorf("p1 fetchCalls = %d, want 1", p1.fetchCalls)
	}
	if p1.emittedRecords != 2 {
		t.Errorf("p1 emittedRecords = %d, want 2 (stopped after second emission)", p1.emittedRecords)
	}
	if p2.fetchCalls != 0 {
		t.Errorf("p2 fetchCalls = %d, want 0 (should never be invoked)", p2.fetchCalls)
	}
}

func TestPublishDiscoveredProxies_LimitCrossProviderFallback(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	// Seed one existing tuple — it must not consume the limit.
	if _, _, err := repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "1.2.3.4", Port: 8080, Source: "seed",
	}); err != nil {
		t.Fatal(err)
	}
	repo.ExistsCalls = 0

	q := &errorQueue{failOn: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Provider 1: one existing + one new → exhausts below limit (published=1).
	// Provider 2: two new → first fills the cumulative limit (published=2), second never reached.
	p1 := &fakeProvider{name: "p1", records: []contract.ProxyRecord{
		{Protocol: "http", IP: "1.2.3.4", Port: 8080},  // existing — skipped, no quota consumed
		{Protocol: "http", IP: "10.0.0.1", Port: 3128}, // new — published, cumulative 1
	}}
	p2 := &fakeProvider{name: "p2", records: []contract.ProxyRecord{
		{Protocol: "http", IP: "10.0.0.2", Port: 3128}, // new — published, cumulative 2 → limit reached
		{Protocol: "http", IP: "10.0.0.3", Port: 3128}, // never reached
	}}

	published, err := publishDiscoveredProxies(context.Background(), q, repo, logger, []contract.ProxyProvider{p1, p2}, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if published != 2 {
		t.Errorf("published = %d, want 2", published)
	}
	if q.calls != 2 {
		t.Errorf("queue Publish calls = %d, want 2", q.calls)
	}
	if p1.fetchCalls != 1 {
		t.Errorf("p1 fetchCalls = %d, want 1", p1.fetchCalls)
	}
	if p1.emittedRecords != 2 {
		t.Errorf("p1 emittedRecords = %d, want 2 (exhausted all records)", p1.emittedRecords)
	}
	if p2.fetchCalls != 1 {
		t.Errorf("p2 fetchCalls = %d, want 1 (invoked after p1 exhausted below limit)", p2.fetchCalls)
	}
	if p2.emittedRecords != 1 {
		t.Errorf("p2 emittedRecords = %d, want 1 (stopped after first emission filled limit)", p2.emittedRecords)
	}
}

// storeOnlyProxyRepository satisfies ProxyRepository through the fake but
// hides the retirement store, as a repository adapter without that support
// would.
type storeOnlyProxyRepository struct{ contract.ProxyRepository }

func TestNewRetirementSweep_RequiresRetirementStore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rule := contract.RetirementRule{MinFailures: 10, MinAge: time.Hour}
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})

	sweep, err := newRetirementSweep(repo, rule, time.Hour, 100, logger)
	if err != nil || sweep == nil {
		t.Fatalf("newRetirementSweep = %v, %v; want a sweep for a repository that supports retirement", sweep, err)
	}
	if _, err := newRetirementSweep(storeOnlyProxyRepository{repo}, rule, time.Hour, 100, logger); err == nil {
		t.Fatal("a repository without a retirement store was accepted")
	}
}

func TestProxyRetireCmd_IsRegistered(t *testing.T) {
	found, _, err := proxyCmd.Find([]string{"retire"})
	if err != nil || found == nil || found.Name() != "retire" {
		t.Fatalf("proxy retire command missing: %v, %v", found, err)
	}
}

func TestProxyScanCmd_DeadScanPercentageFlagRemoved(t *testing.T) {
	if flag := proxyScanCmd.Flags().Lookup("dead-scan-percentage"); flag != nil {
		t.Fatalf("--dead-scan-percentage must be retired, got usage %q", flag.Usage)
	}
}
