"""The domain stays pure: importing it pulls in no IO, transport or Protobuf modules."""

import json
import subprocess
import sys

FORBIDDEN = ("arbscanner", "google", "asyncio", "socket", "nats", "asyncpg", "http", "urllib")

# Imports every module of detector.domain, plus the ports it depends on, in a fresh interpreter
# (this test process has already imported the generated code), and prints the loaded modules.
PROBE = """
import importlib, json, pkgutil, sys
import detector.domain, detector.ports
for m in pkgutil.walk_packages(detector.domain.__path__, "detector.domain."):
    importlib.import_module(m.name)
print(json.dumps(sorted(sys.modules)))
"""


def test_domain_imports_no_io_or_protobuf() -> None:
    out = subprocess.run(  # noqa: S603 - runs this interpreter on a fixed script
        [sys.executable, "-c", PROBE], check=True, capture_output=True, text=True
    ).stdout
    loaded = json.loads(out)

    offending = [m for m in loaded if m.split(".")[0] in FORBIDDEN]
    assert offending == []
    assert "detector.domain.tracker" in loaded  # the probe did import the domain
