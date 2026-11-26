from decimal import Decimal, localcontext

import pytest

from builders import levels
from detector.domain.vwap import Fill, Shortfall, buy_for_notional, sell_quantity

ASKS = levels(("100", "1"), ("101", "2"), ("103", "1"))  # notionals 100, 202, 103
BIDS = levels(("99", "1"), ("98", "2"), ("95", "1"))


def test_buy_within_the_best_level() -> None:
    fill = buy_for_notional(ASKS, Decimal(50))

    assert fill == Fill(
        quantity=Decimal("0.5"),
        notional=Decimal(50),
        vwap_price=Decimal(100),
        best_price=Decimal(100),
        worst_price=Decimal(100),
        levels=1,
    )


def test_buy_walks_into_a_partial_level() -> None:
    # 100 on the first level, 202 on the second, 101 on 0.98058... of the third.
    fill = buy_for_notional(ASKS, Decimal(403))

    assert isinstance(fill, Fill)
    assert (fill.levels, fill.best_price, fill.worst_price) == (3, Decimal(100), Decimal(103))
    with localcontext(prec=60):
        exact = Decimal(3) + Decimal(101) / Decimal(103)
    assert Decimal(0) <= exact - fill.quantity < Decimal("1e-30")
    # The partial level is rounded down, so the buy never spends more than the notional.
    assert Decimal(0) <= Decimal(403) - fill.notional < Decimal(103) * Decimal("1e-30")
    assert Decimal(101) < fill.vwap_price < Decimal(102)


def test_buy_that_exactly_takes_whole_levels() -> None:
    fill = buy_for_notional(ASKS, Decimal(302))

    assert isinstance(fill, Fill)
    assert (fill.quantity, fill.notional, fill.levels) == (Decimal(3), Decimal(302), 2)
    assert fill.worst_price == Decimal(101)


def test_buy_beyond_the_published_levels_is_a_shortfall() -> None:
    assert buy_for_notional(ASKS, Decimal(406)) == Shortfall(
        wanted=Decimal(406), available=Decimal(405)
    )
    assert buy_for_notional((), Decimal(1)) == Shortfall(wanted=Decimal(1), available=Decimal(0))


def test_sell_walks_the_bids() -> None:
    fill = sell_quantity(BIDS, Decimal("2.5"))

    # 99 x 1 + 98 x 1.5
    assert fill == Fill(
        quantity=Decimal("2.5"),
        notional=Decimal(246),
        vwap_price=Decimal("98.4"),
        best_price=Decimal(99),
        worst_price=Decimal(98),
        levels=2,
    )


def test_sell_beyond_the_published_levels_is_a_shortfall() -> None:
    assert sell_quantity(BIDS, Decimal("4.1")) == Shortfall(
        wanted=Decimal("4.1"), available=Decimal(4)
    )


@pytest.mark.parametrize("bad", [Decimal(0), Decimal(-1)])
def test_non_positive_amounts_are_rejected(bad: Decimal) -> None:
    with pytest.raises(ValueError, match="must be positive"):
        buy_for_notional(ASKS, bad)
    with pytest.raises(ValueError, match="must be positive"):
        sell_quantity(BIDS, bad)
