# Container Wiring

The container package centralises dependency construction so business code stays declarative. At startup (`main.go`), a `ServiceContainer` is created and stored in `container.Load`. Subsequent calls fetch configuration, infrastructure singletons, or domain services.

```
serviceContainer := container.NewServiceContainer()
container.Load = serviceContainer
```

## Structure

- `infrastructure.go` — lazily instantiates outbound adapters such as the PostgreSQL driver, queue, StatsD, outbound HTTP clients, LodestoneClient, ProviderRateLimiter, ProxyRepository, ProxyHub, and `UIStatsRepository` behind `port/contract` interfaces.
- `infrastructure/postgres` — hosts the PostgreSQL driver and embedded goose migrations.
- `domain.go` — constructs domain services (added as features are built).
- `main.go` — fetches the embedded config and exposes helper methods.

## Usage Tips

1. Always request dependencies through container accessors (e.g., `container.Load.Database()`, `container.Load.Queue()`, `container.Load.LodestoneClient()`, `container.Load.ProxyHub()`).
2. Keep constructors pure; inject interfaces from `port/contract`.
3. Infrastructure accessors are lazy singletons, so the first call constructs the adapter and subsequent calls reuse it.
4. When adding a new service, update `DomainContainer` and document it here.

`container.Load.ResultWriter()` returns the per-process batching writer (a lazy singleton built from `[writer]` config over `CharacterRepository()`), or nil when the database is unavailable. `Handlers()` and `ProxyCensusHandlers()` register the ID sweep, character census and achievement census handlers with `.WithWriter(s.ResultWriter())`; a handler without a writer fails its jobs with `handler.ErrNoWriter`. `consume` closes the writer after the consumer has stopped, which flushes pending results. See [docs/queue.md](queue.md#batched-writes-and-acks).

Aggregate routes resolve `container.Load.UIStatsService()`. Its repository is a lazy singleton from `UIStatsRepository()`, and its in-process cache settings come from `[census.ui_stats]`. The service is shared by UI controllers, aggregate REST handlers, and the `refresh ui-stats` command.

## Removed Accessors

Previous versions used MySQL, then SQLite. The project now runs on PostgreSQL. The container exposes both `Postgres()` and a `SQLite()` compatibility alias that delegates to `Database()`. The PostgreSQL driver handles migrations automatically on first access — see [docs/external-postgres.md](external-postgres.md) for details.

**Proxy accessors:**
- `ProxyRepository()` — proxy persistence layer; the returned adapter serves both `contract.ProxyRepository` and the scan-store contract (`contract.ProxyScanStore`) used by the scanner
- `ProxyHub()` — creates a proxy acquisition hub from `[proxy.consumer].lock_ttl`, the destination cooldown floor, `[proxy.consumer].lock_refresh_interval` and the shared scan policy. The owner is constructed by the CLI command (e.g. `census-consume-<hostname>-p<pid>-w<workerID>`) and passed to `RunEventsWithProxy`, not to the hub accessor
- `ProxyCensusHandlers(lodestone, rateLimiter)` — handler registry wired to a proxy-aware Lodestone client. Used by `consume --proxy` — each goroutine creates its own handlers with its own proxy-aware client
- `ProxyScrapeProvider()` / `GeonodeProvider()` — proxy discovery providers
- `ProxyChecker()` — general availability checker: proxied GET against the configured health target (`[proxy].test_url`, default The Lodestone) requiring HTTP 200 and a complete non-empty body; used by the scan worker for every general check, so an active proxy is proven usable for Lodestone traffic
- `ProxyScanPolicy()` / `ProxyScanWeights()` — the single parsed scheduler policy and queue capacity shares shared by the repository and the scan worker (the policy carries the `RetirementRule`)
- `ProxyRetirementRule()` / `ProxyRetirementSweepSettings()` — the `[proxy.retire]` thresholds and the sweep cadence used by `proxy scan` and `proxy retire`
- `DiscoveryHTTPClient()` — rotating-proxy HTTP client for public proxy-list providers. Falls back to the direct client when no active proxy exists. Must not be used for Lodestone API traffic
