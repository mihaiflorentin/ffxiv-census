# Logging & Middleware

Middlewares live in `cmd/http/middleware` and compose the HTTP pipeline in `cmd/http/server.go`.

Default order:

1. **Logging** — emits single-line structured logs via `LoggingColored` (overridable in config; `color-simple` is an alias for `color`).
2. **Auth** *(optional)* — validates `X-Auth-Token` if enabled in `config.toml`.
3. **Max Requests** — gracefully drains the server after a configurable number of requests.
4. **Metrics** *(optional)* — forwards timings to StatsD.

## Logging Modes

Configure via `logging.server_default` in `config/config.toml`:

- `color` / `color-simple` — human friendly console output.
- `json` / `pretty-json` — machine-friendly structured logs.
- `simple` — plain text without colour.

Use `logging.Init` in `main.go` to set the default logger for the entire process.

## Structured Logging for Publishers, Consumers, and Handlers

The RabbitMQ queue adapter (`infrastructure/rabbitmq`), the census worker (`domain/census/worker`),
and the three census handlers (`domain/census/handler`) log through a `contract.Logger`
interface that mirrors `*log/slog.Logger` (`DebugContext`/`InfoContext`/`WarnContext`/
`ErrorContext` with alternating key/value args, plus `Enabled` for level checks).
`infrastructure/logging.Logger` is the concrete implementation — a compile-time assertion
in `infrastructure/logging/logger.go` pins it to the contract, so no adapter type exists.
A discard logger (returned when `nil` is injected) always returns `false` from `Enabled`,
allowing callers to skip expensive attribute construction.

Injection:

- Obtain the logger via `container.Load.Logger()`; never import `infrastructure/logging`
  outside the container and `main`.
- Constructors take `logger contract.Logger` as their last parameter. A `nil` logger is
  substituted with a discard logger, so forgetting injection is silent-and-safe.
- Level policy: `Info` = one completed queue job (`Job completed successfully` after all downstream
  publishes succeed) plus lifecycle/provider/proxy state; `Warn` = transient retry/fetch
  errors; `Error` = terminal failure; `Debug` = fetch/store/probe detail, per-ID sweep
  probes, and `Processing job`. Debug is opt-in via `logging.level = "debug"`
  (`LOGGING_LEVEL=debug`); no new config keys were added. Per-ID ID-sweep logs and the
  achievement milestone diagnostic are guarded with `logger.Enabled(ctx, slog.LevelDebug)`
  to avoid building slog attributes when Debug is disabled.

### Consumer log events (live)

A running `consume` worker emits one line per processing moment, with the identifiers
relevant to the event (IDs, names, worlds). All messages are descriptive sentences;
structured attributes carry the identifying context.

#### Character Census (`character-census`)

| Level | Message | Attributes |
|-------|---------|------------|
| Debug | `Processing character census` | `character_id` |
| Debug | `Fetched character from Lodestone` | `character_id`, `name`, `world`, `datacenter`, `duration` |
| Debug | `Stored character in database` | `character_id`, `name`, `world` |
| Debug | `Character census complete` | `character_id`, `name`, `world`, `chained_jobs` |
| Debug | `Character marked as deleted` | `character_id` |
| Warn | `Failed to fetch character` | `character_id`, `source`, `error` |
| Error | `Failed to store character` | `character_id`, `name`, `world`, `source`, `error` |

#### Achievement Census (`achievement-census`)

| Level | Message | Attributes |
|-------|---------|------------|
| Debug | `Processing achievement census` | `character_id` |
| Info | `Waiting for Lodestone rate limiter` | `character_id` |
| Warn | `Failed to query known milestones` | `character_id`, `error` |
| Debug | `Skipping achievement census, all milestones already known` | `character_id`, `known_milestones` |
| Warn | `Failed to fetch achievements from Lodestone` | `character_id`, `error`, `duration` |
| Debug | `Achievements are private` | `character_id` |
| Error | `Failed to process milestone results` | `character_id`, `error` |
| Debug | `Achievement census complete` | `character_id`, `milestones`, `requests`, `private`, `duration` |

#### ID Sweep (`id-sweep`)

| Level | Message | Attributes |
|-------|---------|------------|
| Debug | `Scanning ID range` | `from`, `to`, `count` |
| Debug | `Discovered new character` | `character_id`, `name`, `world`, `source` |
| Debug | `Character not found` | `character_id`, `source` |
| Debug | `ID range scan complete` | `from`, `to`, `discovered` |
| Warn | `Failed to fetch character` | `character_id`, `source`, `error` |
| Error | `Failed to store character` | `character_id`, `name`, `world`, `source`, `error` |

#### Worker

| Level | Message | Attributes |
|-------|---------|------------|
| Info | `Worker started` | `event_types`, `concurrency` |
| Error | `No handler registered for event type` | `event_type` |
| Debug | `Processing job` | `event_type` |
| Info | `Job completed successfully` | `event_type`, `duration` |
| Warn | `Job failed, retrying` | `event_type`, `error`, `attempt` |
| Error | `Failed to publish follow-up job` | `event_type`, `error` |

#### Queue (RabbitMQ)

| Level | Message | Attributes |
|-------|---------|------------|
| Warn | `Discarding permanently failed message` | `event_type`, `reason`, `attempts` |
| Error | `Failed to republish failed message` | `event_type`, `error` |
| Info | `Republished failed message for retry` | `event_type`, `attempt` |
| Warn | `Message permanently failed after max retries` | `event_type`, `attempts` |
| Error | `Failed to publish message to dead letter queue` | `event_type`, `error` |
| Warn | `Retrying message publish` | `event_type`, `attempt`, `error` |
| Error | `Failed to republish message after retry` | `event_type`, `error` |

`Processing job` is Debug (event type only); `Job completed successfully` is Info and emitted once
per fully successful job after all downstream publishes succeed. The full fetch → store →
complete flow is visible at Debug level; at Info level only one `Job completed successfully` line
appears per completed job. Per-ID sweep probes and the achievement milestone
diagnostic are guarded with `logger.Enabled(ctx, slog.LevelDebug)` to avoid building
slog attributes when Debug is disabled.
