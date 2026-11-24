# arbitrage-scanner

Real-time cross-exchange crypto arbitrage scanner. A **Go ingestor** maintains the public order
books of Kraken and Coinbase and publishes the top of each book (up to 100 levels, throttled) and
every trade to **NATS JetStream** as Protobuf. A **Python detector** finds arbitrage after fees
and depth, and **Grafana** shows the pipeline and the opportunities.

> **Status:** M3 — Go ingestor streaming Kraken and Coinbase books and trades to JetStream, with
> Prometheus metrics and a container. See the [roadmap](docs/ROADMAP.md) for what comes next.

## Prerequisites

- [Go](https://go.dev/dl/) 1.27+
- [uv](https://docs.astral.sh/uv/). It installs Python 3.13 and the Python tooling for you.
- [Docker](https://docs.docker.com/get-docker/) with Compose v2+
- GNU Make (the 3.81 that ships with macOS is fine)

`golangci-lint`, `buf` and `protoc` are downloaded automatically into `./bin` at pinned versions
the first time a target needs them (or all at once with `make tools`). `protoc-gen-go` is a
`go tool` of the ingestor module, so it needs no install.

## Quickstart

```sh
make lint test   # all linters and tests, same as CI
make up          # build and start the local stack: NATS with JetStream, and the ingestor
make down        # stop it
```

The ingestor streams the public feeds of Kraken and Coinbase (no API keys). It publishes the
top of each book to `md.book.<exchange>.<symbol>` and every trade to
`md.trade.<exchange>.<symbol>`, as Protobuf. Once it is up:

```sh
curl localhost:8080/healthz    # "ok" while connected to NATS, 503 otherwise
curl localhost:8080/metrics    # Prometheus metrics, listed below
```

To run the ingestor from source instead, start only NATS. The container and a local run both
listen on port 8080, so they can't run at the same time.

```sh
make up-nats
make run-ingestor   # Ctrl-C stops it cleanly
```

### Ingestor configuration

All configuration comes from environment variables and is validated at startup. Every
variable is optional and falls back to its default.

`make run-ingestor` loads them from [`deploy/ingestor.env`](deploy/ingestor.env). That file
lists every variable with its default, and you can edit it. Variables already set in your
shell win over the file, and `INGESTOR_ENV` selects a different file:

```sh
BOOK_DEPTH=100 BOOK_THROTTLE_MS=0 make run-ingestor   # one-off override
make run-ingestor INGESTOR_ENV=my.env                 # another file
```

A test checks that `deploy/ingestor.env` loads and lists exactly the variables below, so a
misspelt name can't be ignored silently.

| Variable           | Default                  | Meaning                                                        |
|--------------------|--------------------------|----------------------------------------------------------------|
| `SYMBOLS`          | `BTC-USD`                | Comma-separated canonical symbols, e.g. `BTC-USD,ETH-USD`      |
| `BOOK_DEPTH`       | `50`                     | Levels per side in each published book, 1–100                  |
| `BOOK_THROTTLE_MS` | `50`                     | Minimum interval between publishes of one book; 0 publishes every update |
| `NATS_URL`         | `nats://localhost:4222`  | NATS server                                                    |
| `KRAKEN_WS_URL`    | `wss://ws.kraken.com/v2` | Kraken WebSocket API v2 endpoint                               |
| `COINBASE_WS_URL`  | `wss://advanced-trade-ws.coinbase.com` | Coinbase Advanced Trade WebSocket endpoint       |
| `HTTP_ADDR`        | `:8080`                  | Listen address of `/metrics` and `/healthz`                    |
| `LOG_LEVEL`        | `info`                   | `debug`, `info`, `warn` or `error`; logs are JSON on stderr     |

Every symbol is streamed from both exchanges. On Coinbase, `BTC-USD` is the unified USD/USDC
book: `-USDC` symbols are aliases of it and are rejected at startup, except USDT-USDC and
EURC-USDC. The compose service reads the same file and only points `NATS_URL` at the `nats`
container.

### Ingestor metrics

`GET /metrics` on `HTTP_ADDR` serves these, plus the standard Go and process metrics:

| Metric                                      | Type      | Meaning                                                    |
|---------------------------------------------|-----------|------------------------------------------------------------|
| `updates_total{exchange,symbol}`            | counter   | In-sync book updates received, before the throttle          |
| `books_published_total{exchange,symbol}`    | counter   | OrderBooks published; the ratio to `updates_total` is what the throttle saves |
| `trades_total{exchange,symbol}`             | counter   | Trades received                                            |
| `resyncs_total{exchange,symbol,reason}`     | counter   | Books taken out of sync: `checksum` (Kraken), `sequence` (Coinbase), `protocol`, `reconnect` |
| `reconnects_total{exchange}`                | counter   | Connections lost or dials failed                           |
| `book_levels{exchange,symbol,side}`         | gauge     | Levels in the local book: about 37k for Coinbase BTC-USD, at most 100 for Kraken |
| `publish_duration_seconds{kind}`            | histogram | Publish to JetStream ack, `book` or `trade`                |
| `publish_errors_total{kind}`                | counter   | Failed publishes, `book` or `trade`                        |

`GET /healthz` answers 200 while the NATS connection is up and 503 otherwise. The image has no
shell or curl, so its healthcheck is the binary itself: `/ingestor healthcheck`.

### Book benchmarks

`go test -bench . ./internal/book` (in `services/ingestor`) runs the benchmarks on a book the
size of Coinbase's BTC-USD (12k bids, 25k asks). Each side is stored worst first, so the
best price is at the end of its slice. Inserts and removals land near the best price: in a
live capture, half were within 60 levels of it and 90% within 146. At the end of the slice
they move almost nothing. Apple M5 Pro, before and after that change:

| Benchmark                       | Best first | Worst first |
|---------------------------------|-----------:|------------:|
| remove + insert at the top      | 60.2 µs    | 155 ns      |
| remove + insert 5 levels down   | 60.1 µs    | 180 ns      |
| remove + insert at the bottom   | 167 ns     | 62.2 µs     |
| replace a quantity (any depth)  | 70–87 ns   | 75–87 ns    |
| load a whole snapshot           | 7.3 ms     | 7.0 ms (`Load`, sorted once) |
| `Top(100)`                      | 1.8 µs     | 2.4 µs      |

## Make targets

| Target          | What it does                                                        |
|-----------------|---------------------------------------------------------------------|
| `help`          | List the targets (the default goal)                                 |
| `lint`          | `lint-go` (golangci-lint), `lint-py` (ruff, ty), `lint-proto` (buf) |
| `test`          | `test-go` (`go test -race`) + `test-py` (pytest)                    |
| `fmt`           | Auto-format Go, Python and protobuf code                            |
| `up`            | `docker compose up -d --wait --build` for `deploy/docker-compose.yml` |
| `up-nats`       | Start only NATS, for `make run-ingestor`                            |
| `down`          | Stop the local stack                                                |
| `run-ingestor`  | Run the ingestor locally with `deploy/ingestor.env` (needs `make up-nats`) |
| `gen`           | Regenerate Go and Python code from `proto/` (commit the result)     |
| `breaking`      | `buf breaking` against `BREAKING_REF` (default `main`)              |
| `tools`         | Install pinned tool binaries into `./bin`                           |
| `hooks`         | Enable the git hooks in `.githooks` (commit message check)          |
| `check-commits` | Check commit messages in `RANGE` (default `origin/main..HEAD`)      |

## Repository layout

```
proto/               Protobuf contracts shared by both services (see ADR-001)
services/ingestor/   Go module: exchange adapters → maintained books + trades → NATS
services/detector/   Python (uv): consumes books and trades, detects and tracks opportunities
fixtures/            test data shared across services (Protobuf golden files)
deploy/              docker-compose (NATS, ingestor), ingestor.env and, later, migrations and observability config
docs/                roadmap, architecture principles, ADRs
scripts/             repo tooling (commit message check, env file loader for `make run-ingestor`)
.githooks/           git hooks, enabled with `make hooks`
```

## Commits

Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org):
`<type>(<scope>): <description>`, for example `feat(ingestor): add Kraken order book adapter`.
Allowed types are `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`
and `revert`. CI rejects non-conforming commits, and `make hooks` turns on the same check locally.

## Design rules

The code follows the binding [architecture principles](docs/ARCHITECTURE_PRINCIPLES.md):
composition over inheritance, depending on interfaces, and constructor injection wired in a single
composition root per service.
