# ffxiv-census

**ffxiv-census** is a high-performance, single-binary FFXIV census engine and data crawler built with Go and PostgreSQL. It follows a hexagonal (ports & adapters) architecture, featuring an embedded HTMX Web UI, REST APIs, OpenAPI/Swagger specifications, Prometheus metrics, and a RabbitMQ-backed push-based consumer with dead-letter retry and provider rate-limiting pause controls.

---

## Quick Start

```bash
go mod tidy
make build
./bin/ffxiv-census server --start --port 8080
```

Access the dashboard at `http://localhost:8080` or API documentation at `http://localhost:8080/swagger/index.html`.

---

## Documentation Index

Detailed architectural and subsystem documentation lives under `docs/`:

- `docs/getting-started.md` — Setup, building, dependencies, and environment notes.
- `docs/architecture.md` — High-level system design, ports & adapters layout, and conventions.
- `docs/container.md` — Explains the Service Locator pattern and dependency wiring.
- `docs/postgres.md` — PostgreSQL storage layer, connection pooling, migrations, and DatabaseDriver contract.
- `docs/external-postgres.md` — External PostgreSQL cluster (pgres-chart), CNPG HA, and Vault integration.
- `docs/queue.md` — RabbitMQ-backed push-based queue with dead-letter retry and provider rate-limiting.
- `docs/proxy.md` — Proxy pool discovery, scanning, health checking, and census consumer integration.
- `docs/lodestone.md` — The Lodestone scraper adapter (rate limiting, Cloudflare protection, retries).
- `docs/census.md` — Domain models, milestones, active character metrics, and repositories.
- `docs/events.md` — Ingest event pipeline, message payloads, and downstream chaining.
- `docs/http-api.md` — REST API endpoints, query filters, search, and data aggregation.
- `docs/metrics.md` — Prometheus metrics, health checks, and scrape endpoints.
- `docs/logging-and-middleware.md` — Structured slog logging, correlation IDs, and HTTP middleware.
- `docs/ui.md` — Embedded HTMX dashboard, server-side Go templates, and Chart.js visualizations.

---

## CLI Reference

All commands accept `--help` and report build details via `--version`. Database migrations run automatically at boot on the first database operation.

### 1. `server` — Web UI, REST API & Metrics

Starts the HTTP/HTTPS server serving the HTMX Web UI, REST API, Swagger UI, and Prometheus `/metrics`.

```bash
# Start server on default port 8080
./bin/ffxiv-census server --start --port 8080

# Start with TLS encryption
./bin/ffxiv-census server --start --port 8443 --cert-file cert.pem --key-file key.pem

# Enable pprof profiler on :6060
./bin/ffxiv-census server --start --port 8080 --profile
```

### 2. `consume` — Push-Based RabbitMQ Worker with Provider Rate-Limit Pausing

Runs long-running consumer goroutines that receive messages via RabbitMQ push delivery. By default, consumes from **all** registered event queues concurrently (`id-sweep`, `character-census`, `achievement-census`). Each event type gets its own dedicated pod in Kubernetes for isolation and independent scaling.

When Lodestone returns HTTP 429 (rate limit exceeded), the worker automatically pauses consumption of Lodestone-dependent queues for a cooldown period; deliveries already claimed wait out the pause in-process, so no message is ever dropped.

```bash
# Consume from ALL event queues concurrently (default 3 workers)
./bin/ffxiv-census consume

# Consume from specific queues using the --events flag
./bin/ffxiv-census consume --events "id-sweep,character-census" --concurrency 8

# Consume a single queue via positional argument
./bin/ffxiv-census consume achievement-census --concurrency 2

# Proxy mode: each goroutine acquires its own proxy from the pool
./bin/ffxiv-census consume --proxy --concurrency 8
```

### 3. `publish` — Enqueue Census Operations

Enqueues census jobs to be processed asynchronously by worker consumers.

#### `publish id-sweep` — Forward Range, Gap-Fill & Kubernetes CronJob Execution
Probes character ID ranges across The Lodestone. Designed for both single-shot scheduled execution (e.g. **Kubernetes CronJob**) and continuous loop mode (`--daemon`). Uses chunking and infinite retries (`max_attempts = 0`).

```bash
# Auto-forward mode (recommended for CronJobs): reserves the next 1,000 IDs from
# a persistent cursor, publishes them, then advances the cursor.
./bin/ffxiv-census publish id-sweep --auto --batch-size 1000 --chunk-size 100

# Gap-fill mode (persistent cursor): each run publishes at most `--count` jobs
# covering the oldest unscanned holes after the last queued ID, then stores
# that ID in the `fill_gaps_state` table. When the cursor reaches MaxID it
# wraps to 0 and the sweep restarts. Genuine 404s store nothing, so dead IDs
# are re-probed once per cycle (Lodestone is authoritative; character IDs are
# assumed never recycled).
./bin/ffxiv-census publish id-sweep --fill-gaps --count 500 --chunk-size 100

# Manual one-shot gap scan from a fixed ID (the cursor is left untouched)
./bin/ffxiv-census publish id-sweep --fill-gaps --min-id 13000000 --count 500 --chunk-size 100

# Explicit range: probe character IDs 1 to 10,000 in chunks of 100
./bin/ffxiv-census publish id-sweep --from 1 --to 10000 --chunk-size 100
```

