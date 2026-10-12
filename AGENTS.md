# AGENTS.md

## Quick Reference

```bash
# Build & run
make build && ./bin/ffxiv-census server --start --port 8080

# Test (strict TDD enforced — red/green/refactor)
make test                    # all tests
go test -v ./path/to/pkg     # single package
go test -run TestName ./pkg  # single test

# Verify before commit
make lint                    # golangci-lint
make fmt                     # gofmt

# Migration (runs automatically on boot, but manual ops available)
./bin/ffxiv-census migrate --direction up|down
```

## Architecture

**Hexagonal (ports &amp; adapters) with service locator:**

- `port/contract` — all interfaces (DatabaseDriver, Queue, LodestoneClient, etc.)
- `domain/` — pure business logic, tech-agnostic, never imports infrastructure
- `infrastructure/` — concrete adapters implementing port contracts
- `container/` — service locator with global `container.Load`; lazy accessor pattern
- `cmd/cli` — cobra commands; `cmd/http` — net/http server + middleware

**Key patterns:**

- Service locator resolves adapters via `container.Load.Postgres()`, `container.Load.Queue()`, etc. — not DI
- Adapters implement interfaces from `port/contract`; domain depends only on contracts
- `cmd/` constructs domain objects directly; everything else stays decoupled
- Config embedded via `//go:embed config.toml` + Viper; env overrides: `POSTGRES_DSN`, `LODESTONE_RATE_LIMIT` (dots and hyphens → underscores)

## Build Constraints

Cross-compile targets use `CGO_ENABLED=0` (Makefile build-linux-amd64, etc.). **Do not use CGO-dependent libraries.** Use pure-Go alternatives where needed.

## Migrations

**Runtime migrations:** `infrastructure/postgres` driver runs `goose.Up()` on first `Postgres()` use. Every binary (server, workers) self-migrates at boot. No separate migration step needed.

Manual ops: `./bin/ffxiv-census migrate --direction down` rolls back all migrations (destructive).

## Testing Discipline

**Strict TDD required:** write failing test first, watch it fail, then minimal code to pass. No production code without a failing test. User will enforce this in reviews.

- Every port gets a fake in `mock/` (two adapters per port rule)
- Postgres tests use temp databases (real SQL, not mocks)
- Worker pool tests: `go test -race`
- Table-driven tests for handlers/domain services

## Documentation

Detailed architecture and operational guides are maintained under `docs/`:

- Overview &amp; Quickstart: `README.md` and `docs/getting-started.md`
- System Architecture &amp; Locator: `docs/architecture.md` and `docs/container.md`
- Database &amp; Migrations: `docs/postgres.md`, `docs/external-postgres.md`, and `docs/census.md`
- Queue &amp; Worker Engine: `docs/queue.md` and `docs/events.md`
- External Adapters: `docs/lodestone.md`
- HTTP REST APIs &amp; Metrics: `docs/http-api.md`, `docs/metrics.md`, and `docs/logging-and-middleware.md`
- Web Interface: `docs/ui.md`

Existing historical specs and plans remain under `docs/superpowers/`; new ones go in GitHub issues. When adding features or changing behavior, always keep documentation in sync with code.

Server-rendered Go templates + HTMX in `cmd/http/ui/` (embedded via `//go:embed`). No separate frontend build pipeline. Vendored Chart.js for charts, no CDN dependencies.

## Workflow

- **Always prompt before implementation** — user will ask "proceed?" before dispatching implementation subagents
- **Specs and tickets are GitHub issues** — see `docs/agents/issue-tracker.md` (spec issue → sub-issue tickets → code)
- **Commit and push** — always commit all changes (code, tests, plans, specs) and push to remote on completion
- **Commit skills artifacts** (`.agents/`, `.claude/`, `skills-lock.json`) — they're project-local skill installs
- **Main model responsibilities** — code edits, mechanical formatting, architectural decisions, planning, implementation, and test suites are handled directly by the main model.


## Agent skills

### Issue tracker

Issues live in GitHub Issues (`mihaiflorentin/ffxiv-census`, via `gh`). See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context; the glossary is `GLOSSARY.md` at the repo root, ADRs in `docs/adr/`. See `docs/agents/domain.md`.
