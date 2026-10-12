# ffxiv-census

A census of FFXIV characters: it fetches public character data from community sites, stores it, and serves aggregate population statistics. This document is the project's shared language.

## Character ingestion

**Census**:
The act of fetching one character's public data from a provider and storing it as this project's record of that character.
_Avoid_: ingest, ingestion

**Character census**:
A census of a character already known to the project, refreshing its stored profile and jobs; if the providers agree the character no longer exists, it is confirmed missing.
_Avoid_: re-census, recheck, profile re-census

**ID sweep**:
A census pass that probes a range of character IDs to discover which IDs exist, storing every character it finds on the spot. Its role is discovery, not freshness; discovered characters are refreshed by later character censuses.
_Avoid_: discovery (reserved for proxy discovery), auto-discovery, sweep

**ID sweep cursor**:
The persistent position of the next unprobed character ID for automatic ID sweeps. It only ever moves forward, and only after a range has been fully handed out.
_Avoid_: frontier, discovery cursor, id_sweep_state

**Achievement census**:
A character's achievement refresh. It runs against The Lodestone exclusively, updates the character's latest earned achievement and earned milestones, and triggers nothing further.
_Avoid_: milestone check, achievement refresh

**Milestone achievement**:
One of a small, curated set of achievements tracked as progress markers — the chocobo acquisition, expansion main-scenario completions, and job level caps — instead of a character's full achievement history.
_Avoid_: checkpoint, milestone (bare)

**Milestone chain**:
The ordered sequence of milestone achievements in which earning a later one implies all earlier ones. Checking walks the chain in order and stops at the first publicly visible unearned milestone.
_Avoid_: achievement chain, chain verification, sequential dependency, early exit

**Confirmed missing**:
The conclusion that a character no longer exists, reached when the providers report not-found; the character stops being part of the census from then on. A not-found from The Lodestone alone is sufficient, because it is authoritative for existence.
_Avoid_: mark deleted, confirmed not found, character gone

**Private profile**:
A character that withholds its identifying data — no race information — from public view. It is deliberately not stored and triggers nothing further.
_Avoid_: privacy skip, skipped character

**Achievements private**:
A character whose achievement history is hidden from public view. This is recorded on the character and is a valid reason for it to have no milestone records.
_Avoid_: hidden achievement list

**The Lodestone**:
FFXIV's official community site, read as web pages rather than through an official API, and the authoritative source of character existence and profile truth. The sole provider for every census event.
_Avoid_: official API

**Provider**:
The character-data source: The Lodestone, the only one. Fetches happen per event through a Lodestone client, direct or proxy-aware.
_Avoid_: source, dual-source

**Proxy provider**:
An external service publishing lists of free proxies, fetched by proxy discovery. Not a provider of character data.
_Avoid_: provider (for proxy lists)

## Queue and events

**Event type**:
The named kind of work a job carries — currently ID sweep, character census, achievement census, and new-proxy. It routes the job to its queue and its event handler.
_Avoid_: job type, routing key, event (bare)

**Queue job**:
One unit of asynchronous work: an event type plus its payload, published by a publisher and executed by a worker.
_Avoid_: message, task

**Publisher**:
The component or scheduled job that enqueues queue jobs and proceeds only once the broker has confirmed each one.
_Avoid_: producer, cronjob entrypoint

**Event handler**:
The domain logic registered for one event type. It processes a single job and returns the jobs that should be published next.
_Avoid_: handler (bare — this repo also has HTTP handlers)

**Worker**:
A long-running process that consumes queue jobs of one event type and dispatches them to the registered event handler.
_Avoid_: consumer, consumer process

**Chaining**:
The pattern in which a handled event causes further events to be published, forming the ingest graph — an ID sweep chains an achievement census for every character it discovers.
_Avoid_: event cascading, downstream jobs, follow-up job

**Leaf event**:
An event type that chains nothing.
_Avoid_: terminal event

**Failed queue**:
The per-event-type holding area for jobs that exhausted their retries, where they stay parked until requeued or deliberately discarded. Retry attempts before exhaustion pass back through it automatically.
_Avoid_: dead-letter queue, DLQ, retry exchange (stale phrasing)

**Push-based consumption**:
The delivery model in which the broker hands jobs to running workers as they arrive, with no polling or claiming. It replaced the project's earlier polling job table.
_Avoid_: claim loop, pull consumption

## Proxy system

