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
| `/ui/dashboard` | `GET` | Overview, top to bottom: the realm tally (total population over a band split by region), stat tiles (active players, characters at the level cap, new characters in the last 30 days with a worded, signed trend), race distribution bar chart and main-scenario completion meters side by side, the 30-day new-character line chart, and the region table with world drill-down. All aggregate data comes from the cached statistics snapshot. |
| `/ui/partials/world-breakdown` | `GET` | HTMX partial returning world and datacenter rows for a requested region (`?region=NA`), including each world's New (30d) count |
| `/ui/races` | `GET` | Playable race demographics with cascading region/DC/world filters, a new-characters (30d) card that follows the active filters, global percentage shares, active ratios, and demographic charts. Filtering selects precomputed snapshot groups and performs no aggregate database query. |
| `/ui/worlds` | `GET` | Global server rankings table (including a per-world New (30d) column) with interactive region/datacenter filters, a filter-scoped new-characters (30d) card and a "Characters / Active players" ranking switch (`?sort=active`; an HTMX request targeting `world-rankings` gets just that region) |
| `/ui/worlds/{world}` | `GET` | World detail page (an unknown world answers the styled `404`): node breadcrumbs (region › datacenter › world, linking to the filtered Worlds page), stat tiles (characters, active players (30d), new characters from chocobo milestone 590 with a worded trend vs the previous 30 days), race distribution bar chart, main-scenario completion meters and the 30-day new-character line chart, each chart with a text alternative |
| `/ui/expansions` | `GET` | Main scenario progression from A Realm Reborn to Dawntrail as stepped, connected nodes: each step shows completions, share of all characters and retention from the previous expansion |
| `/ui/methodology` | `GET` | How the census works: data collection, the active-player signal, the new-character (chocobo) milestone and the capstone achievement of each expansion. Renders without a snapshot. |
| `/ui/characters/{id}` | `GET` | Detailed character profile with Dawntrail Lv 100 job matrix, story milestone timeline, Free Company badge, and external links |
| `/ui/characters` | `GET` | Paginated directory browser of discovered player characters |
| `/ui/characters/search` | `GET` | Global search handler: numeric IDs redirect directly to `/ui/characters/{id}`, text queries filter by character name |
| any other path | `GET`/`HEAD` | Styled not-found page (`404`, through the page shell, `noindex`) for every path no route matches outside `/api/` (for example `/nope` or `/ui/nope`). The disabled character routes below also land here. |
| `/ui/assets/*` | `GET` | Static asset file server (`styles.css`, `htmx.min.js`, `chart.umd.min.js`, `chart-theme.js`, `fonts/*.woff2` as `font/woff2` with year-long immutable caching) |

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

Filter values are matched case-insensitively and resolved once, at parse time, to their canonical names
(`?region=na` → `NA`, `?dc=chaos` → `Chaos`, `?world=cerberus` → `Cerberus`) on Races, Worlds and the
world-breakdown partial, so the figures, current pill, selected options, chips and scope sentence all
agree. Unknown values pass through unchanged (and show the filtered empty state).

## 3. Statistics Snapshot

`refresh ui-stats` builds one versioned JSON read model in `ui_stats_snapshots`. It computes global, region, datacenter, and world population totals; demographic groups; expansion completion counts; and the Chocobo-milestone daily new-character series reaching back 60 days (the 30-day headline window plus the 30-day comparison window). Refreshes use a PostgreSQL advisory lock, a repeatable-read transaction, and an atomic single-row upsert, so readers see either the old complete snapshot or the new complete snapshot.

```bash
# Build/replace the current snapshot immediately
./bin/ffxiv-census refresh ui-stats
```

The Helm chart runs two CronJobs, both with `concurrencyPolicy: Forbid`: `refresh-ui-stats-full` (`--mode=full`) nightly at 01:00 Europe/Bucharest, and `refresh-ui-stats-incremental` (`--mode=incremental`) every 30 minutes (change its `schedule` in `k8s/values.yaml` to change the interval). A full rebuild scans every character (about 6 minutes at 34.5 million rows); the incremental pass does not.

**Incremental mode.** The snapshot stores a watermark (`ui_stats_snapshots.watermark_at`, the database clock when the counts were read). An incremental pass takes the previous snapshot's group totals and adds the characters with `first_seen_at` in (watermark, now] and subtracts those with `deleted_at` in the same window (soft deletes only). Active and max-level counts are recomputed exactly each pass from `latest_achievement_at` and `max_job_level` (partial index `idx_characters_max_job_level`), and the milestone figures are recomputed in full. It falls back to a full rebuild when there is no previous snapshot, the schema version or watermark is unusable, or the adjusted result fails validation. Accepted drift until the next nightly rebuild: a character whose world, datacenter, region, race or tribe changes after it was counted, a soft delete later reverted, and a character that commits after the read snapshot with a `first_seen_at` before the watermark. The advisory lock keeps the two jobs from overlapping (the later one reports "skipped"). Each server process reloads the database row no more frequently than `cache_ttl`; concurrent cold loads are coalesced. If a reload fails after a successful load, the last good snapshot remains available. A cold process with no snapshot returns `503 Service Unavailable` with `Retry-After: 60` and the styled "Statistics are being prepared" page (see section 5, Error, stale and empty states); there is deliberately no fallback to unbounded aggregate queries.

