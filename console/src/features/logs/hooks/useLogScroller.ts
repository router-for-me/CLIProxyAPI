import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { Dispatch, RefObject, SetStateAction, UIEvent } from 'react';
import type { LogState } from '../model/logTypes';

const INITIAL_DISPLAY_LINES = 100;
const LOAD_MORE_LINES = 200;
const LOAD_MORE_THRESHOLD_PX = 72;

type LogAnchor = { id: number; offset: number };
type LogRow = LogAnchor & { bottom: number };

// IDs belong to the original stream, not to positions in a filtered buffer.
export const findVisibleLogAnchor = (rows: readonly LogRow[]): LogAnchor | null => {
  const row = rows.find((entry) => entry.bottom > 0);
  return row ? { id: row.id, offset: row.offset } : null;
};

export const getLogAnchorAdjustment = (
  anchor: LogAnchor,
  rows: readonly LogRow[]
): number | null => {
  const row = rows.find((entry) => entry.id === anchor.id);
  return row ? row.offset - anchor.offset : null;
};

export const countPendingLogLines = (ids: readonly number[], lastSeenId: number | null) =>
  lastSeenId === null ? 0 : ids.filter((id) => id > lastSeenId).length;

const readLogRows = (node: HTMLDivElement): LogRow[] => {
  const top = node.getBoundingClientRect().top + node.clientTop;
  return Array.from(node.querySelectorAll<HTMLElement>('[data-log-id]')).flatMap((row) => {
    const value = row.dataset.logId;
    if (!value?.trim()) return [];
    const id = Number(value);
    if (!Number.isFinite(id)) return [];
    const rect = row.getBoundingClientRect();
    return [{ id, offset: rect.top - top, bottom: rect.bottom - top }];
  });
};

export const isNearBottom = (node: HTMLDivElement | null) => {
  if (!node) return true;
  return node.scrollHeight - node.scrollTop - node.clientHeight <= 24;
};

interface UseLogScrollerOptions {
  logState: LogState;
  setLogState: Dispatch<SetStateAction<LogState>>;
  bufferStart?: number;
  loading: boolean;
  isSearching: boolean;
  filteredLineCount: number;
  hasStructuredFilters: boolean;
  showRawLogs: boolean;
  wrapLogs?: boolean;
}

interface UseLogScrollerReturn {
  logViewerRef: RefObject<HTMLDivElement | null>;
  canLoadMore: boolean;
  handleLogScroll: (e: UIEvent<HTMLDivElement>) => void;
  scrollToBottom: () => void;
  requestScrollToBottom: () => void;
  isFollowing: boolean;
  resumeFollowing: () => void;
  pendingLines: number;
  historyEvicted: boolean;
}

