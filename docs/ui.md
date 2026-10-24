# Web UI & Census Dashboards

`ffxiv-census` provides a server-rendered, interactive Web UI backed by a bounded statistics snapshot, pure Go templates, HTMX, vendored Chart.js, and a dark FFXIV-themed design system.

## 1. Architecture & Design Principles

- **Zero CDN Dependencies**: All CSS and JavaScript (HTMX v2.0.4, Chart.js v4.4.7 UMD) are vendored in `cmd/http/ui/assets/` and compiled directly into the binary via `//go:embed`.
- **Pure Go Rendering**: Built with Go standard library `html/template` and `net/http` ServeMux. No Node.js, Webpack, npm, or frontend build toolchains are required.
- **Hexagonal Isolation**: UI controllers live in `cmd/http/ui/` and resolve aggregate data through `container.Load.UIStatsService()`. Character list/detail routes continue to use `CensusService`; queue health uses `Queue`. Domain logic remains tech-agnostic and decoupled from HTML presentation.
- **Progressive Enhancement via HTMX**: Client-side interactions (such as regional datacenter/world drill-downs and navigation search) use HTMX partial swaps without full page reloads.
- **Bounded Request Work**: Aggregate pages never scan `characters`, `character_jobs`, or achievement tables during an HTTP request. A scheduled read-model refresh does the expensive work once, while each process serves a cached immutable snapshot.

## 2. Route Inventory

| Route | Method | Description |
|---|---|---|
| `/` | `GET` | Dashboard overview served directly at the root URL (same handler as `/ui/dashboard`) |
| `/ui/dashboard` | `GET` | Executive overview: responsive stat-card grid (total population, 30-day active ratio, new characters (30d) with trend vs the previous 30 days, ingest status), race distribution doughnut chart, expansion MSQ completion card, 30-day new-character line chart, and region summary with world drill-down. All aggregate data comes from the cached statistics snapshot. |
| `/ui/partials/world-breakdown` | `GET` | HTMX partial returning world and datacenter rows for a requested region (`?region=NA`), including each world's New (30d) count |
| `/ui/races` | `GET` | Playable race demographics with cascading region/DC/world filters, a new-characters (30d) card that follows the active filters, global percentage shares, active ratios, and demographic charts. Filtering selects precomputed snapshot groups and performs no aggregate database query. |
| `/ui/worlds` | `GET` | Global server rankings table (including a per-world New (30d) column) with interactive region/datacenter filters and a filter-scoped new-characters (30d) card |
| `/ui/worlds/{world}` | `GET` | World detail page: total population, active players (30d), new characters (chocobo milestone 590 in last 30 days, with trend vs the previous 30 days), race breakdown, MSQ completions, and 30-day new-character timeline |
| `/ui/expansions` | `GET` | MSQ story completion funnel (A Realm Reborn, Heavensward, Stormblood, Shadowbringers, Endwalker, Dawntrail) with retention and drop-off metrics |
| `/ui/characters/{id}` | `GET` | Detailed character profile with Dawntrail Lv 100 job matrix, story milestone timeline, Free Company badge, and external links |
| `/ui/characters` | `GET` | Paginated directory browser of discovered player characters |
| `/ui/characters/search` | `GET` | Global search handler: numeric IDs redirect directly to `/ui/characters/{id}`, text queries filter by character name |
| `/ui/assets/*` | `GET` | Static asset file server (`styles.css`, `htmx.min.js`, `chart.umd.min.js`, `fonts/*.woff2` as `font/woff2` with year-long immutable caching) |

### Head Metadata & Canonical URLs

Every indexable page (dashboard, races, worlds, per-world, expansions, methodology) renders SEO head
metadata from the shared `PageData` struct:

- `<link rel="canonical">` with the absolute URL built from `app.base_url` (`container.Load.Config().App.BaseURL`,
  default `https://census.ffxivbard.com`) plus the page's clean path.
- `<meta name="description">` with a concise per-page summary.
- Open Graph tags: `og:site_name` (`FFXIV Census`), `og:title`, `og:description`, `og:type` (`website`), and
  `og:url` matching the canonical URL.
- Favicon: the embedded site icon is served at `/favicon.ico` (`image/png`) and linked from every page's
  head via `<link rel="icon" type="image/png" href="/favicon.ico">`, so search results display the icon.

