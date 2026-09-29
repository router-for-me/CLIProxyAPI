import { useEffect, useRef } from 'react';

export type HeaderRefreshHandler = () => void | Promise<void>;

let activeHeaderRefreshHandler: HeaderRefreshHandler | null = null;

export const triggerHeaderRefresh = async () => {
  if (!activeHeaderRefreshHandler) return;
  await activeHeaderRefreshHandler();
};

export const useHeaderRefresh = (handler?: HeaderRefreshHandler | null, enabled = true) => {
  const lastHandlerRef = useRef<HeaderRefreshHandler | null>(null);

  useEffect(() => {
    const previousHandler = lastHandlerRef.current;
    lastHandlerRef.current = handler ?? null;

    if (!enabled || !handler) {
      if (previousHandler && activeHeaderRefreshHandler === previousHandler) {
        activeHeaderRefreshHandler = null;
      }
      return;
    }

    activeHeaderRefreshHandler = handler;

    return () => {
      if (activeHeaderRefreshHandler === handler) {
        activeHeaderRefreshHandler = null;
      }
    };
  }, [enabled, handler]);
};