```toml
[census.ui_stats]
cache_ttl       = "1m"
stale_warning   = "12h"
refresh_timeout = "2h"
```

Aggregate HTML and HTMX responses include a query-aware `ETag`, `Cache-Control`, and `Vary: HX-Request, HX-Target, HX-History-Restore-Request, Accept-Encoding` (the ETag hashes the same headers, so a switch fragment and its full page never share a cache entry). The page shell displays the snapshot generation time and shows the stale notice when the snapshot is stale (see section 5, Error, stale and empty states, for exactly when). This response contract is suitable for a future reverse proxy such as Varnish without making it a runtime dependency.

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
│   ├── chart-theme.js      # Shared Chart.js theme + window.CensusCharts (palette, race, role and Grand Company colours, formatting)
│   ├── census.js           # Deferred site script: nav current-into-view, switch focus + announcement, entrance settle
│   ├── htmx.min.js         # Vendored HTMX v2.0.4 library
│   ├── favicon.png         # Site icon served at /favicon.ico
│   ├── fonts/
│   │   ├── manrope-latin-var.woff2 # Vendored Manrope variable font (Latin subset, tnum kept)
│   │   ├── OFL.txt         # SIL Open Font License for Manrope
│   │   ├── saira-latin-var.woff2   # Vendored Saira variable font, weight 400-800 x width 75-100% (figures face)
│   │   └── OFL-Saira.txt   # SIL Open Font License for Saira
│   └── styles.css          # Design tokens, shell and component styles (see section 5)
├── templates/
│   ├── layout.html         # Page shell (skip link, brand, primary nav, stale notice, footer, font preload)
│   ├── not_found.html      # Styled 404 page (unknown world or /ui/ route)
│   ├── unavailable.html    # Styled 503 "Statistics are being prepared" page
│   ├── components/
│   │   ├── states.html     # Shared "empty-state", "freshness", "next-refresh" and "state-mark" components
│   │   ├── switch.html     # Shared "switch": segmented link pair swapping a region via HTMX
│   │   ├── filters.html    # Shared "filter-bar" (labelled selects, region pills, removable chips)
│   │   └── figures.html    # Shared "trend" line for new-character tiles
│   ├── dashboard.html      # Realm tally, stat tiles, race and MSQ panels, 30-day chart, region table
│   ├── races.html          # Races and clans: filter bar, stat tiles, race/gender doughnuts, clan and race×gender bars, race table
│   ├── worlds.html         # Worlds: filter bar, stat tiles, world rankings data table
│   ├── world_detail.html   # World: node breadcrumbs, stat tiles, race and MSQ panels, 30-day chart
│   ├── expansions.html     # Main scenario progression steps (completions, share, retention)
│   ├── methodology.html    # Long-form methodology at a 70ch measure
│   ├── character.html      # Character profile (route disabled; not yet restyled)
│   ├── characters_list.html# Character directory (route disabled; not yet restyled)
│   ├── free_companies_list.html # Free company directory (route disabled; not yet restyled)
│   └── partials/
│       ├── world_drilldown.html # HTMX partial: a region's worlds grouped by datacenter
│       └── search_results.html  # HTMX partial for live character search
├── routes.go               # ServeMux route registration and embedded asset handler (font headers)
├── controller.go           # Core UI controller & template renderer
├── nav.go                  # Primary navigation entries (spec order; each new page adds its own)
├── sections.go             # Freshness and NextRefresh view models (nightly, nextRefresh helpers)
├── switch.go               # Switch view model, wantsSwitchRegion and renderRegion (HTMX region fragments)
├── dashboard.go            # Controller for /ui/dashboard & /ui/partials/world-breakdown
├── realm_tally.go          # Realm-tally view model (region shares, band geometry)
├── taxonomy.go             # Census taxonomy for the UI: colour slots, #census-taxonomy data, race rows, scope sentence
├── trend.go                # Worded, signed trend (Up +20 (+200.0%)) for stat tiles
├── filters.go              # FilterBar view model and filterChips (URL with one filter removed)
├── races.go                # Controller for /ui/races (clans grouped by race, race x gender)
├── worlds.go               # Controller for /ui/worlds
├── world_detail.go         # Controller for /ui/worlds/{world}
├── timeline.go             # 30-day new-character series: the 30 complete UTC days before the snapshot day
├── expansions.go           # Controller for /ui/expansions (progression steps and retention)
├── character.go            # Controller for /ui/characters/{id} & /ui/characters/search
├── world_data.go           # UI wrappers over the domain world hierarchy + indexable-world helper
├── template_helpers.go     # Template helper functions (formatting numbers, dates, job roles)
└── *_test.go               # HTTP handler and page-render tests (page_hygiene_test.go checks every page)
```

## 5. Theme & Styling System

The design system lives in `assets/styles.css`. Its concept is **the aetheryte network**: a blue night
base drawn from the game's skies, flat surfaces separated by tone, and the two colours of FFXIV crystals
(Aether and Crystal) reserved for data and focus. It is dark-only. The full direction is in spec #49;
design v2 (spec #56, ticket #58) adds the Saira figures face, the role and Grand Company palettes, the lit
panel rim, the scrolling nav bar and the shared freshness, next-refresh and switch components.

**Principles:** no emoji in the UI, no inline `style` attributes, no ALL-CAPS labels, no glow, blur or
gradient decoration, no decorative motion (only chart transitions, tooltip fades and one entrance per
page, all disabled under `prefers-reduced-motion`), icons are inline SVG. Hand-written CSS, no framework, no
build step.

### Design tokens

Every value is a CSS custom property on `:root`, defined once and reused:

| Token | Value | Use |
|---|---|---|
| `--color-night` | `#0D1220` | Page background |
| `--color-surface` | `#151C2E` | Panels and tiles |
| `--color-raised` | `#1D2640` | Hover, selected rows, open menus, realm-tally band track |
| `--color-line` | `#28324D` | Hairline separators and table rules only (panels use tone, not borders) |
| `--color-rim` | `#2F3B5C` | The lit 1px top edge of panels, tiles and the filter bar (`--rim`, an inset line, not a shadow), after the game's window frames |
| `--color-aether` | `#5FD4F4` | Primary accent: links, primary chart series, focus ring, active nav |
| `--color-crystal` | `#9D8CFF` | Secondary accent: comparison series, brand facet. Never text links |
| `--color-ink` / `--color-ink-muted` | `#E8ECF5` / `#98A2BC` | Text and secondary text |
| `--color-active` / `--color-warning` / `--color-negative` | `#4CC38A` / `#E8B04A` / `#EF7A72` | Status |
| `--color-chart-1` … `--color-chart-8` | `#00A4C5` `#917EF4` `#D24C49` `#B334AD` `#4194F9` `#27AF57` `#DD61A2` `#DD7303` | Categorical chart palette, fixed order (see Charts) |
| `--color-chart-other` | `#7C869E` | Neutral for categories without a slot |
| `--color-role-*` | tank `#5B8DEF`, healer `#4CC38A`, melee `#EF7A72`, phys-ranged `#F0975A`, magic-ranged `#B58CFF`, crafter `#3CC4B4`, gatherer `#E8C14A` | Job-role text colours (`.role-*` classes, AA on every surface) |
| `--color-mark-tank` / `-healer` / `-dps` | `#427FD8` / `#03AF73` / `#D2393A` | Role marks (bars, tiles, rules); never text |
| `--color-gc-maelstrom` / `-twin-adder` / `-immortal-flames` | `#D1364C` / `#C38616` / `#6896D1` | Grand Company marks; "no company" is an outline, not a hue |

