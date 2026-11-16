import { useEffect, useRef, useState, type RefObject } from 'react';

/** The element's content-box width, tracked with a ResizeObserver and coalesced to one update per frame. */
export const useElementWidth = <T extends HTMLElement>(
  fallback: number
): [RefObject<T | null>, number] => {
  const ref = useRef<T | null>(null);
  const [width, setWidth] = useState(fallback);

  useEffect(() => {
    const element = ref.current;

    if (!element || typeof ResizeObserver === 'undefined') return;

    let frame: number | null = null;

    const observer = new ResizeObserver(([entry]) => {
      const nextWidth = Math.round(entry.contentRect.width);

      if (frame !== null) cancelAnimationFrame(frame);

      frame = requestAnimationFrame(() => {
        frame = null;

        if (nextWidth > 0) setWidth(nextWidth);
      });
    });

    observer.observe(element);

    return () => {
      observer.disconnect();

      if (frame !== null) cancelAnimationFrame(frame);
    };
  }, []);

  return [ref, width];
};
