# arbitrage-scanner

Real-time cross-exchange crypto arbitrage scanner. A **Go ingestor** maintains the public order
books of Kraken and Coinbase and publishes the top of each book (up to 100 levels, throttled) and
every trade to **NATS JetStream** as Protobuf. A **Python detector** finds arbitrage after fees
and depth, and **Grafana** shows the pipeline and the opportunities.

> **Status:** M2 — Go ingestor streaming Kraken books and trades to JetStream. See the [roadmap](docs/ROADMAP.md) for what comes
> next.

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
make up          # start the local stack (currently NATS with JetStream)
make down        # stop it
```

Run the ingestor against the local NATS (until it gets a container in M3):

```sh
make up
make run-ingestor
```

It streams Kraken's public feed (no API key). It publishes the top of each book to
`md.book.kraken.<symbol>` and every trade to `md.trade.kraken.<symbol>`, as Protobuf. Ctrl-C
stops it cleanly.

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
| `LOG_LEVEL`        | `info`                   | `debug`, `info`, `warn` or `error`; logs are JSON on stderr     |

## Make targets

| Target          | What it does                                                        |
|-----------------|---------------------------------------------------------------------|
| `lint`          | `lint-go` (golangci-lint), `lint-py` (ruff, ty), `lint-proto` (buf) |
| `test`          | `test-go` (`go test -race`) + `test-py` (pytest)                    |
| `fmt`           | Auto-format Go, Python and protobuf code                            |
| `up`            | `docker compose up -d --wait` for `deploy/docker-compose.yml`       |
| `down`          | Stop the local stack                                                |
| `run-ingestor`  | Run the ingestor locally with `deploy/ingestor.env` (needs `make up`) |
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
deploy/              docker-compose and, later, migrations and observability config
docs/                roadmap, architecture principles, ADRs
scripts/             repo tooling (commit message check)
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