**Semantic mark palettes.** Taken from the game for meaning, not decoration. They colour marks only;
labels stay in Ink and the name is always printed beside the mark, so colour is never the only cue. Both
sets were generated in OKLCH and checked with the dataviz palette validator (dark mode, all pairs) against
Surface and Raised, and every check passes:

```
roles  #427FD8,#D2393A,#03AF73   band PASS  chroma PASS  CVD worst ΔE 9.1 (deutan)  normal worst ΔE 23.6  contrast >= 3:1 on both
GCs    #D1364C,#C38616,#6896D1   band PASS  chroma PASS  CVD worst ΔE 10.4 (deutan) normal worst ΔE 18.7  contrast >= 3:1 on both
```

The roles also differ in lightness (OKLab L: DPS 0.575, tank 0.60, healer 0.665), as far as the chart
band and the 3:1 floor on Raised allow. Immortal Flames is a pale steel because the in-game black would
vanish on Night. `design_v2_test.go` pins each colour's hue family, its 3:1 contrast on Surface and
Raised and the role lightness separation.

Every text token meets WCAG 2.2 AA (4.5:1) on Night, Surface and Raised, and Night text on an Aether fill
does too; `tokens_test.go` enforces this against the served stylesheet. The focus ring
(`--focus-ring`, 2px Aether, applied via `:focus-visible`) is visible on every interactive element.

- **Type scale:** `--step--2` … `--step-6`, a 1.25 modular ratio on a 16px body (`--step-0`), line
  height `--leading-body` 1.55, `--leading-tight` 1.15 for headings, `--measure` 70ch for long-form text.
  Weights: `--weight-body` 400, `--weight-medium` 500, `--weight-heading` 600, `--weight-display` 800.
  `--font-figures` (Saira) with `--width-semi-condensed` (87.5%) or `--width-condensed` (80%) via
  `font-stretch` sets large figures, rank numerals, page titles and the wordmark.
