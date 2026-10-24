# PostgreSQL Storage & Runtime Migrations

PostgreSQL is the primary datastore for ffxiv-census. It holds application data and the message queue, so the service connects to a PostgreSQL instance rather than using an embedded file.

## Configuration

The `[postgres]` section in `config/config.toml` controls the datastore:

```toml
[postgres]
dsn            = "postgres://census:secret@localhost:5432/ffxiv_census?sslmode=disable"
max_open_conns = 10
max_idle_conns = 5
```

| Field           | Purpose                                                                 |
|-----------------|-------------------------------------------------------------------------|
| `dsn`           | PostgreSQL connection DSN (Data Source Name).                            |
| `max_open_conns`| Passed to `sql.DB.SetMaxOpenConns`.                                     |
| `max_idle_conns`| Passed to `sql.DB.SetMaxIdleConns`.                                     |

### Environment override

Set `POSTGRES_DSN` to redirect the database connection without editing the config:

```bash
POSTGRES_DSN=postgres://census:secret@localhost:5432/ffxiv_census?sslmode=disable ./bin/ffxiv-census server --start
```

Viper's `SetEnvKeyReplacer` maps `POSTGRES_DSN` → `postgres.dsn` (dots become underscores, all upper-case).

## Connection Pooling

PostgreSQL uses a server-side connection model with `lib/pq` or `pgx` under the hood. `max_open_conns` and `max_idle_conns` control the `*sql.DB` connection pool. Defaults from `config.toml`:

```
max_open_conns = 10
max_idle_conns = 5
```

Set `max_open_conns` based on expected concurrency and the PostgreSQL server's `max_connections` setting. Keep `max_idle_conns` lower than `max_open_conns` to allow idle connections to be reclaimed.

These defaults stay at 10/5 because the web deployment (`www`), the cronjobs and the CLI use them. Worker pods override them through `POSTGRES_MAX_OPEN_CONNS` / `POSTGRES_MAX_IDLE_CONNS` in `k8s/values.yaml`:

| Pod | Open / idle | Why |
|---|---|---|
| `workers.defaults` (`census-consumer`) | 3 / 2 | The batching writer runs one flush transaction at a time (1 connection). The rest covers short single-statement reads and writes. |
| `proxy-id-sweep`, `proxy-character-census`, `proxy-achievement-census` | 3 / 2 | The writer flush (1) plus proxy lease and lock statements (`ClaimProxy`, `RefreshConsumerLock`, `RecordConsumerFailure`; each a short statement or transaction, issued from many handler goroutines and queued on the pool). |
| `proxy-scan` | 2 / 1 | Short claim and complete transactions only; unchanged. |

Maximum worker backends: 3 x 3 (id-sweep) + 3 (character) + 2 x 3 (achievement) + 5 x 2 (scan) + 3 (census-consumer) + 2 (retry) = 33, down from 135 with the previous 15/10 pools (baseline in production: 76 of 100 connections in use). Instance `env` lists replace `workers.defaults.env` wholesale, so every instance that declares its own env must repeat the pool variables. Handlers wait on the pool when all connections are busy; they never fail for it.

## Synchronous commit

Postgres commits synchronously by default (`synchronous_commit = on`). Only census data writes relax this: `CharacterRepository.StoreResult` (the batch transaction behind the batching writer; `Upsert` calls it too) runs `SET LOCAL synchronous_commit = off` as its first statement. `SET LOCAL` lasts for that transaction only, so the pooled connection is synchronous again afterwards. A server crash can lose the last few commits (up to about three `wal_writer_delay` periods, 600 ms by default) but never corrupts data, and the next census pass rewrites them.

Everything else stays synchronous: UI statistics refresh, migrations (goose), proxy claims, leases, locks and scan results, cursors, and the direct not-found `MarkDeleted` update. Do not add `SET synchronous_commit` (session level) or set it in the DSN. `TestCharacterRepository_StoreResultCommitsAsynchronously` and `TestOtherWritePathsStaySynchronous` enforce this with a trigger that records `current_setting('synchronous_commit')` inside the writing transaction.

## Lock ordering

`StoreResult` writes a batch in ascending character ID order (characters with their jobs, then the achievement update for the same ID), regardless of the order results arrived in. Several pods store overlapping IDs concurrently; taking row locks in one global order makes them queue instead of deadlocking. `TestCharacterRepository_StoreResultWritesInAscendingIDOrder` holds a lock on a middle row and checks that no higher row was touched while the batch waits.

