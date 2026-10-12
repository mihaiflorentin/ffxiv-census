# RabbitMQ Work Queue

ffxiv-census runs its durable async work queue on **RabbitMQ**. The broker replaces the former PostgreSQL-backed `queue_jobs` table, eliminating 30-goroutine polling and moving to push-based consumption. There is no deduplication at the queue level — database upserts in handlers are idempotent.

## Topology

The adapter declares the full topology idempotently on connection and again on every reconnect. All exchanges and queues are durable; messages are persistent.

```
census (exchange, direct)
  ├─ routing key "id-sweep"           → census.id-sweep           (queue)
  ├─ routing key "character-census"   → census.character-census   (queue)
  ├─ routing key "achievement-census" → census.achievement-census (queue)
  └─ routing key "new-proxy"          → census.new-proxy          (queue)

census.<type>.delay.<tier> (queue, per event type and tier; unbound, published to
                            through the default exchange)
  └─ x-message-ttl = <tier delay in ms>
  └─ x-dead-letter-exchange = census
  └─ x-dead-letter-routing-key = <type>

census.<type>.dead (queue, per event type)
  └─ terminal park: poison payloads, no-handler deliveries and jobs that
     exhausted their attempts; inspectable, never auto-consumed

census.<type>.failed (queue, per event type) — LEGACY, kept for the transition
  └─ x-dead-letter-exchange = census
  └─ x-dead-letter-routing-key = <type>
```

The default tiers are **5 s, 30 s, 5 min and 1 h**, so each event type has `census.<type>.delay.5s`, `.delay.30s`, `.delay.5m` and `.delay.1h`. Queue names carry the delay: the arguments of an existing queue cannot change (RabbitMQ answers `PRECONDITION_FAILED`), so a different tier list creates new queues instead of redeclaring old ones. Old tier queues keep their TTL and dead-letter their messages back to the main queue on their own; delete them once empty.

