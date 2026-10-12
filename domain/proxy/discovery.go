package proxy

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// DefaultDiscoveryChunk is how many listed records are buffered before they
// are inserted and marked as listed. It bounds memory for huge lists and keeps
// each database statement short.
const DefaultDiscoveryChunk = 2000

// maxDiscoveryPasses bounds how often one run re-reads a provider that used
// its whole share while budget was left over from the others.
const maxDiscoveryPasses = 3

// errShareReached stops a provider's stream once it used its share of the
// run's budget. It is a control signal, not a failure.
var errShareReached = errors.New("provider share of the discovery budget reached")

// Discoverer is the proxy discovery use case: it reads the providers' lists
// and registers the proxies the pool does not know yet, in bulk. It is
// insertion-only: discovery performs no health checks of its own. Every
// general check runs in the guarded, lease-bound scan worker
// (domain/proxy/worker), so newly discovered proxies carry no evidence until
// the background queue scans them.
type Discoverer struct {
	providers []contract.ProxyProvider
	repo      contract.ProxyRepository
	logger    contract.Logger
	chunk     int
	shuffle   func([]contract.ProxyProvider)
	cache     contract.ProxyListCache
	// known holds the list validators loaded for the current run, updated as
	// lists are read completely.
	known map[string]contract.ListValidators
}

// NewDiscoverer creates the discovery use case over the given providers.
func NewDiscoverer(providers []contract.ProxyProvider, repo contract.ProxyRepository, logger contract.Logger) *Discoverer {
	return &Discoverer{
		providers: providers,
		repo:      repo,
		logger:    logger,
		chunk:     DefaultDiscoveryChunk,
		shuffle: func(ps []contract.ProxyProvider) {
			rand.Shuffle(len(ps), func(i, j int) { ps[i], ps[j] = ps[j], ps[i] })
		},
	}
}

// WithChunkSize sets how many records are buffered per database statement.
func (d *Discoverer) WithChunkSize(n int) *Discoverer {
	if n > 0 {
		d.chunk = n
	}
	return d
}

// WithListCache lets providers that support it skip lists unchanged since the
// last run. Without a cache every list is read on every run.
func (d *Discoverer) WithListCache(cache contract.ProxyListCache) *Discoverer {
	d.cache = cache
	return d
}

// WithShuffle replaces the per-run provider ordering, which is random by
// default. Tests pass a no-op or a fixed permutation.
func (d *Discoverer) WithShuffle(fn func([]contract.ProxyProvider)) *Discoverer {
	if fn != nil {
		d.shuffle = fn
	}
	return d
}

// ProviderResult is what one provider contributed to a run.
type ProviderResult struct {
	Name string
	// Listed counts the usable records the provider listed.
	Listed int
	// Inserted counts the proxies registered from them.
	Inserted int
	// Capped is set when the provider was stopped because it used its share of
	// the budget, so its list may hold more new proxies.
	Capped bool
	// Err is set when the provider failed; its earlier chunks stay registered.
	Err      error
	Duration time.Duration
}

// DiscoveryResult summarises one discovery run.
type DiscoveryResult struct {
	// Inserted is how many new proxies the run registered.
	Inserted int
	// Providers lists one entry per provider read, in read order. A provider
	// read in several passes appears once per pass.
	Providers []ProviderResult
}

// Run reads the providers in random order and registers the new proxies.
//
// limit caps the proxies registered per run (0 means no cap) and is split
// fairly: each provider read gets ceil(remaining budget / providers still to
// read) as its share, so a provider that finds little leaves more for the
// providers after it. Providers that used their whole share are then read
// again with whatever budget is left, at most maxDiscoveryPasses times, so
// the budget is spent even when the small providers happen to come last.
//
// A failing provider never stops the others; Run fails only when every
// provider failed.
func (d *Discoverer) Run(ctx context.Context, limit int) (DiscoveryResult, error) {
	pending := append([]contract.ProxyProvider(nil), d.providers...)
	d.shuffle(pending)

	d.loadListValidators(ctx)

	var res DiscoveryResult
	failed := 0
	remaining := limit
	for pass := 1; pass <= maxDiscoveryPasses && len(pending) > 0; pass++ {
		var capped []contract.ProxyProvider
		for i, p := range pending {
			share := 0
			if limit > 0 {
				if remaining <= 0 {
					break
				}
				share = (remaining + len(pending) - i - 1) / (len(pending) - i)
			}
			pr := d.readProvider(ctx, p, share)
			res.Inserted += pr.Inserted
			remaining -= pr.Inserted
			res.Providers = append(res.Providers, pr)
			if pr.Err != nil {
				failed++
				d.logger.ErrorContext(ctx, "proxy.discover.provider_failed", "provider", pr.Name, "pass", pass, "listed", pr.Listed, "inserted", pr.Inserted, "share", share, "error", pr.Err, "duration", pr.Duration)
				continue
			}
			d.logger.InfoContext(ctx, "proxy.discover.provider_done", "provider", pr.Name, "pass", pass, "listed", pr.Listed, "inserted", pr.Inserted, "share", share, "capped", pr.Capped, "duration", pr.Duration)
			if pr.Capped {
				capped = append(capped, p)
			}
		}
		pending = capped
	}
	if failed > 0 && failed == len(d.providers) {
		return res, fmt.Errorf("proxy discovery failed: all %d providers failed", failed)
	}
	return res, nil
}

