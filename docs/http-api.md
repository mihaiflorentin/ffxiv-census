# HTTP API

ffxiv-census exposes a small versioned read API for the ingested census data. Every endpoint is a `GET` returning JSON (`application/json`). Data endpoints live under `/api/v1/`; the unversioned `/health` probe is the only exception. The same surface is documented in the embedded Swagger 2.0 spec served at `/docs/swagger.json` by the HTTP app.

This is living documentation — when the API changes, update this file with it.

## Conventions

### Errors

All errors use a single envelope:

```json
{"error": "message"}
```

| Status | Meaning |
|---|---|
| 400 | Bad request — missing/invalid query or path parameter |
| 404 | Resource not found — unknown character id (`{"error": "character not found"}`) |
| 500 | Internal error — database failure or unavailable service; the message is the underlying error |
| 503 | Aggregate statistics snapshot is not yet available; retry after the refresh command completes |

### Pagination

`GET /api/v1/census/characters` returns a pagination envelope:

```json
{"items": [], "total": 1234, "limit": 100, "offset": 0}
```

- `items` — the requested page of `CharacterListItem`s.
- `total` — total number of non-deleted characters, independent of `limit`/`offset`.
- `limit` — the applied page size (default 100, maximum 500).
- `offset` — the applied offset (default 0).

### Timestamps

Times are RFC 3339 UTC (Go `time.Time` marshaling). The exception is `NewCharactersDay.day`, a plain `YYYY-MM-DD` string naming a UTC day.

### Activity window

"Active" means the character's globally latest public achievement (`latest_achievement_at`) falls within the activity window. It is an achievement-based signal rather than a direct login or subscription measurement. Characters without a visible achievement (`latest_achievement_at` NULL) are never active. The window is configured under `[census]`:

```toml
[census]
activity_window_days = 90
```

Environment override uses the same rule as `[postgres]` (dots become underscores, section name is the prefix): `CENSUS_ACTIVITY_WINDOW_DAYS=45`.

The default is 90 days because a full census pass takes months: a character's latest achievement is only as recent as the census's last visit, so a short window undercounts active players.

The window drives `active_characters`/`active_ratio` on `GET /api/v1/census/latest` (which also reports it as `activity_window_days`, the window the snapshot was built with), the `active` count on `GET /api/v1/stats/breakdown`, the `is_active` flag on `GET /api/v1/census/characters` and `GET /api/v1/census/characters/{id}`, and the `is_active` column of `GET /api/v1/census/export` and the `export` CLI command. A changed window applies to the snapshot figures from the next refresh.

### New-characters window

A new character is one that earned the Chocobo milestone (achievement 590). The new-character figures count the new-characters window, compared with the window of the same length just before it:

```toml
[census]
new_characters_window_days = 90
```

Environment override: `CENSUS_NEW_CHARACTERS_WINDOW_DAYS=60`. Both windows are whole UTC days and end with the last complete day before the snapshot's generation day. The default is 90 days for the same reason as the activity window: the census sees a new chocobo only when it next visits the character, so recent days are undercounted. The window drives `GET /api/v1/stats/new-characters` (its default range, and the oldest day it serves) and every new-character figure in the UI. A changed window applies from the next refresh: an incremental refresh on a snapshot built with another window rebuilds in full.

### Aggregate snapshot

`/api/v1/census/latest` and `/api/v1/stats/*` read the same versioned snapshot as the dashboard. These request paths do not scan the census tables. The snapshot is refreshed independently with `ffxiv-census refresh ui-stats`; responses can lag live ingestion by the refresh interval. If no valid snapshot has ever been published, these routes return 503 instead of falling back to expensive queries.

## Endpoints

