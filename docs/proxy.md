# Proxy Pool

The proxy pool discovers, validates, and maintains a database of proxies that census workers use to rotate outbound IPs when hitting The Lodestone, reducing rate-limit pressure. Availability is decided by **fresh, persisted evidence** from a lease-based scanner, never by a stored status column or a wall-clock deadline computed in the application.

## Architecture

The proxy feature is a separate bounded context (`domain/proxy/`) with its own CLI commands, worker, and store. It follows the same hexagonal architecture as the census pipeline.

| Layer | Path | Purpose |
|-------|------|---------|
| Port contracts | `port/contract/proxy.go` | `ProxyProvider`, `ProxyRepository`, `ProxyRecord` (scheduling columns) |
| Port contracts | `port/contract/proxy_scan.go` | `ProxyScanStore`, `ProxyScanPolicy`, `ScanLease`, `ScanUpdate`, `ProxyChecker` typed errors, `ProxyEndpointGuard` |
| Domain service | `domain/proxy/service.go` | New-proxy ingestion |
| Domain objects | `domain/proxy/proxy.go` | `Proxy` — consumer lifecycle over `RefreshConsumerLock`, fenced `MarkFailed`, `CooldownDestination`, `ResetDestinationFailures` |
| Domain cooldown | `domain/proxy/cooldown.go` | `DestinationBackoff` — floor doubled per consecutive failure, capped, never below `Retry-After` |
| Domain scheduling | `domain/proxy/scheduling.go` | `ClassifyCheck`, `MakeScanUpdate`, `MakeConsumerFailureUpdate`, `RecoveryDelay` |
| Domain hub | `domain/proxy/hub.go` | `ProxyHub` — freshness-validated, owner-locked handout (`NewProxy`), random discovery selection |
| Domain scanner | `domain/proxy/worker/scan.go` | Single-dispatcher lease-based scan worker |
| Domain admission | `domain/proxy/worker/admission.go` | Persistent round-robin admission state |
| Domain consumer | `domain/proxy/worker/worker.go` | `new-proxy` event consumer |
| Infrastructure | `infrastructure/proxy/health.go` | General checker: complete-response GET through the proxy to the health target (default The Lodestone) |
| Infrastructure | `infrastructure/proxy/guard.go` | `EndpointGuard` — per-replica egress circuit breaker |
| Infrastructure | `infrastructure/proxyscrape/`, `infrastructure/geonode/`, … | Discovery providers (streaming) |
| Infrastructure | `infrastructure/httpclient/proxy_client.go` | Proxy transport construction (HTTP, HTTPS, SOCKS4, SOCKS5), cancellation-safe |
| Infrastructure | `infrastructure/postgres/repository/proxy.go`, `proxy_scan.go` | One adapter serving both `ProxyRepository` and `ProxyScanStore` |
| Mock | `mock/repository/proxy.go`, `mock/proxy/provider.go` | In-memory fakes for tests |

## CLI

```bash
# Discover proxies from configured providers, publish new-proxy events
./bin/ffxiv-census proxy discover [--limit 0]   # 0 = unlimited

# Run a long-running guarded, lease-based scan worker
./bin/ffxiv-census proxy scan -c 150            # -c = total per-replica check concurrency

# Consume new-proxy events (long-running)
./bin/ffxiv-census proxy consume -c 30
```

`proxy scan` takes no scheduling flags other than `-c`; invalid nonpositive explicit values fail startup. In Kubernetes `proxy-scan` runs as a Deployment with 5 replicas × `-c 150`; `proxy-discover` runs as a CronJob every two minutes.

## Scan pipeline

The scan worker leases due rows from three queues and runs each check inside a bounded executor. One dispatcher goroutine owns every scheduling counter, so no helper goroutine picks work on its own.

### Queues

