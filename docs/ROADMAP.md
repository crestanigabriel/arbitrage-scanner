# Roadmap

## Context
Portfolio project to show Go and Python skills: a **Go ingestor** streams the public order books
and trades of Kraken and Coinbase (unauthenticated), maintains the books, and publishes the top of
each book (configurable depth up to 100 levels, throttled) and every trade on **NATS JetStream**
as **Protobuf**. A **Python detector** reads them and finds cross-exchange arbitrage after fees
and depth. It stores opportunities and processing stats in **PostgreSQL + TimescaleDB**.
**Prometheus + Loki + Grafana** show metrics, logs and data. There is no custom frontend: Grafana
is the screen.

The rules in `docs/ARCHITECTURE_PRINCIPLES.md` are binding:
- composition over inheritance
- depend on interfaces
- constructor injection, with all wiring in a single composition root per service (`cmd/ingestor/main.go`, `detector/main.py`)

Every external thing goes behind an interface so tests can use fakes: WebSocket conns, NATS, DB,
clock, logger, config.

What makes this stand out from a toy project:
- correct order-book maintenance in one place (Kraken checksum, Coinbase sequence numbers, resync), published as a throttled top-N book
- one Protobuf schema for Go and Python, with breaking-change checks and a cross-language golden test
- VWAP depth walking with fees
- stale-quote rejection
- tracking opportunities as *lifecycles* (open → peak → close), not one row per tick
- a deterministic record/replay mode, because real arbitrage above fees between Kraken and Coinbase is rare and the demo needs to show something

Working mode: one milestone at a time, each reviewed before the next starts.

## Monorepo layout (target)
```
proto/arbscanner/marketdata/v1/*.proto   buf.yaml, buf.gen.yaml at the root (Go + Python codegen)
services/ingestor/                 Go module
  cmd/ingestor/main.go             composition root
  internal/gen/                    generated Protobuf code (`make gen`)
  internal/contract/               cross-language golden tests for the contracts
  internal/exchange/{kraken,coinbase,replay}/   adapters implementing MarketSource
  internal/book/                   local order book, checksum, top-N view
  internal/throttle/               per-book publish throttle (leading + trailing edge)
  internal/publish/                Publisher interface + NATS JetStream impl
  internal/{config,metrics,clock}/
services/detector/                 Python (uv, src layout, Python 3.13 via uv)
  src/detector/domain/             pure logic: BookStore, VWAP, fees, Opportunity lifecycle
  src/detector/ports.py            Protocols: MarketFeed, Repository, Clock
  src/detector/adapters/{nats,postgres}/
  src/detector/main.py             composition root
  src/arbscanner/                  generated Protobuf code (`make gen`)
  tests/{unit,integration}/
deploy/
  docker-compose.yml
  migrations/                      plain SQL (dbmate)
  prometheus/  loki/  alloy/  grafana/{provisioning,dashboards}/
fixtures/contracts/                Protobuf golden files read by both services' tests
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
- `arbscanner.marketdata.v1`, with prices and quantities as **decimal strings** so there are no float errors (Python uses `Decimal`):
  - `OrderBook`: the complete top `depth` levels per side (at most 100), plus a `sequence` for monitoring. Consumers replace their copy with each message.
  - `Trade`: trade id, price, quantity, taker side
  - exchange, canonical symbol (`BTC-USD`) and `exchange_ts` / `received_ts` / `published_ts` on each
- Subject convention: `md.book.<exchange>.<symbol>`, `md.trade.<exchange>.<symbol>`.
- Opportunities are *not* a wire contract: they are the detector's output and go straight to the database.
- `buf lint` + `buf breaking` in CI. Generated code is committed, and CI fails if `make gen` produces a diff.
- Generators are local and pinned: buf and protoc in `./bin`, `protoc-gen-go` as a `go tool`.
- Golden files in `fixtures/contracts/`, written by Go and decoded by Python, prove both sides agree.
- ADR-001 records the choices: Protobuf, decimal strings, the throttled top-N book, the exchange feeds used, the price / quantity / notional vocabulary, and shared memory as a possible later improvement.
- **Done when:** `make lint test` passes, `make gen` leaves no diff, and the CI Protobuf, Go and Python jobs are green.

### M2 — Go ingestor, Kraken only
- Interfaces:
  - `MarketSource` (`Run(ctx, out chan<- Event) error`), emitting book updates and trades
  - `Publisher`
  - `Clock`
  - `Conn`, a thin WS abstraction so tests can fake it
- Kraken WS v2 (`wss://ws.kraken.com/v2`, no auth):
  - `book` channel: snapshot plus updates, a local book per symbol, CRC32 checksum check over the top 10, resubscribe when it doesn't match.
  - Subscribe at the smallest Kraken depth (10/25/100/500/1000) that covers `BOOK_DEPTH`, and truncate to it after each update. Kraken sends no removal for levels that fall out of range.
  - `trade` channel, normalized to `Trade` (Kraken already reports the taker side).
  - Decode numbers with `json.Number` so the exact digits survive (checksum and decimal strings), never `float64`.