- **Space:** `--space-1` … `--space-8` on a 4px base (4, 8, 12, 16, 24, 32, 48, 64px).
- **Shape:** pages and sections square; `--radius-panel` 12px (panels, tiles); `--radius-control` 8px
  (buttons, selects, nav items); `--radius-pill` for chips and badges. No resting shadows;
  `--shadow-overlay` is only for open menus and tooltips.
- **Layout:** `--content-max` 1280px, `--gutter` 16px on phones and 24px from 768px.
- **Motion:** `--duration-fast` 120ms and `--duration-base` 200ms for state changes;
  `--duration-entrance` 360ms with `--ease-out` for the one entrance per page.
- **Legacy aliases:** a few old names (`--bg-card`, `--border-color`, `--text`, `--text-dim`, `--gold`,
  `--gold-light`, `--cyan`) remain only for the inline styles of the disabled character and free company
  templates; gold resolves to Ink. Every indexable page uses the tokens directly. Delete the aliases when
  those templates are restyled.

### Fonts

Two families. **Manrope** carries text and labels; **Saira** carries figures.

**Saira** (variable, SIL OFL 1.1, from google/fonts `ofl/saira/Saira[wdth,wght].ttf`) is the figures
face: the semi-condensed, HUD-like numeric voice of the game's own interface. It sets the realm-tally
figure, stat values, progression figures, rank numerals, page and state titles and the wordmark. It is
vendored as `assets/fonts/saira-latin-var.woff2` (~60KB): the axes limited to weight 400–800 and width
75–100%, then subset to the same Latin range as Manrope with `tnum`, `pnum`, `case` and `liga` kept. Its
`@font-face` declares `font-stretch: 75% 100%`, so `font-stretch: 87.5%` selects semi-condensed. The
licence is `assets/fonts/OFL-Saira.txt`; the shell preloads it. To regenerate:

```bash
fonttools varLib.instancer 'Saira[wdth,wght].ttf' wdth=75:100 wght=400:800 -o Saira-lim.ttf
pyftsubset Saira-lim.ttf --flavor=woff2 --layout-features='kern,liga,tnum,lnum,pnum,case,calt,ccmp,locl,mark,mkmk' \
  --unicodes='U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+0304,U+0308,U+0329,U+2000-206F,U+2074,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD' \
  --output-file=saira-latin-var.woff2
```

**Manrope** (variable, weight axis 200–800, SIL OFL 1.1) is vendored as
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
- Primary navigation (`<nav aria-label="Primary" data-current-into-view>`) with `aria-current="page"`
  on the active item, rendered from `primaryNav` in `nav.go`. The spec order is Dashboard, Worlds,
  Rankings, Races, Grand Companies, Jobs, Expansions, Methodology; a page is linked only once it exists,
  so each new page adds its own `NavItem` in its place (and its `ActiveNav` key). There is no menu toggle:
  below 900px the nav takes its own row under the brand and scrolls sideways, with faded edges hinting at
  more, and `census.js` scrolls the current item into view (only the bar moves, never the page). Focus
  rings sit inside the bar so the scroller never clips them.
- A polite live region (`#census-announcer`, visually hidden) that the switch component announces through.
- The deferred site script `assets/census.js`; every page works without it.
- A stale-snapshot notice at the top of `main` when the snapshot is stale (see Error, stale and empty
  states below).
- A footer with snapshot freshness ("Statistics updated …"), a Methodology link and a link to
  ffxivbard.com.

Head metadata (canonical, description, Open Graph, favicon) is unchanged. No page references an external
host in `src`, `href` or CSS `url(...)` apart from canonical/OG URLs and the ffxivbard.com footer link;
`assets_test.go` enforces this.

### Components

Shell: `.skip-link`, `.site-header`, `.brand`, `.primary-nav`, `.site-footer`,
`.notice` / `.notice-stale` (with optional `.notice-icon` and `.notice-title`), `.empty-state`, `.state-page`.
Page components (restyled onto the tokens; page templates move onto them as
they are redesigned): `.page-header` / `.page-title` / `.page-desc`, `.stat-grid` / `.stat-card`,
`.panel` / `.panel-header` / `.panel-title` / `.panel-body`, `.chart-container`, `.data-table` inside
`.table-responsive`, `.badge`, `.btn` (`.btn-small`), `.progress-bar-*`, `.visually-hidden`.
Page-specific components: `.node-breadcrumb` / `.node-crumb` (world detail), `.progression` /
`.progression-step` (Expansions) and `.prose` (Methodology), described under Page layouts. Dashboard components: `.realm-tally` / `.realm-band` / `.realm-segment` /
`.realm-key` / `.realm-tooltip`, `.trend` (`.trend-up` / `.trend-down` / `.trend-flat`), `.dashboard-grid`,
`.panel-meta`, `.chart-summary` and `.chart-data` (chart text alternatives), `.progress-list` /
`.progress-meter` (`<meter>` bars), `.drilldown-row` / `.drilldown-body`. `.chart-slot-0` … `.chart-slot-8`
set `--slot-color` for SVG marks and swatches.