Filter query parameters (for example `/ui/worlds?region=NA&dc=Aether`) never appear in the canonical URL:
filtered views always canonicalize to their clean path, so crawlers index exactly one URL per page. The
root URL `/` and `/ui/dashboard` serve the same dashboard handler, and each renders its own canonical path
(`/` and `/ui/dashboard` respectively).

## 3. Statistics Snapshot

`refresh ui-stats` builds one versioned JSON read model in `ui_stats_snapshots`. It computes global, region, datacenter, and world population totals; demographic groups; expansion completion counts; and the Chocobo-milestone daily new-character series reaching back 60 days (the 30-day headline window plus the 30-day comparison window). Refreshes use a PostgreSQL advisory lock, a repeatable-read transaction, and an atomic single-row upsert, so readers see either the old complete snapshot or the new complete snapshot.

```bash
# Build/replace the current snapshot immediately
./bin/ffxiv-census refresh ui-stats
```

The Helm chart runs two CronJobs, both with `concurrencyPolicy: Forbid`: `refresh-ui-stats-full` (`--mode=full`) nightly at 01:00 Europe/Bucharest, and `refresh-ui-stats-incremental` (`--mode=incremental`) every 30 minutes (change its `schedule` in `k8s/values.yaml` to change the interval). A full rebuild scans every character (about 6 minutes at 34.5 million rows); the incremental pass does not.

**Incremental mode.** The snapshot stores a watermark (`ui_stats_snapshots.watermark_at`, the database clock when the counts were read). An incremental pass takes the previous snapshot's group totals and adds the characters with `first_seen_at` in (watermark, now] and subtracts those with `deleted_at` in the same window (soft deletes only). Active and max-level counts are recomputed exactly each pass from `latest_achievement_at` and `max_job_level` (partial index `idx_characters_max_job_level`), and the milestone figures are recomputed in full. It falls back to a full rebuild when there is no previous snapshot, the schema version or watermark is unusable, or the adjusted result fails validation. Accepted drift until the next nightly rebuild: a character whose world, datacenter, region, race or tribe changes after it was counted, a soft delete later reverted, and a character that commits after the read snapshot with a `first_seen_at` before the watermark. The advisory lock keeps the two jobs from overlapping (the later one reports "skipped"). Each server process reloads the database row no more frequently than `cache_ttl`; concurrent cold loads are coalesced. If a reload fails after a successful load, the last good snapshot remains available. A cold process with no snapshot returns `503 Service Unavailable` with `Retry-After`; there is deliberately no fallback to unbounded aggregate queries.

```toml
[census.ui_stats]
cache_ttl       = "1m"
stale_warning   = "12h"
refresh_timeout = "2h"
```

Aggregate HTML and HTMX responses include a query-aware `ETag`, `Cache-Control`, and `Vary: HX-Request, Accept-Encoding`. The page shell displays the snapshot generation time and warns when it exceeds `stale_warning`. This response contract is suitable for a future reverse proxy such as Varnish without making it a runtime dependency.

Operational checks:

```bash
./bin/ffxiv-census refresh ui-stats
curl -i http://localhost:8080/ui/dashboard
curl -I http://localhost:8080/ui/races
curl -s http://localhost:8080/metrics | grep ui_stats
```

Investigate `ui_stats_refresh_total{result="error"}`, a growing `ui_stats_snapshot_age_seconds`, or repeated HTTP 503s. The most recent complete snapshot is safe to serve during a failed refresh.

## 4. Directory Layout

```
cmd/http/ui/
├── assets/
│   ├── chart.umd.min.js    # Vendored Chart.js v4.4.7 UMD bundle
│   ├── htmx.min.js         # Vendored HTMX v2.0.4 library
│   ├── favicon.png         # Site icon served at /favicon.ico
│   ├── fonts/
│   │   ├── manrope-latin-var.woff2 # Vendored Manrope variable font (Latin subset, tnum kept)
│   │   └── OFL.txt         # SIL Open Font License for Manrope
│   └── styles.css          # Design tokens, shell and component styles (see section 5)
├── templates/
│   ├── layout.html         # Page shell (skip link, brand, primary nav, stale notice, footer, font preload)
│   ├── dashboard.html      # Headline stats, 30-day time-series chart, region table
│   ├── races.html          # Race & clan demographics with race, tribe, gender, and race×gender doughnut charts
│   ├── worlds.html         # Global worlds & datacenters table with active ratios
│   ├── expansions.html     # MSQ expansion completion funnel & drop-off rates
│   ├── character.html      # Full character profile (jobs grid, milestones, FC, badges)
│   ├── characters_list.html# Paginated character directory and search results
│   └── partials/
│       ├── world_drilldown.html # HTMX partial for region -> DC -> world drilldown
│       └── search_results.html  # HTMX partial for live character search
├── routes.go               # ServeMux route registration and embedded asset handler (font headers)
├── controller.go           # Core UI controller & template renderer
├── dashboard.go            # Controller for /ui/dashboard & /ui/partials/world-breakdown
├── races.go                # Controller for /ui/races
├── worlds.go               # Controller for /ui/worlds
├── expansions.go           # Controller for /ui/expansions
├── character.go            # Controller for /ui/characters/{id} & /ui/characters/search
├── world_data.go           # UI wrappers over the domain world hierarchy + indexable-world helper
├── template_helpers.go     # Template helper functions (formatting numbers, dates, job roles)
└── ui_test.go              # Table-driven HTTP handler test suite
```

