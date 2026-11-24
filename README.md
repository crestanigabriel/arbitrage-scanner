# arbitrage-scanner

Real-time cross-exchange crypto arbitrage scanner. A **Go ingestor** maintains the public order
books of Kraken and Coinbase and publishes the top of each book (up to 100 levels, throttled) and
every trade to **NATS JetStream** as Protobuf. A **Python detector** finds arbitrage after fees
and depth, and **Grafana** shows the pipeline and the opportunities.

> **Status:** M1 — shared Protobuf contracts. See the [roadmap](docs/ROADMAP.md) for what comes
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

## Make targets

| Target          | What it does                                                        |
|-----------------|---------------------------------------------------------------------|
| `lint`          | `lint-go` (golangci-lint), `lint-py` (ruff, ty), `lint-proto` (buf) |
| `test`          | `test-go` (`go test -race`) + `test-py` (pytest)                    |
| `fmt`           | Auto-format Go, Python and protobuf code                            |
| `up`            | `docker compose up -d --wait` for `deploy/docker-compose.yml`       |
| `down`          | Stop the local stack                                                |
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
