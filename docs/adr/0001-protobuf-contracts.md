# ADR-001: Protobuf market data contracts

- **Status:** accepted
- **Date:** 2026-11-23
- **Milestone:** M1

## Context

The Go ingestor and the Python detector are separate services in separate languages, connected
only through NATS JetStream.

The ingestor reads public, unauthenticated book and trade feeds. These were checked against the
docs and the live feeds on 2026-11-23 for BTC-USD:

| | Kraken WS v2 | Coinbase Advanced Trade WS |
|---|---|---|
| Book | `book`, depth 10/25/100/500/1000 per side; snapshot, then absolute updates; CRC32 over the top 10 | `level2`, the whole book (about 36k levels); snapshot, then absolute updates; `sequence_num` |
| Rate seen | about 240 book messages/s at depth 1000, real time | about 19 events/s, batched at roughly 50 ms |
| Trades | `trade`, taker side | `market_trades`, maker side, 250 ms batches |
| Needs auth | only `level3` (individual orders) | only `user` and `futures_balance_summary` |

The detector needs a correct book deep enough to walk the VWAP for its configured notional, plus
the trades. A book that is a few milliseconds old is fine. The exchanges themselves batch at
50–250 ms.

The format has to:

- generate typed code for both languages from one source
- let the schema change without silently breaking the other side
- carry prices and quantities without losing precision

Arbitrage spreads are a few basis points, so rounding errors are the same size as the signal.

## Decision

### Protobuf (proto3), managed with buf

- The schema lives in `proto/`. Go and Python code is generated from it by `make gen`, and the
  generated code is committed.
- CI runs three checks:
  - `buf lint` with the `STANDARD` rules, plus `buf format`
  - `buf breaking` with `FILE` rules against the base commit
  - a check that `make gen` produces no diff
- Every generator is local and pinned:
  - `protoc-gen-go` is a `go tool` of the ingestor module, so it always matches the Go runtime.
  - The Python and `.pyi` generators come from a pinned protoc (`PROTOC_VERSION` in the
    Makefile), whose minor version matches the Python `protobuf` runtime.
  - Nothing depends on the buf.build remote plugins, so codegen works offline and isn't subject
    to rate limits.
- Golden files in `fixtures/contracts/` are written by Go and decoded by Python. They prove that
  both sides read the same bytes the same way.

Protobuf was chosen over the alternatives for these reasons:

- **JSON:** has no schema, no codegen and no breaking-change check. Messages are larger and
  slower to parse.
- **Avro:** needs a schema registry to be useful, and its Go and Python tooling is weaker.
  Schema evolution is the only thing it does better, and buf's breaking-change check covers that.

### The ingestor owns the book and publishes a throttled top-N `OrderBook`

| Subject | Message | When |
|---|---|---|
| `md.book.<exchange>.<symbol>` | `OrderBook`: the top `BOOK_DEPTH` levels per side | at most once per `BOOK_THROTTLE_MS` per book, always the latest state |
| `md.trade.<exchange>.<symbol>` | `Trade` | every trade, not throttled |

- **All book logic lives in the ingestor:**
  - It applies the exchange snapshot and updates, and verifies checksums and sequence numbers.
  - It resubscribes when verification fails. Coinbase is the exception (amended in M3).
    Its `sequence_num` counts every message of the connection, across all channels, so a gap
    can't be traced to one book. The ingestor resets every Coinbase book and reconnects
    instead.
  - It publishes only books that are in sync. While a book resyncs nothing is published, so the
    detector's staleness check takes that book out of detection.
- **Each message is the complete top of the book.** The detector replaces its copy with the
  latest one and keeps no other state. There are no deltas, no recovery protocol and no book
  code in Python. `sequence` is there only to monitor dropped or reordered messages.
