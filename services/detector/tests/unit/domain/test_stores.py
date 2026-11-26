from datetime import timedelta
from decimal import Decimal

import pytest

from builders import T0, book, trade
from detector.domain.book_store import BookStore
from detector.domain.fees import FeeSchedule
from detector.domain.trades import RecentTrades

MS = timedelta(milliseconds=1)


class TestBookStore:
    def test_a_newer_book_replaces_the_old(self) -> None:
        store = BookStore()
        first, second = book(sequence=1), book(sequence=2, published_ts=T0 + MS)

        assert store.put(first)
        assert store.put(second)
        assert store.get("kraken", "BTC-USD") is second

    def test_an_older_book_is_ignored_and_reported(self) -> None:
        store = BookStore()
        newer = book(sequence=7, published_ts=T0 + MS)
        store.put(newer)

        assert not store.put(book(sequence=6, published_ts=T0))
        assert store.get("kraken", "BTC-USD") is newer

    def test_published_ts_decides_not_sequence(self) -> None:
        # The sequence restarts with the ingestor.
        store = BookStore()
        store.put(book(sequence=500))
        restarted = book(sequence=1, published_ts=T0 + MS)

        assert store.put(restarted)
        assert store.get("kraken", "BTC-USD") is restarted

    def test_books_are_kept_per_exchange_and_symbol(self) -> None:
        store = BookStore()
        kraken, coinbase = book("kraken"), book("coinbase")
        eth = book("kraken", "ETH-USD")
        for b in (kraken, eth, coinbase):
            store.put(b)

        assert store.for_symbol("BTC-USD") == [coinbase, kraken]  # ordered by exchange
        assert store.for_symbol("ETH-USD") == [eth]
        assert store.for_symbol("SOL-USD") == []
        assert store.get("coinbase", "ETH-USD") is None


class TestRecentTrades:
    def test_last_price_is_the_latest_trade_in_the_window(self) -> None:
        trades = RecentTrades(window=timedelta(seconds=10), max_per_book=100)
        trades.add(trade("kraken", "100", T0))
        trades.add(trade("kraken", "101", T0 + MS))
        trades.add(trade("coinbase", "200", T0))

        assert trades.last_price("kraken", "BTC-USD", T0 + MS) == Decimal(101)
        assert trades.last_price("coinbase", "BTC-USD", T0 + MS) == Decimal(200)
        assert trades.last_price("kraken", "ETH-USD", T0) is None

    def test_trades_older_than_the_window_are_dropped(self) -> None:
        trades = RecentTrades(window=timedelta(seconds=10), max_per_book=100)
        trades.add(trade("kraken", "100", T0, trade_id="1"))
        trades.add(trade("kraken", "101", T0 + timedelta(seconds=5), trade_id="2"))

        at = T0 + timedelta(seconds=12)
        assert [t.trade_id for t in trades.recent("kraken", "BTC-USD", at)] == ["2"]
        assert trades.last_price("kraken", "BTC-USD", T0 + timedelta(seconds=16)) is None

    def test_at_most_max_per_book_are_kept(self) -> None:
        trades = RecentTrades(window=timedelta(hours=1), max_per_book=2)
        for i in range(5):
            trades.add(trade("kraken", "100", T0 + i * MS, trade_id=str(i)))

        assert [t.trade_id for t in trades.recent("kraken", "BTC-USD", T0)] == ["3", "4"]

    @pytest.mark.parametrize(
        ("window", "max_per_book"), [(timedelta(0), 1), (timedelta(seconds=1), 0)]
    )
    def test_invalid_limits(self, window: timedelta, max_per_book: int) -> None:
        with pytest.raises(ValueError, match="must be"):
            RecentTrades(window, max_per_book)


class TestFeeSchedule:
    def test_bps_become_a_rate(self) -> None:
        fees = FeeSchedule({"kraken": Decimal(40), "coinbase": Decimal("120")})

        assert fees.taker_rate("kraken") == Decimal("0.004")
        assert fees.taker_rate("coinbase") == Decimal("0.012")

    def test_an_unknown_exchange_is_an_error(self) -> None:
        with pytest.raises(KeyError, match="no taker fee configured for exchange 'binance'"):
            FeeSchedule({"kraken": Decimal(40)}).taker_rate("binance")

    @pytest.mark.parametrize("bps", [Decimal(-1), Decimal(10_000), Decimal("NaN")])
    def test_out_of_range_fees_are_rejected(self, bps: Decimal) -> None:
        with pytest.raises(ValueError, match="taker fee of 'kraken'"):
            FeeSchedule({"kraken": bps})
