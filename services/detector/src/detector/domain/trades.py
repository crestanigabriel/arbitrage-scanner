"""Recent trades per book, kept as context for the opportunities."""

from collections import deque
from datetime import datetime, timedelta
from decimal import Decimal

from detector.domain.model import Trade


class RecentTrades:
    """Keeps the trades of the last `window` for each (exchange, symbol).

    At most `max_per_book` trades are kept per book, so memory stays bounded however busy the
    market is. Age is measured from `published_ts`, like the staleness of books.
    """

    def __init__(self, window: timedelta, max_per_book: int) -> None:
        if window <= timedelta(0):
            raise ValueError(f"window must be positive, got {window}")
        if max_per_book < 1:
            raise ValueError(f"max_per_book must be at least 1, got {max_per_book}")
        self._window = window
        self._max_per_book = max_per_book
        self._trades: dict[tuple[str, str], deque[Trade]] = {}

    def add(self, trade: Trade) -> None:
        """Record `trade`. Trades of one book arrive in order, oldest first."""
        key = (trade.exchange, trade.symbol)
        trades = self._trades.get(key)
        if trades is None:
            trades = self._trades[key] = deque(maxlen=self._max_per_book)
        trades.append(trade)

    def recent(self, exchange: str, symbol: str, now: datetime) -> list[Trade]:
        """Return the trades published within the window before `now`, oldest first."""
        trades = self._trades.get((exchange, symbol))
        if trades is None:
            return []
        cutoff = now - self._window
        while trades and trades[0].published_ts < cutoff:
            trades.popleft()
        return list(trades)

    def last_price(self, exchange: str, symbol: str, now: datetime) -> Decimal | None:
        """Return the price of the latest trade within the window, or None if there is none."""
        trades = self.recent(exchange, symbol, now)
        return trades[-1].price if trades else None