| Queue | Eligible rows | Order | Deadline honored |
|-------|---------------|-------|------------------|
| verification | `general_healthy` | `next_attempt_at` (oldest first) | yes |
| recovery | not healthy, completed at least once | `next_attempt_at` (oldest first) | yes |
| background | not healthy | `last_completed_at` (oldest/NULL first) | no — ignores the recovery deadline, honors the common cooldown |

Long-run capacity shares default to 10/45/45 (`weight_verification`, `weight_recovery`, `weight_background`; must sum to 100). Reservations are filled verification-first; remaining free capacity is lent by round-robin borrowing. Below three slots a persistent small-worker round robin owns every admission. An empty queue never blocks the others: completions trigger immediate re-admission and a 1 s poll timer guarantees progress while nothing completes.

### Leasing and fencing

`ClaimScans` runs in one short transaction: eligible rows are locked with `FOR UPDATE SKIP LOCKED` and stamped with a fresh 128-bit hex token, the claimed `observation_version`, and a lease expiry computed from database time. Claims are bounded by free capacity and a claim timeout of half the lease (lease default 30 s). A crashed process therefore holds rows only until its lease expires; expired rows are reclaimed lazily by the next eligible claim — there is no reaper, and leftover expired-lease rows are normal. `ReleaseScan` matches the captured claim version so an old process cannot clear a lease the row no longer carries.

`CompleteScan` persists one observation only if the lease token, the captured claim version, and an unexpired lease (measured against the post-lock database timestamp) all still match. A failed CAS returns `(false, nil)` and leaves every evidence column untouched; a persistence error returns an error, engages a brief exponential claim pause (100 ms doubling to 1 s ceiling), and never fabricates a completed attempt.

### Checks and classification

The general checker (`infrastructure/proxy/health.go`) performs one GET of the configured health target through the proxy — by default The Lodestone, so a passing check proves the proxy is usable for real census traffic, not merely alive. Success requires HTTP 200 plus a complete, bounded (≤512 KiB), non-empty body; body shape is the target's own concern. Every check runs under the check timeout (default 10 s); there is no direct fallback.

Outcomes are typed (`ProxyCheckKind`: `local`, `proxy`, `target`, `deadline`, `cancelled`) and decisions use `errors.As`/`errors.Is`, never error text:

- nil error → **success**
- caller cancellation or untyped errors → **inconclusive** (fail closed)
- proven proxy dial/negotiation failure → **failure**, only while the guard was healthy and unchanged across the attempt
- deadline failures → **failure** under the same guard condition, otherwise **inconclusive**
- target errors (HTTP status, payload, target TLS) → always **inconclusive**

The dispatcher re-reads the guard immediately before persistence: a guard generation change anywhere between the attempt's start snapshot and that recheck fences conclusive failure attribution into an inconclusive result. Inconclusive attempts never increment failure history; they reschedule no sooner than `inconclusive_retry` (default 60 s) and preserve the recovery step.

### Transitions

- **success** — `general_healthy = true`, latency recorded, next verification scheduled at `verification_interval` (default 60 s), recovery sequence reset.
- **failure** — `general_healthy = false`, recovery step advances (delay doubles from `recovery_base` 1 m to `recovery_cap` 15 m, bounded by `recovery_horizon` 1 h), the common repeat position moves by `min_repeat_interval` only so recovery backoff never widens the background cooldown. The historical `status` column still dead-ends at `fail_count_threshold` (5) or `dead_threshold_days` (48 h since last success, first-seen fallback); it is bookkeeping, not availability.
- **inconclusive** — no history change; retried after the floor.

Shutdown releases leases it still owns on bounded fresh contexts after a ≤5 s drain of in-flight results; anything unreleased expires and is reclaimed lazily.

### Endpoint guard

Each scan replica owns an `EndpointGuard` observing its own egress with a bounded direct request to the same health target (`control_interval`, default 30 s, ±10 % jitter, serial controls, 10 s control budget). A failed control pauses claiming until a later success; the generation counter increments on each healthy→unhealthy transition and is never reset. A healthy control never mutates proxy state. The guard gates claiming only — in-flight checks always run to completion and are fenced by generation as described above.

