# Event Model & Ingest Pipeline

The census ingests Lodestone data through a durable, event-driven pipeline. Publishers enqueue jobs into the RabbitMQ-backed queue (`docs/queue.md`); consumers receive jobs via push-based consumption, run a handler, and chain downstream events. See the design spec (`docs/superpowers/specs/2026-08-16-lodestone-census-design.md`) for the full picture.

## Events

| Event | Purpose | Status |
|---|---|---|
| `id-sweep` | Probe a range of character IDs; ingest any that exist | ✅ implemented |
| `character-census` | Re-census a known character's profile + jobs | ✅ implemented |
| `achievement-census` | Refresh the globally latest achievement and incrementally fetch tracked milestones | ✅ implemented |
| `new-proxy` | Register and test a newly discovered proxy | ✅ implemented |

Recurring proxy scans are performed directly by the `proxy scan` database worker, not via queue events. See `docs/proxy.md`.

## Payloads

Queue job payloads are JSON. Types are declared in `domain/census/handler/event.go`.

- **id-sweep**: `{"from": 1, "to": 1000, "source": "auto"}` — inclusive range of character IDs to probe. `source` is `"auto"` or `"lodestone"` (both probe The Lodestone); the legacy value `"tomestone"` is tolerated on in-flight messages and behaves as `"auto"`.
- **character-census**: `{"character_id": 123}` — a character to re-census.
- **achievement-census**: `{"character_id": 123}` — a character to run an achievement census on.
- **new-proxy**: `{"protocol": "http", "ip": "1.2.3.4", "port": 8080, "country": "US", "anonymity": "elite", "source": "proxyscrape", "uptime_percent": 95.5}` — a proxy to register and test.

Proxy event types are declared in `domain/proxy/handler/handler.go`. The `new-proxy` event is a leaf event (no chaining).

## Chaining

Handlers return the downstream jobs they want published; the worker publishes each one individually via `queue.Publish(ctx, job)`. The queue's push-based `Consume` method handles retry and failure internally — no explicit `Complete`, `Retry`, or `Fail` calls.

- `id-sweep` → `achievement-census`
- `character-census` → `achievement-census`
- `achievement-census` → (leaf)

## Loop safety

Handlers are idempotent (`CharacterRepository.StoreResult` is a conflict-upsert), so a retried `id-sweep` chunk re-probes safely without duplicating chained jobs. RabbitMQ does not deduplicate at the queue level — idempotency is enforced by the database layer. The `proxy discover` command checks PostgreSQL read-only before publishing each `new-proxy` event; existing tuples are counted as `skipped_existing` and never published. Lookup errors fail closed per provider. The consumer independently checks then inserts conflict-safely via `INSERT ... ON CONFLICT DO NOTHING`.

## Ingest behavior

**`id-sweep` (character discovery):**
1. Handlers probe **The Lodestone** — the sole data source and the authority for
   existence for both the proxy and direct workflows.
2. A genuine Lodestone 404 means the ID has no character: it is skipped without
   failing the chunk.
3. Any transient error (HTTP 202 challenge, timeout, 5xx, dial failure) fails
   the delivery so the queue can retry it later — silently skipping here would
   lose characters.

**`character-census` (profile re-census):**
1. Handlers query **The Lodestone** as the sole source of truth.
2. A genuine Lodestone 404 marks the character deleted immediately — no second
   provider is consulted for confirmation.
3. A transient error fails the delivery for retry with backoff.

**Shared rules:**
- Downstream `achievement-census` jobs are uniformly chained using
  `BuildDependentCharacterJobs` after a successful store.
- **Worker Rate-Limit Coordination**: When Lodestone encounters HTTP 429 or is
  paused in the `ProviderRateLimiter`, workers wait out the cooldown before the
  next attempt — deliveries are never dropped and never burned against the
  database while paused.

### Rate-Limit Handling

Achievement checks always request the achievement list once to refresh global latest activity and detect HTTP 403 privacy. They then request only missing milestones in chain order and stop at the first public unearned detail result; already stored checkpoints are not rechecked. A complete tracked history makes one list request and no detail requests.

