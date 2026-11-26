"""Property tests: invariants of depth walking, fees and lifecycles over generated books."""

from datetime import timedelta
from decimal import Decimal

from hypothesis import given
from hypothesis import strategies as st

from builders import T0, book, levels
from detector.domain.detection import (
    Evaluation,
    InsufficientLiquidity,
    Leg,
    Outcome,
    Pair,
    Scan,
    Spread,
    Stale,
    evaluate,
)
from detector.domain.fees import FeeSchedule
from detector.domain.model import Level
from detector.domain.tracker import Closed, Opened, OpportunityTracker
from detector.domain.vwap import Fill, Shortfall, buy_for_notional, sell_quantity
from strategies import fee_bps, notionals, price_levels, quantities, sides


def fees(a: Decimal, b: Decimal) -> FeeSchedule:
    return FeeSchedule({"a": a, "b": b})


@given(asks=price_levels(ascending=True), notional=notionals)
def test_a_buy_spends_the_notional_or_reports_what_is_there(
    asks: tuple[Level, ...], notional: Decimal
) -> None:
    available = sum((lvl.price * lvl.quantity for lvl in asks), Decimal(0))
    result = buy_for_notional(asks, notional)

    if available < notional:
        assert result == Shortfall(wanted=notional, available=available)
    else:
        assert isinstance(result, Fill)
        # The partial level is rounded down to 1e-30 of a unit of base, at most 1e-30 x price.
        assert Decimal(0) <= notional - result.notional <= asks[-1].price * Decimal("1e-30")


@given(bids=price_levels(ascending=False), quantity=quantities)
def test_a_sell_sells_the_quantity_or_reports_what_is_there(
    bids: tuple[Level, ...], quantity: Decimal
) -> None:
    available = sum((lvl.quantity for lvl in bids), Decimal(0))
    result = sell_quantity(bids, quantity)

    if available < quantity:
        assert result == Shortfall(wanted=quantity, available=available)
    else:
        assert isinstance(result, Fill)
        assert result.quantity == quantity


@given(asks=price_levels(ascending=True), notional=notionals)
def test_buy_vwap_lies_between_the_best_and_worst_ask_walked(
    asks: tuple[Level, ...], notional: Decimal
) -> None:
    fill = buy_for_notional(asks, notional)
    if isinstance(fill, Fill):
        assert fill.best_price == asks[0].price
        assert fill.worst_price == asks[fill.levels - 1].price
        assert fill.best_price <= fill.vwap_price <= fill.worst_price


@given(bids=price_levels(ascending=False), quantity=quantities)
def test_sell_vwap_lies_between_the_worst_and_best_bid_walked(
    bids: tuple[Level, ...], quantity: Decimal
) -> None:
    fill = sell_quantity(bids, quantity)
    if isinstance(fill, Fill):
        assert fill.best_price == bids[0].price
        assert fill.worst_price == bids[fill.levels - 1].price
        assert fill.worst_price <= fill.vwap_price <= fill.best_price


@given(book_sides=sides(), notional=notionals, fee_a=fee_bps, fee_b=fee_bps)
def test_identical_books_never_show_an_opportunity(
    book_sides: tuple[tuple[Level, ...], tuple[Level, ...]],
    notional: Decimal,
    fee_a: Decimal,
    fee_b: Decimal,
) -> None:
    bids, asks = book_sides
    result = evaluate(
        book("a", bids=bids, asks=asks),
        book("b", bids=bids, asks=asks),
        notional,
        fees(fee_a, fee_b),
    )

    if isinstance(result, Spread):
        # Buying at or above the best ask and selling at or below the best bid always loses.
        assert result.net_bps <= result.gross_bps < 0


@given(
    a=sides(),
    b=sides(),
    notional=notionals,
    fee_a=fee_bps,
    fee_b=fee_bps,
    more_a=fee_bps,
    more_b=fee_bps,
)
def test_net_spread_does_not_increase_as_fees_grow(
    a: tuple[tuple[Level, ...], tuple[Level, ...]],
    b: tuple[tuple[Level, ...], tuple[Level, ...]],
    notional: Decimal,
    fee_a: Decimal,
    fee_b: Decimal,
    more_a: Decimal,
    more_b: Decimal,
) -> None:
    buy, sell = book("a", bids=a[0], asks=a[1]), book("b", bids=b[0], asks=b[1])
    low = evaluate(buy, sell, notional, fees(fee_a, fee_b))
    high = evaluate(buy, sell, notional, fees(fee_a + more_a, fee_b + more_b))

    if isinstance(low, Spread):
        assert isinstance(high, Spread)  # fees don't change the fills
        assert high.gross_bps == low.gross_bps
        assert high.net_bps <= low.net_bps <= low.gross_bps
    else:
        assert high == low


# --- Lifecycles ------------------------------------------------------------------------------

PAIR = Pair("BTC-USD", "a", "b")
_FILL = buy_for_notional(levels(("100", "1")), Decimal(50))
assert isinstance(_FILL, Fill)
_LEG = Leg("a", _FILL, Decimal(0), Decimal(0))

outcomes: st.SearchStrategy[Outcome] = st.one_of(
    st.integers(-50, 50).map(
        lambda bps: Spread(_LEG, _LEG, gross_bps=Decimal(bps), net_bps=Decimal(bps))
    ),
    st.just(Stale("a", timedelta(seconds=5))),
    st.just(InsufficientLiquidity("a", "buy", Decimal(50), Decimal(1))),
)


@given(steps=st.lists(outcomes, max_size=50), threshold=st.integers(-10, 30).map(Decimal))
def test_lifecycles_alternate_and_record_their_peak(
    steps: list[Outcome], threshold: Decimal
) -> None:
    tracker = OpportunityTracker(threshold)
    is_open = False
    qualifying: list[Decimal] = []  # net bps seen during the current lifecycle

    for i, outcome in enumerate(steps):
        at = T0 + i * timedelta(seconds=1)
        events = tracker.observe(Scan("BTC-USD", at, (Evaluation(PAIR, outcome, None, None),)))
        qualifies = isinstance(outcome, Spread) and outcome.net_bps >= threshold

        if qualifies:
            assert isinstance(outcome, Spread)
            qualifying.append(outcome.net_bps)
        if not is_open and qualifies:
            assert [type(e) for e in events] == [Opened]
            is_open = True
        elif is_open and not qualifies:
            [closed] = events
            assert isinstance(closed, Closed)
            op = closed.opportunity
            assert op.duration >= timedelta(0)
            assert op.peak_net_bps == max(qualifying) >= threshold
            assert op.observations == len(qualifying)
            is_open, qualifying = False, []
        else:
            assert not any(isinstance(e, Opened | Closed) for e in events)

    assert len(tracker.open_opportunities()) == int(is_open)


def test_lifecycle_peak_keeps_the_first_of_equal_spreads() -> None:
    # Only a strictly higher spread is a new peak, so peak_at is when it was first reached.
    tracker = OpportunityTracker(Decimal(0))
    peak = Spread(_LEG, _LEG, gross_bps=Decimal(5), net_bps=Decimal(5))
    tracker.observe(Scan("BTC-USD", T0, (Evaluation(PAIR, peak, None, None),)))
    again = T0 + timedelta(seconds=1)
    tracker.observe(Scan("BTC-USD", again, (Evaluation(PAIR, peak, None, None),)))

    [op] = tracker.open_opportunities()
    assert op.peak_at == T0
