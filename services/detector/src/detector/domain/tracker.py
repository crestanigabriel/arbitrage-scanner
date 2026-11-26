"""Opportunity lifecycles: open -> update -> close, rather than one record per scan.

An opportunity opens when a pair's net spread reaches the threshold. It stays open while the
spread stays at or above it, recording the peak. It closes at the first scan where the pair is
below the threshold, stale or short of liquidity. There is no hysteresis: a spread that hovers
around the threshold opens and closes often, and each lifecycle's duration stays honest.
"""

from dataclasses import dataclass, replace
from datetime import datetime, timedelta
from decimal import Decimal
from typing import Literal

from detector.domain.detection import InsufficientLiquidity, Pair, Scan, Spread, Stale

type CloseReason = Literal["below_threshold", "stale", "insufficient_liquidity", "gone"]


@dataclass(frozen=True, slots=True)
class Opportunity:
    """One lifecycle of one pair."""

    pair: Pair
    opened_at: datetime
    last_seen_at: datetime  # the latest scan at or above the threshold
    observations: int  # scans at or above the threshold
    peak: Spread  # the spread with the highest net bps, with its quantity and notional
    peak_at: datetime
    buy_last_trade_price: Decimal | None  # trade context at the peak
    sell_last_trade_price: Decimal | None
    closed_at: datetime | None = None

    @property
    def peak_net_bps(self) -> Decimal:
        return self.peak.net_bps

    @property
    def duration(self) -> timedelta:
        """From opening to closing, or to the latest observation while still open."""
        return (self.closed_at or self.last_seen_at) - self.opened_at


@dataclass(frozen=True, slots=True)
class Opened:
    opportunity: Opportunity


@dataclass(frozen=True, slots=True)
class Updated:
    """The spread reached a new peak."""

    opportunity: Opportunity


@dataclass(frozen=True, slots=True)
class Closed:
    opportunity: Opportunity
    reason: CloseReason


type Event = Opened | Updated | Closed


class OpportunityTracker:
    """Turns scans into opportunity lifecycle events."""

    def __init__(self, threshold_bps: Decimal) -> None:
        if not threshold_bps.is_finite():
            raise ValueError(f"threshold_bps must be finite, got {threshold_bps}")
        self._threshold = threshold_bps
        self._open: dict[Pair, Opportunity] = {}

    def open_opportunities(self) -> list[Opportunity]:
        """Return the opportunities open now."""
        return list(self._open.values())

    def observe(self, scan: Scan) -> list[Event]:
        """Apply one scan and return what changed, in the order of its evaluations.

        Open opportunities of the scanned symbol whose pair is missing from the scan close as
        "gone".
        """
        events: list[Event] = []
        seen: set[Pair] = set()
        for ev in scan.evaluations:
            seen.add(ev.pair)
            current = self._open.get(ev.pair)
            outcome = ev.outcome
            if isinstance(outcome, Spread) and outcome.net_bps >= self._threshold:
                if current is None:
                    opened = Opportunity(
                        pair=ev.pair,
                        opened_at=scan.at,
                        last_seen_at=scan.at,
                        observations=1,
                        peak=outcome,
                        peak_at=scan.at,
                        buy_last_trade_price=ev.buy_last_trade_price,
                        sell_last_trade_price=ev.sell_last_trade_price,
                    )
                    self._open[ev.pair] = opened
                    events.append(Opened(opened))
                elif outcome.net_bps > current.peak_net_bps:
                    updated = replace(
                        current,
                        last_seen_at=scan.at,
                        observations=current.observations + 1,
                        peak=outcome,
                        peak_at=scan.at,
                        buy_last_trade_price=ev.buy_last_trade_price,
                        sell_last_trade_price=ev.sell_last_trade_price,
                    )
                    self._open[ev.pair] = updated
                    events.append(Updated(updated))
                else:
                    self._open[ev.pair] = replace(
                        current, last_seen_at=scan.at, observations=current.observations + 1
                    )
            elif current is not None:
                events.append(self._close(ev.pair, scan.at, _reason(outcome)))

        gone = [p for p in self._open if p.symbol == scan.symbol and p not in seen]
        events.extend(self._close(p, scan.at, "gone") for p in gone)
        return events

    def _close(self, pair: Pair, at: datetime, reason: CloseReason) -> Closed:
        closed = replace(self._open.pop(pair), closed_at=at)
        return Closed(closed, reason)


def _reason(outcome: Spread | Stale | InsufficientLiquidity) -> CloseReason:
    match outcome:
        case Spread():
            return "below_threshold"
        case Stale():
            return "stale"
        case InsufficientLiquidity():
            return "insufficient_liquidity"
