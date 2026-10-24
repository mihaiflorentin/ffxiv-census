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

**Gap fill**:
An ID sweep over the unprobed gaps between stored character IDs, published in capped runs. Each run queues at most a fixed number of ID sweep jobs and resumes from the gap-fill cursor; an explicit minimum ID runs a manual pass that leaves the cursor untouched. Not-found IDs are deliberately not remembered, so resurrected characters are rediscovered on the next cycle.
_Avoid_: fill-gaps (bare), 404 ledger

**Gap-fill cursor**:
The persistent last-queued ID for gap fills, separate from the ID sweep cursor. It wraps back to 0 once no gaps remain after it (or it reaches the highest stored character ID), and the same run searches again from the lowest ID, so the gap fill cycles for ever.
_Avoid_: fill_gaps_state, gap cursor

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
The conclusion that a character no longer exists, reached when the providers report not-found; the character stops being part of the regular character census from then on. Only a small periodic batch of them is re-probed, so a character that comes back is found again. A not-found from The Lodestone alone is sufficient, because it is authoritative for existence.
_Avoid_: mark deleted, confirmed not found, character gone

**Private profile**:
A character that withholds its identifying data — no race information — from public view. It is deliberately not stored and triggers nothing further.
_Avoid_: privacy skip, skipped character

**Achievements private**:
A character whose achievement history is hidden from public view. This is recorded on the character and is a valid reason for it to have no milestone records.
_Avoid_: hidden achievement list

**Achievements unchecked**:
A character that is not achievements private but has no latest achievement recorded yet: the census has not read its achievement history. It is the census's backlog, not the player's choice, and is always counted apart from achievements private. Shown to readers as "Not yet checked".
_Avoid_: unscanned, pending privacy

**Private recheck window**:
How long a character found achievements private is left alone before its achievement census asks The Lodestone again — 30 days by default. A job inside the window is skipped and succeeds; a character whose flag was never verified is checked once.
_Avoid_: private skip, private cooldown

**The Lodestone**:
FFXIV's official community site, read as web pages rather than through an official API, and the authoritative source of character existence and profile truth. The sole provider for every census event.
_Avoid_: official API

**Provider**:
The character-data source: The Lodestone, the only one. Fetches happen per event through a Lodestone client, direct or proxy-aware.
_Avoid_: source, dual-source

**Proxy provider**:
An external service publishing lists of free proxies, fetched directly by proxy discovery. Not a provider of character data.
_Avoid_: provider (for proxy lists)

## Queue and events

**Event type**:
The named kind of work a job carries — currently ID sweep, character census and achievement census. It routes the job to its queue and its event handler.
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

**Result writer**:
The per-worker component that event handlers hand a finished census result to instead of storing it themselves. A handler waits until the result's write batch has committed, so a queue job is acknowledged only once its data is durable. Marking a character confirmed missing is the one write that bypasses it.
_Avoid_: batch writer, persister, store (as a noun)

**Write batch**:
The results the result writer has gathered, stored together in one database transaction. It closes when it holds a set number of results or about a second has passed. If the database rejects one result, that result is split off and retried alone so its neighbours still commit; if the batch fails for any other reason, every job in it stays unacknowledged and is retried through a delay tier.
_Avoid_: flush (as a noun for the unit), chunk

**Chaining**:
The pattern in which a handled event causes further events to be published, forming the ingest graph — an ID sweep chains an achievement census for every character it discovers.
_Avoid_: event cascading, downstream jobs, follow-up job

**Leaf event**:
An event type that chains nothing.
_Avoid_: terminal event

**Delay tier**:
One of the retry queues a failed job waits in before it returns to the main queue: 5 seconds, 30 seconds, 5 minutes and 1 hour by default, each a separate queue whose expiry sends the job back on its own. The tier comes from the job's attempt count, which counts only failures the job itself caused; a failure caused by a proxy (dial failure, challenge, timeout, 429, 5xx) does not raise it.
_Avoid_: backoff queue, retry ladder (as a place), failed queue (the legacy name)

**Failed queue**:
The legacy per-event-type holding area where retries waited, with a per-message backoff, before delay tiers existed. Nothing is published to it any more; it stays declared only so jobs still parked there can drain, and it can be removed once empty.
_Avoid_: dead-letter queue, DLQ, retry exchange (stale phrasing)

