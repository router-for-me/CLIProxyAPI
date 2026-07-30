#!/usr/bin/env python3
"""Shim: delegates to the usage_dashboard package."""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from usage_dashboard.__main__ import main  # noqa: E402

if __name__ == "__main__":
    main()
