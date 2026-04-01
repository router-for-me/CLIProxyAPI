import { ReactNode, useCallback, useLayoutEffect, useRef, useState } from 'react';
import { useLocation, type Location } from 'react-router-dom';
import { animate } from 'motion/mini';
import type { AnimationPlaybackControlsWithThen } from 'motion-dom';
import { PageTransitionLayerContext, type LayerStatus } from './PageTransitionLayer';
import './PageTransition.scss';

interface PageTransitionProps {
  render: (location: Location) => ReactNode;
  getRouteOrder?: (pathname: string) => number | null;
  getTransitionVariant?: (fromPathname: string, toPathname: string) => TransitionVariant;
  scrollContainerRef?: React.RefObject<HTMLElement | null>;
}

const VERTICAL_TRANSITION_DURATION = 0.35;
const VERTICAL_TRAVEL_DISTANCE = 60;
const IOS_TRANSITION_DURATION = 0.42;
const IOS_ENTER_FROM_X_PERCENT = 100;
const IOS_EXIT_TO_X_PERCENT_FORWARD = -30;
const IOS_EXIT_TO_X_PERCENT_BACKWARD = 100;
const IOS_ENTER_FROM_X_PERCENT_BACKWARD = -30;
const IOS_EXIT_DIM_OPACITY = 0.72;
const IOS_SHADOW_VALUE = '-14px 0 24px rgba(0, 0, 0, 0.16)';

const easePower2Out = (progress: number) => 1 - (1 - progress) ** 3;
const easeCircOut = (progress: number) => Math.sqrt(1 - (progress - 1) ** 2);

const buildVerticalTransform = (y: number) => `translate3d(0px, ${y}px, 0px)`;
const buildIosTransform = (xPercent: number, y: number) => `translate3d(${xPercent}%, ${y}px, 0px)`;

const clearLayerStyles = (element: HTMLElement | null) => {
  if (!element) return;
  element.style.removeProperty('transform');
  element.style.removeProperty('opacity');
  element.style.removeProperty('box-shadow');
};

type Layer = {
  key: string;
  location: Location;
  status: LayerStatus;
};

type TransitionDirection = 'forward' | 'backward';

type TransitionVariant = 'vertical' | 'ios';

