import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { ApiError, request } from "./api";

export interface Poll<T> {
  data: T | undefined;
  error: ApiError | undefined;
  loading: boolean;
  updatedAt: number | undefined;
  reload: () => Promise<void>;
}

/**
 * Fetches `path` now and every `intervalMs` while the tab is visible. Data from
 * the previous fetch stays on screen during reloads and after errors, so the
 * current selection and scroll position are never disturbed.
 */
export function usePoll<T>(path: string | null, intervalMs = 10000): Poll<T> {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<ApiError>();
  const [updatedAt, setUpdatedAt] = useState<number>();
  const [loading, setLoading] = useState(false);
  const seq = useRef(0);

  const load = useCallback(async () => {
    if (!path) return;
    const mine = ++seq.current;
    setLoading(true);
    try {
      const d = await request<T>(path);
      if (mine !== seq.current) return;
      setData(d);
      setError(undefined);
      setUpdatedAt(Date.now());
    } catch (e) {
      if (mine !== seq.current) return;
      setError(e instanceof ApiError ? e : new ApiError(0, "unknown", String(e)));
    } finally {
      if (mine === seq.current) setLoading(false);
    }
  }, [path]);

  useEffect(() => {
    if (!path) return;
    void load();
    let timer: number | undefined;
    const schedule = () => {
      window.clearInterval(timer);
      if (document.visibilityState === "visible") timer = window.setInterval(() => void load(), intervalMs);
    };
    const onVis = () => {
      if (document.visibilityState === "visible") void load();
      schedule();
    };
    schedule();
    document.addEventListener("visibilitychange", onVis);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", onVis);
    };
  }, [path, intervalMs, load]);

  return { data, error, loading, updatedAt, reload: load };
}

// One shared 1-second clock for countdowns, so every row ticks together.
let nowValue = Date.now();
const nowSubs = new Set<() => void>();
let nowTimer: number | undefined;
function subscribeNow(fn: () => void) {
  nowSubs.add(fn);
  if (nowTimer === undefined) {
    nowValue = Date.now();
    nowTimer = window.setInterval(() => {
      nowValue = Date.now();
      nowSubs.forEach((f) => f());
    }, 1000);
  }
  return () => {
    nowSubs.delete(fn);
    if (nowSubs.size === 0) {
      window.clearInterval(nowTimer);
      nowTimer = undefined;
    }
  };
}
export function useNow(): number {
  return useSyncExternalStore(subscribeNow, () => nowValue);
}

export function useMedia(query: string): boolean {
  const get = () => window.matchMedia(query).matches;
  const [m, setM] = useState(get);
  useEffect(() => {
    const mq = window.matchMedia(query);
    const on = () => setM(mq.matches);
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, [query]);
  return m;
}

// Minimal toast bus.
export interface Toast {
  id: number;
  text: string;
  tone?: "critical" | "info";
}
let toastSeq = 0;
let toasts: Toast[] = [];
const toastSubs = new Set<() => void>();
export function toast(text: string, tone?: Toast["tone"]) {
  const t = { id: ++toastSeq, text, tone };
  toasts = [...toasts, t];
  toastSubs.forEach((f) => f());
  window.setTimeout(() => {
    toasts = toasts.filter((x) => x.id !== t.id);
    toastSubs.forEach((f) => f());
  }, 4500);
}
export function useToasts(): Toast[] {
  return useSyncExternalStore(
    (fn) => {
      toastSubs.add(fn);
      return () => toastSubs.delete(fn);
    },
    () => toasts,
  );
}