The auto-forward cursor initializes from `MAX(characters.id) + 1` on first use and
then advances independently of discoveries, so an empty range cannot pin later
cron runs to the same IDs. It advances only after every queue publish succeeds;
a partial failure safely retries the full range on the next run.
It never runs more than `--lookahead` IDs (default 10000) past the highest stored
ID; past that frontier it rewinds to `MAX(characters.id) + 1`, so newly created
characters keep being found.

#### `publish character-census`
Enqueues the oldest `last_census_at` live characters (NULL first) for re-census. Deleted characters are skipped. By default (`--older-than 0`), all live characters are eligible and results are ordered oldest-first; pass a positive `--older-than` to filter by age. Each run also re-probes a small batch of deleted characters so that ones that come back are found: `--deleted-limit` (default 10, `0` disables) caps the batch and `--deleted-older-than` (default `720h`) is how long a character must have been deleted before it is re-probed.
```bash
# Enqueue the 1000 oldest live characters (no age filter)
./bin/ffxiv-census publish character-census --limit 1000

# Re-census characters not seen in 30 days
./bin/ffxiv-census publish character-census --older-than 720h --limit 1000
```

### 4. `export` — Data Export (CSV & JSON)

Exports census data for characters or achievements directly to disk.

```bash
# Export all active characters to CSV
./bin/ffxiv-census export characters --format csv --output ./exports/characters.csv

# Export character achievements
./bin/ffxiv-census export achievements --format csv --output ./exports/achievements.csv
```

### 5. `refresh ui-stats` — Rebuild Aggregate Read Model

Builds and atomically publishes the bounded statistics snapshot consumed by all aggregate UI pages and statistics APIs. Run it after the initial migration and whenever an immediate refresh is needed; Kubernetes schedules a full rebuild nightly at 01:00 (Europe/Bucharest) and an incremental refresh every 30 minutes. `--mode=full` (the default) rebuilds every figure; `--mode=incremental` adjusts the previous snapshot (see [docs/ui.md](docs/ui.md)).

```bash
./bin/ffxiv-census refresh ui-stats
```

Web requests never fall back to census-wide aggregate queries. Until the first successful refresh, aggregate routes return `503 Service Unavailable`; a failed later refresh leaves the previous complete snapshot available.

### 6. `backfill job-masks` — Fill Job Bitmasks of Existing Characters

