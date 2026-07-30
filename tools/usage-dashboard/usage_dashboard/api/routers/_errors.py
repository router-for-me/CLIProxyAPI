"""Shared error mapping for read-only API routers.

Wraps ValueError from query.py into HTTPException with appropriate status
codes so every router is identical except for the query function call.
"""

import functools

from fastapi import HTTPException


def map_query_errors(fn):
    """Decorator that catches ValueError and maps to HTTPException.

    Pricing/interval/negative/rate errors → 500 (pricing config error).
    Everything else → 400 (bad request).
    """

    @functools.wraps(fn)
    def wrapper(*args, **kwargs):
        try:
            return fn(*args, **kwargs)
        except ValueError as exc:
            msg = str(exc)
            if any(kw in msg.lower() for kw in ("pricing", "interval", "negative", "rate")):
                raise HTTPException(500, "pricing configuration error") from None
            raise HTTPException(400, msg) from None

    return wrapper
