from datetime import timedelta
from decimal import Decimal

import pytest

from builders import T0, book, levels, trade
from detector.domain.book_store import BookStore
from detector.domain.detection import (
    DetectionConfig,
    Detector,
    InsufficientLiquidity,
    Pair,
    Spread,
    Stale,
    evaluate,
)
from detector.domain.fees import FeeSchedule
from detector.domain.trades import RecentTrades
from fakes import FakeClock

NO_FEES = FeeSchedule({"kraken": Decimal(0), "coinbase": Decimal(0)})
FEES = FeeSchedule({"kraken": Decimal(10), "coinbase": Decimal(20)})

KRAKEN = book("kraken", bids=levels(("99", "1")), asks=levels(("100", "1")))
COINBASE = book("coinbase", bids=levels(("101", "1")), asks=levels(("102", "1")))


def test_spread_before_fees() -> None:
    spread = evaluate(KRAKEN, COINBASE, Decimal(50), NO_FEES)

    assert isinstance(spread, Spread)
    assert (spread.quantity, spread.notional) == (Decimal("0.5"), Decimal(50))
    assert spread.sell.fill.notional == Decimal("50.5")
    assert spread.gross_bps == spread.net_bps == Decimal(100)


def test_taker_fees_are_charged_on_both_legs() -> None:
    spread = evaluate(KRAKEN, COINBASE, Decimal(50), FEES)

    assert isinstance(spread, Spread)
    assert spread.gross_bps == Decimal(100)
    assert (spread.buy.fee_rate, spread.sell.fee_rate) == (Decimal("0.001"), Decimal("0.002"))
    assert (spread.buy.fee_notional, spread.sell.fee_notional) == (
        Decimal("0.05"),
        Decimal("0.101"),
    )
    # cost 50 x 1.001 = 50.05, revenue 50.5 x 0.998 = 50.399
    assert round(spread.net_bps, 10) == Decimal("69.7302697303")


def test_the_buy_leg_short_of_asks() -> None:
    assert evaluate(KRAKEN, COINBASE, Decimal(500), NO_FEES) == InsufficientLiquidity(
        exchange="kraken", side="buy", wanted=Decimal(500), available=Decimal(100)
    )


def test_the_sell_leg_short_of_bids() -> None:
    deep_kraken = book("kraken", asks=levels(("100", "10")))

    assert evaluate(deep_kraken, COINBASE, Decimal(500), NO_FEES) == InsufficientLiquidity(
        exchange="coinbase", side="sell", wanted=Decimal(5), available=Decimal(1)
    )


@pytest.mark.parametrize(
    ("notional", "max_staleness", "match"),
    [
        (Decimal(0), timedelta(seconds=1), "notional must be positive"),
        (Decimal("Infinity"), timedelta(seconds=1), "notional must be positive"),
        (Decimal(1), timedelta(0), "max_staleness must be positive"),
    ],
)
def test_invalid_config(notional: Decimal, max_staleness: timedelta, match: str) -> None:
    with pytest.raises(ValueError, match=match):
        DetectionConfig(notional=notional, max_staleness=max_staleness)


class TestDetector:
    def setup_method(self) -> None:
        self.clock = FakeClock(T0)
        self.books = BookStore()
        self.trades = RecentTrades(window=timedelta(seconds=60), max_per_book=10)
        config = DetectionConfig(notional=Decimal(50), max_staleness=timedelta(seconds=3))
        self.detector = Detector(self.books, self.trades, NO_FEES, config, self.clock)

    def test_every_ordered_pair_is_evaluated(self) -> None:
        self.books.put(KRAKEN)
        self.books.put(COINBASE)
        self.trades.add(trade("kraken", "99.5", T0))

        scan = self.detector.scan("BTC-USD")

        assert (scan.symbol, scan.at) == ("BTC-USD", T0)
        assert [e.pair for e in scan.evaluations] == [
            Pair("BTC-USD", buy_exchange="coinbase", sell_exchange="kraken"),
            Pair("BTC-USD", buy_exchange="kraken", sell_exchange="coinbase"),
        ]
        backwards, forwards = (e.outcome for e in scan.evaluations)
        assert isinstance(backwards, Spread)
        assert isinstance(forwards, Spread)
        # Buy at 102, sell at 99; buy at 100, sell at 101.
        assert backwards.net_bps < 0 < forwards.net_bps
        assert scan.evaluations[1].buy_last_trade_price == Decimal("99.5")
        assert scan.evaluations[1].sell_last_trade_price is None

    def test_a_single_exchange_has_no_pairs(self) -> None:
        self.books.put(KRAKEN)

        assert self.detector.scan("BTC-USD").evaluations == ()
        assert self.detector.scan("ETH-USD").evaluations == ()

    def test_a_book_older_than_max_staleness_is_stale(self) -> None:
        self.books.put(KRAKEN)
        self.books.put(
            book(
                "coinbase",
                bids=COINBASE.bids,
                asks=COINBASE.asks,
                published_ts=T0 + timedelta(seconds=2),
            )
        )

        self.clock.advance(timedelta(seconds=3))  # kraken is exactly 3s old: still fresh
        assert all(isinstance(e.outcome, Spread) for e in self.detector.scan("BTC-USD").evaluations)

        self.clock.advance(timedelta(milliseconds=1))
        outcomes = [e.outcome for e in self.detector.scan("BTC-USD").evaluations]
        stale = Stale("kraken", timedelta(seconds=3, milliseconds=1))
        assert outcomes == [stale, stale]  # the sell leg of one pair, the buy leg of the other
