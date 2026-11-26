"""Hypothesis strategies for order books.

Prices have 2 decimals and quantities 8, like the BTC-USD books of Kraken and Coinbase.
"""

from decimal import Decimal

from hypothesis import strategies as st

from detector.domain.model import Level


def cents(lo: int, hi: int) -> st.SearchStrategy[Decimal]:
    """Decimal prices or notionals from lo to hi cents."""
    return st.integers(lo, hi).map(lambda c: Decimal(c).scaleb(-2))


quantities = st.integers(1, 100_000_000_000).map(lambda s: Decimal(s).scaleb(-8))  # 1e-8 - 1000

notionals = cents(1, 1_000_000_000)  # 0.01 - 10M

fee_bps = st.integers(0, 200).map(Decimal)


@st.composite
def sides(draw: st.DrawFn) -> tuple[tuple[Level, ...], tuple[Level, ...]]:
    """The bids and asks of a book that is not crossed, each best first."""
    best_bid = draw(st.integers(2_000_000, 10_000_000))  # 20k - 100k
    spread = draw(st.integers(1, 1_000))
    bid_steps = draw(st.lists(st.integers(1, 500), min_size=1, max_size=20))
    ask_steps = draw(st.lists(st.integers(1, 500), min_size=1, max_size=20))

    bid_prices = [best_bid - sum(bid_steps[:i]) for i in range(len(bid_steps))]
    first_ask = best_bid + spread
    ask_prices = [first_ask + sum(ask_steps[:i]) for i in range(len(ask_steps))]

    def side(prices: list[int]) -> tuple[Level, ...]:
        qs = draw(st.lists(quantities, min_size=len(prices), max_size=len(prices)))
        return tuple(Level(Decimal(p).scaleb(-2), q) for p, q in zip(prices, qs, strict=True))

    return side(bid_prices), side(ask_prices)


def price_levels(ascending: bool) -> st.SearchStrategy[tuple[Level, ...]]:
    """One side of a book, best first: asks ascending, bids descending."""

    @st.composite
    def build(draw: st.DrawFn) -> tuple[Level, ...]:
        bids, asks = draw(sides())
        return asks if ascending else bids

    return build()
