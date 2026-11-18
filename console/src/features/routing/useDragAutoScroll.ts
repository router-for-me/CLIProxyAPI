import { useEffect, type RefObject } from 'react';

/** Distance from a scroll edge, in px, inside which a drag starts scrolling. */
export const AUTO_SCROLL_EDGE = 80;
/** Scroll speed, in px per second, with the pointer at (or past) the edge itself. */
export const AUTO_SCROLL_MAX_SPEED = 1100;

/**
 * Scroll velocity (px/s, negative = up) for a pointer at `y` within a visible band
 * `[top, bottom]`. Zero in the middle; ramps up quadratically through the edge zone so
 * a light approach nudges and a firm push races.
 */
export function edgeScrollVelocity(
  y: number,
  top: number,
  bottom: number,
  edge = AUTO_SCROLL_EDGE,
  maxSpeed = AUTO_SCROLL_MAX_SPEED
): number {
  // Short viewports: never let the two zones overlap and fight.
  const zone = Math.min(edge, Math.max(0, (bottom - top) / 3));
  if (zone <= 0) return 0;
  const strength = (distance: number) => {
    const t = Math.min(1, Math.max(0, (zone - distance) / zone));
    return t * t * maxSpeed;
  };
  if (y < top + zone) return -strength(y - top);
  if (y > bottom - zone) return strength(bottom - y);
  return 0;
}

function scrollParentOf(node: HTMLElement | null): HTMLElement {
  for (let el = node?.parentElement ?? null; el; el = el.parentElement) {
    const { overflowY } = getComputedStyle(el);
    if ((overflowY === 'auto' || overflowY === 'scroll') && el.scrollHeight > el.clientHeight) {
      return el;
    }
  }
  return (document.scrollingElement as HTMLElement | null) ?? document.documentElement;
}

/**
 * While `active`, scrolls the nearest scrollable ancestor of `anchorRef` when a native
 * HTML5 drag hovers near its top or bottom edge. Browsers either don't auto-scroll inner
 * scroll containers during a drag or only do so in a sliver under floating chrome.
 */
export function useDragAutoScroll(active: boolean, anchorRef: RefObject<HTMLElement | null>) {
  useEffect(() => {
    if (!active || typeof window === 'undefined') return;
    const scroller = scrollParentOf(anchorRef.current);
    const isRoot = scroller === document.scrollingElement || scroller === document.documentElement;
    let pointerY: number | null = null;
    let frame = 0;
    let last = 0;
    // Some engines round scrollTop to whole pixels; carry the remainder so slow speeds still move.
    let carry = 0;

    const tick = (now: number) => {
      const elapsed = last ? Math.min(now - last, 50) : 16;
      last = now;
      if (pointerY !== null) {
        const rect = isRoot ? null : scroller.getBoundingClientRect();
        const top = Math.max(rect?.top ?? 0, 0);
        const bottom = Math.min(rect?.bottom ?? window.innerHeight, window.innerHeight);
        const velocity = edgeScrollVelocity(pointerY, top, bottom);
        carry = velocity === 0 ? 0 : carry + (velocity * elapsed) / 1000;
        const step = Math.trunc(carry);
        if (step !== 0) {
          scroller.scrollTop += step;
          carry -= step;
        }
      }
      frame = requestAnimationFrame(tick);
    };

    // dragover keeps firing while the pointer rests, so the last value stays fresh.
    const track = (event: DragEvent) => {
      pointerY = event.clientY;
    };
    // Leaving the window: stop rather than keep scrolling on a stale position. relatedTarget is
    // unreliable for drag events, so judge by coordinates (browsers report 0,0 or out-of-bounds).
    const leave = (event: DragEvent) => {
      const { clientX: x, clientY: y } = event;
      const outside =
        (x <= 0 && y <= 0) || x < 0 || y < 0 || x >= window.innerWidth || y >= window.innerHeight;
      if (outside) pointerY = null;
    };
    const stop = () => {
      pointerY = null;
    };

    document.addEventListener('dragover', track, true);
    document.addEventListener('dragenter', track, true);
    document.addEventListener('dragleave', leave, true);
    document.addEventListener('drop', stop, true);
    document.addEventListener('dragend', stop, true);
    frame = requestAnimationFrame(tick);
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener('dragover', track, true);
      document.removeEventListener('dragenter', track, true);
      document.removeEventListener('dragleave', leave, true);
      document.removeEventListener('drop', stop, true);
      document.removeEventListener('dragend', stop, true);
    };
  }, [active, anchorRef]);
}
