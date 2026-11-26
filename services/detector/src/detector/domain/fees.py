"""Taker fees per exchange."""

from collections.abc import Mapping
from decimal import Decimal
from types import MappingProxyType

from detector.domain.model import ARITHMETIC, BPS


class FeeSchedule:
    """The taker fee of each exchange, configured in basis points.

    Detection always takes liquidity on both legs, so only taker fees apply.
    """

    def __init__(self, taker_bps: Mapping[str, Decimal]) -> None:
        for exchange, bps in taker_bps.items():
            if not bps.is_finite() or not 0 <= bps < BPS:
                raise ValueError(f"taker fee of {exchange!r} must be 0-{BPS} bps, got {bps}")
        self._rates = MappingProxyType(
            {ex: ARITHMETIC.divide(bps, Decimal(BPS)) for ex, bps in taker_bps.items()}
        )

    def taker_rate(self, exchange: str) -> Decimal:
        """Return the taker fee of `exchange` as a fraction of the notional (0.004 for 40 bps).

        An exchange without a configured fee is an error: assuming zero would overstate every
        spread through it.
        """
        try:
            return self._rates[exchange]
        except KeyError:
            raise KeyError(f"no taker fee configured for exchange {exchange!r}") from None
