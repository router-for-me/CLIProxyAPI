import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent  # tools/usage-dashboard/


def test_makefile_exists():
    assert (ROOT / "Makefile").is_file()


def test_make_help_lists_targets():
    """`make help` must list the canonical targets."""
    result = subprocess.run(
        ["make", "help"],
        cwd=str(ROOT),
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0
    for target in ("dev", "test", "lint", "build-frontend", "api-types"):
        assert target in result.stdout
