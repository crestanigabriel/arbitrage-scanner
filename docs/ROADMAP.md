# Roadmap

## Context
Portfolio project to show Go and Python skills: a **Go ingestor** streams live order books from
Kraken and Coinbase, normalizes them and publishes them on **NATS JetStream** as **Protobuf**. A
**Python detector** reads them and finds cross-exchange arbitrage after fees and depth. It stores
opportunities and processing stats in **PostgreSQL + TimescaleDB**. **Prometheus + Loki + Grafana**
show metrics, logs and data. There is no custom frontend: Grafana is the screen.

The repo currently has only `docs/ARCHITECTURE_PRINCIPLES.md`, and its rules are binding:
- composition over inheritance
- depend on interfaces
- constructor injection, with all wiring in a single composition root per service (`cmd/ingestor/main.go`, `detector/main.py`)

Every external thing goes behind an interface so tests can use fakes: WebSocket conns, NATS, DB,
clock, logger, config.

What makes this stand out from a toy project:
- correct order-book maintenance (Kraken checksum)
- VWAP depth walking with fees
- stale-quote rejection
- tracking opportunities as *lifecycles* (open → peak → close), not one row per tick
- a deterministic record/replay mode, because real arbitrage above fees between Kraken and Coinbase is rare and the demo needs to show something

Working mode: one milestone at a time, each reviewed before the next starts.

## Monorepo layout (target)
```
proto/marketdata/v1/*.proto        buf.yaml, buf.gen.yaml (Go + Python codegen)
services/ingestor/                 Go module
  cmd/ingestor/main.go             composition root
  internal/exchange/{kraken,coinbase,replay}/   adapters implementing BookSource
  internal/book/                   local order book + checksum
  internal/publish/                Publisher interface + NATS impl
  internal/{config,metrics,clock}/
services/detector/                 Python (uv, src layout, Python 3.13 via uv)
  src/detector/domain/             pure logic: BookStore, VWAP, fees, Opportunity lifecycle
  src/detector/ports.py            Protocols: BookFeed, OpportunitySink, Repository, Clock
  src/detector/adapters/{nats,postgres}/
  src/detector/main.py             composition root
  tests/{unit,integration}/
deploy/
  docker-compose.yml
  migrations/                      plain SQL (dbmate)
  prometheus/  loki/  alloy/  grafana/{provisioning,dashboards}/
fixtures/recordings/               captured raw feeds for replay
docs/  ARCHITECTURE_PRINCIPLES.md, architecture.md, adr/
Makefile       .github/workflows/ci.yml
```

## Milestones

### M0 — Foundations
- Create the layout above, plus `.gitignore`, `.editorconfig`, README skeleton, and a `Makefile` with `.PHONY` targets `lint`, `test`, `up`, `down`, `gen` (kept compatible with the GNU Make 3.81 that ships with macOS; CI calls the same targets).
- Go module with a hello-world `main`; `golangci-lint` config.
- Python project on the Astral toolchain, all configured in `pyproject.toml`:
  - **uv** installs Python 3.13 (pinned in `.python-version`) and manages dependencies, with `uv.lock` committed. CI runs `uv sync --locked`.
  - **ruff** handles both linting and formatting (`ruff check`, `ruff format --check`), so there's no black/isort/flake8.
  - **ty** type checks, with warnings treated as errors (`ty check --error-on-warning`).
  - pytest config. Every tool runs through `uv run`, so local and CI use the same versions.
- `docker-compose.yml` containing only NATS with JetStream enabled.
- GitHub Actions CI: Go and Python jobs, using path filters.
- **Done when:** `make lint test` passes locally and in CI, and `make up` starts NATS.

### M1 — Shared contracts (Protobuf)
- `marketdata.v1.OrderBookSnapshot`:
  - exchange, canonical symbol (`BTC-USD`), sequence
  - top-N bids/asks, with prices and quantities as **decimal strings** so there are no float errors (Python uses `Decimal`)
  - `exchange_ts`, `received_ts`, `published_ts`
- `arb.v1.Opportunity` and lifecycle events.
- Subject convention: `md.book.<exchange>.<symbol>`, `arb.opportunity.<symbol>`.
- `buf lint` + `buf breaking` in CI. Generated code is committed, and CI fails if `buf generate` produces a diff.
- ADR-001 records the choice of Protobuf and decimal strings.

### M2 — Go ingestor, Kraken only
- Interfaces:
  - `BookSource` (`Run(ctx, out chan<- BookUpdate) error`)
  - `Publisher`
  - `Clock`
  - `Conn`, a thin WS abstraction so tests can fake it