## 5. Theme & Styling System

The design system lives in `assets/styles.css`. Its concept is **the aetheryte network**: a blue night
base drawn from the game's skies, flat surfaces separated by tone, and the two colours of FFXIV crystals
(Aether and Crystal) reserved for data and focus. It is dark-only. The full direction is in spec #49.

**Principles:** no emoji in the UI, no inline `style` attributes, no ALL-CAPS labels, no glow, blur or
gradient decoration, no decorative motion (only chart transitions, tooltip fades and the nav toggle, all
disabled under `prefers-reduced-motion`), icons are inline SVG. Hand-written CSS, no framework, no build step.

### Design tokens

Every value is a CSS custom property on `:root`, defined once and reused:

| Token | Value | Use |
|---|---|---|
| `--color-night` | `#0D1220` | Page background |
| `--color-surface` | `#151C2E` | Panels and tiles |
| `--color-raised` | `#1D2640` | Hover, selected rows, open menus, realm-tally band track |
| `--color-line` | `#28324D` | Hairline separators and table rules only (panels use tone, not borders) |
| `--color-aether` | `#5FD4F4` | Primary accent: links, primary chart series, focus ring, active nav |
| `--color-crystal` | `#9D8CFF` | Secondary accent: comparison series, brand facet. Never text links |
| `--color-ink` / `--color-ink-muted` | `#E8ECF5` / `#98A2BC` | Text and secondary text |
| `--color-active` / `--color-warning` / `--color-negative` | `#4CC38A` / `#E8B04A` / `#EF7A72` | Status |
| `--color-role-*` | tank `#5B8DEF`, healer `#4CC38A`, melee `#EF7A72`, phys-ranged `#F0975A`, magic-ranged `#B58CFF`, crafter `#3CC4B4`, gatherer `#E8C14A` | Job-role colours (`.role-*` classes) |

Every text token meets WCAG 2.2 AA (4.5:1) on Night, Surface and Raised, and Night text on an Aether fill
does too; `tokens_test.go` enforces this against the served stylesheet. The focus ring
(`--focus-ring`, 2px Aether, applied via `:focus-visible`) is visible on every interactive element.

- **Type scale:** `--step--2` … `--step-6`, a 1.25 modular ratio on a 16px body (`--step-0`), line
  height `--leading-body` 1.55, `--leading-tight` 1.15 for headings, `--measure` 70ch for long-form text.
  Weights: `--weight-body` 400, `--weight-medium` 500, `--weight-heading` 600, `--weight-display` 800
  (headline figures and page titles, with `--tracking-display` -0.02em).
- **Space:** `--space-1` … `--space-8` on a 4px base (4, 8, 12, 16, 24, 32, 48, 64px).
- **Shape:** pages and sections square; `--radius-panel` 12px (panels, tiles); `--radius-control` 8px
  (buttons, selects, nav items); `--radius-pill` for chips and badges. No resting shadows;
  `--shadow-overlay` is only for open menus and tooltips.
- **Layout:** `--content-max` 1280px, `--gutter` 16px on phones and 24px from 768px.
- **Legacy aliases:** the old names (`--bg-card`, `--text-muted`, `--cyan`, `--gold`, …) are kept as
  aliases onto the new tokens while page templates still use them; gold no longer exists and resolves to
  Ink. Remove each alias once nothing references it.

### Font