- Config from env, validated at startup:
  - `BOOK_DEPTH`: levels per side, default 50, maximum 100.
  - `BOOK_THROTTLE_MS`: default 50; 0 publishes on every update.
- Throttle per book with a leading and a trailing edge: publish the first change at once, fold later changes inside the interval into one publish at its end. Inject the `Clock` so tests control time.
- Publish only books that are in sync; nothing is published while a book resyncs.
- Publish to JetStream:
  - Create the streams at startup. JetStream limits apply per stream, so books and trades get one each:
    - `MD_BOOKS` (`md.book.>`): one message per subject, since only the latest matters.
    - `MD_TRADES` (`md.trade.>`): a short max age and a duplicate window.
  - `OrderBook`s on `md.book.<ex>.<sym>`.
  - `Trade`s on `md.trade.<ex>.<sym>`, with `Nats-Msg-Id` for deduplication.
- Reconnect with exponential backoff and jitter. Graceful shutdown with `context` and `errgroup`.
- Logging with `log/slog` in JSON, injected.
- Tests: table-driven book tests (including depth truncation), checksum tests against recorded Kraken frames, throttle tests with a fake clock, adapter tests over a fake `Conn`, and `go test -race`.

### M3 — Coinbase adapter, metrics, container
- Coinbase Advanced Trade WS (`wss://advanced-trade-ws.coinbase.com`, no auth, confirmed 2026-11-23):
  - `level2` channel: the whole book (about 36k levels for BTC-USD) as a snapshot, then absolute `new_quantity` updates. There is no checksum, so gaps are detected with `sequence_num` and trigger a resubscribe.
  - `market_trades` channel (250 ms batches). Coinbase reports the *maker's* side, so invert it to the taker side.
  - `heartbeats` channel so that idle subscriptions stay open. Respect the limit of 8 connections or messages per second per IP, and send a single channel per subscribe message.
  - The ingestor keeps the whole book (Coinbase has no depth option) and publishes the top `BOOK_DEPTH` levels through the same throttle as Kraken.
- Symbol normalization (`BTC/USD` / `BTC-USD` → canonical). Watch the USD vs USDC quote currency.
- **Proof of the abstraction:** adding Coinbase changes only the adapter package and `main.go`.
- Prometheus `/metrics`:
  - `updates_total{exchange,symbol}`, `books_published_total{exchange,symbol}` (their ratio shows what the throttle saves), `trades_total{exchange,symbol}`
  - `reconnects_total`, `resyncs_total{reason}`
  - `checksum_failures_total`, `sequence_gaps_total`
  - `book_levels{exchange,symbol,side}` gauge
  - publish latency histogram
- `/healthz` endpoint. Multi-stage distroless Dockerfile, added to compose.
- Go benchmarks for applying book updates.