**Dead queue**:
The per-event-type terminal parking area for jobs that cannot be routed or decoded (poison payloads, unknown event types) or that exceed the retry safety-net attempt limit. Nothing is silently dropped.
_Avoid_: dead-letter queue, DLQ

**Retry worker**:
The transition worker that drains the legacy failed queues without sleeping, moving each job to the main exchange under its canonical event type, into the delay tier that covers its remaining wait, or to the dead queue, acknowledging only after the broker confirms. New failures do not pass through it.
_Avoid_: failed worker, census-retry (that is the deployment name)

**Push-based consumption**:
The delivery model in which the broker hands jobs to running workers as they arrive, with no polling or claiming. It replaced the project's earlier polling job table.
_Avoid_: claim loop, pull consumption

## Proxy system

**Proxy pool**:
The maintained set of free proxies that census workers rotate through as outbound addresses when contacting The Lodestone.
_Avoid_: pool, proxies (bare)

**Proxy discovery**:
The recurring cron run that fetches proxy lists straight from the proxy providers (never through the pool), registers the addresses not already in the pool with bulk inserts, and marks every listed address as listed again. Its per-run limit is split fairly across providers, and lists a provider reports as unchanged are skipped.
_Avoid_: streaming discovery

**Proxy scan**:
The recurring re-test of pool proxies that keeps their general-health evidence current. It reads proxies straight from the database, not through the queue, and is spread across replicas by scan leases.
_Avoid_: scan worker, health check

**General-health check**:
The proxy test against a neutral public IP-echo endpoint (not The Lodestone), accepted only on a valid JSON response within the deadline. It establishes whether a proxy works at all; it says nothing about whether The Lodestone accepts it.
_Avoid_: ping, liveness check

**Fresh proxy**:
A proxy whose latest general-health check succeeded recently enough (within the freshness window) with no newer failure. Only fresh proxies count as available; stored status is historical and is never the live count.
_Avoid_: alive proxy, active proxy (for live availability)

**Scan queue**:
One of three logical queues the proxy scan draws from: verification (re-checking proxies whose evidence is about to expire), recovery (retrying recently working proxies that failed, with growing delays), and background (a continuous sweep of every non-succeeding proxy that is not retired). Each holds a reserved share of scan concurrency and lends unused capacity to the others.
_Avoid_: scan pool, cohort, dead pool

**Scan lease**:
The fenced, expiring claim one scanner holds on a proxy for a single scan attempt, so replicas never check the same proxy at once and stale results are discarded. Distinct from the proxy lock.
_Avoid_: scan lock

**Endpoint guard**:
The per-replica baseline check of the general-health endpoint itself. While it is unhealthy, general-health claims pause and deadline failures are not held against proxies.
_Avoid_: circuit breaker

**Proxy status**:
A proxy's historical classification in the pool: active, inactive, or dead. It never gates the background scan (retirement does) and never decides live availability; freshness does.
_Avoid_: health status

**Retired proxy**:
A proxy that never worked, has failed at least a set number of times (default 10) and was first seen more than a set number of days ago (default 14). The background scan skips it, but its row stays in the pool so the unique key keeps discovery from registering it again. A provider listing it again revives it once it has been retired for a set time (default 7 days), which also resets its failure count. Unlike proxy status, retirement gates scanning.
_Avoid_: dead proxy (that is the historical status), deleted proxy, banned proxy, archived proxy

**Proxy lock**:
The exclusive, time-limited hold one census worker has on a proxy while using it. Expired holds may be taken over by other workers.
_Avoid_: proxy lease (that is the scan lease), consumer lease

**Freshness-only handout**:
The rule that a proxy is handed to a worker on scan freshness alone (general health verified within the freshness window, 120 s), with ownership and freshness revalidated against the database right after the claim. No separate Lodestone request is made at hand-out; the first real request of the job is the destination check, and a proxy The Lodestone rejects then is released and put on a destination cooldown at once.
_Avoid_: Test-before-handout, handout check, destination check at handout, blind handout