`achievement-census` waits for the Lodestone cooldown in-handler: when Lodestone is rate-limited or paused, the handler calls `WaitUntilAvailable(ctx, ProviderLodestone)` to block until the cooldown expires, then retries the fetch inline. This is more efficient than returning an error immediately (which triggers queue retry + backoff).

`WaitUntilAvailable` blocks exactly once per rate-limit cooldown. If the subsequent fetch fails again (new 429), the handler returns an error and the queue's exponential backoff kicks in. The handler does NOT loop on `WaitUntilAvailable`.

One worker goroutine blocks during the wait. With the default `concurrency=4`, the other 3 goroutines continue processing. Kubernetes' 180s termination grace period covers extended Lodestone cooldowns.

The explicit `lodestone` source mode behaves identically to `auto`; it is kept for payload compatibility with legacy queued messages.

### Proxy Mode Behavior

When `consume --proxy` is used, the event pipeline runs through proxy-aware clients:

- **`id-sweep` in proxy mode**: The Lodestone is the only provider. Proxies bypass Lodestone's per-IP rate limit, so each goroutine probes through its own proxy.
- **`achievement-census` in proxy mode**: Same as non-proxy — Lodestone-only, with rate-limit waiting.
- **Proxy rotation on failure**: If a proxy fails during any request (connection refused, timeout, host unreachable), the worker immediately marks it as failed and acquires a fresh proxy from the pool. This ensures workers quickly rotate through bad proxies.
- **Per-goroutine isolation**: Each goroutine has its own proxy, its own LodestoneClient, and its own rate limiter. No shared state between goroutines.

## CLI

```bash
# Long-running consumer (one per event type; k8s deployment per consumer).
./bin/ffxiv-census consume id-sweep --concurrency 4
# Proxy mode: each goroutine acquires its own proxy
./bin/ffxiv-census consume --proxy --concurrency 8

# One-shot auto-discovery publisher (claims the next 1000 persistent-cursor IDs).
./bin/ffxiv-census publish id-sweep --count 1000 --chunk-size 100 --source auto

# Manual ID sweep over explicit range.
./bin/ffxiv-census publish id-sweep --from 1 --to 50000000 --chunk-size 100 --source auto

# Enqueue the 1000 oldest characters (no age filter, NULL first).
./bin/ffxiv-census publish character-census --limit 1000

# Re-census characters not seen in 30 days (recheck cron).
./bin/ffxiv-census publish character-census --older-than 720h --limit 1000
```

`consume` handles SIGINT/SIGTERM gracefully. `publish id-sweep` divides the sweep range into `chunk-size`-sized jobs. Automatic ranges come from the persistent `id_sweep_state` cursor, which initializes at `MAX(characters.id) + 1` and advances after all chunks are confirmed by RabbitMQ. Empty discovery batches still advance. Partial publication failures leave the cursor unchanged, favoring safe duplicate retries over skipped IDs. Explicit ranges and gap-fill runs do not change the forward cursor.

`character_milestones` is intentionally not one-to-one with `characters`. A private achievement history, a public character with no earned Chocobo achievement, or a character whose first missing chain milestone is incomplete has no new milestone row. Use the forward cursor and queue metrics to monitor discovery progress; comparing the two tables' maximum character IDs is not a reliable pipeline-health check.

**`publishAll` behaviour:** Each publish call waits for a broker confirmation before returning. If any publish is nacked or the connection drops, the command fails immediately rather than continuing to enqueue jobs that may never be delivered. After all publishes succeed, the queue connection is closed so the process exits cleanly.

**`proxy discover` callback publication:** Providers are invoked sequentially in configured order. Each provider streams records one at a time via a callback (`emit`). Inside the callback, each record is checked read-only against PostgreSQL; existing tuples are skipped and do not consume the publication quota. New records are published immediately as `new-proxy` events — the first queue publish happens before the provider fetches its next record/page. This keeps memory bounded regardless of provider response size. When the `--limit` publication cap is reached, the current provider's stream is aborted and all remaining providers are skipped. When a provider exhausts below the limit, the next provider is invoked to fill the remaining quota. `--limit 0` disables the cap. If a publish fails, that provider is stopped (logged as `proxy.discover.publish_failed`) and the command continues to the next provider. Partial provider failures are tolerated: the command reports per-provider fetched/published counts and returns an error only when every provider fails (`proxy discovery failed: all providers failed (%d errors)`).
