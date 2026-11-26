"""Market data as the domain sees it.

The vocabulary is ADR-001's: a **price** is quote currency per unit of base, a **quantity** is in
base units and a **notional** is price x quantity in quote currency. All three are exact
`Decimal`s, never floats.
"""

from dataclasses import dataclass
from datetime import datetime, timedelta
from decimal import (
    ROUND_HALF_EVEN,
    Context,
    Decimal,
    DivisionByZero,
    InvalidOperation,
    Overflow,
)
from typing import Literal

# Decimal context for domain arithmetic. Prices and quantities from the exchanges have fewer
# than 25 significant digits, so sums and products of them are exact at this precision. Only
# divisions round, far below any meaningful digit.
ARITHMETIC = Context(
    prec=50, rounding=ROUND_HALF_EVEN, traps=[InvalidOperation, DivisionByZero, Overflow]
)

# One basis point is 1/10000.
BPS = 10_000

type Side = Literal["buy", "sell"]


@dataclass(frozen=True, slots=True)
class Level:
    """One price level of a book side."""

    price: Decimal
    quantity: Decimal


@dataclass(frozen=True, slots=True)
class Book:
    """The top of one exchange's book for one symbol, as last published by the ingestor.

    Each side is best first: bids from the highest price down, asks from the lowest up.
    """

    exchange: str
    symbol: str
    sequence: int
    bids: tuple[Level, ...]
    asks: tuple[Level, ...]
    exchange_ts: datetime
    received_ts: datetime
    # The book was in sync at this time; the ingestor's keepalive renews it.
    published_ts: datetime

    def age(self, now: datetime) -> timedelta:
        """How long ago, at `now`, the ingestor last vouched for this book."""
        return now - self.published_ts


@dataclass(frozen=True, slots=True)
class Trade:
    """One execution, from the taker's side."""

    exchange: str
    symbol: str
    trade_id: str
    price: Decimal
    quantity: Decimal
    taker_side: Side
    exchange_ts: datetime
    received_ts: datetime
    published_ts: datetime