- **Configuration lives in the ingestor, because that is where books are built:**
  - `BOOK_DEPTH`: levels per side, default 50, maximum 100. It is validated at startup.
  - `BOOK_THROTTLE_MS`: default 50, and 0 publishes on every update.
  - The ingestor subscribes at the smallest exchange depth that covers `BOOK_DEPTH`. That is
    Kraken depth 100 for both 50 and 100. Coinbase always sends the whole book.
- **The throttle** has both a leading and a trailing edge. The first change after a quiet
  interval is published at once. Later changes in the same interval are folded into one publish
  at its end. A book is never more than one interval out of date, and a busy book costs at most
  1000 / `BOOK_THROTTLE_MS` messages per second.
- **Message size:** 100 levels × 2 sides at about 24 bytes per level is about 5 KB per book. At
  50 ms that is about 100 KB/s per book, so the NATS defaults (1 MB payload) are fine.
- **JetStream:** books and trades go to two streams, because JetStream applies its retention
  limits to a whole stream and the two need different ones. (Amended in M2. The first version
  of this ADR named a single `MARKETDATA` stream.)
  - `MD_BOOKS` (`md.book.>`, memory storage) keeps one message per subject, since only the
    latest one matters. A consumer that starts or restarts reads the last message per subject,
    so it never waits for the next publish.
  - A NATS restart drops a memory stream entirely, configuration included. The ingestor
    recreates the streams when JetStream reports that no stream took a message, then
    publishes again (amended in M3).
  - `MD_TRADES` (`md.trade.>`, file storage) keeps trades for 10 minutes, with a 2 minute
    duplicate window for `Nats-Msg-Id`.

The other options considered, and why they were rejected:

- **Snapshot + delta stream:** this is the lowest latency at full depth. But the detector would
  need its own book replica, with sequence, session and gap-recovery logic. With a depth of 100
  and a 50 ms throttle a full book costs very little, so that complexity is not justified.
- **The full book on every update:** a Coinbase book is about 0.9 MB, and Kraken updates arrive
  at about 240/s at depth 1000. That works out to 10–16 MB/s per symbol, plus decoding tens of
  thousands of levels in Python every time.
- **Raw exchange messages:** these would put exchange-specific parsing and checksum logic in the
  Python service, which is what the ingestor exists to hide.

### Vocabulary: price, quantity, notional

The same three words are used in the schema, the code of both services, the database and the
dashboards:

| Term | Meaning | Unit | Example |
|---|---|---|---|
| **price** | quote currency per one unit of base | quote per base | `67123.4` USD per BTC |
| **quantity** | how much of the base asset | base | `0.15` BTC |
| **notional** | the value in quote currency: price × quantity | quote | `10000` USD |

- `quantity` follows FIX (`OrderQty`, `LastQty`), Kraken (`qty`) and Coinbase (`new_quantity`).
- We never use `amount`, `size`, `volume` or `qty` for these values. "Amount" in particular
  means base units in some APIs (CCXT, Bitfinex) and money in others. That ambiguity is
  dangerous here, because the detector walks a book of quantities to fill a target notional.
- Exchange-specific names are translated at the adapter boundary. For example, Coinbase's trade
  `size` becomes `quantity`.
- Derived values keep the word: `vwap_price`, `fee_notional`, `available_quantity`.

### Decimals as strings

- Prices and quantities are `string` fields holding plain decimals: `^[0-9]+(\.[0-9]+)?$`. They
  have no exponent and no thousands separator.
- Consumers parse them with an exact decimal type: `decimal.Decimal` in Python, a decimal library
  in Go. They never use a float.
- Kraken sends prices as JSON numbers. The ingestor must keep the literal text, using
  `json.Number` or an equivalent, rather than decoding to `float64`. The CRC32 checksum is
  computed over those same digits.

The alternatives were rejected for these reasons:

- **`double`:** `0.1 + 0.2 != 0.3`. Exchanges send decimal text in the first place, so a double
  adds error that was not there before.
- **Scaled `int64` (price × 10^k):** it needs a scale per symbol and exchange, can overflow on
  very small quantities, and every consumer needs the scale table.
