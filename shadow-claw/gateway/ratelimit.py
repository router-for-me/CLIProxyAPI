"""Per-user sliding window rate limiter for Shadow-Claw gateway."""

import time
from collections import defaultdict, deque


class RateLimiter:
    """Sliding window counter rate limiter, keyed by user ID."""

    def __init__(self, max_requests: int = 30, window_seconds: int = 60):
        self._max_requests = max_requests
        self._window_seconds = window_seconds
        self._requests: dict[int, deque] = defaultdict(deque)

    def check(self, user_id: int) -> bool:
        """Return True if the request is allowed, False if rate-limited."""
        now = time.monotonic()
        window = self._requests[user_id]
        cutoff = now - self._window_seconds

        # Evict expired entries
        while window and window[0] < cutoff:
            window.popleft()

        if len(window) >= self._max_requests:
            return False

        window.append(now)
        return True

    def remaining(self, user_id: int) -> int:
        """Return how many requests remain in the current window."""
        now = time.monotonic()
        window = self._requests[user_id]
        cutoff = now - self._window_seconds
        while window and window[0] < cutoff:
            window.popleft()
        return max(0, self._max_requests - len(window))

    def reset(self, user_id: int) -> None:
        """Clear rate limit state for a specific user."""
        self._requests.pop(user_id, None)