**Destination cooldown**:
The temporary exclusion of a proxy from Lodestone use after The Lodestone challenges, rejects, rate-limits or times out on it. It does not mark the proxy generally unhealthy. It starts at the cooldown floor (60 s) and doubles for each consecutive failure of the same proxy up to a configurable cap (15 min), never undercutting a server `Retry-After`; the first successful job on the proxy resets the streak. A cooled proxy is not handed out again until the cooldown ends, and afterwards it ranks behind proxies with no recent failures.
_Avoid_: proxy ban, proxy failure

**Proxy mode**:
The census-worker mode in which every worker owns a proxy for the duration of a job and all provider requests route through it — there are no direct requests.
_Avoid_: proxied mode

**Provider cooldown**:
The temporary pause on requests to one provider after it signals a rate limit. It is never treated as a proxy failure and never triggers a proxy switch.
_Avoid_: provider pause, rate-limit error

**Immediate proxy switch**:
The policy of abandoning a proxy on its first transport-level failure rather than retrying through it. In proxy mode the same job is retried in-process on a different proxy (at most three proxies per job) after a proxy-dial failure, challenge, timeout or server error; a 404 or private outcome is never a reason to switch.
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
The bounded trailing period, 90 days by default (`[census] activity_window_days`), that defines which characters count as active. It is wide because a full census pass takes months. Copy states its length from the snapshot, never as a literal.
_Avoid_: 30-day window, 90-day window (the length is configurable)

**New character**:
The canonical definition of a newly created character: one that has earned the chocobo milestone, counted per UTC day.
_Avoid_: new-character series, created character, new player (players own characters; the census counts characters)

**New-characters window**:
The bounded trailing period, 90 days by default (`[census] new_characters_window_days`), that new characters are counted over: whole UTC days ending the day before the snapshot was built, compared with the window of the same length before it. It also bounds the dates the new-characters statistic may be requested for. It is wide because the census sees a new chocobo only when it next visits the character. Copy states its length from the snapshot, never as a literal.
_Avoid_: 30-day window, 90-day window (the length is configurable)

**Max-level character**:
A character whose census record shows at least one class or job at the current level cap — combat, crafting, or gathering alike — counted once per character no matter how many of its jobs sit at the cap.
_Avoid_: maxed character, max-level jobs (the census counts characters, not jobs)

**Job bitmask**:
One of the two 64-bit values stored on each character recording which jobs it has at any level and which it has at their level cap, one bit per job (base classes folded into their jobs; the limited jobs, Blue Mage and Beastmaster, measured against their own lower caps). Empty until computed, by a census write or the one-off backfill.
_Avoid_: job flags, job set

**Any level**:
A job a character has at level 1 or more, the Jobs page's default measure beside "at the level cap". The stored mask and code keep the older name "levelled".
_Avoid_: levelled (on any page), unlocked

**Job coverage**:
How many characters the job figures include: those whose job bitmasks were computed when the nightly rebuild ran, out of all characters it counted. Below 100% while the backfill is still running.
_Avoid_: job completeness

**Party balance**:
The split of combat jobs at the level cap into tanks, healers and DPS for a datacenter or selection, counted per job (a character with two tanks at the cap counts twice) and leaving out the limited jobs (Blue Mage, Beastmaster). Compared against a light party's 1 tank, 1 healer and 2 DPS.
_Avoid_: role ratio, role distribution

**Level band**:
The expansion a character's highest job level falls in: each expansion's band runs from the previous expansion's level cap (level 1 for A Realm Reborn) to just below its own, and the last band, "Level cap", is the game's current maximum level. Derived from the configured expansions. Characters with no stored job levels are counted apart as "No job levels yet" and left out of the shares.
_Avoid_: level bracket, tier, max level (bare)

**Members seen**:
The live characters the census has found in a Free Company: the size the Free Company boards rank by. Not the official roster, which only the Lodestone shows.
_Avoid_: members (bare), roster size, FC size

**Free Company board**:
The top 100 Free Companies of a world, datacenter, region or the whole game, ranked by members seen or by active members, among companies with at least 2 members seen. Rebuilt nightly from each world's own top 100 for each measure, kept in a snapshot row of its own.
_Avoid_: FC leaderboard, guild ranking

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

**Data-sync notice**:
The dashboard banner stating that the census is still gathering characters from The Lodestone and that full coverage is expected by January 2027. Figures shown before then are partial.
_Avoid_: maintenance banner, warning
