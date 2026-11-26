"""Test-wide configuration."""

import os

from hypothesis import settings

# CI runs a fixed set of examples, so a failure there reproduces locally with
# HYPOTHESIS_PROFILE=ci. Locally every run explores new examples.
settings.register_profile("ci", derandomize=True, database=None, print_blob=True)
settings.load_profile(
    os.environ.get("HYPOTHESIS_PROFILE", "ci" if os.environ.get("CI") else "default")
)