### M4 — Python detector: pure domain (no IO)
- `BookStore` holds the latest `OrderBook` per (exchange, symbol); each new one replaces the old.
- Keep the recent trades per book, for context in the opportunity records and as a sanity check.
- Detection: for each pair of exchanges, check buying on A's asks and selling on B's bids.
  - Walk the book depth to get the VWAP for a configured notional. If the published levels don't cover the notional, report insufficient liquidity rather than guessing.
  - Subtract per-exchange taker fees.
  - Keep the opportunity if net spread ≥ threshold in bps.
- Reject quotes older than `max_staleness_ms` by the injected `Clock`.
- An `OpportunityTracker` state machine moves each opportunity through open → update (peak spread, quantity, notional) → close (duration).
- Tests: pytest plus **hypothesis** property tests (e.g. no opportunity when books are identical; net spread decreases as fees grow; VWAP lies between the best and worst price walked).

### M5 — Detector wiring and end-to-end flow
- `ports.py` Protocols, with a JetStream adapter (nats-py, asyncio) for `MarketFeed`:
  - Books from `md.book.>`, starting from the last message per subject so a restart has every book at once.
  - Trades from `md.trade.>`.
- Composition root in `main.py`; pydantic-settings for config; structlog for JSON logs. Opportunities are logged until M6 stores them.
- Prometheus metrics:
  - `books_received_total{exchange,symbol}`, `books_out_of_order_total` (from `sequence`)
  - detection latency histogram
  - `opportunities_opened_total`
  - end-to-end lag (`now - exchange_ts`)
- Dockerfile (uv-based), added to compose.
- **Done when:** `make up` streams live books and trades through Go → NATS → Python, and opportunities appear in the logs and in `opportunities_opened_total`.

### M6 — Persistence (TimescaleDB)
- Migrations via a dbmate container in compose:
  - `opportunities` table: lifecycle rows with start, end, peak bps, quantity, notional, exchanges
  - `spread_samples` hypertable
  - `processing_stats` hypertable, for periodic rollups of books and trades received, and out-of-order books, per exchange/symbol
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
- Ingestor `--record` writes raw frames as NDJSON. A `replay` adapter implements `MarketSource` from a file, at original or accelerated speed.
- A crafted fixture with known opportunities makes the demo and tests deterministic.
- CI e2e job:
  1. compose up in replay mode
  2. check that the expected opportunities exist in Postgres

### M9 — Portfolio polish
- README: pitch, mermaid architecture diagram, quickstart (`make up` → Grafana URL), screenshots/GIF, design decisions.
- `docs/architecture.md` and ADRs (opportunity lifecycle, DI approach; ADR-001 already covers Protobuf and the throttled top-N book).
- CI badges; benchmark results; "what I'd do next" section.

### Stretch (optional)
- A third exchange (Binance/Bitstamp), to show adapter scalability.
- Triangular arbitrage within one exchange.
- OpenTelemetry traces across Go → NATS headers → Python.
- Shared memory instead of NATS for books when both services share a host. The ingestor writes fixed-layout books into an `mmap`'d region (seqlock slot or ring buffer) and the detector reads them with no serialization or broker hop. It only pays off with an unthrottled, unbatched feed and a detector without a GIL. See "Possible improvement" in ADR-001.
- k8s manifests / Helm chart.

## Verification (per milestone)
- `make lint test` passes: golangci-lint, `go test -race ./...`, ruff (check + format), ty, buf (lint + format), pytest.
- From M1 on: `make gen` leaves no diff, and the CI Protobuf job (lint, breaking, codegen drift) is green.
- From M5 on: `make up`, then `nats sub 'md.>'` shows book and trade traffic, the detector logs opportunities, and the counters move on `/metrics` for both services.
- From M6 on: `psql` queries on `opportunities` / `processing_stats` return rows.
- From M7 on: Grafana at `localhost:3000` shows all three dashboards with live data and logs.
- From M8 on: the CI e2e job is green in replay mode.
- Every new component is checked against the ARCHITECTURE_PRINCIPLES checklist: can it be unit tested with injected fakes and no changes to the component?
