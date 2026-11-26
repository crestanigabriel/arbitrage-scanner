"""Walking book depth to price a trade at its volume-weighted average price (VWAP).

A buy spends a notional on the asks, a sell sells a quantity into the bids. When the published
levels don't cover the trade, the result says so rather than guessing at deeper levels.
"""

from collections.abc import Sequence
from dataclasses import dataclass
from decimal import ROUND_DOWN, Decimal, localcontext

from detector.domain.model import ARITHMETIC, Level

# The quantity taken from a partly taken level is rounded down to this quantum, so a buy never
# spends more than its notional. Exchange quantities have at most 8-10 decimals, so this is far
# below any real digit, and it keeps every later sum and product exact in ARITHMETIC.
QUANTITY_QUANTUM = Decimal("1e-30")
_DOWN = ARITHMETIC.copy()
_DOWN.rounding = ROUND_DOWN


@dataclass(frozen=True, slots=True)
class Fill:
    """The result of walking one side of a book."""

    quantity: Decimal
    notional: Decimal
    vwap_price: Decimal
    best_price: Decimal  # the first level taken
    worst_price: Decimal  # the last level taken, possibly in part
    levels: int  # how many levels were taken, in whole or in part


@dataclass(frozen=True, slots=True)
class Shortfall:
    """The levels ran out before the trade was filled.

    `wanted` and `available` are a notional for a buy and a quantity for a sell.
    """

    wanted: Decimal
    available: Decimal


def buy_for_notional(asks: Sequence[Level], notional: Decimal) -> Fill | Shortfall:
    """Spend `notional` on `asks`, best (lowest) first."""
    if notional <= 0:
        raise ValueError(f"notional must be positive, got {notional}")
    with localcontext(ARITHMETIC):
        left, quantity, spent = notional, Decimal(0), Decimal(0)
        for i, level in enumerate(asks):
            level_notional = level.price * level.quantity
            if level_notional >= left:
                part = _DOWN.quantize(_DOWN.divide(left, level.price), QUANTITY_QUANTUM)
                quantity += part
                spent += level.price * part
                return _fill(quantity, spent, asks[0].price, level.price, i + 1)
            quantity += level.quantity
            spent += level_notional
            left -= level_notional
        return Shortfall(wanted=notional, available=spent)


def sell_quantity(bids: Sequence[Level], quantity: Decimal) -> Fill | Shortfall:
    """Sell `quantity` into `bids`, best (highest) first."""
    if quantity <= 0:
        raise ValueError(f"quantity must be positive, got {quantity}")
    with localcontext(ARITHMETIC):
        left, proceeds = quantity, Decimal(0)
        for i, level in enumerate(bids):
            if level.quantity >= left:
                proceeds += level.price * left
                return _fill(quantity, proceeds, bids[0].price, level.price, i + 1)
            proceeds += level.price * level.quantity
            left -= level.quantity
        return Shortfall(wanted=quantity, available=quantity - left)


def _fill(quantity: Decimal, notional: Decimal, best: Decimal, worst: Decimal, levels: int) -> Fill:
    with localcontext(ARITHMETIC):
        vwap = notional / quantity
    return Fill(
        quantity=quantity,
        notional=notional,
        vwap_price=vwap,
        best_price=best,
        worst_price=worst,
        levels=levels,
    )
