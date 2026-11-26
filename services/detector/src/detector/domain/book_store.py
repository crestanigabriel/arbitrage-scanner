"""The latest book per (exchange, symbol)."""

from detector.domain.model import Book


class BookStore:
    """Holds the latest book of each exchange for each symbol.

    Each book the ingestor publishes is the complete top of the book, so a new one simply
    replaces the old. There is no delta logic here.
    """

    def __init__(self) -> None:
        self._books: dict[str, dict[str, Book]] = {}

    def put(self, book: Book) -> bool:
        """Store `book` in place of the previous one for its exchange and symbol.

        A book published before the stored one is out of order: it is ignored, and put returns
        False. The ingestor's `sequence` restarts with the ingestor, so `published_ts` decides.
        """
        by_exchange = self._books.setdefault(book.symbol, {})
        current = by_exchange.get(book.exchange)
        if current is not None and book.published_ts < current.published_ts:
            return False
        by_exchange[book.exchange] = book
        return True

    def get(self, exchange: str, symbol: str) -> Book | None:
        """Return the stored book, or None if none was stored yet."""
        return self._books.get(symbol, {}).get(exchange)

    def for_symbol(self, symbol: str) -> list[Book]:
        """Return the stored books of `symbol`, one per exchange, ordered by exchange."""
        by_exchange = self._books.get(symbol, {})
        return [by_exchange[ex] for ex in sorted(by_exchange)]