Each event type gets a **main queue** (`census.<type>`) bound to the `census` exchange with routing key equal to the event type, one **delay-tier queue** per tier, and a **dead queue**. The **failed queue** (`census.<type>.failed`) is no longer written to. It stays declared with its original arguments so messages parked there before the tiers existed can still drain (see [Legacy failed queues](#legacy-failed-queues)). Nothing is ever silently dropped: deliveries the consumers cannot route are parked on the dead queue.

## Message Flow

```
Publisher                     Broker                          Consumer
─────────                     ──────                          ────────
Publish(ctx, job)
  │
  ├─ exchange: census
  ├─ routing key: job.Type
  └─ body: JSON payload      census.<type> queue
                                │
                                ├─ push to consumer (job.Type comes
                                │  from the consumer tag, never the
                                │  delivery routing key)
                                │
                                │   handler(ctx, job)
                                │     ├─ success            → Ack
                                │     ├─ poison/no handler  → publish to
                                │     │                        census.<type>.dead, Ack
                                │     ├─ attempts reach     → publish to
                                │     │   max_attempts         census.<type>.dead, Ack
                                │     └─ other error        → publish to
                                │           census.<type>.delay.<tier>, Ack
                                │
                              census.<type>.delay.<tier>
                                └─ after <tier delay> the broker dead-letters
                                   the message to the census exchange with
                                   routing key <type> → census.<type>
```

No consumer reads the delay queues. The broker alone moves a message out of a tier, so there is no sleeping consumer and a message with a long delay never holds back one with a short delay: each tier has a single TTL, so a tier is FIFO and its head is always the next to expire.

## Retry Mechanism

When a handler returns an error, the adapter calls `contract.DecideRetry(err, attempts, max_attempts, tiers)`. `attempts` is the `x-attempts` header: the number of **job-attributable** failures the message already had.

| Condition | Action | Result |
|-----------|--------|--------|
| Error chain carries `contract.ErrPoisonPayload` (undecodable JSON, invalid range) or `contract.ErrNoHandler` (event type not registered in this process) | Publish to `census.<type>.dead` | Terminal park, inspectable and recoverable, never retried |
| `contract.IsProxyAttributable(err)` | Publish to the tier of the job's last failure; `x-attempts` unchanged, `x-proxy-failures` + 1 | Retried after that tier's delay. Never dead-parks by itself, never moves the job up the ladder |
| Any other error, new `x-attempts` below `max_attempts` | `x-attempts` + 1, publish to tier `min(attempts, number of tiers)` (1-based) | 1st failure waits 5 s, 2nd 30 s, 3rd 5 min, 4th and later 1 h |
| Any other error, new `x-attempts` reaches `max_attempts` | Publish to `census.<type>.dead` | Safety net for a job that keeps failing for its own reasons |

Only the handler itself can end the retry of a job that keeps failing for its own reasons before the cap, by acking a terminal outcome: handlers mark characters deleted on genuine `contract.ErrCharacterNotFound` and treat private profiles as complete.

### What counts as a job attempt

A failure is **proxy-attributable** when the handler error carries, through `errors.As`:

- a `*contract.ProxyCheckError` of kind `CheckProxy` (proxy dial or negotiation failure), `CheckTarget` (a destination challenge, 429 or 5xx) or `CheckDeadline` (timeout), or
- a `*contract.ProxyFaultError`, the explicit wrapper for proxy trouble that has no `ProxyCheckError` of its own.

Error text is never parsed. Everything else, including `CheckLocal`, is the job's. The worker already retries a proxy-caused failure in-process on up to three other proxies; only when those are exhausted does the error reach the queue, still typed, so the job waits in a tier without being blamed. A job failing only on proxies therefore cycles at the tier of its last real failure (5 s for a job that never failed on its own) instead of climbing to 1 h, and `x-proxy-failures` shows how often it happened. It is informational; only `x-attempts` drives the tier and the cap.

`max_attempts` comes from `[queue] max_attempts` (default **50** in the embedded `config.toml`, overridable via `QUEUE_MAX_ATTEMPTS`). The ladder now has four steps and the 1 h tier repeats, so 50 job-attributable failures take roughly two days. Proxy-attributable failures do not count toward it, so a job that only meets bad proxies is not dead-parked by bad luck; it keeps retrying, bounded by the proxy cooldowns that already throttle it.

The tiers come from `[queue] retry_tiers` (default `["5s", "30s", "5m", "1h"]`, override with `QUEUE_RETRY_TIERS=5s,30s,5m,1h`). They must be strictly ascending and at least 1 ms. A different list creates new `delay` queues (named after the delay); see Topology.

The `x-attempts` and `x-proxy-failures` counters ride in message headers and are rewritten on every retry. RabbitMQ's own `x-death` bookkeeping is stripped on republish. The original message is always acked after the replacement publish was broker-confirmed (publisher confirms, mandatory publish); a failed publish nacks with requeue so no message is lost. Ack-after-commit of the batching writer is unchanged: the handler returns only after its batch committed, and only then does the queue ack or retry.

## Legacy failed queues

Before the tiers, a failure was published to `census.<type>.failed` with a per-message TTL and an `x-not-before-ms` header, and a single sleeping `census-retry` consumer republished it. Those queues still exist and may hold messages at deployment time. They are kept declared with unchanged arguments, and nothing publishes to them anymore.

`ffxiv-census consume failed` (the `census-retry` deployment, `ConsumeFailed`) is now a **drain**. It never sleeps. For each message it decides with `contract.DecideLegacyDrain`, in this order:

1. event type unknown: publish to `census.<unknown>.dead`, ack;
2. `x-attempts >= max_attempts`: publish to `census.<type>.dead`, ack;
3. `x-not-before-ms` already passed (or absent): republish to the `census` exchange under the canonical routing key (taken from the consumer tag, not from the delivery's routing key), ack;
4. otherwise: publish to the smallest delay tier whose delay covers the remaining wait (the longest tier when none does), ack. The message is never retried earlier than the old ladder promised.

`x-attempts` is carried over unchanged. Each move waits for broker confirmation before the ack. Messages that nothing drains still leave the legacy queue by themselves: the queue's dead-letter arguments send an expired per-message TTL back to the main queue, as before.

Transition: deploy the new binary everywhere; keep one `census-retry` pod (concurrency can stay 1, it no longer blocks) until `rabbitmqctl list_queues -p ffxiv-census name messages | grep '\.failed'` shows 0 for every `census.<type>.failed`; then remove the `census-retry` deployment. The `consume` workers started with `--events ...,id-sweep.failed,...` also read the failed queues and process their messages directly; they can drop those names at the same point.

Lifecycle logs: `rabbitmq.retry` (error, one per tier move, with `attempts`, `proxy_fault`, `proxy_failures`, `queue`), `rabbitmq.dead_park` (error), and for the drain `rabbitmq.failed.republished` (info), `rabbitmq.failed.moved_to_tier` (info), `rabbitmq.failed.dead_letter` and `rabbitmq.failed.unknown_event` (error).

Slow acks are visible cluster-wide: any consumer whose claim→ack window exceeds 30s logs a `queue.slow_ack` warning with the event type, attempt count, and handler duration.

## Consumer Pattern

Consumption is **push-based**. The worker calls `queue.Consume` which blocks until the context is cancelled. RabbitMQ delivers messages to consumers as they arrive — no polling, no claim loops, no `FOR UPDATE SKIP LOCKED`.

```go
// contract.Queue — the simplified interface
type Queue interface {
    Publish(ctx context.Context, job QueueJob) error
    Consume(ctx context.Context, eventTypes []string, concurrency int, handler func(ctx context.Context, job QueueJob) error) error
    Close() error
}
```

**`Publish`** sends a single job to the `census` exchange with routing key = `job.Type`. The payload is JSON bytes. It uses deferred publisher confirms (`PublishWithDeferredConfirmWithContext`) so a successful return guarantees the durable target queue accepted the message. After confirmation, `Publish` drains the mandatory return channel — if RabbitMQ reports the message as unroutable, the call returns an error with the AMQP reply code and routing key. This fail-fast behaviour means callers never silently lose messages to misconfigured bindings.

**`Consume`** starts `concurrency` worker goroutines. Each worker opens a dedicated AMQP channel with `prefetch(1)`, consumes from all specified event type queues (including `.failed` queues — their deliveries are normalized to the canonical event type and dispatched normally), and dispatches messages to the handler. On handler return:
- `nil` → message is acked
- `ErrPoisonPayload` / `ErrNoHandler` in the error chain → message is dead-parked on `census.<type>.dead`
- any other error → message is published to the delay tier chosen by `contract.DecideRetry` (see Retry Mechanism)

**Dual-context shutdown:** `Consume` creates two independent contexts:
1. **`stopClaiming`** — derived from the caller's `ctx`. When `ctx` is cancelled (SIGTERM), `stopClaiming` cancels immediately. Workers stop claiming new deliveries; any unclaimed delivery is Nack'd with `requeue=true` so RabbitMQ redelivers it to another consumer.
2. **`processCtx`** — an independent context cancelled only after all in-flight handlers have finished (`wg.Wait()`). Handlers that are already running continue to completion against `processCtx`, giving them time to finish gracefully.

This two-phase shutdown ensures no message is lost: unclaimed messages are requeued, and in-flight handlers either succeed (ack) or fail (move to a delay tier or the dead queue) before the worker exits.

**Worker usage:**

```go
processJob := func(ctx context.Context, job contract.QueueJob) error {
    h, ok := handlers.Get(job.Type)
    if !ok {
        return fmt.Errorf("no handler for %s", job.Type)
    }
    next, err := h.Handle(ctx, job.Payload)
    if err != nil {
        return err // queue handles retry/dead-letter
    }
    // Publish downstream jobs individually
    for _, j := range next {
        if err := queue.Publish(ctx, j); err != nil {
            return err
        }
    }
    return nil
}

err := queue.Consume(ctx, eventTypes, concurrency, processJob)
```

### Batched writes and acks

Handlers that store census data do not write directly. The ID sweep, character census and achievement census handlers hand their finished results (characters and jobs; or an achievement census's milestones and latest achievement) to the per-pod `contract.ResultWriter` (`infrastructure/postgres/writer`) and **block in `Write` until the batch containing them has committed**. Because the queue acks a delivery only when its handler returns `nil`, the ack always follows the commit, and a failed commit makes `Write` (and so the handler) return the error: the job is not acked and takes the normal retry path through a delay tier. Dependent jobs (achievement census) are chained only after the commit. A character census result carries the character and its jobs; an achievement census result carries the milestones and latest-achievement summary (or just the private flag), and both are stored with the rest of the batch in the same transaction.

**Failure handling.** A failed commit makes `Write` (and so the handler) return the error: the job is not acked and takes the normal retry path through a delay tier. The writer decides per error class how widely a failure spreads:

- *Data-level errors* (`contract.ErrInvalidData`, which `CharacterRepository.StoreResult` joins to Postgres SQLSTATE class 22 data exceptions and class 23 constraint violations): the transaction rolled back, but one result is to blame. The writer bisects the batch, retrying each half in its own transaction until the offending result is alone. Valid neighbours commit and are acked; only the offender's `Write` returns the error, so only its job retries. Cost is about `2*log2(N)` extra transactions per bad result.
- *Everything else* (connection loss, timeouts, serialization failures, shutdown): not isolated. The batch's waiters all get the error from that one attempt, and if it happens in the middle of a bisection the remaining pieces fail immediately too, so a struggling database is never hit with N retries. All those jobs stay unacked and retry.
- *Partial ID sweeps*: when a Lodestone error interrupts a sweep part-way, the handler first writes the characters found so far (best effort, idempotent), then returns the original error so the job still retries. A write failure there is logged and the Lodestone error is still the one returned.

`result_writer_flush_*` metrics observe every transaction attempt, including those made while isolating a bad result.

The writer flushes when `[writer] batch_size` results (characters or achievement updates) are pending or `[writer] flush_interval` has passed since the first pending result, in ONE transaction (`CharacterRepository.StoreResult`); results count by rows of work (characters plus achievement updates). Env overrides: `WRITER_BATCH_SIZE`, `WRITER_FLUSH_INTERVAL`. One batch transaction is bounded by the writer's `FlushTimeout` (30 s); this one is not exposed in `config.toml`, so it only changes in code (`writer.Config.FlushTimeout`). A timeout counts as a transient error (see below). Handlers wait for a flush, so the number of concurrent consumers bounds how many jobs can share a batch; small batches fall back to the interval. On shutdown the consumer closes the writer after `Consume` has returned (all in-flight handlers finished), which flushes anything still pending. Metrics: `result_writer_batch_size`, `result_writer_flush_duration_seconds`, `result_writer_flush_failures_total`. Only the not-found path (marking a character deleted) still writes directly. A handler registered without a writer does not store anything itself: it fails the job with `handler.ErrNoWriter`, so a missing writer shows up as retried jobs and never as writes that bypass the ack-after-commit rule. CLI commands do not run census handlers; `consume` is the only entry point and it closes the writer after the consumer stops.

**Commit mode and ordering.** The batch transaction runs with `SET LOCAL synchronous_commit = off`, so `Write` returns once Postgres has the commit in memory, not on disk; a database crash can lose roughly the last 600 ms of acked census data, which the next census pass rewrites. Only this transaction does this; stats refresh, migrations and proxy leases stay synchronous. A batch costs a fixed number of statements however many characters it holds: one multi-row `characters` upsert (arrays through `unnest`, rows inserted in ascending ID order), one delete of jobs the characters no longer have, one multi-row jobs upsert, then the achievement updates. If the same character ID appears twice in a batch, the last record wins (one `INSERT ... ON CONFLICT` statement cannot update a row twice). When a batch holds achievements, the rows of all its characters are first locked in ascending ID order, so pods with overlapping IDs queue instead of deadlocking. Because the writer flushes serially, a worker needs one connection for it: worker pods run with 2-3 open connections (see `docs/postgres.md`).

Handler panics are caught with `defer/recover`, formatted with stack traces, and returned as errors — the worker goroutine does not crash.

## Configuration

```toml
[rabbitmq]
url      = "amqp://guest:guest@localhost:5672/ffxiv-census"
host     = "localhost"
port     = 5672
user     = "guest"
password = "guest"
vhost    = "ffxiv-census"

[queue]
max_attempts = 50
retry_tiers  = ["5s", "30s", "5m", "1h"]
```

[queue]

| Field | Purpose |
|-------|---------|
| `max_attempts` | Job-attributable failures before a message parks on its dead queue |
| `retry_tiers` | Delays of the retry tier queues, strictly ascending (default `["5s", "30s", "5m", "1h"]`; env `QUEUE_RETRY_TIERS=5s,30s,5m,1h`) |

| Field | Purpose |
|-------|---------|
| `url` | Full AMQP connection URL (takes precedence over individual fields) |
| `host` | RabbitMQ hostname |
| `port` | AMQP port (default 5672) |
| `user` | Authentication username |
| `password` | Authentication password |
| `vhost` | Virtual host (default `ffxiv-census`) |

If `url` is empty, it is constructed from the individual fields: `amqp://<user>:<password>@<host>:<port>/<vhost>`.

**Environment overrides** — dots become underscores, section name is the prefix:

| Variable | Overrides |
|----------|-----------|
| `RABBITMQ_URL` | `url` |
| `RABBITMQ_HOST` | `host` |
| `RABBITMQ_PORT` | `port` |
| `RABBITMQ_USER` | `user` |
| `RABBITMQ_PASSWORD` | `password` |
| `RABBITMQ_VHOST` | `vhost` |

In Kubernetes, `RABBITMQ_USER` and `RABBITMQ_PASSWORD` are injected from Vault (`rabbitmq/prod` secret) via External Secrets Operator. The URL is constructed in the deployment env:

```yaml
- name: RABBITMQ_URL
  value: "amqp://$(RABBITMQ_USER):$(RABBITMQ_PASSWORD)@rabbitmq.default.svc.cluster.local:5672/ffxiv-census"
```

## Kubernetes Deployment

Each event type runs as a **separate Deployment** with its own replica count and concurrency setting. This allows independent scaling and restart of each consumer type.

```yaml
# k8s/values.yaml — worker instances
workers:
  instances:
    - name: census-consumer
      command: [/app/ffxiv-census, consume, --events, "id-sweep,character-census,achievement-census,id-sweep.failed,character-census.failed,achievement-census.failed", -c, "10"]
    - name: proxy-id-sweep
      command: [/app/ffxiv-census, consume, id-sweep, --proxy, -c, "25"]
    - name: proxy-character-census
      command: [/app/ffxiv-census, consume, character-census, --proxy, -c, "15"]
    - name: proxy-achievement-census
      command: [/app/ffxiv-census, consume, achievement-census, --proxy, -c, "25"]
    - name: proxy-new
      command: [/app/ffxiv-census, proxy, consume, -c, "30"]
    - name: census-retry
      command: [/app/ffxiv-census, consume, failed, --events, "id-sweep,character-census,achievement-census", -c, "1"]
```

`census-retry` is the transition drain for the legacy failed queues (see [Legacy failed queues](#legacy-failed-queues)); it is not part of the normal retry path any more and can be removed once those queues are empty.

Proxy scanning (`proxy scan`) is a separate lease-based worker that reads due rows directly from PostgreSQL and bypasses RabbitMQ entirely; see [docs/proxy.md](proxy.md). Concurrency is set per-deployment via the `-c` flag. The default is 4 if not specified. Each worker goroutine opens its own AMQP channel with `prefetch(1)`.

**Scaling:** Increase `replicaCount` or `-c` to handle higher throughput. RabbitMQ distributes messages across consumers automatically (round-robin with prefetch=1). There is no risk of double-delivery — each message is delivered to exactly one consumer.

**Graceful shutdown:** On SIGTERM, the context is cancelled. Workers stop accepting new messages and finish their current handler calls. The `terminationGracePeriodSeconds` (default 180s) gives in-flight jobs time to complete.

**`Close`** closes the publishing channel first, then the AMQP connection. Both errors are joined with `errors.Join` so callers see every failure. Channel-before-connection ordering prevents the connection from closing while a publish confirm is still in flight.

## Monitoring

RabbitMQ exposes a **management UI** on port 15672. In the cluster it is accessible via NodePort 31672:

```
http://<node-ip>:31672
```

The management UI shows:
- Queue depths and message rates per queue
- Consumer connections and channel counts
- Exchange bindings and routing
- Message rates (publish, deliver, ack)

Useful CLI commands inside the pod:

```bash
# List all queues with message counts
kubectl exec rabbitmq-0 -- rabbitmqctl list_queues -p ffxiv-census name messages consumers

# List connections
kubectl exec rabbitmq-0 -- rabbitmqctl list_connections

# Purge a queue (e.g. to clear stuck messages)
kubectl exec rabbitmq-0 -- rabbitmqctl purge_queue census.id-sweep -p ffxiv-census
```

## Migration from PostgreSQL

The `migrate queue` command moves all pending, claimed, and failed jobs from the legacy `queue_jobs` table to RabbitMQ:

```bash
# Dry run — shows what would be migrated
./bin/ffxiv-census migrate queue --dry-run

# Execute migration
./bin/ffxiv-census migrate queue
```

The command:
1. Queries all non-done jobs from `queue_jobs` (`status IN ('pending', 'claimed', 'failed')`)
2. Publishes each job individually to RabbitMQ via `queue.Publish`
3. Logs counts per event type and status
4. Deletes the migrated rows from PostgreSQL (`DELETE FROM queue_jobs WHERE status != 'done'`)

**Pre-migration steps:**

```bash
# Scale down all workers
kubectl scale deploy/ffxiv-census-worker-id-sweep --replicas=0
kubectl scale deploy/ffxiv-census-worker-character-census --replicas=0
kubectl scale deploy/ffxiv-census-worker-achievement-census --replicas=0
kubectl scale deploy/ffxiv-census-worker-proxy-id-sweep --replicas=0
kubectl scale deploy/ffxiv-census-worker-proxy-character-census --replicas=0
kubectl scale deploy/ffxiv-census-worker-proxy-achievement-census --replicas=0
kubectl scale deploy/ffxiv-census-worker-proxy-new --replicas=0
kubectl scale deploy/ffxiv-census-worker-proxy-scan --replicas=0

# Suspend cronjobs
kubectl patch cronjob publish-character -p '{"spec":{"suspend":true}}'
kubectl patch cronjob publish-id-sweep -p '{"spec":{"suspend":true}}'
kubectl patch cronjob proxy-discover -p '{"spec":{"suspend":true}}'
kubectl patch cronjob proxy-scan -p '{"spec":{"suspend":true}}'
```

**Post-migration steps:**

```bash
# Scale workers back up
kubectl scale deploy/ffxiv-census-worker-id-sweep --replicas=1
kubectl scale deploy/ffxiv-census-worker-character-census --replicas=1
kubectl scale deploy/ffxiv-census-worker-achievement-census --replicas=1
kubectl scale deploy/ffxiv-census-worker-proxy-id-sweep --replicas=1
kubectl scale deploy/ffxiv-census-worker-proxy-character-census --replicas=1
kubectl scale deploy/ffxiv-census-worker-proxy-achievement-census --replicas=1
kubectl scale deploy/ffxiv-census-worker-proxy-new --replicas=1
kubectl scale deploy/ffxiv-census-worker-proxy-scan --replicas=1

# Resume cronjobs
kubectl patch cronjob publish-character -p '{"spec":{"suspend":false}}'
kubectl patch cronjob publish-id-sweep -p '{"spec":{"suspend":false}}'
kubectl patch cronjob proxy-discover -p '{"spec":{"suspend":false}}'
kubectl patch cronjob proxy-scan -p '{"spec":{"suspend":false}}'
```

## Event Types

The queue carries events for census and proxy contexts. See `docs/events.md` for payloads and chaining details.

| Context | Events | Consumer Command |
|---------|--------|-----------------|
| Census | `id-sweep`, `character-census`, `achievement-census` | `ffxiv-census consume` |
| Proxy | `new-proxy` | `ffxiv-census proxy consume` |

Proxy scans are performed directly by the `proxy scan` database worker, not via RabbitMQ events. Application consumers fail fast when RabbitMQ closes their delivery channels; Kubernetes restarts them automatically.

**Handler chaining:** When a handler succeeds, it returns downstream jobs. The worker publishes each downstream job individually:
- `id-sweep` → `achievement-census` (per discovered character)
- `character-census` → `achievement-census` (per re-censused character)

The automatic ID-sweep publisher persists its next unscanned ID in
`id_sweep_state`. After every chunk in a range receives broker confirmation, the
cursor advances beyond that range even when no character was found. A partial
publish failure does not advance it, so the next invocation retries rather than
leaving an ID hole. Completion logs expose `from_id`, `to_id`, and `next_id`.

## Contract

`port/contract.Queue` (see `port/contract/queue.go`) is implemented by `infrastructure/rabbitmq` and `mock/queue` (in-memory fake for tests). The interface has four methods: `Publish`, `Consume`, `ConsumeFailed`, and `Close`. All retry and dead-letter logic is internal to the adapter — callers only see success or error from the handler. The tier and dead-queue decisions are pure functions in `port/contract/retry.go` (`DecideRetry`, `DecideLegacyDrain`, `IsProxyAttributable`, `ProxyFaultError`), shared by both adapters so they cannot diverge.

The in-memory fake models the tiers: a failed job lands in the tier `DecideRetry` picks and becomes deliverable once the tier delay has passed on a clock injected with `mockqueue.WithClock`. `Retries()` reports the attempt count, tier and proxy-fault flag per failure, `Waiting(tier)` the jobs in a tier, `Dead()` the dead queue, and `PublishLegacyFailed` plus `ConsumeFailed` model the legacy drain. Worker tests assert tier selection and attempt counting against it.

## Verifying the delay tiers on a real RabbitMQ

The adapter's broker wiring (queue declarations, expiry out of a tier, the legacy drain, dead-parking) has no automated broker-backed test; the pure parts (tier selection, header handling, queue argument builders, topology list, legacy routing) are unit-tested. Run this by hand against a disposable RabbitMQ (never production first) after changing the topology code:

1. **Declarations.** Start a worker or any command that calls `container.Load.Queue()`. Run `rabbitmqctl list_queues -p ffxiv-census name arguments messages`. Expect, per event type, `census.<type>`, `census.<type>.delay.5s|30s|5m|1h` with `x-message-ttl` 5000, 30000, 300000, 3600000 and `x-dead-letter-exchange=census`, `x-dead-letter-routing-key=<type>`, plus `.dead` and the legacy `.failed`. Start a second process and then restart one: no `PRECONDITION_FAILED` and no duplicate queues (idempotent declaration). Kill the broker connection (`rabbitmqctl close_all_connections "test"`) and publish again: the topology is re-declared without errors.
2. **Expiry from tier 5 s back to main.** Stop all consumers of `census.id-sweep`. Publish a job whose handler fails (or move a message by hand with `rabbitmqadmin publish exchange= routing_key=census.id-sweep.delay.5s payload=...`). Expect it in `census.id-sweep.delay.5s` with message count 1, and 5 s later in `census.id-sweep` (count 1, delay queue 0) with the `x-death` header naming reason `expired`. A message published to the 30 s tier a moment later must not be delivered before its own 30 s (no head-of-line blocking across tiers).
3. **Attempt counting.** Start a consumer whose handler returns a plain error. Expect `x-attempts` 1, 2, 3 on the successive deliveries and the message in tiers 5 s, 30 s, 5 min. Switch the handler to return a challenge error (`*contract.ProxyCheckError`, kind `CheckTarget`): `x-attempts` must stay constant, `x-proxy-failures` must rise, and the message must return to the tier of the last real failure.
4. **Poison to dead.** Publish a payload the handler rejects with `contract.ErrPoisonPayload`. Expect it in `census.<type>.dead` at once, with no tier hop, and `rabbitmqctl list_queues` showing the main queue empty. Repeat with a handler error and `max_attempts` set to 2: the second plain failure lands in `.dead`.
5. **Legacy drain.** Publish a message straight into `census.id-sweep.failed` with header `x-attempts=3` and `x-not-before-ms` 20 s ahead, one with the header in the past, one with `x-attempts` at `max_attempts`, and one into a failed queue of an unknown event type if you can declare one. Run `ffxiv-census consume failed`. Expect: the future one in `delay.30s`, the past one in `census.id-sweep`, the exhausted one in `.dead`, the failed queue empty, and the worker never blocking.

Status of this verification for ticket #39: broker wiring is **unverified**. The implementation was built without access to a RabbitMQ binary or a usable Docker daemon, so none of the steps above has been run.

## Connection Resilience

The RabbitMQ adapter handles connection drops with automatic reconnect. On `Publish`, if the connection is closed, it dials a new connection and channel before retrying. The topology is re-declared on reconnect (idempotent). Consumer goroutines will error and the `Consume` call will return if the connection cannot be recovered.
