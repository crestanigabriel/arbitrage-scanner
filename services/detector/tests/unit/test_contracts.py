"""Decode the protobuf golden files written by the ingestor's contract tests.

The reference values live in services/ingestor/internal/contract/golden_test.go. Both sides read
the same bytes from fixtures/contracts, so a schema or codegen mismatch between Go and Python fails
here.
"""

from datetime import UTC, datetime
from decimal import Decimal
from pathlib import Path

from arbscanner.marketdata.v1 import order_book_pb2, trade_pb2

GOLDEN_DIR = Path(__file__).resolve().parents[4] / "fixtures" / "contracts"


def at(*args: int) -> datetime:
    return datetime(*args, tzinfo=UTC)


def load_order_book() -> order_book_pb2.OrderBook:
    return order_book_pb2.OrderBook.FromString((GOLDEN_DIR / "order_book.binpb").read_bytes())


def test_order_book() -> None:
    book = load_order_book()

    assert (book.exchange, book.symbol) == ("kraken", "BTC-USD")
    assert book.sequence == 42
    assert book.depth == 50
    assert [(lvl.price, lvl.quantity) for lvl in book.bids] == [
        ("67123.40", "0.5"),
        ("67123.3", "1.25"),
    ]
    assert [(lvl.price, lvl.quantity) for lvl in book.asks] == [
        ("67123.5", "0.1"),
        ("67124", "0.2"),
        ("67124.1", "0.00012"),
    ]
    assert book.exchange_ts.ToDatetime(tzinfo=UTC) == at(2026, 11, 23, 12, 0, 0, 123456)
    assert book.received_ts.ToDatetime(tzinfo=UTC) == at(2026, 11, 23, 12, 0, 0, 125000)
    assert book.published_ts.ToDatetime(tzinfo=UTC) == at(2026, 11, 23, 12, 0, 0, 126000)


def test_order_book_decimals_are_exact() -> None:
    book = load_order_book()

    # As floats 0.1 + 0.2 != 0.3; decimal strings keep the sum of the first two asks exact.
    assert 0.1 + 0.2 != 0.3
    assert Decimal(book.asks[0].quantity) + Decimal(book.asks[1].quantity) == Decimal("0.3")
    # The trailing zero survives transit, but it is not significant.
    assert book.bids[0].price == "67123.40"
    assert Decimal(book.bids[0].price) == Decimal("67123.4")


def test_trade() -> None:
    trade = trade_pb2.Trade.FromString((GOLDEN_DIR / "trade.binpb").read_bytes())

    assert (trade.exchange, trade.symbol, trade.trade_id) == ("coinbase", "BTC-USD", "912345678")
    assert Decimal(trade.price) == Decimal("67150.01")
    assert Decimal(trade.quantity) == Decimal("0.00012")
    assert trade.taker_side == trade_pb2.SIDE_SELL
    assert trade.exchange_ts.ToDatetime(tzinfo=UTC) == at(2026, 11, 23, 12, 0, 1, 500000)
    assert trade.received_ts.ToDatetime(tzinfo=UTC) == at(2026, 11, 23, 12, 0, 1, 750000)
    assert trade.published_ts.ToDatetime(tzinfo=UTC) == at(2026, 11, 23, 12, 0, 1, 750500)
