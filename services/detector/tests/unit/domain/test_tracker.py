from datetime import datetime, timedelta
from decimal import Decimal

import pytest

from builders import T0, levels
from detector.domain.detection import (
    Evaluation,
    InsufficientLiquidity,
    Leg,
    Outcome,
    Pair,
    Scan,
    Spread,
    Stale,
)
from detector.domain.tracker import Closed, Opened, OpportunityTracker, Updated
from detector.domain.vwap import Fill, buy_for_notional

PAIR = Pair("BTC-USD", "kraken", "coinbase")
OTHER = Pair("BTC-USD", "coinbase", "kraken")
S = timedelta(seconds=1)


def spread(net_bps: str) -> Spread:
    """A spread with the given net bps. The legs are placeholders."""
    fill = buy_for_notional(levels(("100", "1")), Decimal(50))
    assert isinstance(fill, Fill)
    leg = Leg("kraken", fill, Decimal(0), Decimal(0))
    return Spread(buy=leg, sell=leg, gross_bps=Decimal(net_bps), net_bps=Decimal(net_bps))


def scan(at: datetime, *outcomes: tuple[Pair, Outcome], last_price: str | None = None) -> Scan:
    price = Decimal(last_price) if last_price else None
    return Scan(
        symbol="BTC-USD",
        at=at,
        evaluations=tuple(Evaluation(p, o, price, price) for p, o in outcomes),
    )


def test_lifecycle_open_peak_close() -> None:
    tracker = OpportunityTracker(threshold_bps=Decimal(10))

    assert tracker.observe(scan(T0, (PAIR, spread("5")))) == []

    [opened] = tracker.observe(scan(T0 + S, (PAIR, spread("12")), last_price="100"))
    assert isinstance(opened, Opened)
    assert opened.opportunity.opened_at == T0 + S
    assert opened.opportunity.peak_net_bps == Decimal(12)
    assert opened.opportunity.buy_last_trade_price == Decimal(100)

    [updated] = tracker.observe(scan(T0 + 2 * S, (PAIR, spread("15")), last_price="101"))
    assert isinstance(updated, Updated)
    assert (updated.opportunity.peak_net_bps, updated.opportunity.peak_at) == (
        Decimal(15),
        T0 + 2 * S,
    )
    assert updated.opportunity.buy_last_trade_price == Decimal(101)  # context at the peak

    assert tracker.observe(scan(T0 + 3 * S, (PAIR, spread("11")))) == []  # open, not a new peak
    assert tracker.observe(scan(T0 + 4 * S, (PAIR, spread("10")))) == []  # at the threshold counts

    [closed] = tracker.observe(scan(T0 + 5 * S, (PAIR, spread("9.99"))))
    assert isinstance(closed, Closed)
    assert closed.reason == "below_threshold"
    op = closed.opportunity
    assert (op.opened_at, op.last_seen_at, op.closed_at) == (T0 + S, T0 + 4 * S, T0 + 5 * S)
    assert op.duration == 4 * S
    assert op.observations == 4
    assert op.peak_net_bps == Decimal(15)
    assert tracker.open_opportunities() == []


@pytest.mark.parametrize(
    ("outcome", "reason"),
    [
        (Stale("kraken", 4 * S), "stale"),
        (
            InsufficientLiquidity("kraken", "buy", Decimal(50), Decimal(10)),
            "insufficient_liquidity",
        ),
    ],
)
def test_a_leg_that_cannot_be_priced_closes(outcome: Outcome, reason: str) -> None:
    tracker = OpportunityTracker(threshold_bps=Decimal(10))
    tracker.observe(scan(T0, (PAIR, spread("20"))))

    [closed] = tracker.observe(scan(T0 + S, (PAIR, outcome)))

    assert isinstance(closed, Closed)
    assert closed.reason == reason


def test_a_pair_missing_from_its_symbols_scan_is_gone() -> None:
    tracker = OpportunityTracker(threshold_bps=Decimal(10))
    tracker.observe(scan(T0, (PAIR, spread("20"))))

    other_symbol = Scan(symbol="ETH-USD", at=T0 + S, evaluations=())
    assert tracker.observe(other_symbol) == []

    [closed] = tracker.observe(scan(T0 + 2 * S))
    assert isinstance(closed, Closed)
    assert (closed.reason, closed.opportunity.closed_at) == ("gone", T0 + 2 * S)


def test_pairs_are_tracked_independently() -> None:
    tracker = OpportunityTracker(threshold_bps=Decimal(10))

    events = tracker.observe(scan(T0, (PAIR, spread("20")), (OTHER, spread("30"))))
    assert [type(e) for e in events] == [Opened, Opened]

    events = tracker.observe(scan(T0 + S, (PAIR, spread("1")), (OTHER, spread("30"))))
    assert [type(e) for e in events] == [Closed]
    assert [o.pair for o in tracker.open_opportunities()] == [OTHER]


def test_an_opportunity_can_reopen() -> None:
    tracker = OpportunityTracker(threshold_bps=Decimal(10))
    tracker.observe(scan(T0, (PAIR, spread("20"))))
    tracker.observe(scan(T0 + S, (PAIR, spread("0"))))

    [reopened] = tracker.observe(scan(T0 + 2 * S, (PAIR, spread("11"))))

    assert isinstance(reopened, Opened)
    assert reopened.opportunity.opened_at == T0 + 2 * S
    assert reopened.opportunity.peak_net_bps == Decimal(11)


def test_threshold_must_be_finite() -> None:
    with pytest.raises(ValueError, match="must be finite"):
        OpportunityTracker(threshold_bps=Decimal("NaN"))
