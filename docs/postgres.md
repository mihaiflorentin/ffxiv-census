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
| `proxy-id-sweep`, `proxy-character-census`, `proxy-achievement-census`, `proxy-new` | 3 / 2 | The writer flush (1) plus proxy lease and lock statements (`ClaimProxy`, `RefreshConsumerLock`, `RecordConsumerFailure`; each a short statement or transaction, issued from many handler goroutines and queued on the pool). |
| `proxy-scan` | 2 / 1 | Short claim and complete transactions only; unchanged. |
| `census-retry` | 2 / 1 | Republishes failed messages, no census writes. |

Maximum worker backends: 3 x 3 (id-sweep) + 3 (character) + 2 x 3 (achievement) + 3 (proxy-new) + 5 x 2 (scan) + 3 (census-consumer) + 2 (retry) = 36, down from 135 with the previous 15/10 pools (baseline in production: 76 of 100 connections in use). Instance `env` lists replace `workers.defaults.env` wholesale, so every instance that declares its own env must repeat the pool variables. Handlers wait on the pool when all connections are busy; they never fail for it.

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

### Migration log

| Version | Description |
|---------|-------------|
| `00013` | Drops `avatar_url` and `portrait_url` columns from `characters`. These fields are no longer extracted from Lodestone or stored. Down migration re-adds both columns as `TEXT`.
| `00014` | Adds the singleton persistent cursor used by automatic ID sweeps.
| `00015` | Adds the denormalized `characters.max_job_level` value and the single-row `ui_stats_snapshots` JSONB read model. Existing character job levels are backfilled during migration.
| `00016` | Widens the `ui_stats_snapshots` schema-version check to `2` so the daily new-character series can carry the 60-day comparison window. Stored v1 snapshots are rejected by readers until the next refresh.
| `00017` | Adds the proxy scan-scheduling evidence columns (`general_healthy`, completion/verification/deadline and cooldown timestamps, recovery step, observation version, lease token/version) plus four partial indexes for queue eligibility and freshness reads. No success is backfilled: every proxy starts unproven and becomes fresh only after a new scan. Down migration drops the indexes and columns in reverse order.
| `00019` | Adds two partial indexes on `characters` for the `publish character-census` selections: `idx_characters_live_census (last_census_at NULLS FIRST, id) WHERE deleted_at IS NULL` and `idx_characters_deleted_at (deleted_at, id) WHERE deleted_at IS NOT NULL`. Both are created with `CREATE INDEX CONCURRENTLY` (the file is `-- +goose NO TRANSACTION`), so writes to `characters` continue during the build. If a concurrent build is interrupted it leaves an `INVALID` index: drop it and run the migration again. Down migration drops both concurrently.

### Aggregate read model

The UI and aggregate REST endpoints load primary key `1` from `ui_stats_snapshots`; request handlers do not run census-wide aggregates. `refresh ui-stats` computes a complete snapshot in a repeatable-read transaction, serializes it, then atomically replaces that row. PostgreSQL advisory locking prevents overlapping refreshes from competing.

`characters.max_job_level` is maintained by every character upsert. This removes the former correlated `character_jobs` membership check from max-level counts: the refresh scans the character row directly with `max_job_level >= $maxLevel`. At tens of millions of characters, refresh cost still grows with the source tables, but web request cost remains bounded by the snapshot payload rather than census cardinality.

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
