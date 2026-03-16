"""Agent tool implementations for Shadow-Claw.

Importing this package triggers @tool decorator registration for all tools.
Optional dependencies are handled gracefully — tools that require missing
packages register but return an error message when invoked.
"""

from tools import memory  # noqa: F401
from tools import browser  # noqa: F401
from tools import scraper  # noqa: F401
from tools import finance  # noqa: F401
from tools import researcher  # noqa: F401
from tools import planner  # noqa: F401
from tools import security  # noqa: F401
from tools import desktop  # noqa: F401
from tools import voice  # noqa: F401
from tools import payments  # noqa: F401
from tools import osint  # noqa: F401
from tools import ocr  # noqa: F401
from tools import marketing  # noqa: F401