### Indexes

All `proxies` indexes beyond the primary key and the `(protocol, ip, port)` unique constraint are partial:

| Index | Columns | Predicate | Serves |
|-------|---------|-----------|--------|
| `idx_proxies_verification_due` | `(next_attempt_at, id)` | `general_healthy` | verification queue |
| `idx_proxies_recovery_due` | `(next_attempt_at, id)` | `NOT general_healthy AND last_verified_at IS NOT NULL` | recovery queue (older shape) |
| `idx_proxies_recovery_claim` | `(next_attempt_at NULLS FIRST, id) INCLUDE (last_verified_at, scan_lease_until, scan_not_before)` | `NOT general_healthy AND last_verified_at IS NOT NULL` | recovery queue, deadline order |
| `idx_proxies_recovery_recent` | `(last_verified_at)` | `NOT general_healthy AND last_verified_at IS NOT NULL` | recovery queue, range of rows verified within the horizon |
| `idx_proxies_background_completed` | `(last_completed_at NULLS FIRST, id)` | `NOT general_healthy` | background queue |
| `idx_proxies_fresh_verified` | `(last_verified_at)` | `general_healthy` | freshness reads (`ListActive`, `ClaimProxy`, `RandomActive`, `CountByStatus`) |

Migration `00020` records the two recovery indexes created by hand on production on 2026-10-12 (`IF NOT EXISTS`, so production is unaffected) and drops `idx_proxies_available` and `idx_proxies_scan_priority`. No query filters on `status`, `locked_at` or `last_scanned_at` with an index in mind, and `locked_at`/`status` change on every consumer claim, refresh and release, so keeping them indexed forced non-HOT updates.

The recovery claim inlines the recovery horizon as a SQL literal instead of a bind parameter. With a bind parameter, PostgreSQL's cached generic plan cannot estimate the horizon's selectivity and scans `idx_proxies_recovery_claim` in deadline order, discarding every long-unverified row; with the literal it takes the small `idx_proxies_recovery_recent` range and sorts it.

## Fresh availability (general vs destination health)

All consumer-facing reads share one general-health predicate:

```sql
general_healthy AND last_verified_at > <database time> - freshness_ttl
```

`freshness_ttl` defaults to 120 s and must exceed `verification_interval + test_timeout` (validated at startup). The predicate gates `ListActive`, `CountByStatus`, `ClaimProxy`, `RefreshConsumerLock`, and `RandomActive`:

- `active` = the fresh predicate holds; a historically active but non-fresh row counts as `stale`; historical `inactive`/`dead` categories are preserved.
- `ClaimProxy` and `RefreshConsumerLock` additionally require an expired/absent destination cooldown and an unlocked (or TTL-expired) row.
- `RandomActive` applies the fresh predicate and ID exclusions but no destination-cooldown filter — cooldowns gate consumers, not provider discovery.
- There is no stale fallback: a non-fresh proxy is never handed to a consumer.

When every scanner stops, fresh lists, counts, and acquisitions decay to zero within `freshness_ttl` of each row's last verification — measured at exactly this staggered 120 s boundary in the local exercise.

**Destination cooldowns** are independent: a consumer failure at the destination (Lodestone challenge, timeout, 5xx or 429) raises `destination_cooldown_until` to at least `database now + delay` via `GREATEST`, increments `destination_failures`, and releases only the caller's own lock, all in one statement. General evidence and the scan observation version are untouched, so such a failure never marks a proxy unhealthy and never blocks scanner claims or provider discovery.

The delay grows with the proxy's consecutive failures: `max(Retry-After, min(cooldown_max, cooldown * 2^destination_failures))`, where `destination_failures` is the count before the new failure. With the defaults a proxy that keeps failing is cooled for 1, 2, 4, 8, then 15 minutes. A `Retry-After` hint longer than the cap is honored. The streak is cleared by the first successful job on the proxy (`ResetDestinationFailures`); it is never reset by time alone.

