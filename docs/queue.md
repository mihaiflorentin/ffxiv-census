# RabbitMQ Work Queue

ffxiv-census runs its durable async work queue on **RabbitMQ**. The broker replaces the former PostgreSQL-backed `queue_jobs` table, eliminating 30-goroutine polling and moving to push-based consumption. There is no deduplication at the queue level — database upserts in handlers are idempotent.

## Topology

The adapter declares the full topology idempotently on connection. All exchanges and queues are durable; messages are persistent.

```
census (exchange, direct)
  ├─ routing key "id-sweep"           → census.id-sweep           (queue)
  ├─ routing key "character-census"   → census.character-census   (queue)
  └─ routing key "achievement-census" → census.achievement-census (queue)

census.<type>.failed (queue, per event type)
  └─ x-dead-letter-exchange = census
  └─ x-dead-letter-routing-key = <type>

census.<type>.dead (queue, per event type)
  └─ terminal park: poison payloads, no-handler deliveries, and
     failed-queue ladder exhaustion; inspectable, never auto-consumed
```

Each event type gets a **main queue** (`census.<type>`) bound to the `census` exchange with routing key equal to the event type, a **failed queue** (`census.<type>.failed`) that dead-letters back to the main exchange, and a **dead queue** (`census.<type>.dead`) for terminal parking. Retry messages in the failed queue have a TTL — when the TTL expires, RabbitMQ dead-letters them back to the main queue automatically. Nothing is ever silently dropped: deliveries the consumers cannot route are parked on the dead queue.

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
                                │     ├─ poison/no handler  → park on
                                │     │                        census.<type>.dead, Ack
                                │     └─ other error        → publish to
                                │           census.<type>.failed with TTL, Ack
                                │
                              census.<type>.failed
                                ├─ retry worker holds the delivery until its
                                │   x-not-before-ms instant (the TTL ladder),
                                │   then republishes → census.<type>
                                │   (READY messages behind it expire via TTL
                                │   and dead-letter to census.<type> instead)
                                └─ attempts >= max_attempts → census.<type>.dead
```

## Retry Mechanism

When a handler returns an error, the adapter classifies it via `failureDecision` and the `x-attempts` header:

| Condition | Action | TTL | Result |
|-----------|--------|-----|--------|
| Error chain carries `contract.ErrPoisonPayload` (undecodable JSON, invalid range) or `contract.ErrNoHandler` (event type not registered in this process) | Publish to `census.<type>.dead` | None | Terminal park — inspectable and recoverable, never retried |
| Any other error (Lodestone challenge, timeout, 429, 5xx, connection errors) | Publish to `census.<type>.failed` | `min(5 × 2^(attempts-1), 3600)` seconds | Auto-dead-letters back to the main queue after the TTL — retried forever until the handler succeeds or acks a terminal outcome (genuine Lodestone 404/403) |

Only the handler itself can end the retry ladder, by acking a terminal outcome: handlers mark characters deleted on genuine `contract.ErrCharacterNotFound` and treat private profiles as complete. There is no attempt-based dead-lettering for retryable failures.

`max_attempts` comes from `[queue] max_attempts` (default **50** in the embedded `config.toml`, overridable via `QUEUE_MAX_ATTEMPTS`). It is the **failed-worker safety net**: a delivery that reaches `census.<type>.failed` with `attempts >= max_attempts` is dead-parked on `census.<type>.dead` instead of republished. The first backoff steps are **5, 10, 20, 40, 80** … capped at 3600s.

The attempt count is tracked in the `x-attempts` message header and incremented on every republish. The original message is always acked after the replacement publish was broker-confirmed; a failed publish Nacks with requeue so no message is lost.

## Failed-Queue Retry Worker

`ConsumeFailed` runs the `census-retry` worker: it consumes the failed queues, normalizes each delivery's event type from the **consumer tag** (`census.id-sweep.failed` → `id-sweep`), and republishes to the `census` exchange under that canonical routing key. Legacy or misrouted deliveries that arrived with prefixed routing keys (`census.census.id-sweep.failed`) are therefore routed correctly instead of dying with `no handler registered`. The worker enables publisher confirms and Acks only after the broker confirms the republish. Unknown event types dead-park on `<failed queue>.dead`.

Lifecycle logs (visible at `LOGGING_LEVEL=debug`, set on the `census-retry` deployment): `rabbitmq.failed.received` (debug), `rabbitmq.failed.republished` (info), `rabbitmq.failed.dead_letter` / `rabbitmq.failed.unknown_event` (error).

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
- any other error → message is forwarded to `census.<type>.failed` with a retry TTL

**Dual-context shutdown:** `Consume` creates two independent contexts:
1. **`stopClaiming`** — derived from the caller's `ctx`. When `ctx` is cancelled (SIGTERM), `stopClaiming` cancels immediately. Workers stop claiming new deliveries; any unclaimed delivery is Nack'd with `requeue=true` so RabbitMQ redelivers it to another consumer.
2. **`processCtx`** — an independent context cancelled only after all in-flight handlers have finished (`wg.Wait()`). Handlers that are already running continue to completion against `processCtx`, giving them time to finish gracefully.

This two-phase shutdown ensures no message is lost: unclaimed messages are requeued, and in-flight handlers either succeed (ack) or fail (forward to dead-letter) before the worker exits.

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

Handlers that store census data do not write directly. The ID sweep, character census and achievement census handlers hand their finished results (characters and jobs; or an achievement census's milestones and latest achievement) to the per-pod `contract.ResultWriter` (`infrastructure/postgres/writer`) and **block in `Write` until the batch containing them has committed**. Because the queue acks a delivery only when its handler returns `nil`, the ack always follows the commit, and a failed commit makes `Write` (and so the handler) return the error: the job is not acked and takes the normal retry path through the failed queue. Dependent jobs (achievement census) are chained only after the commit. A character census result carries the character and its jobs; an achievement census result carries the milestones and latest-achievement summary (or just the private flag), and both are stored with the rest of the batch in the same transaction.

**Failure handling.** A failed commit makes `Write` (and so the handler) return the error: the job is not acked and takes the normal retry path through the failed queue. The writer decides per error class how widely a failure spreads:

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
```