// readProvider streams one provider's list through chunked bulk inserts and
// stops once share new proxies are registered (share 0 means no cap).
func (d *Discoverer) readProvider(ctx context.Context, p contract.ProxyProvider, share int) ProviderResult {
	start := time.Now()
	pr := ProviderResult{Name: p.Name()}
	pending := make([]contract.ProxyRecord, 0, d.chunk)

	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		room := 0
		if share > 0 {
			room = share - pr.Inserted
		}
		inserted, err := d.repo.InsertNew(ctx, pending, room)
		if err != nil {
			return fmt.Errorf("insert new proxies: %w", err)
		}
		pr.Inserted += len(inserted)
		// Everything the provider listed, new or known, counts as listed
		// again: that is what revives a retired proxy. Rows stamped by the
		// insert above are skipped by the once-per-hour rule.
		endpoints := make([]contract.ProxyEndpoint, len(pending))
		for i, rec := range pending {
			endpoints[i] = contract.ProxyEndpoint{Protocol: rec.Protocol, IP: rec.IP, Port: rec.Port}
		}
		if _, err := d.repo.MarkListed(ctx, endpoints); err != nil {
			return fmt.Errorf("mark proxies listed: %w", err)
		}
		pending = pending[:0]
		if share > 0 && pr.Inserted >= share {
			return errShareReached
		}
		return nil
	}

	emit := func(rec contract.ProxyRecord) error {
		if rec.Protocol == "" || rec.IP == "" || rec.Port <= 0 || rec.Port > 65535 {
			return nil
		}
		pending = append(pending, rec)
		pr.Listed++
		if len(pending) >= d.chunk {
			return flush()
		}
		return nil
	}

	var err error
	if cp, ok := p.(contract.ConditionalProvider); ok && d.cache != nil {
		err = cp.FetchProxiesIfChanged(ctx, d.knownValidators, emit, func(url string, v contract.ListValidators) error {
			// The list was read to its last record: store what is pending,
			// and only then remember the version as ingested.
			if err := flush(); err != nil {
				return err
			}
			d.rememberList(ctx, p.Name(), url, v)
			return nil
		})
	} else {
		err = p.FetchProxies(ctx, emit)
	}
	if err == nil {
		err = flush()
	}
	if errors.Is(err, errShareReached) {
		pr.Capped = true
		err = nil
	}
	pr.Err = err
	pr.Duration = time.Since(start)
	return pr
}

// loadListValidators reads the stored list validators for this run. A cache
// that cannot be read only costs the skipping: every list is read.
func (d *Discoverer) loadListValidators(ctx context.Context) {
	d.known = map[string]contract.ListValidators{}
	if d.cache == nil {
		return
	}
	stored, err := d.cache.LoadListValidators(ctx)
	if err != nil {
		d.logger.WarnContext(ctx, "proxy.discover.list_cache_load_failed", "error", err)
		return
	}
	d.known = stored
}

func (d *Discoverer) knownValidators(url string) (contract.ListValidators, bool) {
	v, ok := d.known[url]
	return v, ok
}

// rememberList stores the validators of a list that was read completely and
// whose records are stored. A failed save only costs a re-read next run.
func (d *Discoverer) rememberList(ctx context.Context, provider, url string, v contract.ListValidators) {
	if v.IsZero() {
		return
	}
	if err := d.cache.SaveListValidators(ctx, url, v); err != nil {
		d.logger.WarnContext(ctx, "proxy.discover.list_cache_save_failed", "provider", provider, "url", url, "error", err)
		return
	}
	d.known[url] = v
}