Persistence: `destination_failures integer NOT NULL DEFAULT 0` on `proxies` (migration `00023`). A column next to `destination_cooldown_until` lets the failure write set the cooldown, bump the counter and release the lock in the same `UPDATE`, and the claim and lock-refresh queries already return the row, so the consumer reads the streak without an extra query. The column is not indexed, so the write stays HOT. A success costs a write only when the streak is non-zero (`WHERE destination_failures > 0`); healthy proxies write nothing extra per job.

`ClaimProxy` skips a row while its cooldown is live, and orders by `destination_failures ASC` before uptime, latency and fail count. A proxy that just returned from a cooldown therefore ranks behind every clean proxy: the best-looking proxies do not come back to the top the moment 60 s are over while they keep failing.

## Consumer integration (`consume --proxy`)

`ProxyHub.NewProxy(ctx, owner)` hands out a proxy after two steps, and returns `nil` (caller retries with backoff) when either fails:

1. `ClaimProxy` — fresh, uncooldowned, unlocked, protocol-supported row (`FOR UPDATE SKIP LOCKED`), ordered by destination failures, uptime, latency, failure count.
2. `RefreshConsumerLock` — revalidates freshness, ownership, lock validity and cooldown, extends the lock, and returns the record with the **live observation version** the consumer must present later.

A hand-out sends no request to The Lodestone (freshness-only handout). Scan freshness, general health verified within `freshness_ttl` (120 s), is trusted, and the **first real request of the job is the destination check**. A proxy The Lodestone rejects there (challenge, 429, timeout, 5xx) or that fails to dial is handled by the outcome table below: released and put on the growing destination cooldown (or failed conclusively on a dial failure), and the job retries in-process on another proxy, except after a 429, where the delivery returns to the queue. A dead or blocked proxy therefore costs one failed real request instead of an extra full Lodestone page fetch and tunnel on every hand-out.

Every goroutine owns exactly one proxy (`census-consume-<host>-p<pid>-w<workerID>`). Per job:

- `Proxy.CanUse` revalidates through `RefreshConsumerLock` — the historical local-status shortcut is gone; a lock lost or a freshness lapse is detected against the database. The refresh is throttled: a worker revalidates and extends the lock of its held proxy at most once per `proxy.consumer.lock_refresh_interval` (default 100 s), not on every job, because refreshing on every job was about 95% of the consumer-side writes on the proxies table. A lost lock, a lapsed freshness or a destination cooldown is therefore noticed within that interval, and a newly acquired proxy counts as refreshed by its hand-out (`NewProxy` ends with `RefreshConsumerLock`), so the first job does not refresh again. A proxy-caused failure (dial failure, destination rejection or timeout) forces a refresh before the next job. The interval must stay below half of `lock_ttl` (checked at startup), so the lock always has at least half its lifetime left when the next refresh is due.
- A typed conclusive proxy failure is persisted by `Proxy.MarkFailed` → `RecordConsumerFailure` under a CAS on the version captured **before** the job's I/O, the owner, and a still-valid lock; a stale observation is rejected and only its own lock is released. The failure transition mirrors the scanner's (recovery delay, common-cooldown move, dead thresholds) and invalidates any older scan lease.
- A failed job attempt is classified from the typed `*contract.ProxyCheckError` (never from error text) and retried in-process on a different proxy, up to `proxy.consumer.job_attempts` proxies per job (default 3, first attempt included). When the attempts are exhausted the error is returned and the job takes the normal queue error path. The table lists what each outcome does:

| Outcome (typed as) | Proxy write | In-process retry on another proxy |
|---|---|---|
| Proxy dial/negotiation failure (`CheckProxy`) | fenced general failure (`MarkFailed`), claim released | yes |
| Challenge: 202 or 403 Cloudflare interstitial (`CheckTarget`, `Challenge`) | destination cooldown (grows), claim released | yes |
| Response timeout (`CheckDeadline`, client timeout through an established tunnel) | destination cooldown (grows), claim released | yes |
| Server error, HTTP 5xx (`CheckTarget`, `Status >= 500`) | destination cooldown (grows), claim released | yes |
| 429 (`CheckTarget`, no challenge, `Status` 429) | destination cooldown (grows, at least `Retry-After`), claim released | no: the provider-wide pause applies, the delivery returns to the queue |
| Genuine 404, private achievements (403 without Cloudflare markers), parse or business error, cancellation | none | no |

A timeout the caller's own context imposed is not typed as `CheckDeadline`, so shutdown never cools a proxy. Each in-process attempt reports exactly one request outcome to the Lodestone observer, on the client of the proxy it used.
- Local errors and cancellation release without evidence writes. A database error while persisting a check outcome or revalidating the lock also releases the claim before the error propagates, so a failed acquisition never leaves a proxy locked until the TTL.
- Swapping proxies closes the replaced client's idle connections (`CustomClient.CloseIdleConnections`); the live client does the same when the worker exits.
- Replacement claims are acquired before the previous lock is released (release-before-reacquire at cleanup, no hold-and-wait).