Shared figure and filter components live in `templates/components/` and are parsed into every page and
partial: `filter-bar` (see Cascading Filters), `trend` (the arrow + word + signed delta line for a
`Trend` from `newTrend`, used by the new-characters tiles on the dashboard, Races and Worlds),
`empty-state`, and the three v2 components below. Page grids: `.panel-grid` pairs chart panels (two columns from 720px). A stat-tile row
names its layout: `.stat-grid-trio` (three tiles: the first spans the row over the other two on phones,
then one row of three from 600px) and `.stat-grid-quad` (four tiles: 2 × 2 on phones and tablets, one row
of four from 1024px); neither stacks into one tall column. On phones the paired tiles tighten their
padding and value size so a seven-figure value fits. `.chart-container-tall` sizes bar charts with many categories.

### Shared components for new pages (design v2)

Later dashboards (Rankings, Free Companies, Grand Companies, Jobs, Privacy) use these rather than
inventing their own. Each is covered by tests in `design_v2_test.go` and `switch_test.go`.

- **`freshness`**: "Rebuilt nightly, last on 24 October 2026" with a `<time datetime>`, for a snapshot
  section only the nightly full refresh rebuilds. `{{template "freshness" (nightly .Data.BuiltAt)}}`; a
  zero time renders nothing.
- **`next-refresh`**: the state for a page or section whose data the stored snapshot predates (a
  version 2 snapshot): a lit crystal on a dashed orbit, "<Subject> arrive with the next refresh." and
  a line saying the rest of the page is current.
  `{{template "next-refresh" (nextRefresh "Grand Company figures")}}`.
- **`switch`**: a segmented link pair choosing one view of a region (`Switch` / `SwitchOption` in
  `switch.go`). Every option is a plain link (`href`), so without JavaScript it loads the full page; the
  same link carries `hx-get`, `hx-target="#<region>"`, `hx-swap="outerHTML"` and `hx-push-url="true"`.
  The current option has `aria-current="true"` and sits lifted on the track with an Aether rule. The
  pattern for the region: a page template block whose root has the region `id` and a
  `data-switch-announce` sentence, containing the switch itself and one heading with
  `data-switch-heading tabindex="-1"`. The controller renders just that block when
  `wantsSwitchRegion(r, id)` (HTMX request whose `HX-Target` is the region, never a history-restore
  request) via `c.renderRegion(w, page, block, data)`, and the full page otherwise. After the swap,
  `census.js` focuses the heading and writes the announcement into `#census-announcer`. Unknown values
  fall back to the default view. First used by the Worlds rankings ("Characters / Active players").
- **Entrance**: mark the page's one signature element with `data-entrance` (unfurls from the left),
  `data-entrance="rise"` (grows up from the baseline) or `data-entrance="fade"`. It runs once, in 360ms,
  on a full page load only: `census.js` adds `.census-settled` to the root after 400ms, which switches
  entrances off for anything HTMX swaps in later, and reduced motion switches them off entirely. One per
  page (`TestOneEntrancePerPage`); the dashboard's is the realm band.
- **Semantic colours in charts**: `CensusCharts.role("tank" | "healer" | "dps")` and
  `CensusCharts.grandCompany("maelstrom" | "twin-adder" | "immortal-flames")`; in CSS the
  `--color-mark-*` and `--color-gc-*` tokens.

### Page layouts

- **Node breadcrumb** (world detail): `<nav aria-label="Breadcrumb">` with an ordered list of region ›
  datacenter › world. Each crumb is drawn as a small aetheryte node (an Aether ring) joined to the
  previous one by a short line; the current world is a filled node, marked `aria-current="page"` and not
  linked. Region and datacenter link to `/ui/worlds?region=NA` and `/ui/worlds?region=NA&dc=Aether`.
- **World detail** reuses the dashboard vocabulary: three stat tiles (with the shared `trend`), a
  `.dashboard-grid` pairing the race distribution bar chart (race colours from `CensusCharts`, a summary
  sentence and a "Show the numbers" table with characters, share and active) with the main-scenario
  `.progress-list`, then the 30-day line chart (zero-filled by `newCharactersTimeline`, the same series
  as the dashboard) with its summary and table.
- **Progression steps** (Expansions): one `<li>` per expansion in an `<ol class="progression">`. Each
  step has a node on a vertical connector, the expansion name (`h3`) with patch, level cap and finale,
  a `<meter>` for its share of all characters (so the bars step down), and a `<dl>` of Completed, Share
  of total and "Retention from {previous expansion}". A Realm Reborn reads "Starting point"; retention
  after an expansion nobody finished reads "Not measured". Below 600px the figures fold into two
  columns with retention on its own row. Expansion names are spelled out; the domain `ExpansionConfig.Icon`
  emoji is no longer rendered anywhere.
- **Prose** (Methodology): an `<article class="prose">` capped at `--measure` (70ch) with an
  "On this page" list, `h2` sections separated by hairlines, an info `.notice` for profile visibility, a
  definition block for the chocobo milestone and the capstone table in a scroll region.

### Data table

