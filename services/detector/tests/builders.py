"""Concise builders for domain values in tests."""

from datetime import UTC, datetime
from decimal import Decimal

from detector.domain.model import Book, Level, Side, Trade

T0 = datetime(2026, 11, 24, 12, 0, 0, tzinfo=UTC)


def levels(*pairs: tuple[str, str]) -> tuple[Level, ...]:
    """Levels from (price, quantity) strings, in the order given."""
    return tuple(Level(Decimal(p), Decimal(q)) for p, q in pairs)


def book(
    exchange: str = "kraken",
    symbol: str = "BTC-USD",
    *,
    bids: tuple[Level, ...] = (),
    asks: tuple[Level, ...] = (),
    published_ts: datetime = T0,
    sequence: int = 1,
) -> Book:
    return Book(
        exchange=exchange,
        symbol=symbol,
        sequence=sequence,
        bids=bids,
        asks=asks,
        exchange_ts=published_ts,
        received_ts=published_ts,
        published_ts=published_ts,
    )


def trade(
    exchange: str,
    price: str,
    published_ts: datetime,
    *,
    symbol: str = "BTC-USD",
    trade_id: str = "1",
    taker_side: Side = "buy",
) -> Trade:
    return Trade(
        exchange=exchange,
        symbol=symbol,
        trade_id=trade_id,
        price=Decimal(price),
        quantity=Decimal("0.01"),
        taker_side=taker_side,
        exchange_ts=published_ts,
        received_ts=published_ts,
        published_ts=published_ts,
    )