export function useLogScroller(options: UseLogScrollerOptions): UseLogScrollerReturn {
  const {
    logState,
    setLogState,
    bufferStart = 0,
    loading,
    isSearching,
    filteredLineCount,
    hasStructuredFilters,
    showRawLogs,
    wrapLogs = false,
  } = options;

  const logViewerRef = useRef<HTMLDivElement | null>(null);
  const [isFollowing, setIsFollowing] = useState(true);
  const [pendingLines, setPendingLines] = useState(0);
  const [historyEvicted, setHistoryEvicted] = useState(false);
  const followingRef = useRef(true);
  const anchorRef = useRef<LogAnchor | null>(null);
  const lastScrollTopRef = useRef(0);
  const newestIdRef = useRef<number | null>(null);
  const previousBufferStartRef = useRef(bufferStart);
  const pendingScrollToBottomRef = useRef(false);
  const pendingPrependScrollRef = useRef<{ scrollHeight: number; scrollTop: number } | null>(null);
  const canLoadMore = logState.visibleFrom > 0;

  const resumeFollowing = useCallback(() => {
    followingRef.current = true;
    setIsFollowing(true);
    setPendingLines(0);
    setHistoryEvicted(false);
    anchorRef.current = null;
    pendingPrependScrollRef.current = null;
    pendingScrollToBottomRef.current = true;
    setLogState((prev) => {
      const visibleFrom = Math.max(prev.buffer.length - INITIAL_DISPLAY_LINES, 0);
      return prev.visibleFrom === visibleFrom ? prev : { ...prev, visibleFrom };
    });
    const node = logViewerRef.current;
    if (node) {
      node.scrollTop = node.scrollHeight;
      lastScrollTopRef.current = node.scrollTop;
    }
  }, [setLogState]);

  // A data arrival may finish in a closure from an older render. Only request
  // scrolling here; resizing the window belongs to the explicit resume action.
  const requestScrollToBottom = useCallback(() => {
    followingRef.current = true;
    setIsFollowing(true);
    setPendingLines(0);
    pendingScrollToBottomRef.current = true;
    anchorRef.current = null;
  }, []);

  const prependVisibleLines = useCallback(() => {
    const node = logViewerRef.current;
    if (!node || pendingPrependScrollRef.current || !canLoadMore) return;
    anchorRef.current = findVisibleLogAnchor(readLogRows(node));
    // Retain the height fallback for existing callers without data-log-id rows.
    pendingPrependScrollRef.current = {
      scrollHeight: node.scrollHeight,
      scrollTop: node.scrollTop,
    };
    setLogState((prev) => ({
      ...prev,
      visibleFrom: Math.max(prev.visibleFrom - LOAD_MORE_LINES, 0),
    }));
  }, [canLoadMore, setLogState]);

  const handleLogScroll = useCallback(
    (e: UIEvent<HTMLDivElement>) => {
      const node = e.currentTarget;
      const rows = readLogRows(node);
      const movedUp = node.scrollTop < lastScrollTopRef.current;
      if (movedUp && followingRef.current) {
        pendingScrollToBottomRef.current = false;
        followingRef.current = false;
        setIsFollowing(false);
        newestIdRef.current = rows.length ? rows[rows.length - 1].id : null;
      }
      // Reaching the bottom while browsing does not discard the expanded history;
      // the explicit resume action is what restores the 100-line live window.
      lastScrollTopRef.current = node.scrollTop;
      anchorRef.current = findVisibleLogAnchor(rows);
      if (!followingRef.current && node.scrollTop <= LOAD_MORE_THRESHOLD_PX) {
        prependVisibleLines();
      }
    },
    [prependVisibleLines]
  );

  useLayoutEffect(() => {
    const node = logViewerRef.current;
    const reset = bufferStart < previousBufferStartRef.current || logState.buffer.length === 0;
    previousBufferStartRef.current = bufferStart;
    if (reset) {
      anchorRef.current = null;
      newestIdRef.current = null;
      pendingPrependScrollRef.current = null;
      followingRef.current = true;
      setIsFollowing(true);
      setPendingLines(0);
      setHistoryEvicted(false);
    }
    if (!node) return;
    const rows = readLogRows(node);
    const newestId = rows.length ? rows[rows.length - 1].id : null;
    const prepend = pendingPrependScrollRef.current;
    if (followingRef.current || pendingScrollToBottomRef.current) {
      if (!loading) {
        node.scrollTop = node.scrollHeight;
        pendingScrollToBottomRef.current = false;
      }
      newestIdRef.current = newestId;
    } else {
      const anchor = anchorRef.current;
      if (anchor) {
        const adjustment = getLogAnchorAdjustment(anchor, rows);
        if (adjustment !== null) {
          node.scrollTop += adjustment;
        } else {
          setHistoryEvicted(true);
        }
      } else if (prepend) {
        node.scrollTop = prepend.scrollTop + node.scrollHeight - prepend.scrollHeight;
      }
      const added = countPendingLogLines(
        rows.map((row) => row.id),
        newestIdRef.current
      );
      if (added > 0) setPendingLines((count) => count + added);
      if (newestId !== null) {
        newestIdRef.current = Math.max(newestIdRef.current ?? newestId, newestId);
      }
    }
    pendingPrependScrollRef.current = null;
    lastScrollTopRef.current = node.scrollTop;
    anchorRef.current = findVisibleLogAnchor(readLogRows(node));
  }, [
    bufferStart,
    loading,
    logState.buffer,
    logState.visibleFrom,
    showRawLogs,
    wrapLogs,
    isFollowing,
  ]);

  const tryAutoLoadMoreUntilScrollable = useCallback(() => {
    const node = logViewerRef.current;
    if (!node || loading || !canLoadMore || pendingPrependScrollRef.current) return;
    if (node.scrollHeight > node.clientHeight + 1) return;
    prependVisibleLines();
  }, [canLoadMore, loading, prependVisibleLines]);

  useEffect(() => {
    if (loading) return;
    const raf = window.requestAnimationFrame(tryAutoLoadMoreUntilScrollable);
    return () => window.cancelAnimationFrame(raf);
  }, [
    filteredLineCount,
    hasStructuredFilters,
    isSearching,
    loading,
    logState.visibleFrom,
    showRawLogs,
    tryAutoLoadMoreUntilScrollable,
  ]);

  useEffect(() => {
    let raf: number | null = null;
    const onResize = () => {
      if (raf !== null) window.cancelAnimationFrame(raf);
      raf = window.requestAnimationFrame(() => {
        raf = null;
        tryAutoLoadMoreUntilScrollable();
      });
    };
    window.addEventListener('resize', onResize);
    return () => {
      window.removeEventListener('resize', onResize);
      if (raf !== null) window.cancelAnimationFrame(raf);
    };
  }, [tryAutoLoadMoreUntilScrollable]);

  return {
    logViewerRef,
    canLoadMore,
    handleLogScroll,
    scrollToBottom: resumeFollowing,
    requestScrollToBottom,
    isFollowing,
    resumeFollowing,
    pendingLines,
    historyEvicted,
  };
}
