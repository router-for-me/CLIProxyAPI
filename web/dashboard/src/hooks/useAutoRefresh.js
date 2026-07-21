import { useEffect, useRef } from 'react';

// useAutoRefresh invokes `callback` every `intervalMs` (default 60s) while
// the page is visible. It pauses when the tab is hidden (document.hidden)
// to avoid hammering the server from backgrounded dashboards. The callback
// may return a Promise; rejections are swallowed (caller surfaces its own
// errors). The hook is a no-op when `enabled` is false (default true), so
// pages can toggle auto-refresh on/off without unmounting.
//
// The interval is reset whenever `callback` or `intervalMs` changes so a
// fresh callback identity does not double-fire on the same tick.
export function useAutoRefresh(callback, intervalMs = 60000, enabled = true) {
  const savedCallback = useRef(callback);
  useEffect(() => {
    savedCallback.current = callback;
  }, [callback]);

  useEffect(() => {
    if (!enabled) return;
    let timer = null;
    const tick = () => {
      if (typeof document !== 'undefined' && document.hidden) {
        return;
      }
      try {
        const result = savedCallback.current?.();
        if (result && typeof result.catch === 'function') {
          result.catch(() => { /* caller surfaces errors */ });
        }
      } catch {
        /* swallow — caller owns error state */
      }
    };
    timer = setInterval(tick, intervalMs);
    return () => clearInterval(timer);
  }, [intervalMs, enabled]);
}