## Runtime Migrations (goose)

Schema changes are managed with [goose](https://github.com/pressly/goose) using SQL migrations embedded into the binary at compile time.

### Embedded migration files

Migrations live in `infrastructure/postgres/migration/query/*.sql` and are embedded via `//go:embed query/*.sql` in `infrastructure/postgres/migration/embed.go`. The `FS()` function returns an `fs.FS` scoped to the `query/` subdirectory, which is passed to the goose provider.

### Automatic migration on first use

Every binary that calls `container.Load.Postgres()` — the HTTP server, consumers, publishers — triggers `goose.Up()` automatically the first time the driver is acquired. This is implemented with `sync.Once` inside `Driver.initialise()`:

```go
func (d *Driver) initialise(ctx context.Context) error {
    d.once.Do(func() {
        d.err = d.migrateUp(ctx)
    })
    return d.err
}
```

No separate migration step is needed. The schema is always current when the process starts.

### Concurrent boots

Several pods (server, consumers, publishers) boot at once after a deploy, and each runs `goose.Up()`. Every migration run holds a PostgreSQL session advisory lock (`postgres.MigrationLockID`, goose's default id) through goose's `WithSessionLocker`, so the runs serialise at the database: the first applies the pending migrations, the others wait and then find nothing left to apply. A waiting boot retries every 5 seconds for up to 30 minutes before giving up, long enough for a concurrent index build on the full `characters` table. `TestMigrateUpWaitsForTheDatabaseMigrationLock` holds the lock from another session and checks that migrating waits for it.

**Slow first boots after a deploy.** A migration that builds an index `CONCURRENTLY` on `characters` runs inside that first boot, so the first pod to start after the deploy is not ready until the build finishes, and the others wait on the lock behind it. On the 35M-row production table this takes minutes: `00025` (`idx_characters_max_job_level`) and `00028` (`idx_characters_fc_live`) both do. Give the readiness and startup probes enough headroom for that boot (or roll the deploy one pod first), and expect the waiting pods to log nothing until the lock is released.

### Migration log

| Version | Description |
|---------|-------------|
| `00013` | Drops `avatar_url` and `portrait_url` columns from `characters`. These fields are no longer extracted from Lodestone or stored. Down migration re-adds both columns as `TEXT`.
| `00014` | Adds the singleton persistent cursor used by automatic ID sweeps.
| `00015` | Adds the denormalized `characters.max_job_level` value and the single-row `ui_stats_snapshots` JSONB read model. Existing character job levels are backfilled during migration.
| `00016` | Widens the `ui_stats_snapshots` schema-version check to `2` so the daily new-character series can carry the 60-day comparison window. Stored v1 snapshots are rejected by readers until the next refresh.
| `00017` | Adds the proxy scan-scheduling evidence columns (`general_healthy`, completion/verification/deadline and cooldown timestamps, recovery step, observation version, lease token/version) plus four partial indexes for queue eligibility and freshness reads. No success is backfilled: every proxy starts unproven and becomes fresh only after a new scan. Down migration drops the indexes and columns in reverse order.
| `00019` | Adds two partial indexes on `characters` for the `publish character-census` selections: `idx_characters_live_census (last_census_at NULLS FIRST, id) WHERE deleted_at IS NULL` and `idx_characters_deleted_at (deleted_at, id) WHERE deleted_at IS NOT NULL`. Both are created with `CREATE INDEX CONCURRENTLY` (the file is `-- +goose NO TRANSACTION`), so writes to `characters` continue during the build. If a concurrent build is interrupted it leaves an `INVALID` index: drop it and run the migration again. Down migration drops both concurrently.
| `00020` | Records the recovery-queue indexes `idx_proxies_recovery_claim` and `idx_proxies_recovery_recent` (created by hand on production on 2026-10-12; `IF NOT EXISTS`) and drops the unused `idx_proxies_available` and `idx_proxies_scan_priority`. Runs `NO TRANSACTION` with `CONCURRENTLY` so it does not block writers on `proxies`. Down migration recreates the two legacy indexes and drops the recovery indexes.
| `00021` | Adds the nullable `proxies.retired_at` (retired marker) and `proxies.last_listed_at` (last provider listing) columns and the partial index `idx_proxies_background_live` on `(last_completed_at NULLS FIRST, id) WHERE NOT general_healthy AND retired_at IS NULL`, the live candidates of the background scan. Columns are added without defaults (catalog-only, no table rewrite) and the index is built `CONCURRENTLY`, so the migration runs `NO TRANSACTION` and every statement is `IF NOT EXISTS`. It deliberately retires no existing row: the batched `proxy scan` / `proxy retire` sweep does that (see [proxy.md](proxy.md#retirement-and-revival)). `idx_proxies_background_completed` is kept for replicas still running the previous claim. Down migration drops the index and both columns. A `CREATE INDEX CONCURRENTLY` interrupted midway leaves an invalid index that `IF NOT EXISTS` will not rebuild, so drop `idx_proxies_background_live` and rerun in that case.
| `00024` | Adds `proxy_list_validators (url PRIMARY KEY, etag, last_modified, updated_at)`, the `ETag` / `Last-Modified` of each proxy provider list that discovery last read completely, so `proxy discover` can send a conditional GET and skip unchanged lists (see [proxy.md](proxy.md#discovery)). Down migration drops the table. |
| `00025` | Adds `ui_stats_snapshots.watermark_at` (the incremental refresh watermark) and the partial index `idx_characters_max_job_level (max_job_level) WHERE max_job_level > 0`, built `CONCURRENTLY` (`NO TRANSACTION`). The build scans the whole `characters` table at the first boot after deploy and can take minutes on production; see [Concurrent boots](#concurrent-boots). |
| `00026` | Adds the nullable `characters.jobs_levelled_mask` and `characters.jobs_at_cap_mask` [job bitmasks](census.md#job-bitmasks) (no default: catalog-only, no table rewrite, no backfill) and the singleton `job_mask_backfill_state` cursor. Existing rows stay NULL until the operator runs `backfill job-masks`; the migration never fills them. Down migration drops the table and both columns. |
| `00028` | Replaces the full `idx_characters_fc (fc_id)` with the slim partial `idx_characters_fc_live (fc_id) INCLUDE (world, latest_achievement_at) WHERE deleted_at IS NULL AND fc_id IS NOT NULL`, read by the nightly Free Company pass as one index-only scan. Every other `fc_id` lookup (the census API `free_company_id` filter and the achievement filters) already restricts to live characters, so it uses the partial index too. Built and dropped `CONCURRENTLY` (`NO TRANSACTION`, `IF [NOT] EXISTS`) at the first boot after deploy: on the 35M-row table the build takes minutes, so allow for it in the readiness probes (see [Concurrent boots](#concurrent-boots)); an interrupted build leaves an `INVALID` index that `IF NOT EXISTS` will not rebuild, so drop it and rerun. Down migration recreates the full index and drops the partial one. |
| `00030` | Widens the `ui_stats_snapshots` schema-version check to `1, 2, 3, 4` for snapshot schema v4 (the recorded new-characters window, a whole-game daily series over two windows and per-world window totals). Readers still accept stored v2 and v3 snapshots; the next refresh writes v4. A v3 binary rejects a v4 snapshot, so during a rolling deploy old pods serve their last snapshot frozen, or the "Statistics are being prepared" page when cold, until they are replaced: deploy all web pods together (a normal rollout is fine); the cron refresh writes v4 after. Constraint only on a one-row table, no table rewrite. Down migration deletes a stored v4 snapshot and restores `1, 2, 3`. |
| `00029` | Widens the `ui_stats_snapshots` key check to `('current', 'free_companies')`: the Free Company boards live in their own row. Constraint only. Down migration deletes the `free_companies` row and restores `snapshot_key = 'current'`. |
| `00027` | Widens the `ui_stats_snapshots` schema-version check to `1, 2, 3` for snapshot schema v3 (Grand Company groups and the later dashboard sections). Readers still accept stored v2 snapshots; the next refresh writes v3. Constraint only, no table rewrite. Down migration deletes a stored v3 snapshot and restores `1, 2`. |

### Aggregate read model

The UI and aggregate REST endpoints load primary key `1` from `ui_stats_snapshots`; request handlers do not run census-wide aggregates. `refresh ui-stats` computes a snapshot (`--mode=full`, or `--mode=incremental` from the previous snapshot and its `watermark_at`) in a repeatable-read transaction, serializes it, then atomically replaces that row. PostgreSQL advisory locking prevents overlapping refreshes from competing.

The full refresh reads `characters` in one statement (besides the milestone query): a plain `GROUP BY` over live characters at the fine grain (region, datacenter, world, race, tribe, gender, Grand Company) with filtered counts (total, active, at cap, achievements private and achievements unchecked), planned as one sequential scan feeding a HashAggregate. The full refresh then runs the Free Company pass (`ui_stats_free_companies.go`): `GROUP BY fc_id` over `idx_characters_fc_live`, planned as an Index Only Scan feeding a streaming GroupAggregate (hash aggregation, sequential and bitmap scans are switched off for that last query of the transaction, so the plan cannot drift into holding every company in memory). Go keeps two bounded top-100 heaps per world, reads names for the kept companies only, and writes them to the `free_companies` row in the same transaction as `current`. Every scope and dimension is rolled up from those rows in Go; the incremental delta and active queries use the same grain and roll-up (see [ui.md](ui.md)).

`characters.max_job_level` is maintained by every character upsert. This removes the former correlated `character_jobs` membership check from max-level counts: the refresh scans the character row directly with `max_job_level >= $maxLevel`. At tens of millions of characters, refresh cost still grows with the source tables, but web request cost remains bounded by the snapshot payload rather than census cardinality.

`characters.jobs_levelled_mask` and `characters.jobs_at_cap_mask` are maintained the same way, so job figures can be summed from the character row without reading `character_jobs`. Rows stored before migration `00026` are filled by the throttled, resumable `backfill job-masks` command (see [census.md](census.md#job-bitmasks)), which is the only job-table reader outside the census write path. The full UI statistics refresh sums them in its single pass over `characters` (one levelled and one at-cap filtered count per job bit, plus the covered count); no refresh reads `character_jobs`.

The opt-in scale regression test is:

```bash
UI_STATS_SCALE_ROWS=1000000 go test -run TestUIStatsRefreshScale -count=1 -v ./infrastructure/postgres/repository
```

On the 2026-08-24 development run, the one-million-character refresh phase completed in 1.49 seconds and produced a 7,168-byte payload (7.44 seconds including data generation and cleanup). This validates bounded request behavior and the query shape at one million rows; it is not a substitute for a full 80–90 million-row staging benchmark. Before scaling production to that size, record refresh duration, PostgreSQL temporary-file use, CPU/I/O, ingest impact, and snapshot size on production-like hardware.

## The DatabaseDriver Contract

The `port/contract.DatabaseDriver` interface defines the surface area:

```go
type DatabaseDriver interface {
    Acquire(ctx context.Context) (*sql.DB, error)
    Close() error
    Execute(ctx context.Context, query string, args ...any) (sql.Result, error)
    FetchOne(ctx context.Context, query string, args ...any) (*sql.Row, error)
    FetchMany(ctx context.Context, query string, args ...any) (*sql.Rows, error)
    MigrateUp(ctx context.Context) error
    MigrateDown(ctx context.Context) error
}
```

- **`Acquire`** returns the underlying `*sql.DB` for code that needs direct access (e.g., transactions).
- **`Execute` / `FetchOne` / `FetchMany`** are convenience wrappers for common query patterns.
- **`MigrateUp` / `MigrateDown`** apply or reverse goose migrations.
- **`Close`** shuts down the database handle.

Infrastructure code in `infrastructure/postgres/driver.go` implements this contract. Domain and application code depend only on the interface.

## Container Wiring

The `container.Load.Postgres()` accessor lazily constructs the driver:

```go
func (s *ServiceContainer) Postgres() contract.DatabaseDriver {
    // ... reads config, passes embedded migrations FS, caches the instance
}
```

First call opens the database, runs migrations, and applies pool settings. Subsequent calls return the cached driver.

## Adding a New Migration

1. Create a new file in `infrastructure/postgres/migration/query/` following goose naming: `NNNNN_description.sql`.
2. Add `-- +goose Up` and `-- +goose Down` sections with `-- +goose StatementBegin` / `-- +goose StatementEnd` delimiters.
3. Commit. The next binary start applies it automatically.

Example:

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE characters (
    id    SERIAL PRIMARY KEY,
    name  TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE characters;
-- +goose StatementEnd
```
