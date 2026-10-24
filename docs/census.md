# Census Domain Model

This document describes the census data model and the persistence layer that stores it. The census ingests FINAL FANTASY XIV character data scraped from The Lodestone and stores it in PostgreSQL.

## Tables

All timestamps are stored as TEXT in UTC `"2006-01-02T15:04:05.000Z"` (millisecond precision).

### `characters`

One row per Lodestone character. `id` is the Lodestone character ID (externally assigned, not auto-incremented).

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | Lodestone character ID |
| `name` | TEXT NOT NULL | empty `''` until a full census has run |
| `world`, `datacenter`, `region` | TEXT NOT NULL | world / datacenter names; `region` derived from datacenter (NA/EU/JP/OCE) |
| `race`, `tribe`, `grand_company` | TEXT (nullable) | absent until fully censused |
| `gender` | INTEGER | 0 = none, 1 = male, 2 = female |
| `fc_id`, `fc_name` | TEXT (nullable) | NULL = not in a free company |
| `achievements_private` | INTEGER | 1 when the character hides achievements |
| `achievements_checked_at` | TIMESTAMPTZ (nullable) | when an achievement census last found the character private, i.e. when `achievements_private` was last verified; NULL until then. Written only by the achievement census (migration `00022`); a character refresh never touches it |
| `latest_achievement_id`, `latest_achievement_at` | nullable | globally latest earned achievement and when, from the Lodestone achievement list |
| `first_seen_at` | TEXT NOT NULL | first discovery time |
| `last_census_at` | TEXT (nullable) | NULL until a full census has run |
| `deleted_at` | TEXT (nullable) | set when Lodestone returns 404 (character gone) |
| `max_job_level` | SMALLINT NOT NULL | highest stored class/job level, maintained by every upsert |
| `jobs_levelled_mask`, `jobs_at_cap_mask` | BIGINT (nullable) | the two [job bitmasks](#job-bitmasks): jobs levelled and jobs at their level cap. NULL = not computed yet (rows stored before migration `00026`, until `backfill job-masks` reaches them) |

**Discovery:** the `id-sweep` handler collects every character it finds in its range (profile + jobs), hands them to the per-pod batching writer as one result and waits for the batch to commit before chaining the dependent `achievement-census` jobs. `last_census_at` is set by the upsert; `latest_achievement_at` stays NULL until the achievement census runs.

**Stale selection (`publish character-census`):** the publisher reads two id-only selections from `CharacterRepository` and publishes one `character-census` job per id.

- `ListStaleIDs(cutoff, limit)` returns up to `limit` **live** characters (`deleted_at IS NULL`) ordered by `last_census_at ASC NULLS FIRST, id ASC`. A zero `cutoff` disables the age predicate; a positive `cutoff` filters to rows whose `last_census_at` is before the cutoff (NULL `last_census_at` counts as stale in both modes). Deleted characters are never part of this selection, so the head of the queue is not held by characters that no longer exist. The partial index `idx_characters_live_census (last_census_at NULLS FIRST, id) WHERE deleted_at IS NULL` serves the order, so the query reads `limit` index entries instead of sorting the table. With a positive cutoff the age predicate is applied as an index filter in the same order: it stops after `limit` matches, but scans the rest of the index when fewer rows are stale.
- `ListDeletedIDs(deletedBefore, limit)` returns up to `limit` deleted characters whose `deleted_at` is before `deletedBefore`, longest-deleted first (`deleted_at ASC, id ASC`), served by the partial index `idx_characters_deleted_at (deleted_at, id) WHERE deleted_at IS NOT NULL`. This is the re-probe batch that keeps resurrection self-healing: a character that is back on the Lodestone is stored again and `Upsert` clears its `deleted_at`; one that is still missing is marked deleted again with a fresh `deleted_at`, so it is not selected again for `--deleted-older-than`.

The command publishes the live selection first and then the deleted batch. `--deleted-limit` (default 10) caps the deleted characters per run and `0` disables the re-probe; `--deleted-older-than` (default `720h`, 30 days) is how long a character must have been deleted before it is re-probed. With the production cron every 2 minutes the defaults re-probe at most 7,200 deleted characters a day, each at most once every 30 days. Both selections read only `id`.

### `character_jobs`

One row per (character, class/job) pair: `character_id`, `class_job_id`, `name`, `level`, `exp_level`. Primary key is `(character_id, class_job_id)`; the character repository replaces the whole set on each upsert. The `class_job_id` values come from the Lodestone name→ID lookup table (see `lodestoneJobIDs` in `infrastructure/lodestone/client.go`).
### Job bitmasks

Each character stores which jobs it has levelled (level 1 or more) and which it has at their level cap as two 64-bit masks, so the statistics refresh can count jobs without reading `character_jobs`. Bit `n` is the official job id `n` after folding base classes into their jobs (Gladiator → Paladin, Pugilist → Monk, Marauder → Warrior, Lancer → Dragoon, Archer → Bard, Conjurer → White Mage, Thaumaturge → Black Mage, Arcanist → Summoner, Rogue → Ninja). Arcanist leads to both Summoner and Scholar; it counts towards Summoner. Every job's cap is the configured `census.max_level`, except Blue Mage, a limited job capped at 80 (`census.BlueMageLevelCap`).

The rules live in the census taxonomy (`domain/census/jobs.go`: `Jobs`, `JobByID`, `FoldJobID`, `Roles`, `Job.LevelCap`, `JobMasks`), shared by the write path, the backfill and the pages. The character write path sets both masks whenever it stores a character's jobs, exactly like `max_job_level`; a character with no jobs gets `0`, `0`.

Rows stored before the columns existed stay NULL until the operator runs the backfill once, during quiet hours:

```bash
./bin/ffxiv-census backfill job-masks --batch-size 1000 --pause 200ms
```

It walks characters by ascending ID in keyset batches, one short transaction per batch, filling only NULL masks from the stored jobs (already-set masks are left alone) and storing its cursor in `job_mask_backfill_state` with each batch. Stopping it (Ctrl-C) is safe: a rerun resumes from the cursor. It is not a migration and never runs at boot; characters re-censused meanwhile get their masks from the normal write path.

### `character_gear`

One row per equipped gear slot: `(character_id, slot)` primary key, `item_id`, `name`, `item_level`, `dye`, `materia` (JSON array of materia names/tiers), `updated_at`. Stores equipped gear pieces scraped from character profiles.

### `milestone_achievements`

The registry of achievements the census tracks is data-driven — add a row to track a new milestone. `achievement_id` is the game achievement ID, not a Lodestone playguide URL slug. `kind` is one of `expansion_msq`, `job_level`, `chocobo`. `expansion` is non-null only for `expansion_msq` milestones. `detail` is a human-readable description.

### `character_milestones`

A character's earned milestones: `(character_id, achievement_id)` primary key plus `achieved_at`.

### `census_runs`

Operational tracking of census sweeps: `started_at`, `finished_at`, `characters_seen`, `new_characters`.

## Milestone achievement IDs

Achievement IDs are the **game** achievement IDs (small sequential integers), verified against the XIVAPI Achievement sheet (`/api/sheet/Achievement/{id}`) and ffxivcollect's achievement data — not the hex slugs used in Lodestone `playguide/db/achievement/...` URLs.

The milestone registry is driven dynamically by `config.toml` (`[census.expansions]`) alongside the foundational Chocobo milestone and is synced to the `milestone_achievements` table via `CensusService.SyncMilestones` (idempotent `INSERT OR IGNORE`).

Default entries:

| Kind | ID | Expansion | Detail |
|---|---|---|---|
| chocobo | 590 | — | My Little Chocobo |
| expansion_msq | 1129 | A Realm Reborn | My Left Arm |
| expansion_msq | 1139 | Heavensward | Looking Up |
| expansion_msq | 1794 | Stormblood | The Measure of His Reach |
| expansion_msq | 2298 | Shadowbringers | Shadowbringers |
| expansion_msq | 2958 | Endwalker | That Its Chorus Might Ring for All |
| expansion_msq | 3496 | Dawntrail | In the Glow of a New Dawn |

Achievement census always requests the achievement list once to refresh the global latest activity timestamp and check privacy. It then requests only missing entries from this ordered chain, preserves prior rows during upsert, and stops at the first missing entry that is not complete. Already stored checkpoints are skipped even across historical gaps. Dates on historical rows are not backfilled; newly discovered earned rows use the date parsed from the completed achievement HTML row.

### Private characters

About 94 % of characters hide their achievements, so their achievement census would otherwise ask the Lodestone for a list page that only ever answers 403. A character flagged `achievements_private` whose flag was verified (`achievements_checked_at`) less than `[census] private_recheck_days` ago (default 30; env `CENSUS_PRIVATE_RECHECK_DAYS`) is left alone:

- The `achievement-census` handler checks this first, before the rate-limit wait and any Lodestone request, and returns success, so the worker acks the job. This also drains jobs already queued for such characters.
- The `character-census` handler does not chain an achievement census for such a character. The `id-sweep` handler chains as before: it mostly discovers characters with no flag yet, and any chained job for a known private character is skipped by the rule above.
- A character that is not flagged private is censused every time, and so is one with no row. A character flagged private with `achievements_checked_at` NULL (never verified, including every flag stored before migration `00022`) is **not** sent to the Lodestone: its `achievement-census` job trusts the flag and stamps `achievements_checked_at` with the current time through the batching writer (one cheap write, once per character), and the `character-census` handler still chains that job so the stamp gets written. From then on the rule above applies, so the first real verification happens `private_recheck_days` after first sight, and a public result then clears the flag.

Migration `00022` adds the nullable column without a default, a catalog-only change. It does not backfill: the roughly 32.5 million characters already flagged private are stamped lazily, one write each, the first time their `achievement-census` job runs after deploy, and cost no Lodestone request. No operator backfill is needed.

Milestone rows are sparse by design and their maximum `character_id` need not
match `characters.MAX(id)`. Private achievement histories and public characters
that have not earned the first missing milestone produce no new row.
## Repositories

Four contracts in `port/contract`, each with a PostgreSQL implementation in `infrastructure/postgres/repository/` and an in-memory fake in `mock/repository/`:

- **`CharacterRepository`** — `Upsert` (one character + jobs atomically; a thin wrapper over `StoreResult`, kept for tests, tools and fixtures — no census handler calls it), `StoreResult`, `Get`, `GetJobs`, `UpsertGear`, `GetGear`, `FindIDGaps`, `MarkDeleted`, `ListStaleIDs`, `ListDeletedIDs`, `List`, `Count`, `CountActive`, `Breakdown`, `NewPerDay`, `MaxID`. The complete persistence and query contract for character data.
- **`CharacterRepository.StoreResult`** — stores a whole `contract.CensusResult` in one transaction (all or nothing): the characters and jobs (one multi-row upsert for all characters, one jobs delete and one jobs upsert, shared by the whole batch; the last record of a repeated ID wins), then each `AchievementWrite` (milestones upserted additively, then the latest-achievement summary; a private profile only flips `achievements_private`, records `achievements_checked_at` and keeps earlier milestones). `StoreResult` is the only way census data reaches the database: the batching writer calls it, and `Upsert` delegates to it. A character with its jobs, and an achievement census with its milestones and latest achievement, always commit together. The `character-census` and `achievement-census` handlers store through the writer (`Service.PrepareCharacter` / `Service.PrepareMilestoneResults` build the write) and return only after the batch commits; a handler built without a writer fails its job with `handler.ErrNoWriter` (and so retries) instead of writing on its own, so there is no second, per-job write path. Marking a character deleted (Lodestone 404) is a single-row update and stays direct.
- **`AchievementRepository`** — `SyncMilestones` (idempotent registry upsert), `ListMilestones`, `UpsertCharacterMilestones` (batch multi-row INSERT, single round-trip), `ListCharacterMilestones`, `CountExpansions`, `CountExpansionsFiltered`, `NewCharactersPerDay`, `CountChocoboMilestones`.
- **`CensusRunRepository`** — `Start`, `Finish`.
- **`UIStatsRepository`** — `LoadCurrent` and `Refresh` for the versioned aggregate read model. Refresh uses a bounded number of census-wide queries outside the HTTP request path and publishes the result atomically.

Repositories are resolved via the service locator (`container.Load.CharacterRepository()`, etc.), which builds them from the shared `DatabaseDriver`.

### CharacterFilter

The `CharacterFilter` struct (`port/contract/character_repository.go`) controls `List`, `Count`, `Stream`, and `Breakdown` queries:

| Field | Type | Effect |
|---|---|---|
| `World`, `Datacenter`, `Region`, `Race`, `GrandCompany`, `FreeCompanyID` | `string` | Exact match (ignored when empty) |
| `Name` | `string` | Case-insensitive substring (`ILIKE`) |
| `ActiveOnly` | `bool` | Adds `deleted_at IS NULL`. Without `Since`, this is redundant with the base query which already excludes deleted characters. |
| `Since` | `*time.Time` | When non-nil, only characters with `latest_achievement_at >= Since` are returned (activity window filter). This is the proper way to filter by "recently active". |
| `MinLevel` | `uint32` | When > 0, only characters with at least one persisted job at or above this level are returned. Aggregate summary refreshes use the denormalized `max_job_level`, which character upserts maintain atomically with `character_jobs`. |
| `SortBy` | `string` | Column to sort by: `"id"`, `"name"`, `"world"`, `"created_at"`, `"updated_at"`, `"achievement_points"` |
| `SortOrder` | `string` | `"asc"` (default) or `"desc"` |

**Active filtering**: To filter by the activity window (e.g. "active in last 30 days"), set `Since` to the window start time. `ActiveOnly` alone does not include the activity window — it only checks `deleted_at IS NULL`.

## CensusService

`domain/census/service.go` is the domain brain: it converts Lodestone DTOs into persisted records and computes milestone/activity facts. Constructed via `container.Load.CensusService()` with the three repositories; the ingest handlers call it. The service caches the milestone registry in memory (5-minute TTL) to avoid re-querying the DB on every achievement census; `SyncMilestones` invalidates the cache.

- `SyncMilestones(ctx)` — seeds configured expansion milestones and chocobo achievement into the DB (idempotent). Invalidates the in-memory milestone cache so the next `PrepareMilestoneResults` picks up the fresh registry.
- `PrepareCharacter(*contract.CharacterProfile)` — validates a Lodestone character and converts it, with its jobs, into the `contract.CharacterWrite` a handler hands to the writer; nothing is stored here. `region` is derived from the datacenter via `RegionForDatacenter` (table below). A profile whose race is empty or `----` returns `ErrProfileHidden` and is **not** persisted (hidden profile — see below); an existing row keeps its last known good data.
- `PrepareMilestoneResults(ctx, charID, summary)` — filters earned milestones against the registry and builds the `contract.AchievementWrite` (milestones, private flag, global latest achievement from the list page) a handler hands to the writer; nothing is stored here. Uses the in-memory milestone cache (5-minute TTL) to avoid re-querying the DB on every call.
- `AchievementCensusCheck(ctx, charID)` — returns `AchievementCheckSkip` for a character flagged private whose flag was verified within the private recheck window, `AchievementCheckStamp` for one flagged private but never verified, and `AchievementCheckDue` otherwise (also with the error when the lookup fails). The `achievement-census` handler skips on Skip, writes the stamp without a Lodestone request on Stamp and fetches on Due; the `character-census` handler chains the job unless the answer is Skip (see "Private characters").
- `SetPrivateRecheckWindow(d)` — overrides the private recheck window (default 30 days, `[census] private_recheck_days`); a no-op for `d <= 0`.
- `MaxCharacterID(ctx)` — returns the highest known character ID in the repository (excluding deleted characters), used for auto-discovery sweeps.
- `MilestoneIDs(ctx)` — returns the set of tracked milestone achievement IDs from the cached registry. Useful for handler-level pre-filtering.
- `IsActive(latestAt)` — true when the globally latest public achievement is within the activity window (default 30 days, configurable via `SetActivityWindow` / `[census] activity_window_days`). This is an achievement-based signal, not direct login tracking.
- `SetActivityWindow(d)` — overrides the activity window; a no-op for `d <= 0`.
- `Summary(ctx)` — total, active, and max-level character counts (`total, active, maxLevelCount, err`) for internal/direct callers. Public aggregate routes use `UIStatsService` instead.
- `ListCharacters(ctx, filter, limit, offset)` — one page of characters matching `filter` plus the matching count (the HTTP pagination/filtering source).
- `CharacterDetail(ctx, id)` — character plus jobs and milestones, with the free company when the character is in one; `nil` when the id is unknown.
- `WorldDetail(ctx, worldName)` — full census stats for a specific world, returned as `WorldDetailStats` (total population, active players, new characters in last 30 days, race breakdown, MSQ completions, 30-day new-character timeline, and a sample character). Fans out seven database queries concurrently and joins results with deterministic error precedence.
- `Breakdown(ctx, by)` — per-`race`/`world`/`datacenter`/`region` totals and active counts; any other dimension returns `ErrInvalidDimension`.
- `NewCharacters(ctx, since, until)` — characters who earned the Chocobo milestone (achievement 590) per UTC day in `[since, until)`. The Chocobo milestone is the canonical definition for "new character" as it indicates the character has started playing.
- `ExpansionCompletions(ctx)` — distinct characters per expansion that completed that expansion's MSQ.


### Hidden profiles

Three Lodestone states leave a character without usable race data. The census skips such characters on every ingest path (`ErrProfileHidden` from `PrepareCharacter`, mapped to a skip by the handlers — no store, no chained jobs, no queue error):

| State | Lodestone response | Stored shape (now rejected) |
| ------ | ------------------ | --------------------------- |
| Private profile | HTTP 200, "This character's profile is private" — no race block at all | `race=''`, `gender=0` |
| Access-restricted profile | HTTP 403 "Access Restricted" (deterministic per character) | `race=''`, `gender=1` |
| Suppressed race/clan | HTTP 200, race and clan rendered as `----` placeholders | `race='----'`, `tribe='----'` |

If a hidden profile later becomes public, the next census of that character stores it normally; while it stays hidden, the existing row is left untouched.

## UIStatsService

`domain/census/ui_stats_service.go` is the only aggregate-data source wired into production UI and statistics API routes. It validates schema version and metadata, caches an immutable snapshot for the configured TTL, coalesces concurrent reloads, and defensively clones data for callers. A failed warm reload serves the last known-good snapshot and reports it stale until a reload succeeds; a routine background reload after `cache_ttl` serves the cached copy without marking it stale (only a failed reload or an age of at least `stale_warning` does). A missing cold snapshot becomes a fast 503 rather than triggering raw fallback queries.

The snapshot contains only the dimensions consumed by routes: global summary; global/region/datacenter/world population groups; scoped race, tribe, gender, and race×gender groups; scoped expansion completions; and global/world daily new-character counts. Adding a new aggregate page requires extending the versioned snapshot and refresh query, not adding census-wide SQL to a request handler.

**DC→region mapping** (`domain/census/region.go`):

| Region | Datacenters |
|---|---|
| NA | Aether, Primal, Crystal, Dynamis |
| EU | Chaos, Light |
| JP | Elemental, Gaia, Mana, Meteor |
| OCE | Materia |