Fills the [job bitmasks](docs/census.md#job-bitmasks) of characters stored before they existed (NULL masks), from their stored jobs. Run it once after upgrading to migration `00026`, during quiet hours. It walks keyset batches of `--batch-size` characters (default 1000), one short transaction each, pausing `--pause` (default `200ms`) between batches. Progress is stored after every batch, so it is safe to stop and rerun: it resumes from its cursor. Already-set masks are left alone, and it never runs at boot.

```bash
./bin/ffxiv-census backfill job-masks --batch-size 1000 --pause 200ms
```

### 7. `migrate` — Manual Schema Migrations

Manage database schema versions manually with Goose.

```bash
# Run all pending migrations (default)
./bin/ffxiv-census migrate --direction up

# Roll back all migrations (destructive)
./bin/ffxiv-census migrate --direction down
```
---

## Configuration & Environment Variables

`ffxiv-census` embeds `config.toml` by default and uses Viper with `strings.NewReplacer("-", "_", ".", "_")` and `AutomaticEnv()`. Any configuration key can be overridden using uppercase environment variables (both nested keys with `.` and hyphenated keys with `-` map to underscores `_`).
| Environment Variable | Description | Default |
|---|---|---|
| `POSTGRES_DSN` | PostgreSQL connection DSN | `postgres://census:secret@localhost:5432/census?sslmode=disable` |
| `LOGGING_LEVEL` | Log level (`debug`, `info`, `warn`, `error`) | `info` |
| `LODESTONE_RATE_LIMIT` | Lodestone scraper rate limit (requests/sec, max 1.0) | `1.0` |
| `RABBITMQ_URL` | RabbitMQ connection URL | `amqp://guest:guest@localhost:5672/ffxiv-census` |
| `CENSUS_UI_STATS_CACHE_TTL` | Per-process statistics snapshot cache TTL | `1m` |
| `CENSUS_UI_STATS_STALE_WARNING` | Age at which the UI marks statistics stale | `12h` |
| `CENSUS_UI_STATS_REFRESH_TIMEOUT` | Maximum duration of a snapshot refresh | `2h` |

---

## Architecture & Event Pipeline

The census ingest pipeline is durable and event-driven via RabbitMQ with push-based consumption:

```text
publish id-sweep ──────────► consume (all queues) ────► achievement-census

publish character-census ──► consume (all queues) ────► achievement-census
```

### Ingest Events

| Event Name | Purpose | Payload Schema | Provider(s) | Downstream Event Cascading |
|---|---|---|---|---|
| `id-sweep` | Probes ranges of character IDs to discover and ingest newly created or active characters. Non-existent IDs are skipped without failing the chunk. | `{"from": 1, "to": 1000, "source": "auto"}` | **Lodestone** (sole source, authoritative for existence) | `achievement-census` |
| `character-census` | Re-censuses a known character's profile, job levels, and affiliation. A genuine Lodestone 404 marks the character deleted. | `{"character_id": 12345}` | **Lodestone** (sole source, authoritative for existence) | `achievement-census` |
| `achievement-census` | Fetches character achievements and updates registered expansion and milestone progression. | `{"character_id": 12345}` | **Lodestone-exclusive** | *None (leaf job)* |

### Provider Coordination

- **All census events are Lodestone-only.** The Lodestone is both the data source and the sole authority for existence: a genuine 404 is terminal (the character is marked deleted on `character-census` and skipped on `id-sweep`), while any transient error (challenge, timeout, 429, 5xx, dial) fails the delivery into a delay tier (5 s, 30 s, 5 min, 1 h) of the queue's retry ladder; see [docs/queue.md](docs/queue.md).
- **Rate-limit pauses:** a 429 pauses the Lodestone provider for the `Retry-After` hint (default 30s); workers wait out the pause before their next attempt without burning database transactions or CPU cycles.
- **Achievement-census** chains only after a successful character store; it is a leaf job.
---

## Build & Test Automation

```bash
# Run all tests
make test

# Format source code
make fmt

# Run static linter
make lint

# Compile production binary
make build
```

---

## Release Workflow

The application release process involves reading current release tags from GitHub, bumping the version tag according to Semantic Versioning (`vMAJOR.MINOR.PATCH`), creating and pushing the Git tag, building cross-compiled Linux AMD64/ARM64 artifacts, pushing multi-architecture Docker images, and deploying via Helm to Kubernetes.

> **Important**: Always check existing Git and Docker tags before releasing, and bump the version tag (e.g., `v1.0.0` -> `v1.0.1`). Do not reuse or overwrite existing release tags.

### 1. Read Current Tags from GitHub & Determine Next Version

Fetch and list the latest tags from GitHub:

```bash
# Fetch and inspect all tags from origin
git fetch --tags origin
git tag -l --sort=-v:refname
git ls-remote --tags origin
```

### 2. Create & Push New Git Tag

Determine the next Semantic Version (e.g., `v1.0.2`), create the Git tag, and push it to GitHub:

```bash
git tag -a v1.0.1 -m "Release v1.0.1"
git push origin v1.0.1
```

### 3. Build & Push Docker Image

Build multi-architecture production images for `linux/amd64` and `linux/arm64`, push them to Docker Hub as a manifest list under `latest`, then tag the release version on top of the same multi-arch manifest:

```bash
# 1. Cross-compile AMD64 + ARM64 binaries, build and push the
#    multi-arch image as latest (buildx pushes directly)
make docker-build

# 2. Tag the pushed multi-arch image with the release version
make docker-tag TAG=v1.0.1

# 3. Verify the release tag serves both platforms
docker buildx imagetools inspect mihaiflorentin88/census:v1.0.1
```

The published tag is a manifest list covering `linux/amd64` and `linux/arm64`; Kubernetes nodes of either architecture pull the matching image automatically, so a single image tag serves mixed ARM64/X86-64 clusters with no Helm changes.

### 4. Deploy to Kubernetes

Deploy the release to the Kubernetes cluster using Helm:

```bash
# Deploy via root Makefile
make k8s-release TAG=v1.0.1

# Or directly via Helm Makefile in k8s/
make -C k8s deploy TAG=v1.0.1

# Verify rollout status
make -C k8s post-deploy-check
```

After a release that introduces the snapshot table, or whenever an immediate data refresh is required, create a one-off refresh from the scheduled CronJob and wait for it before validating aggregate routes:

```bash
kubectl create job --from=cronjob/ffxiv-census-cron-refresh-ui-stats-full ffxiv-census-refresh-ui-stats-manual
kubectl wait --for=condition=complete --timeout=2h job/ffxiv-census-refresh-ui-stats-manual
curl -fsS https://census.ffxivbard.com/ui/dashboard >/dev/null
```
### 5. Internal Cluster Services & Monitoring Endpoints
The Kubernetes cluster provides internal monitoring and metrics services accessible to components within the cluster network:

| Service | Internal Cluster Host / Endpoint | Type | Description |
|---|---|---|---|
| **Prometheus** | `monitoring-kube-prometheus-prometheus.monitoring.svc.cluster.local:9090` | Core / Metrics Scraper | Cluster-wide Prometheus server |
| **StatsD** | `graphite.monitoring.svc.cluster.local:8125` | Graphite / StatsD Exporter | UDP StatsD metrics collector |
| **Grafana** | `http://grafana.local` | Ingress / Web UI | Dashboards & visualization interface |
