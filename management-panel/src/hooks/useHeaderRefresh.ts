import { useEffect } from 'react';

export type HeaderRefreshHandler = () => void | Promise<void>;

let activeHeaderRefreshHandler: HeaderRefreshHandler | null = null;

export const triggerHeaderRefresh = async () => {
  if (!activeHeaderRefreshHandler) return;
  await activeHeaderRefreshHandler();
};

export const useHeaderRefresh = (handler?: HeaderRefreshHandler | null) => {
  useEffect(() => {
    if (!handler) return;

    activeHeaderRefreshHandler = handler;

    return () => {
      if (activeHeaderRefreshHandler === handler) {
        activeHeaderRefreshHandler = null;
      }
    };
  }, [handler]);
};