`NotifyAvailable` (wired as the scan worker's success notifier) wakes waiting consumers as soon as a scanner accepts a success.

`RandomActive`/`SwapActive` remain for the discovery pipeline only: unlocked, unclaimed random selection over the fresh pool. They must not be used for Lodestone APIs.

### Removed interfaces

The rewrite removed, with all callers and fakes migrated: `UpdateStatus`, `UpdateScanTime`, `ListForScan`, `ListDeadForScan`, `MarkFailedProxy`, the `ExtendLock` port method (superseded by `RefreshConsumerLock`), the old narrow `Scanner`, split-pool helpers, `dead_scan_interval_days` configuration, and the `--dead-scan-percentage` CLI flag. `cmd/http` intentionally exposes no live proxy view.

## Configuration

```toml
[proxy]
test_url             = "https://na.finalfantasyxiv.com/lodestone/"  # general health target: proxies must prove Lodestone works
test_timeout         = "10s"                                  # per-check budget (also the stall deadline)
dead_threshold_days  = 2                                      # historical dead classification
fail_count_threshold = 5                                      # historical dead classification

[proxy.scan]
verification_interval = "60s"   # healthy-row re-verification cadence
freshness_ttl         = "120s"  # general-freshness window (> verification_interval + test_timeout)
recovery_base         = "1m"    # first failure retry delay
recovery_cap          = "15m"   # saturated failure retry delay
recovery_horizon      = "1h"    # bound on the stored recovery progression
weight_verification   = 10      # capacity shares (must sum to 100)
weight_recovery       = 45
weight_background     = 45
lease_duration        = "30s"   # scan lease (> test_timeout)
min_repeat_interval   = "1s"    # common minimum-repeat cooldown (scan_not_before)
inconclusive_retry    = "60s"   # floor for retrying inconclusive attempts
control_interval      = "30s"   # guard control cadence

[proxy.consumer]
lock_ttl             = "5m"                                    # consumer lock TTL
lodestone_rate_limit = 1.0                                     # req/s override in proxy mode
request_timeout      = "30s"                                   # proxy-aware client timeout
cooldown             = "60s"                                   # destination cooldown floor (first failure)
cooldown_max         = "15m"                                   # ceiling of the per-failure doubling
job_attempts         = 3                                       # proxies one job may try in-process
lock_refresh_interval = "100s"                                 # max one lock refresh per held proxy (< lock_ttl / 2)
```

`request_timeout` (30s) bounds how long one consumer HTTP attempt may hang on a stalled proxy. It stays deliberately more generous than the scanner's `test_timeout` (10s): a slow-but-alive proxy that answers a character page in 10–25s still completes, and failures fall through to queue retries rather than wasting an otherwise working identity.

Every field overrides through an environment variable named by upper-casing the key path with `.` → `_` (viper `AutomaticEnv`): `PROXY_TEST_URL`, `PROXY_SCAN_LEASE_DURATION`, `PROXY_SCAN_WEIGHT_BACKGROUND`, `PROXY_CONSUMER_LOCK_TTL`, and so on. Startup validation fails closed on impossible combinations (freshness vs verification+timeout, lease vs timeout, weights sum, nonpositive durations, `lock_refresh_interval` not below half of `lock_ttl`).

## Kubernetes deployment

```yaml
- name: proxy-scan
  replicaCount: 5
  command: [/app/ffxiv-census, proxy, scan, -c, "150"]
  env:
    - name: POSTGRES_MAX_OPEN_CONNS   # instance env replaces chart defaults
      value: "2"
    - name: POSTGRES_MAX_IDLE_CONNS
      value: "1"
  resources:
    requests: { memory: 100Mi, cpu: 500m }
    limits:   { memory: 1Gi,  cpu: 1000m }
```

**Connection budgeting is bounded by design**: each scan replica opens at most 2 database connections (1 idle), so 5 replicas contribute at most 10 backends regardless of the 750-slot check capacity. Consumer pods (`proxy-id-sweep`, `proxy-character-census`, `proxy-achievement-census`, `proxy-new`) use 3 open / 2 idle connections: one for the batching writer flush and two for the short proxy claim, lock-refresh and failure statements. Instance `env` lists replace the chart defaults; the required queue entries (`QUEUE_MAX_ATTEMPTS`, `RABBITMQ_URL`) stay in the list even though scanning bypasses RabbitMQ.

## Observability

The scanner logs one structured event per persisted attempt on the existing `slog` logger — only `accepted=true` observations are evidence, and only they count toward throughput:

```json
{"msg":"proxy_scan.completion","proxy_id":42,"queue":"verification","outcome":"success",
 "reason":"","duration":105000000,"accepted":true,"recovery_step":0,
 "previously_healthy":true,"verify_late":1000000000}
```

`queue` ∈ verification/recovery/background; `outcome` ∈ success/failure/inconclusive; `reason` is the bounded typed label; `verify_late` (when present) is how far past `next_attempt_at` the check landed. Further events: `proxy_scan.claim` (debug; queue, requested/claimed, duration), `proxy_scan.claim_error`, `proxy_scan.complete_error`, `proxy_scan.release_error`, `proxy_scan.shutdown_incomplete`, `proxy_scan.panic_recovered`, and the guard transitions `endpoint control failed; pausing scan claims` / `endpoint control succeeded; scan claims resume` (with generation). The consumer side logs `worker.proxy_acquired`, `worker.proxy_bad`, `worker.proxy_reacquired`, `worker.proxy_waiting`, and `worker.job_retry`.

Experiment/operations SQL over the same database (no separate telemetry subsystem): health totals via `CountByStatus`; oldest completed background age = `min(last_completed_at)` over not-healthy completed rows; unknown-history count = rows with `last_completed_at IS NULL`; overdue verification count = healthy rows with `next_attempt_at < now() - grace`. Distinguish locally logged attempts from accepted persisted observations by filtering `accepted=true`.

## Measured local exercise (2026-09-09)

Throwaway fixtures on one Apple M1 host: local TLS IP-echo target, HTTP CONNECT / HTTPS CONNECT / SOCKS4 / SOCKS5 proxies (100 ms forwarding delay), immediate-refusal and 10 s-stall populations; 10 000 seeded rows per run (6000 delayed-success, 3900 refusal, 100 stall); `verification_interval` 10 s, all other defaults; per-process `-c 30`; identical policy and measurement method per run; scanners ran as Linux containers, one run per dataset reset. Accepted completions counted from `accepted=true` log events only. Printed rates divide accepted completions by the full per-run wall-clock span from scanner start to shutdown completion (168 s, 171 s and 172 s respectively), which includes the startup ramp and the graceful-shutdown drain tail; the periodic CPU, connection and progress samples were taken inside the 150 s steady window and do not set the denominator.

| Scanners (-c 30 each) | Accepted completions/s | Verify lateness avg/p95 | Postgres container CPU | Scanner CPU each |
|---|---|---|---|---|
| 1 | 130.4 | 54.1 s / 98.9 s | 16.9 % | 28.7 % |
| 2 | 244.3 | 39.2 s / 53.5 s | 40.6 % | 27–31 % |
| 5 | 445.7 | 18.5 s / 22.8 s | 120.4 % | 27–30 % |

Every run: zero fencing discards, zero claim/persistence errors. Throughput scaled 1.9× at two replicas and 3.4× at five while the single-container PostgreSQL instance became the binding resource (120 % CPU) — the honest ceiling in this setup, not scanner capacity (scanners stayed near 30 % CPU each with headroom on the host). No queue starved: the background sweep drained completely in each run (≤35 s at five replicas), the recovery population kept cycling, and the verification backlog shrank monotonically with replica count. Database connections matched the budget: one steady-state backend per replica (≤10 total at five replicas, sampled via `pg_stat_activity`).

Kill test: one of three replicas was `SIGKILL`ed holding 26 in-flight leases. All 26 rows were reclaimed lazily by the two survivors after the 30 s lease expiry and completed successfully; a captured pre-kill lease token replayed through `CompleteScan` was rejected (`accepted=false`, no error) by the version/token fence.

Smoke: valid bodies activated rows on all four protocols (~105 ms latency recorded); a malformed target body caused the guard to pause claiming with no failure history written (inconclusive, fail closed); refusal proxies failed conclusively in ~1.4 ms; stalls ended at the 10 s deadline (10.00–10.01 s observed); stopping all scanners decayed fresh lists, counts and acquisitions to zero across a single `freshness_ttl` window (staggered per row); hub handout then required claim + destination check + freshness revalidation (the destination check was later removed from hand-out: the first real request of the job is the check); three destination 429s raised only destination cooldowns (Retry-After honored) while all 20 rows' general health and verification timestamps stayed intact, and no proxy was handed out while the destination rejected.

## Coordinated rollout procedure

The scanner rewrite changes evidence columns, predicates, and consumer fencing; old and new binaries must not run against the same proxies concurrently. Roll out as a stop-the-world cutover, executed only with user authorization (the README release workflow governs image build and tag steps):

1. Stop the old proxy writers and consumers: `proxy-scan`, `proxy-new`, all `--proxy` census consumers, and `proxy-discover` CronJobs. Old live readers that serve proxy data (currently none — `cmd/http` exposes no proxy view) stop with them.
2. Migrate the schema with the new binary (`00017_proxy_scan_scheduling.sql` is embedded and self-applies on first database use). The migration adds columns and indexes; it does not backfill success — every row starts with `general_healthy = false` and will be proven fresh only by a new scan.
3. Start the new `proxy-scan` replicas. Watch `proxy_scan.completion` events, guard transitions, and `CountByStatus` until the fresh `active` count reflects real, recent evidence.
4. Start the new consumers (`proxy-new`, `--proxy` census consumers) and any proxy readers. Consumers must be the new binary: a pre-rewrite consumer cannot present an observation version and will have its failure writes fenced off.

There is no rolling mixed-binary cutover, no automatic destructive rollback (roll forward by redeploying the previous release the same way), and no synthetic backfill of availability.

## Discovery

`proxy discover` streams provider responses directly to RabbitMQ (bounded memory), deduplicates each tuple with a read-only `Exists` check before publication, and treats `--limit` as a global publication cap. Providers run sequentially in configured order; a provider lookup error fails that provider closed without publishing. The rotating discovery HTTP client uses `RandomActive` (fresh pool, ID exclusions) and swaps on 403/429/5xx or transport failure without marking proxies — provider blocks say nothing about general health.
