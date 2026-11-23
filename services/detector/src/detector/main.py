"""Composition root for the detector service.

Concrete dependencies are constructed here and only here, then injected into
the components that use them.
"""

import sys
from typing import TextIO


def run(out: TextIO) -> None:
    """Program logic, taking its dependencies as parameters so it can be tested."""
    out.write("detector: hello\n")


def main() -> None:
    run(sys.stdout)


if __name__ == "__main__":
    main()