Every data table (world rankings, the Races table, the dashboard region table and its drill-down, and
the "Show the numbers" tables) uses one pattern:

```html
<div class="table-scroll" role="region" aria-labelledby="rankings-heading" tabindex="0">
  <table class="data-table">
    <caption class="visually-hidden">World rankings, in Europe</caption>
    <thead><tr><th scope="col">World</th><th scope="col" class="text-right">Characters</th>…</tr></thead>
    <tbody><tr><th scope="row"><a href="/ui/worlds/Cerberus">Cerberus</a></th><td class="text-right">827</td>…</tr></tbody>
  </table>
</div>
```

- The `.table-scroll` region scrolls sideways on its own (the page never does), is focusable for
  keyboard scrolling and is named by its panel heading or an `aria-label`.
- The first column is the row header (`th scope="row"`: world, race, region) and sticks to the left
  edge while the other columns scroll under it; below 768px a hairline marks that edge.
- Numeric columns are right-aligned (`text-right`) with tabular figures; the ranking metric comes
  straight after the name, identity columns (datacenter, region) last.
- From 1024px, where tables fit, `.table-scroll` stops being a scroll container and column headers
  stick below the site header (panels use `overflow: clip` so sticky works). Inside "Show the numbers"
  the table scrolls in a 22rem box with a sticky header.
- Rows highlight on hover and `:focus-within`; world names are underlined Aether links to the world
  detail page. The rankings show the rank before the name (`.row-rank`), the Races table a race-colour
  `.swatch` and a `<meter>` share bar.
- **Drill-down.** On the dashboard the "Show worlds" trigger sits under the region name in the sticky
  first column, so it stays reachable at 360px. The loaded drill-down (`partials/world_drilldown.html`)
  is sized to the visible scroll area (`width: 100cqi` of the region table's `.table-scroll`, which is
  an inline-size container) and sticks to its left edge; its own table groups worlds by datacenter
  under `th scope="rowgroup"` headings with a small node marker.

**Races charts.** Race distribution is a doughnut in fixed race order and colours (`sortRaces`,
`raceColors`); gender is a doughnut (Male Aether, Female Crystal, unrecorded neutral); clans are a
horizontal bar chart grouped by race and coloured by their race (`clanRace` in `races.go`); race by
gender is a grouped horizontal bar chart. Each has a summary sentence and, except the race doughnut
(whose figures are the "Every race" table), a "Show the numbers" table.

### Realm tally

