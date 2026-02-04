import { useEffect, useRef } from 'react';

type SwipeBackOptions = {
  enabled?: boolean;
  edgeSize?: number;
  threshold?: number;
  onBack: () => void;
};

type ActiveGesture = {
  pointerId: number;
  startX: number;
  startY: number;
  active: boolean;
};

const DEFAULT_EDGE_SIZE = 28;
const DEFAULT_THRESHOLD = 90;
const VERTICAL_TOLERANCE_RATIO = 1.2;

export function useEdgeSwipeBack({
  enabled = true,
  edgeSize = DEFAULT_EDGE_SIZE,
  threshold = DEFAULT_THRESHOLD,
  onBack,
}: SwipeBackOptions) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const gestureRef = useRef<ActiveGesture | null>(null);

  useEffect(() => {
    if (!enabled) return;
    const el = containerRef.current;
    if (!el) return;

    const reset = () => {
      gestureRef.current = null;
    };

    const handlePointerMove = (event: PointerEvent) => {
      const gesture = gestureRef.current;
      if (!gesture?.active) return;
      if (event.pointerId !== gesture.pointerId) return;

      const dx = event.clientX - gesture.startX;
      const dy = event.clientY - gesture.startY;

      if (Math.abs(dy) > Math.abs(dx) * VERTICAL_TOLERANCE_RATIO) {
        reset();
      }
    };

    const handlePointerUp = (event: PointerEvent) => {
      const gesture = gestureRef.current;
      if (!gesture?.active) return;
      if (event.pointerId !== gesture.pointerId) return;

      const dx = event.clientX - gesture.startX;
      const dy = event.clientY - gesture.startY;
      const isHorizontal = Math.abs(dx) > Math.abs(dy) * VERTICAL_TOLERANCE_RATIO;

      reset();

      if (dx >= threshold && isHorizontal) {
        onBack();
      }
    };

    const handlePointerCancel = (event: PointerEvent) => {
      const gesture = gestureRef.current;
      if (!gesture?.active) return;
      if (event.pointerId !== gesture.pointerId) return;
      reset();
    };

    const handlePointerDown = (event: PointerEvent) => {
      if (event.pointerType !== 'touch') return;
      if (!event.isPrimary) return;
      if (event.clientX > edgeSize) return;

      gestureRef.current = {
        pointerId: event.pointerId,
        startX: event.clientX,
        startY: event.clientY,
        active: true,
      };
    };

    el.addEventListener('pointerdown', handlePointerDown, { passive: true });
    window.addEventListener('pointermove', handlePointerMove, { passive: true });
    window.addEventListener('pointerup', handlePointerUp, { passive: true });
    window.addEventListener('pointercancel', handlePointerCancel, { passive: true });

    return () => {
      el.removeEventListener('pointerdown', handlePointerDown);
      window.removeEventListener('pointermove', handlePointerMove);
      window.removeEventListener('pointerup', handlePointerUp);
      window.removeEventListener('pointercancel', handlePointerCancel);
    };
  }, [edgeSize, enabled, onBack, threshold]);

  return containerRef;
}