- **A `Decimal` message (`units` + `nanos`):** it is exact but awkward to use from Python.
  Strings map directly to `Decimal(s)`.

### Other schema choices

- **Symbols are canonical `BASE-QUOTE`** (`BTC-USD`) whatever the exchange calls them: Kraken's
  `BTC/USD` is translated by its adapter. On Coinbase, `-USDC` products other than USDT-USDC
  and EURC-USDC are aliases of the `-USD` book, and Coinbase reports their data under the
  `-USD` product id. The Coinbase adapter rejects those symbols at startup, so the same book is
  never published twice under two names (amended in M3).
- **Exchange is a lowercase string** (`"kraken"`), not an enum. It is the same token used in the
  subjects. Adding a third exchange then touches only its adapter and `main.go`, with no schema
  change.
- **Trades are normalized to the taker side.** Coinbase reports the maker's side, so the
  ingestor inverts it. `trade_id` is a string whatever the exchange's native type is. The
  ingestor sets `Nats-Msg-Id` to `<exchange>.<symbol>.<trade_id>` so JetStream drops duplicates
  after a reconnect.
- **Namespaced package `arbscanner.marketdata.v1`.** Python gencode imports by proto package, so
  the package name becomes a top-level Python package. A project prefix keeps that to one
  package, `arbscanner`, next to `detector`.
- **Opportunities are not a wire contract.**
  - Opportunities are the detector's output. They are a Python domain type that the detector
    writes straight to TimescaleDB.
  - Grafana reads them from there with SQL and never sees Protobuf. No other service consumes
    them, so they are not published on NATS.
  - If a consumer appears (an alerter, an executor), an `arbscanner.arb.v1` package is added
    then.

### Subject tokens

- `<exchange>` is the lowercase exchange id. `<symbol>` is the canonical upper-case `BASE-QUOTE`.
- Neither token may contain `.`, `*`, `>` or whitespace, which NATS reserves.
- Useful wildcards:
  - `md.book.*.BTC-USD` for one symbol across exchanges
  - `md.trade.>` for all trades
  - `md.>` for everything

## Consequences

- Schema changes go through `proto/` and `make gen`, and CI rejects stale generated code and
  breaking changes. A real break means a new package version (`v2`) published alongside `v1`.
- The detector sees at most 100 levels per side. A notional that needs more depth than that
  counts as insufficient liquidity, and the detector reports it as such rather than guessing.
- Detection latency is bounded below by the throttle. Lowering `BOOK_THROTTLE_MS` trades
  bandwidth and detector CPU for freshness, with no code change.
- Both services pay a small cost to parse decimals. Any arithmetic on prices must use decimal
  types.
- Bumping protoc means bumping the Python `protobuf` runtime too. The generated code checks the
  runtime version at import, so a mismatch fails fast in tests.
- Coinbase's fastest unauthenticated book feed is batched at about 50 ms. The unbatched one needs
  authentication, so it is out of scope.

## Possible improvement: shared memory instead of NATS

When the ingestor and detector run on the same host and microseconds matter, the book could be
passed through shared memory instead of NATS. The ingestor would write fixed-layout books into
an `mmap`'d region, for example a seqlock-protected slot per book or a ring buffer (as in Aeron
IPC). The detector would read them in place, with no serialization and no broker hop.

That is not done here, for these reasons:

- **Coupling:** it ties both services to one machine.
- **Lost features:** it gives up what JetStream provides: replay for M8, several consumers,
  monitoring, and the `nats` CLI for debugging.
- **Engineering cost:** it needs a binary layout both Go and Python agree on, plus careful
  memory-ordering code on both sides.
- **No measurable gain:** the latency budget is set by the exchanges (50–250 ms batching) and by
  the throttle. A NATS hop on localhost is in the tens to hundreds of microseconds, so shared
  memory would not change the result.

It becomes worth doing if the throttle goes to 0, the feeds become unbatched (authenticated or
colocated), and the detector moves to a language without a GIL.