| Method | Path | Query / path params | Success shape | Error shape |
|---|---|---|---|---|
| GET | `/health` | — | `{"status": "ok"}` | — |
| GET | `/api/v1/census/latest` | — | `CensusSummary` | 500, 503 |
| GET | `/api/v1/census/characters` | `limit` (int, default 100, max 500), `offset` (int, default 0), `world` (string), `datacenter` (string), `region` (string), `race` (string), `name` (string) | `PaginatedCharacters` | 400 (invalid `limit`/`offset`), 500 |
| GET | `/api/v1/census/characters/{id}` | `id` (path, uint32 Lodestone character id) | `CharacterDetail` | 400 (invalid id), 404 (not found), 500 |
| GET | `/api/v1/census/export` | Streaming data export (CSV/JSON/NDJSON, optional Gzip) | query: `format`, `gzip`, `world`, `datacenter`, `region`, `race`, `name` | |
| GET | `/api/v1/stats/breakdown` | `by` (required, `race`\|`world`\|`datacenter`\|`region`) | `[BreakdownGroup]` | 400 (missing/unknown `by`), 500, 503 |
| GET | `/api/v1/stats/new-characters` | `since` (optional, `YYYY-MM-DD`, default the start of the new-characters window), `until` (optional, `YYYY-MM-DD`, default the snapshot day) | `[NewCharactersDay]` | 400 (invalid or outside the snapshot's two windows), 500, 503 |
| GET | `/api/v1/stats/expansion` | `name` (optional, exact match) | `[ExpansionStat]` | 500, 503 |
| GET | `/sitemap.xml` | — | Sitemap-protocol XML document (`application/xml`) | — (degrades to static URLs, never 503) |

### GET /health

Liveness probe.

```json
{"status": "ok"}
```

### GET /api/v1/census/latest

Total, active, and max-level character counts plus the active ratio (`active / total`; `0` when `total` is `0`). `activity_window_days` is the [activity window](#activity-window) behind `active_characters`, in days, as the snapshot was built.

```json
{
  "total_characters": 1234,
  "active_characters": 900,
  "active_ratio": 0.7293,
  "max_level_characters": 450,
  "activity_window_days": 90
}
```

### GET /api/v1/census/characters

One page of characters. Supports optional AND-combined filters: `world`, `datacenter`, `region`, `race` (exact match), and `name` (case-insensitive substring). `limit` defaults to 100 and is clamped to 500; `offset` defaults to 0. Missing/empty parameters fall back to the defaults (no filter / full list); non-numeric or non-positive `limit` (<= 0) and negative `offset` (< 0) are rejected with 400.
```json
{
  "items": [
    {
      "id": 36795950,
      "name": "Example Name",
      "world": "Louisoix",
      "datacenter": "Chaos",
      "region": "EU",
      "race": "Miqo'te",
      "gender": 1,
      "free_company_id": "9234567890123456789",
      "free_company_name": "Example Free Company",
      "achievements_private": false,
      "latest_achievement_id": 1139,
      "first_seen_at": "2026-08-17T10:00:00Z",
      "last_census_at": "2026-08-17T10:05:00Z"
    }
  ],
  "total": 1234,
  "limit": 100,
  "offset": 0
}
```

The nullable fields `free_company_id`, `free_company_name`, `latest_achievement_id`, and `last_census_at` are omitted when null.

### GET /api/v1/census/characters/{id}

Full detail for one character. A non-numeric id is a 400 (`{"error": "invalid character id"}`); an unknown id is a 404 (`{"error": "character not found"}`).

```json
{
  "character": {
    "id": 36795950,
    "name": "Example Name",
    "world": "Louisoix",
    "datacenter": "Chaos",
    "region": "EU",
    "race": "Miqo'te",
    "gender": 1,
    "free_company_id": "9234567890123456789",
    "free_company_name": "Example Free Company",
    "achievements_private": false,
    "first_seen_at": "2026-08-17T10:00:00Z",
    "last_census_at": "2026-08-17T10:05:00Z"
  },
  "jobs": [
    {"class_job_id": 21, "name": "Paladin", "level": 90, "exp_level": 204735}
  ],
  "milestones": [
    {"achievement_id": 1139, "achieved_at": "2026-08-17T10:04:00Z"}
  ],
  "free_company": {
    "id": "9234567890123456789",
    "name": "Example Free Company",
    "world": "Louisoix",
    "datacenter": "Chaos",
    "member_count": 128
  }
}
```

`free_company` is omitted when the character is not in a free company. `jobs` and `milestones` are always arrays (possibly empty).

### GET /api/v1/stats/breakdown?by=...

Population per group, sorted by `total` descending. `by` is required; a missing parameter is a 400 (`{"error": "missing by parameter"}`) and an unknown dimension is a 400 (`{"error": "invalid breakdown dimension: want race|world|datacenter|region"}`).

```json
[
  {"key": "Louisoix", "total": 42, "active": 31},
  {"key": "Cerberus", "total": 38, "active": 25}
]
```

`active` counts characters whose `latest_achievement_at` is within the activity window (see [Activity window](#activity-window)).

### GET /api/v1/stats/new-characters[?since=YYYY-MM-DD&until=YYYY-MM-DD]

Characters who earned the Chocobo milestone (achievement 590) per UTC day in `[since, until)`, ordered ascending by day, for the whole game. Without parameters it serves the [new-characters window](#new-characters-window): its complete UTC days before the snapshot's generation day. Without `until` the range ends with the last complete UTC day (before the generation day, or before today when the server has no snapshot store). `since` and `until` must parse as dates. The snapshot keeps two windows (current and previous), so `since` may reach back to the start of the previous window (on a snapshot from before schema 4, to the first of the 60 days ending with its generation day), and `until` at most to the day after the generation day; malformed or out-of-range requests are 400s. This keeps response work bounded.

```json
[
  {"day": "2026-08-16", "count": 1},
  {"day": "2026-08-17", "count": 3}
]
```

### GET /api/v1/stats/expansion[?name=...]

How many distinct characters completed each expansion's MSQ. The optional `name` filter narrows the list to that expansion (exact match); no match returns an empty array, not a 404.

```json
[
  {"expansion": "Heavensward", "count": 2},
  {"expansion": "Stormblood", "count": 1}
]
```

### GET /sitemap.xml

Sitemap-protocol document (`application/xml; charset=utf-8`) listing every indexable page as an absolute URL: the five static pages (`/`, `/ui/races`, `/ui/worlds`, `/ui/expansions`, `/ui/methodology`) plus one `/ui/worlds/{world}` entry per world in the aggregate snapshot. Worlds without a known datacenter mapping are skipped, matching the worlds page. URLs are joined onto `[app]` `base_url`.

Every `<url>` carries `<lastmod>` with the snapshot's `generated_at` in RFC 3339 UTC. When no valid snapshot is available, the sitemap degrades to the five static URLs without `<lastmod>` and still returns 200 — a sitemap must never fail.

## See also

- `docs/census.md` — the data model behind these endpoints.
- `/docs/swagger.json` — machine-readable spec (Swagger 2.0).