- Kraken WS v2 `book` channel: snapshot plus deltas, a local book per symbol, CRC32 checksum check, resubscribe when the checksum doesn't match.
- Reconnect with exponential backoff and jitter. Graceful shutdown with `context` and `errgroup`.
- Publish *conflated* top-N snapshots to JetStream, so Python doesn't need to rebuild books.
- Logging with `log/slog` in JSON, injected. Config from env.
- Tests: table-driven book tests, checksum tests against recorded Kraken frames, adapter tests over a fake `Conn`, and `go test -race`.

### M3 — Coinbase adapter, metrics, container
- Coinbase Advanced Trade WS `level2` channel. Check during this milestone that it is still public without auth; if not, fall back to the Exchange `level2_batch` feed.
- Symbol normalization (`BTC/USD` / `BTC-USD` → canonical). Watch the USD vs USDC quote currency.
- **Proof of the abstraction:** adding Coinbase changes only the adapter package and `main.go`.
- Prometheus `/metrics`:
  - `updates_total{exchange,symbol}`
  - `reconnects_total`
  - `checksum_failures_total`
  - publish latency histogram
- `/healthz` endpoint. Multi-stage distroless Dockerfile, added to compose.
- Go benchmarks for applying book updates.

### M4 — Python detector: pure domain (no IO)
- `BookStore` holds the latest snapshot per (exchange, symbol).
- Detection: for each pair of exchanges, check buying on A's asks and selling on B's bids.
  - Walk the book depth to get the VWAP for a configured notional.
  - Subtract per-exchange taker fees.
  - Keep the opportunity if net spread ≥ threshold in bps.
- Reject quotes older than `max_staleness_ms` by the injected `Clock`.
- An `OpportunityTracker` state machine moves each opportunity through open → update (peak spread, size) → close (duration).
- Tests: pytest plus **hypothesis** property tests (e.g. no opportunity when books are identical; net spread decreases as fees grow).

### M5 — Detector wiring and end-to-end flow
- `ports.py` Protocols, with adapters for:
  - a JetStream durable pull consumer (nats-py, asyncio)
  - an opportunity publisher back onto NATS
- Composition root in `main.py`; pydantic-settings for config; structlog for JSON logs.
- Prometheus metrics:
  - `books_processed_total`
  - detection latency histogram
  - `opportunities_opened_total`
  - end-to-end lag (`now - exchange_ts`)
- Dockerfile (uv-based), added to compose.
- **Done when:** `make up` streams live books through Go → NATS → Python, and opportunities show up on `arb.opportunity.*` (check with the `nats` CLI).

### M6 — Persistence (TimescaleDB)
- Migrations via a dbmate container in compose:
  - `opportunities` table: lifecycle rows with start, end, peak bps, size, exchanges
  - `spread_samples` hypertable
  - `processing_stats` hypertable, for periodic rollups of books processed per exchange/symbol
- `Repository` Protocol with an asyncpg implementation; batched writes off the hot path.
- Integration tests with testcontainers-python against a real Timescale container.

### M7 — Observability
- Prometheus scrapes both services.
- Grafana Alloy ships container logs to Loki. Promtail is deprecated; Alloy replaces it.
- Grafana is provisioned as code:
  - datasources: Prometheus, Loki, Timescale
  - dashboard "Pipeline health": update rates, reconnects, lag, errors
  - dashboard "Market": live spreads per pair, from Timescale
  - dashboard "Opportunities": table, counts, duration distribution
  - a logs panel
- Optionally, a couple of alert rules (feed stale, checksum failures).

### M8 — Record and replay
- Ingestor `--record` writes raw frames as NDJSON. A `replay` adapter implements `BookSource` from a file, at original or accelerated speed.
- A crafted fixture with known opportunities makes the demo and tests deterministic.
- CI e2e job:
  1. compose up in replay mode
  2. check that the expected opportunities exist in Postgres

### M9 — Portfolio polish
- README: pitch, mermaid architecture diagram, quickstart (`make up` → Grafana URL), screenshots/GIF, design decisions.
- `docs/architecture.md` and ADRs (Protobuf, conflated snapshots, opportunity lifecycle, DI approach).
- CI badges; benchmark results; "what I'd do next" section.

### Stretch (optional)
- A third exchange (Binance/Bitstamp), to show adapter scalability.
- Triangular arbitrage within one exchange.
- OpenTelemetry traces across Go → NATS headers → Python.
- k8s manifests / Helm chart.

## Verification (per milestone)
- `make lint test` passes: golangci-lint, `go test -race ./...`, ruff (check + format), ty, pytest.
- From M5 on: `make up`, then `nats sub 'arb.opportunity.>'` shows events, and the counters move on `/metrics` for both services.
- From M6 on: `psql` queries on `opportunities` / `processing_stats` return rows.
- From M7 on: Grafana at `localhost:3000` shows all three dashboards with live data and logs.
- From M8 on: the CI e2e job is green in replay mode.
- Every new component is checked against the ARCHITECTURE_PRINCIPLES checklist: can it be unit tested with injected fakes and no changes to the component?