export function PageTransition({
  render,
  getRouteOrder,
  getTransitionVariant,
  scrollContainerRef,
}: PageTransitionProps) {
  const location = useLocation();
  const currentLayerRef = useRef<HTMLDivElement>(null);
  const exitingLayerRef = useRef<HTMLDivElement>(null);
  const transitionDirectionRef = useRef<TransitionDirection>('forward');
  const transitionVariantRef = useRef<TransitionVariant>('vertical');
  const exitScrollOffsetRef = useRef(0);
  const enterScrollOffsetRef = useRef(0);
  const scrollPositionsRef = useRef(new Map<string, number>());
  const nextLayersRef = useRef<Layer[] | null>(null);

  const [isAnimating, setIsAnimating] = useState(false);
  const [layers, setLayers] = useState<Layer[]>(() => [
    {
      key: location.key,
      location,
      status: 'current',
    },
  ]);
  const currentLayer =
    layers.find((layer) => layer.status === 'current') ?? layers[layers.length - 1];
  const currentLayerKey = currentLayer?.key ?? location.key;
  const currentLayerPathname = currentLayer?.location.pathname;

  const resolveScrollContainer = useCallback(() => {
    if (scrollContainerRef?.current) return scrollContainerRef.current;
    if (typeof document === 'undefined') return null;
    return document.scrollingElement as HTMLElement | null;
  }, [scrollContainerRef]);

  useLayoutEffect(() => {
    if (isAnimating) return;
    if (location.key === currentLayerKey) return;
    if (currentLayerPathname === location.pathname) return;
    const scrollContainer = resolveScrollContainer();
    const exitScrollOffset = scrollContainer?.scrollTop ?? 0;
    exitScrollOffsetRef.current = exitScrollOffset;
    scrollPositionsRef.current.set(currentLayerKey, exitScrollOffset);

    enterScrollOffsetRef.current = scrollPositionsRef.current.get(location.key) ?? 0;
    const resolveOrderIndex = (pathname?: string) => {
      if (!getRouteOrder || !pathname) return null;
      const index = getRouteOrder(pathname);
      return typeof index === 'number' && index >= 0 ? index : null;
    };
    const fromIndex = resolveOrderIndex(currentLayerPathname);
    const toIndex = resolveOrderIndex(location.pathname);
    const nextVariant: TransitionVariant = getTransitionVariant
      ? getTransitionVariant(currentLayerPathname ?? '', location.pathname)
      : 'vertical';

    let nextDirection: TransitionDirection =
      fromIndex === null || toIndex === null || fromIndex === toIndex
        ? 'forward'
        : toIndex > fromIndex
          ? 'forward'
          : 'backward';

    // When using iOS-style stacking, history POP within the same "section" can have equal route order.
    // In that case, prefer treating navigation to an existing layer as a backward (pop) transition.
    if (nextVariant === 'ios' && layers.some((layer) => layer.key === location.key)) {
      nextDirection = 'backward';
    }

    transitionDirectionRef.current = nextDirection;
    transitionVariantRef.current = nextVariant;

    const shouldSkipExitLayer = (() => {
      if (nextVariant !== 'ios' || nextDirection !== 'backward') return false;
      const normalizeSegments = (pathname: string) =>
        pathname
          .split('/')
          .filter(Boolean)
          .filter((segment) => segment.length > 0);
      const fromSegments = normalizeSegments(currentLayerPathname ?? '');
      const toSegments = normalizeSegments(location.pathname);
      if (!fromSegments.length || !toSegments.length) return false;
      return fromSegments[0] === toSegments[0] && toSegments.length === 1;
    })();

    setLayers((prev) => {
      const variant = transitionVariantRef.current;
      const direction = transitionDirectionRef.current;
      const previousCurrentIndex = prev.findIndex((layer) => layer.status === 'current');
      const resolvedCurrentIndex =
        previousCurrentIndex >= 0 ? previousCurrentIndex : prev.length - 1;
      const previousCurrent = prev[resolvedCurrentIndex];
      const previousStack: Layer[] = prev
        .filter((_, idx) => idx !== resolvedCurrentIndex)
        .map((layer): Layer => ({ ...layer, status: 'stacked' }));

      const nextCurrent: Layer = { key: location.key, location, status: 'current' };

      if (!previousCurrent) {
        nextLayersRef.current = [nextCurrent];
        return [nextCurrent];
      }

      if (variant === 'ios') {
        if (direction === 'forward') {
          const exitingLayer: Layer = { ...previousCurrent, status: 'exiting' };
          const stackedLayer: Layer = { ...previousCurrent, status: 'stacked' };

          nextLayersRef.current = [...previousStack, stackedLayer, nextCurrent];
          return [...previousStack, exitingLayer, nextCurrent];
        }

        const targetIndex = prev.findIndex((layer) => layer.key === location.key);
        if (targetIndex !== -1) {
          const targetStack: Layer[] = prev.slice(0, targetIndex + 1).map((layer, idx): Layer => {
            const isTarget = idx === targetIndex;
            return {
              ...layer,
              location: isTarget ? location : layer.location,
              status: isTarget ? 'current' : 'stacked',
            };
          });

          if (shouldSkipExitLayer) {
            nextLayersRef.current = targetStack;
            return targetStack;
          }

          const exitingLayer: Layer = { ...previousCurrent, status: 'exiting' };
          nextLayersRef.current = targetStack;
          return [...targetStack, exitingLayer];
        }
      }

      if (shouldSkipExitLayer) {
        nextLayersRef.current = [nextCurrent];
        return [nextCurrent];
      }

      const exitingLayer: Layer = { ...previousCurrent, status: 'exiting' };

      nextLayersRef.current = [nextCurrent];
      return [exitingLayer, nextCurrent];
    });
    setIsAnimating(true);
  }, [
    isAnimating,
    location,
    currentLayerKey,
    currentLayerPathname,
    getRouteOrder,
    getTransitionVariant,
    resolveScrollContainer,
    layers,
  ]);

  // Run Motion animation when animating starts
  useLayoutEffect(() => {
    if (!isAnimating) return;

    if (!currentLayerRef.current) return;

    const currentLayerEl = currentLayerRef.current;
    const exitingLayerEl = exitingLayerRef.current;
    const transitionVariant = transitionVariantRef.current;

    clearLayerStyles(currentLayerEl);
    clearLayerStyles(exitingLayerEl);

    const scrollContainer = resolveScrollContainer();
    const exitScrollOffset = exitScrollOffsetRef.current;
    const enterScrollOffset = enterScrollOffsetRef.current;
    if (scrollContainer && exitScrollOffset !== enterScrollOffset) {
      scrollContainer.scrollTo({ top: enterScrollOffset, left: 0, behavior: 'auto' });
    }

    const transitionDirection = transitionDirectionRef.current;
    const isForward = transitionDirection === 'forward';
    const enterFromY = isForward ? VERTICAL_TRAVEL_DISTANCE : -VERTICAL_TRAVEL_DISTANCE;
    const exitToY = isForward ? -VERTICAL_TRAVEL_DISTANCE : VERTICAL_TRAVEL_DISTANCE;
    const exitBaseY = enterScrollOffset - exitScrollOffset;
    const activeAnimations: AnimationPlaybackControlsWithThen[] = [];
    let cancelled = false;
    let completed = false;
    const completeTransition = () => {
      if (completed) return;
      completed = true;

      const nextLayers = nextLayersRef.current;
      nextLayersRef.current = null;
      setLayers((prev) => nextLayers ?? prev.filter((layer) => layer.status !== 'exiting'));
      setIsAnimating(false);

      clearLayerStyles(currentLayerEl);
      clearLayerStyles(exitingLayerEl);
    };

    if (transitionVariant === 'ios') {
      const exitToXPercent = isForward
        ? IOS_EXIT_TO_X_PERCENT_FORWARD
        : IOS_EXIT_TO_X_PERCENT_BACKWARD;
      const enterFromXPercent = isForward
        ? IOS_ENTER_FROM_X_PERCENT
        : IOS_ENTER_FROM_X_PERCENT_BACKWARD;

      if (exitingLayerEl) {
        exitingLayerEl.style.transform = buildIosTransform(0, exitBaseY);
        exitingLayerEl.style.opacity = '1';
      }

      currentLayerEl.style.transform = buildIosTransform(enterFromXPercent, 0);
      currentLayerEl.style.opacity = '1';

      const topLayerEl = isForward ? currentLayerEl : exitingLayerEl;
      if (topLayerEl) {
        topLayerEl.style.boxShadow = IOS_SHADOW_VALUE;
      }

      if (exitingLayerEl) {
        activeAnimations.push(
          animate(
            exitingLayerEl,
            {
              transform: [
                buildIosTransform(0, exitBaseY),
                buildIosTransform(exitToXPercent, exitBaseY),
              ],
              opacity: [1, isForward ? IOS_EXIT_DIM_OPACITY : 1],
            },
            {
              duration: IOS_TRANSITION_DURATION,
              ease: easePower2Out,
            }
          )
        );
      }

      activeAnimations.push(
        animate(
          currentLayerEl,
          {
            transform: [buildIosTransform(enterFromXPercent, 0), buildIosTransform(0, 0)],
            opacity: [1, 1],
          },
          {
            duration: IOS_TRANSITION_DURATION,
            ease: easePower2Out,
          }
        )
      );
    } else {
      // Exit animation: fade out with slight movement (runs simultaneously)
      if (exitingLayerEl) {
        exitingLayerEl.style.transform = buildVerticalTransform(exitBaseY);
        activeAnimations.push(
          animate(
            exitingLayerEl,
            {
              transform: [
                buildVerticalTransform(exitBaseY),
                buildVerticalTransform(exitBaseY + exitToY),
              ],
              opacity: [1, 0],
            },
            {
              duration: VERTICAL_TRANSITION_DURATION,
              ease: easeCircOut,
            }
          )
        );
      }

      // Enter animation: fade in with slight movement (runs simultaneously)
      currentLayerEl.style.transform = buildVerticalTransform(enterFromY);
      currentLayerEl.style.opacity = '0';
      activeAnimations.push(
        animate(
          currentLayerEl,
          {
            transform: [buildVerticalTransform(enterFromY), buildVerticalTransform(0)],
            opacity: [0, 1],
          },
          {
            duration: VERTICAL_TRANSITION_DURATION,
            ease: easeCircOut,
          }
        )
      );
    }

    if (!activeAnimations.length) {
      completeTransition();
    } else {
      void Promise.all(
        activeAnimations.map((animation) => animation.finished.catch(() => undefined))
      ).then(() => {
        if (cancelled) return;
        completeTransition();
      });
    }

    return () => {
      cancelled = true;
      activeAnimations.forEach((animation) => animation.stop());
    };
  }, [isAnimating, resolveScrollContainer]);

  return (
    <div className={`page-transition${isAnimating ? ' page-transition--animating' : ''}`}>
      {(() => {
        const currentIndex = layers.findIndex((layer) => layer.status === 'current');
        const resolvedCurrentIndex = currentIndex === -1 ? layers.length - 1 : currentIndex;
        const keepStackedIndex = layers
          .slice(0, resolvedCurrentIndex)
          .map((layer, index) => ({ layer, index }))
          .reverse()
          .find(({ layer }) => layer.status === 'stacked')?.index;

        return layers.map((layer, index) => {
          const shouldKeepStacked = layer.status === 'stacked' && index === keepStackedIndex;
          return (
            <div
              key={layer.key}
              className={[
                'page-transition__layer',
                layer.status === 'exiting' ? 'page-transition__layer--exit' : '',
                layer.status === 'stacked' ? 'page-transition__layer--stacked' : '',
                shouldKeepStacked ? 'page-transition__layer--stacked-keep' : '',
              ]
                .filter(Boolean)
                .join(' ')}
              aria-hidden={layer.status !== 'current'}
              inert={layer.status !== 'current'}
              ref={
                layer.status === 'exiting'
                  ? exitingLayerRef
                  : layer.status === 'current'
                    ? currentLayerRef
                    : undefined
              }
            >
              <PageTransitionLayerContext.Provider value={{ status: layer.status }}>
                {render(layer.location)}
              </PageTransitionLayerContext.Provider>
            </div>
          );
        });
      })()}
    </div>
  );
}