[queue]

| Field | Purpose |
|-------|---------|
| `max_attempts` | Delivery attempt budget before a message parks permanently in its failed queue |

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
    - name: census-retry
      command: [/app/ffxiv-census, consume, failed, --events, "id-sweep,character-census,achievement-census", -c, "1"]
```

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
kubectl scale deploy/ffxiv-census-worker-proxy-scan --replicas=1

# Resume cronjobs
kubectl patch cronjob publish-character -p '{"spec":{"suspend":false}}'
kubectl patch cronjob publish-id-sweep -p '{"spec":{"suspend":false}}'
kubectl patch cronjob proxy-discover -p '{"spec":{"suspend":false}}'
kubectl patch cronjob proxy-scan -p '{"spec":{"suspend":false}}'
```

## Event Types

The queue carries the census events. See `docs/events.md` for payloads and chaining details.

| Context | Events | Consumer Command |
|---------|--------|-----------------|
| Census | `id-sweep`, `character-census`, `achievement-census` | `ffxiv-census consume` |

Proxy scans are performed directly by the `proxy scan` database worker, and proxy discovery inserts new proxies directly (`proxy discover`); neither uses RabbitMQ events. The former `new-proxy` event, its queues and the `proxy consume` command were removed. A broker that still has the old `census.new-proxy`, `census.new-proxy.failed` and `census.new-proxy.dead` queues keeps them (the adapter no longer declares or consumes them); delete them once empty. Application consumers fail fast when RabbitMQ closes their delivery channels; Kubernetes restarts them automatically.

**Handler chaining:** When a handler succeeds, it returns downstream jobs. The worker publishes each downstream job individually:
- `id-sweep` → `achievement-census` (per discovered character)
- `character-census` → `achievement-census` (per re-censused character)

The automatic ID-sweep publisher persists its next unscanned ID in
`id_sweep_state`. After every chunk in a range receives broker confirmation, the
cursor advances beyond that range even when no character was found. A partial
publish failure does not advance it, so the next invocation retries rather than
leaving an ID hole. Completion logs expose `from_id`, `to_id`, and `next_id`.

## Contract

`port/contract.Queue` (see `port/contract/queue.go`) is implemented by `infrastructure/rabbitmq` and `mock/queue` (in-memory fake for tests). The interface has four methods: `Publish`, `Consume`, `ConsumeFailed`, and `Close`. All retry and dead-letter logic is internal to the adapter — callers only see success or error from the handler.

## Connection Resilience

The RabbitMQ adapter handles connection drops with automatic reconnect. On `Publish`, if the connection is closed, it dials a new connection and channel before retrying. The topology is re-declared on reconnect (idempotent). Consumer goroutines will error and the `Consume` call will return if the connection cannot be recovered.