The dashboard's one bold element. `realm_tally.go` builds it in the UI controller from the snapshot's
global `region` groups (no snapshot schema change): each region's share of the census total, rounded once
to 0.1% so the drawn width, the label and the offsets agree, plus a display name (NA → North America,
EU → Europe, JP → Japan, OCE → Oceania), a link to `/ui/worlds?region=<code>` and a fixed colour slot
(the region's position in the census taxonomy: NA 1, EU 2, JP 3, OCE 4, anything else the neutral slot). Segments are ordered by population.

The markup is a `<section aria-labelledby>` whose heading is the page `h1` ("Characters in the census"):

- the total as an oversized Saira semi-condensed 600 figure in Aether (`<data value>`), with
  proportional figures;
- an inline SVG band (`role="group"`, "Population by region") on the Raised track. Each segment is an SVG
  `<a>` with an `aria-label` ("Europe: 10,138 characters, 25.3% of the census") and a `<title>`, so it is
  in the tab order and works without JavaScript. Widths are SVG `x`/`width` attributes, not inline
  styles. Segments of at least 9% carry their region code in Night 800 (AA large text on every slot);
  a 2px Night stroke separates neighbours;
- a small script on the dashboard shows one positioned tooltip (count first, then region and share) on
  hover and keyboard focus; the other segments dim and the focused one gets an Ink outline;
- a visible key (swatch, name, share, count; `aria-hidden`) and a visually hidden table captioned
  "Population by region" with the same figures for screen readers.

### Charts

Every Chart.js chart on every page starts from `assets/chart-theme.js`, loaded by the layout right after
Chart.js and before any page script. It reads colours from the CSS tokens at runtime (the stylesheet stays
the single source) and sets `Chart.defaults`: Manrope at 12px, Ink-muted text, Line-coloured hairline
grid, the overlay tooltip (Raised, Line border, value in Ink 600), circular legend markers at the bottom,
2px lines with no resting points and a 12px hit radius, drawn with monotone cubic interpolation (a curve
never overshoots its data, so sparse daily counts cannot dip below zero), bars capped at 24px with 4px rounded ends, a 2px
Surface gap between doughnut arcs, `responsive` with `maintainAspectRatio: false` (size the
`.chart-container`), and no animation under `prefers-reduced-motion` (updated live). It redraws charts once
the web font has loaded. Opt-in plugins: `censusCrosshair` (vertical hairline at the hovered X, for line
charts) and `censusBarValues` (value at each bar tip).

Page scripts never hard-code colours (the dashboard test rejects hex/`rgba(` literals in its inline
scripts); they use `window.CensusCharts`:

| Helper | Returns |
|---|---|
| `primary()` / `comparison()` | Aether for a single or primary series, Crystal for a comparison series |
| `slot(n)` | Categorical slot 1-8; anything else is `--color-chart-other` |
| `raceColor(name)` / `raceColors(labels)` | The fixed race colour |
| `regionColor(code)` | The fixed region colour (NA, EU, JP, OCE) |
| `role(key)` / `grandCompany(key)` | Role mark colour (`tank`, `healer`, `dps`); Grand Company mark colour (`maelstrom`, `twin-adder`, `immortal-flames`); unknown keys get the neutral colour |
| `sortRaces(labels, values)` | Labels and values in fixed race order, for doughnuts |
| `wash(colour)` / `surface()` | 10% area fill for a line; Surface for rings and gaps |
| `format(n)` / `compact(n)` / `formatDate(iso)` | `1,234`; `1.2K` axis ticks; `Sat, 24 Oct 2026` tooltip titles |

**Categorical palette.** Eight slots, assigned in fixed order and never cycled. Slot 1 is the Aether hue
and slot 2 the Crystal hue, stepped down into the dark-mode chart lightness band (OKLCH L 0.48–0.67),
because the brand accents themselves (L 0.82 and 0.71) are too light to sit beside other series:
`#00A4C5` and `#917EF4` keep the Aether and Crystal hues at the top of the band (L 0.665), the closest
the validator allows, and their pair sets the palette's worst CVD separation. Slots 3-8 are red
`#D24C49`, magenta `#B334AD`, blue `#4194F9`, green `#27AF57`, pink `#DD61A2` and orange `#DD7303`.
There is no gold or amber slot (the spec drops gold entirely; a test rejects OKLCH hues 60-115°): the
former amber slot 3 was replaced by a searched non-gold hue and slots 3-8 reordered so the new set still
passes. Generated (scratch search over OKLCH hue, lightness and chroma) and checked with the dataviz
palette validator against Night and Surface: every slot inside the band with chroma ≥ 0.10, worst adjacent CVD
ΔE 8.2 (protan/deutan, target 8), worst adjacent normal-vision ΔE 17.0 (floor 15), every slot ≥ 3:1 on
Surface; slots 1-4 also pass the all-pairs test, which is why regions use them. Single-series charts use
Aether itself, comparisons Crystal. Past eight categories, fold the tail into "Other" (neutral slot).

**Census taxonomy, one source.** The race order, the clan → race map and the region facts (code, display
name, display order) live in `domain/census/taxonomy.go`. A race's or region's colour slot is its 1-based
position in that order: Hyur 1, Elezen 2, Lalafell 3, Miqo'te 4, Roegadyn 5, Au Ra 6, Hrothgar 7,
Viera 8; NA 1, EU 2, JP 3, OCE 4. The server uses it for swatches and the realm tally (`taxonomy.go` in
`cmd/http/ui`), and the layout renders it into every page as
`<script type="application/json" id="census-taxonomy">{"races":[…],"regions":[…]}</script>`, which
`chart-theme.js` reads, so the chart colours and the server-rendered swatches cannot drift apart. A race
keeps its colour on every chart whatever its rank. In a
doughnut, call `sortRaces` first so neighbouring arcs are the validated adjacent pairs.

**Text alternatives.** Every `<canvas>` has `role="img"`, an `aria-label` naming the chart, and
`aria-describedby` pointing at a one-sentence summary below it; a "Show the numbers" `<details>` holds the
full data table, so the figures are readable without JavaScript.

On the dashboard the race distribution is a horizontal bar chart (eight close values compare better as
bars than as a doughnut), main-scenario completion is a list of native `<meter>` elements, and the
new-character trend on the stat tile is an arrow icon plus a word and a signed delta ("Down −124
(−5.3%) on the previous 30 days"), never colour alone.

**30-day windows use complete days.** The new-character timeline (dashboard and world detail) and the
"New characters, last 30 days" tiles and their trend (`census.NewCharactersWindow`) cover the 30 complete
UTC days before the snapshot's generation day, so the partial current day never shows as a false drop or
biases the trend. The timeline always has 30 points; its summary and "Show the numbers" table print
dates like "Oct 23" (the table wraps each in `<time datetime="2026-10-23">`).

### Error, stale and empty states

All three render through the page shell, so they keep the header, navigation and footer.

- **No snapshot (503).** When no statistics snapshot can be served (cold process, empty database or the
  snapshot service is missing), every snapshot-backed page answers `503 Service Unavailable` with
  `Retry-After: 60` and `templates/unavailable.html`: "Statistics are being prepared", a line telling
  the visitor to try again in a minute or two, a "Try again" link to the same URL and a link to
  Methodology (which renders without a snapshot). HTMX fragment requests (`HX-Request: true`) keep a
  short plain-text 503 body, which HTMX does not swap.
- **Not found (404).** An unknown world (`/ui/worlds/{world}` for a name outside the world hierarchy,
  matched case-insensitively) and any other unknown path under `/ui/` answer `404` with
  `templates/not_found.html`: a heading ("World not found" or "Page not found"), one line of direction,
  and links to the dashboard and the worlds list. The footer still shows snapshot freshness when a
  snapshot exists, but a missing snapshot never turns a 404 into a 503, and the stale notice is not shown
  (the page has no figures). Any other unmatched path answers the same styled 404 (`GET`/`HEAD`):
  `NotFoundFallback` wraps the server mux and only steps in when no route matches, so API, health,
  metrics and docs routes, a handler's own 404, method-mismatch 405s and redirects are untouched, and
  unknown paths under `/api/` keep Go's plain-text 404.
- **Freshness on snapshot-free pages.** The 404 and Methodology pages read freshness only from the
  snapshot already held in memory (`UIStatsService.Cached`); they never trigger or wait on a database
  load. With nothing cached yet, the footer reads "Statistics are being prepared".
- Both error pages set `PageData.NoIndex`: the head carries `<meta name="robots" content="noindex">`
  instead of a canonical link, and no primary-nav item is marked `aria-current`.
- **Stale notice.** `UIStatsService.Current` reports a snapshot stale only when it is at least
  `stale_warning` old, or when the most recent reload attempt failed while a warm snapshot was held.
  A routine background reload after `cache_ttl` (stale-while-revalidate, metric result `stale_served`)
  does not mark it stale. A later successful reload or refresh clears the failed-reload flag. Each load
  and refresh takes a generation number, and a load applies its outcome only if nothing newer has
  started or landed since: a slow reload that fails after a refresh installed a newer snapshot cannot
  flag it stale or reset its cache time, and a slow successful one cannot overwrite it. The notice
  (`role="status"`) reads "Statistics may be behind." and names the snapshot time; the footer adds
  "(stale)".
- **Empty states.** Every "no data here" block uses the shared `empty-state` component from
  `templates/components/states.html`, which is parsed into every page and partial. Build its argument
  with the `emptyState` / `emptyStateLink` helpers:

  ```
  {{template "empty-state" (emptyState "No race data yet." "Statistics appear after the next census refresh.")}}
  {{template "empty-state" (emptyStateLink "No worlds match these filters." "Try another region or datacenter, or show every world." "/ui/worlds" "Show all worlds")}}
  ```

  The title says what is missing; the hint says what happens next or what to do. Use the link form when
  filters caused the emptiness, pointing at the unfiltered page.

## 6. Cascading Filters

The `/ui/races` and `/ui/worlds` pages share one **filter bar** that narrows the page along the
hierarchy **Region → Datacenter → World**. Query parameters (`region`, `dc`, `world`), the cascading
option lists and canonical URLs are unchanged by the redesign.

### Filter bar component

`templates/components/filters.html` (`{{template "filter-bar" .Data.Filters}}`) renders a `FilterBar`
view model built in the controller (`filters.go`):

- **Selects** (`FilterSelect`): each `<select>` has an id and a visible `<label for>` above it
  (Region, Datacenter, World), an "All …" first option, and submits the form on change
  (`onchange="this.form.submit()"`). Without JavaScript a `<noscript>` Apply button submits it.
  Region options show display names (North America, Europe, Japan, Oceania) with the codes as values.
- **Pills** (`FilterPills`, Worlds only): a `role="group"` labelled by a visible "Region" label; each
  pill is a link, and the selected one carries `aria-current="true"`. Regions are listed in the
  canonical order NA, EU, JP, OCE.
- **Hidden fields** keep the selected region when the Worlds datacenter select submits.
- **Chips** (`FilterChip`, built by `filterChips(path, []appliedFilter)`): one per applied filter, in a
  list labelled "Applied filters". Each chip ("Region: Europe ×", accessible name "Remove filter
  Region: Europe") links to the same page with only that filter removed, keeping the others in
  `region`, `dc`, `world` order. With two or more chips a "Clear all filters" link goes to the
  unfiltered page.

### Races Page (`/ui/races`)

Three selects in a single form. Selecting a region narrows the Datacenter options to that region's
DCs; selecting a DC narrows the World options to that DC's worlds. When no parent filter is selected,
all options are shown. The most specific filter scopes the figures (world, then DC, then region).

### Worlds Page (`/ui/worlds`)

Region is chosen with the pills (a pill link drops any datacenter); the Datacenter select lists only
the DCs of the selected region, or every DC with worlds when no region is selected. The World rankings
panel carries a "Rank worlds by: Characters / Active players" switch (`?sort=active`, default by
characters; ties fall back to characters, then name). It keeps the filters, swaps only the rankings
region over HTMX and marks the ranked column with `aria-sort="descending"`.

### Implementation

Filter lists are derived server-side using `DCsForRegion()` and `WorldsForDC()` helper
functions in `cmd/http/ui/world_data.go`, which delegate to the world hierarchy in the
domain (world → datacenter → region). The same hierarchy resolves the scope of every
new-characters (30d) card and column. Each filter change triggers a full page reload.

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

