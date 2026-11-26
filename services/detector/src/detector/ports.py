"""Abstractions of the outside world that the detector depends on.

Concrete implementations live in `detector.adapters` and are wired in `detector.main`, the
composition root. Tests use fakes. The message feed and the repository are added in M5.
"""

from datetime import datetime
from typing import Protocol


class Clock(Protocol):
    """A source of the current time."""

    def now(self) -> datetime:
        """Return the current time, timezone-aware."""
        ...
