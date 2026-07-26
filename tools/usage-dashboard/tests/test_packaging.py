"""Verify the project is installable via uv and the legacy entry point
still works after pyproject.toml is introduced."""
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]


def test_pyproject_exists():
    assert (ROOT / "tools/usage-dashboard/pyproject.toml").is_file()


def test_uv_lock_exists():
    assert (ROOT / "tools/usage-dashboard/uv.lock").is_file()


def test_legacy_entry_point_importable():
    """The shim `usage_dashboard.py` must still import without any
    optional dependencies installed."""
    pkg = ROOT / "tools/usage-dashboard"
    result = subprocess.run(
        [sys.executable, "-c", "import usage_dashboard"],
        cwd=str(pkg),
        capture_output=True,
        text=True,
        env={"PATH": "/usr/bin:/bin"},
    )
    assert result.returncode == 0, result.stderr