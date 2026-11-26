"""Cross-exchange arbitrage detection: buy on one exchange's asks, sell on another's bids.

For each ordered pair of exchanges (A, B) quoting a symbol, the detector:

1. spends the configured notional N on A's asks, which buys a quantity Q;
2. sells that same Q into B's bids, for proceeds P;
3. charges each leg its taker fee: cost C = N x (1 + fee_A), revenue R = P x (1 - fee_B);
4. reports the net spread (R - C) / C in basis points.

Every pair gets an outcome. Which spreads count as opportunities is the tracker's decision.

A book older than `max_staleness` (by `published_ts`) takes its pairs out of detection, and so
do levels too thin to fill the trade.
"""

from collections.abc import Sequence
from dataclasses import dataclass
from datetime import datetime, timedelta
from decimal import Decimal, localcontext
from typing import Protocol

from detector.domain.model import ARITHMETIC, BPS, Book, Side
from detector.domain.vwap import Fill, Shortfall, buy_for_notional, sell_quantity
from detector.ports import Clock


class Books(Protocol):
    """Where the detector reads the latest books. `BookStore` implements it."""

    def for_symbol(self, symbol: str) -> Sequence[Book]: ...


class TradeHistory(Protocol):
    """Recent trade prices, as context. `RecentTrades` implements it."""

    def last_price(self, exchange: str, symbol: str, now: datetime) -> Decimal | None: ...


class Fees(Protocol):
    """Taker fees. `FeeSchedule` implements it."""

    def taker_rate(self, exchange: str) -> Decimal: ...


@dataclass(frozen=True, slots=True)
class DetectionConfig:
    """How pairs are priced. The threshold for an opportunity is the tracker's."""

    notional: Decimal  # quote currency spent on the buy leg
    max_staleness: timedelta  # oldest book still used, by published_ts

    def __post_init__(self) -> None:
        if not self.notional.is_finite() or self.notional <= 0:
            raise ValueError(f"notional must be positive, got {self.notional}")
        if self.max_staleness <= timedelta(0):
            raise ValueError(f"max_staleness must be positive, got {self.max_staleness}")


@dataclass(frozen=True, slots=True)
class Pair:
    """One direction of one symbol across two exchanges."""

    symbol: str
    buy_exchange: str
    sell_exchange: str


@dataclass(frozen=True, slots=True)
class Leg:
    """One side of a priced pair."""

    exchange: str
    fill: Fill
    fee_rate: Decimal
    fee_notional: Decimal


@dataclass(frozen=True, slots=True)
class Spread:
    """A pair priced at the configured notional, both legs filled."""

    buy: Leg
    sell: Leg
    gross_bps: Decimal  # before fees
    net_bps: Decimal  # after both taker fees

    @property
    def quantity(self) -> Decimal:
        """The quantity bought on one exchange and sold on the other."""
        return self.buy.fill.quantity

    @property
    def notional(self) -> Decimal:
        """The notional spent on the buy leg, before fees."""
        return self.buy.fill.notional


@dataclass(frozen=True, slots=True)
class Stale:
    """A book of the pair is too old to trust."""

    exchange: str
    age: timedelta


@dataclass(frozen=True, slots=True)
class InsufficientLiquidity:
    """The published levels of one leg don't cover the trade.

    `wanted` and `available` are a notional for the buy leg and a quantity for the sell leg.
    """

    exchange: str
    side: Side
    wanted: Decimal
    available: Decimal


type Outcome = Spread | Stale | InsufficientLiquidity


@dataclass(frozen=True, slots=True)
class Evaluation:
    """The outcome for one pair, with the latest trade price of each exchange as context."""

    pair: Pair
    outcome: Outcome
    buy_last_trade_price: Decimal | None
    sell_last_trade_price: Decimal | None


@dataclass(frozen=True, slots=True)
class Scan:
    """Every pair of one symbol, evaluated at one time."""

    symbol: str
    at: datetime
    evaluations: tuple[Evaluation, ...]


def evaluate(
    buy_book: Book, sell_book: Book, notional: Decimal, fees: Fees
) -> Spread | InsufficientLiquidity:
    """Price buying `notional` on `buy_book` and selling the same quantity on `sell_book`."""
    bought = buy_for_notional(buy_book.asks, notional)
    if isinstance(bought, Shortfall):
        return InsufficientLiquidity(buy_book.exchange, "buy", bought.wanted, bought.available)
    sold = sell_quantity(sell_book.bids, bought.quantity)
    if isinstance(sold, Shortfall):
        return InsufficientLiquidity(sell_book.exchange, "sell", sold.wanted, sold.available)

    buy_rate = fees.taker_rate(buy_book.exchange)
    sell_rate = fees.taker_rate(sell_book.exchange)
    with localcontext(ARITHMETIC):
        buy_fee = bought.notional * buy_rate
        sell_fee = sold.notional * sell_rate
        cost = bought.notional + buy_fee
        revenue = sold.notional - sell_fee
        gross_bps = (sold.notional - bought.notional) / bought.notional * BPS
        net_bps = (revenue - cost) / cost * BPS
    return Spread(
        buy=Leg(buy_book.exchange, bought, buy_rate, buy_fee),
        sell=Leg(sell_book.exchange, sold, sell_rate, sell_fee),
        gross_bps=gross_bps,
        net_bps=net_bps,
    )


class Detector:
    """Evaluates every ordered pair of exchanges quoting a symbol."""

    def __init__(
        self,
        books: Books,
        trades: TradeHistory,
        fees: Fees,
        config: DetectionConfig,
        clock: Clock,
    ) -> None:
        self._books = books
        self._trades = trades
        self._fees = fees
        self._config = config
        self._clock = clock

    def scan(self, symbol: str) -> Scan:
        """Evaluate `symbol` now, on the latest book of each exchange."""
        now = self._clock.now()
        books = self._books.for_symbol(symbol)
        evaluations = tuple(
            self._evaluate(buy, sell, now)
            for buy in books
            for sell in books
            if buy.exchange != sell.exchange
        )
        return Scan(symbol=symbol, at=now, evaluations=evaluations)

    def _evaluate(self, buy: Book, sell: Book, now: datetime) -> Evaluation:
        outcome: Outcome
        if stale := self._stale(buy, now) or self._stale(sell, now):
            outcome = stale
        else:
            outcome = evaluate(buy, sell, self._config.notional, self._fees)
        return Evaluation(
            pair=Pair(buy.symbol, buy.exchange, sell.exchange),
            outcome=outcome,
            buy_last_trade_price=self._trades.last_price(buy.exchange, buy.symbol, now),
            sell_last_trade_price=self._trades.last_price(sell.exchange, sell.symbol, now),
        )

    def _stale(self, book: Book, now: datetime) -> Stale | None:
        age = book.age(now)
        return Stale(book.exchange, age) if age > self._config.max_staleness else None
