import React, { useEffect, useState, useCallback } from 'react';
import { ApiError } from '../api/client.js';

// useAsync — minimal hook for async data fetching with loading/error states.
//
// The async fn receives a cancellation-aware AbortSignal-less environment;
// we intentionally avoid AbortController to keep things tiny (the server
// already processes fast queries). Errors of type ApiError with status 401/403
// are surfaced to the global handler (App.jsx attaches a window listener).
export function useAsync(fn, deps = []) {
  const [data, setData] = useState(null);
  const [error, setError] = useState(null);
  const [loading, setLoading] = useState(true);
  const [reloadToken, setReloadToken] = useState(0);

  const reload = useCallback(() => setReloadToken((t) => t + 1), []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    Promise.resolve()
      .then(() => fn())
      .then((result) => {
        if (cancelled) return;
        setData(result);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err);
        if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
          window.dispatchEvent(new CustomEvent('nixllm:api-error', { detail: err }));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, reloadToken]);

  return { data, error, loading, reload };
}

// useToggle — small helper for modal/accordion open state.
export function useToggle(initial = false) {
  const [open, setOpen] = useState(initial);
  const toggle = useCallback(() => setOpen((v) => !v), []);
  const close = useCallback(() => setOpen(false), []);
  const show = useCallback(() => setOpen(true), []);
  return { open, toggle, close, show };
}
