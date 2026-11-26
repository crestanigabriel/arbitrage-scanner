"""Fakes for the detector's ports."""

from datetime import datetime, timedelta


class FakeClock:
    """A `detector.ports.Clock` whose time moves only when a test says so."""

    def __init__(self, start: datetime) -> None:
        self._now = start

    def now(self) -> datetime:
        return self._now

    def advance(self, d: timedelta) -> None:
        self._now += d