**Proxy pool**:
The maintained set of free proxies that census workers rotate through as outbound addresses when contacting The Lodestone.
_Avoid_: pool, proxies (bare)

**Proxy discovery**:
The continuous fetch of proxy lists from proxy providers, registering only addresses not already in the pool.
_Avoid_: streaming discovery

**Rotating discovery client**:
The HTTP client used only to fetch proxy lists, routed through random pool proxies. It is forbidden for provider traffic.
_Avoid_: rotating proxy client, discovery HTTP client

**Proxy scan**:
The recurring re-test of pool proxies against The Lodestone that updates each proxy's status, latency, and liveness. It reads proxies straight from the database, not through the queue.
_Avoid_: scan worker, health check

**Proxy status**:
A proxy's standing in the pool: active (last test reached The Lodestone), inactive (recently failed but not written off), or dead (written off, re-tested only occasionally).
_Avoid_: health status

**Dead-scan percentage**:
The fixed share of proxy-scan capacity reserved for re-testing dead proxies, so dead re-validation and live scanning can never starve each other.
_Avoid_: dead pool (it is not a pool of proxies), dead worker percentage

**Proxy lock**:
The exclusive, time-limited hold one worker has on a proxy. Expired holds may be taken over by other workers.
_Avoid_: proxy lease

**Test-before-handout**:
The rule that a proxy is verified with a live check before a worker is ever asked to use it.
_Avoid_: blind handout

**Proxy mode**:
The census-worker mode in which every worker owns a proxy for the duration of a job and all provider requests route through it — there are no direct requests.
_Avoid_: proxied mode

**Provider cooldown**:
The temporary pause on requests to one provider after it signals a rate limit. It is never treated as a proxy failure and never triggers a proxy switch.
_Avoid_: provider pause, rate-limit error

**Immediate proxy switch**:
The policy of abandoning a proxy on its first transport-level failure rather than retrying through it.
_Avoid_: bad-proxy retry

## Statistics

**Statistics snapshot**:
The complete, versioned set of precomputed aggregates that all analytics pages and aggregate API routes serve from, replacing per-request scans of raw character data.
_Avoid_: read model, UI stats snapshot, cache (bare)

**Snapshot refresh**:
The out-of-band rebuild of the statistics snapshot that computes all aggregates in one pass and swaps the stored snapshot atomically. Overlapping refreshes are skipped.
_Avoid_: ui-stats refresh, recompute

**Snapshot cache**:
Each web process's in-memory copy of the current statistics snapshot, reloaded at most once per interval. A process with no snapshot serves an explicit unavailable state rather than falling back to raw scans.
_Avoid_: warm/cold cache, page cache

**Stats scope**:
The geographic slice a statistic is computed for: global, a region, a datacenter, or a world.
_Avoid_: aggregation scope, scope (bare)

**Demographic dimension**:
The population axis a group is counted along: race, tribe, gender, race-by-gender, or world.
_Avoid_: dimension (bare), grouping

**Activity window**:
The bounded trailing period, currently thirty days, that defines which characters count as active and bounds the date ranges a statistic may be requested for.
_Avoid_: 30-day window

**New character**:
The canonical definition of a newly created character: one that has earned the chocobo milestone, counted per UTC day.
_Avoid_: new-character series, created character, new player (players own characters; the census counts characters)

**Max-level character**:
A character whose census record shows at least one class or job at the current level cap — combat, crafting, or gathering alike — counted once per character no matter how many of its jobs sit at the cap.
_Avoid_: maxed character, max-level jobs (the census counts characters, not jobs)

**MSQ completion funnel**:
The expansion-by-expansion view of how many characters' milestone records show each expansion's main-scenario story completed, read as retention and drop-off. Characters with private or unscanned achievements are invisible to it, however far they have progressed.

**Cascading filters**:
The linked region → datacenter → world filters in which each selection narrows the next.
_Avoid_: drill-down filters

**World drill-down**:
The descent from region to datacenter to world population numbers without a full page reload.
_Avoid_: world breakdown

**World hierarchy**:
The fixed game geography mapping every world to its datacenter and every datacenter to its region.
_Avoid_: world data

## Public site

**Indexable page**:
A UI page served as complete HTML at its canonical URL and listed in the sitemap: the dashboard, races, worlds, per-world, expansions, and methodology pages. HTMX partials, the JSON API, and the disabled character pages are not indexable.
_Avoid_: SEO page, content page, crawlable page