One family, **Manrope** (variable, weight axis 200–800, SIL OFL 1.1), vendored as
`assets/fonts/manrope-latin-var.woff2`: a ~24KB subset of the google/fonts `Manrope[wght].ttf` covering
Latin + Latin-1 plus common punctuation (curly apostrophes, dashes, `›`), with all OpenType layout
features retained, including `tnum`. The licence is alongside it in `assets/fonts/OFL.txt`. The page shell
preloads it, `@font-face` uses `font-display: swap`, and the system sans stack is the fallback. The body
sets `font-variant-numeric: tabular-nums` so every figure uses tabular numerals.

To regenerate the subset (fontTools + brotli in a venv):

```bash
python3 -m fontTools.subset 'Manrope[wght].ttf' --flavor=woff2 --layout-features='*' \
  --unicodes='U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+0304,U+0308,U+0329,U+2000-206F,U+2074,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD' \
  --output-file=manrope-latin-var.woff2
```

Ship a changed font under a new file name: the asset route serves `.woff2` files as `font/woff2` with
`Cache-Control: public, max-age=31536000, immutable`.

### Page shell

`templates/layout.html` wraps every page:

- A "Skip to content" link (first focusable element) targeting `<main id="main">`.
- A header with the brand: an original faceted-crystal SVG glyph (Aether and Crystal facets) and the
  "FFXIV Census" wordmark, linking to `/`.
- Primary navigation (`<nav aria-label="Primary">`: Dashboard `/`, Races, Worlds, Expansions,
  Methodology) with `aria-current="page"` on the active item. Below 768px it collapses behind a "Menu"
  toggle that is a native checkbox and label, so it opens and closes without JavaScript.
- A stale-snapshot notice at the top of `main` when the snapshot is older than `stale_warning`.
- A footer with snapshot freshness ("Statistics updated …"), a Methodology link and a link to
  ffxivbard.com.

Head metadata (canonical, description, Open Graph, favicon) is unchanged. No page references an external
host in `src`, `href` or CSS `url(...)` apart from canonical/OG URLs and the ffxivbard.com footer link;
`assets_test.go` enforces this.

### Components

Shell: `.skip-link`, `.site-header`, `.brand`, `.primary-nav`, `.nav-toggle`, `.site-footer`,
`.notice` / `.notice-stale`. Page components (restyled onto the tokens; page templates move onto them as
they are redesigned): `.page-header` / `.page-title` / `.page-desc`, `.stat-grid` / `.stat-card`,
`.panel` / `.panel-header` / `.panel-title` / `.panel-body`, `.chart-container`, `.data-table` inside
`.table-responsive`, `.badge`, `.btn`, `.progress-bar-*`, `.funnel-*` (progression steps),
`.visually-hidden`.

## 6. Cascading Filters

The `/ui/races` and `/ui/worlds` pages support cascading filter dropdowns that narrow
options based on parent selections. The hierarchy is **Region → Datacenter → World**.

### Races Page (`/ui/races`)

Three `<select>` dropdowns in a single form. Selecting a region updates the Datacenter
dropdown to show only DCs in that region; selecting a DC updates the World dropdown to
show only worlds in that DC. When no parent filter is selected, all options are shown.

### Worlds Page (`/ui/worlds`)

Region is selected via button pills; the Datacenter `<select>` dropdown shows only DCs
belonging to the selected region. When no region is selected, all DCs are shown.

### Implementation

Filter lists are derived server-side using `DCsForRegion()` and `WorldsForDC()` helper
functions in `cmd/http/ui/world_data.go`, which delegate to the world hierarchy in the
domain (world → datacenter → region). The same hierarchy resolves the scope of every
new-characters (30d) card and column. Each filter change triggers a full page reload
via `onchange="this.form.submit()"`.

## 7. Search Console

The site is registered in Google Search Console as `census.ffxivbard.com`. Crawl policy is
Cloudflare-managed (`robots.txt` at the edge); the app contributes discovery and signals:
`/sitemap.xml` (all indexable pages, snapshot-derived `lastmod`), per-page canonical URLs,
meta descriptions and Open Graph tags, and `/favicon.ico`.

After deploying a release that changes indexable pages or URLs:

1. Verify production: `curl -sS https://census.ffxivbard.com/sitemap.xml | head`, check `/` serves the
   dashboard (no redirect), and a page's canonical URL matches its address.
2. In Search Console, submit or re-submit `https://census.ffxivbard.com/sitemap.xml` under *Sitemaps*.
3. Run *URL Inspection → Live Test* on `https://census.ffxivbard.com/` (this also proves Googlebot can
   fetch the page end-to-end, including any edge/proxy layer).
4. Request indexing for `/` to seed a recrawl.

