import {
  ReactNode,
  createContext,
  useCallback,
  useContext,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import { useLocation, type Location } from 'react-router-dom';
import gsap from 'gsap';
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

type LayerStatus = 'current' | 'exiting' | 'stacked';

type Layer = {
  key: string;
  location: Location;
  status: LayerStatus;
};

type TransitionDirection = 'forward' | 'backward';

type TransitionVariant = 'vertical' | 'ios';

type PageTransitionLayerContextValue = {
  status: LayerStatus;
};

const PageTransitionLayerContext = createContext<PageTransitionLayerContextValue | null>(null);

export function usePageTransitionLayer() {
  return useContext(PageTransitionLayerContext);
}

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
          const targetStack: Layer[] = prev
            .slice(0, targetIndex + 1)
            .map((layer, idx): Layer => {
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

  // Run GSAP animation when animating starts
  useLayoutEffect(() => {
    if (!isAnimating) return;

    if (!currentLayerRef.current) return;

    const currentLayerEl = currentLayerRef.current;
    const exitingLayerEl = exitingLayerRef.current;
    const transitionVariant = transitionVariantRef.current;

    gsap.set(currentLayerEl, { clearProps: 'transform,opacity,boxShadow' });
    if (exitingLayerEl) {
      gsap.set(exitingLayerEl, { clearProps: 'transform,opacity,boxShadow' });
    }

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

    const tl = gsap.timeline({
      onComplete: () => {
        const nextLayers = nextLayersRef.current;
        nextLayersRef.current = null;
        setLayers((prev) => nextLayers ?? prev.filter((layer) => layer.status !== 'exiting'));
        setIsAnimating(false);

        if (currentLayerEl) {
          gsap.set(currentLayerEl, { clearProps: 'transform,opacity,boxShadow' });
        }
        if (exitingLayerEl) {
          gsap.set(exitingLayerEl, { clearProps: 'transform,opacity,boxShadow' });
        }
      },
    });

    if (transitionVariant === 'ios') {
      const exitToXPercent = isForward
        ? IOS_EXIT_TO_X_PERCENT_FORWARD
        : IOS_EXIT_TO_X_PERCENT_BACKWARD;
      const enterFromXPercent = isForward
        ? IOS_ENTER_FROM_X_PERCENT
        : IOS_ENTER_FROM_X_PERCENT_BACKWARD;

      if (exitingLayerEl) {
        gsap.set(exitingLayerEl, {
          y: exitBaseY,
          xPercent: 0,
          opacity: 1,
        });
      }

      gsap.set(currentLayerEl, {
        xPercent: enterFromXPercent,
        opacity: 1,
      });

      const shadowValue = '-14px 0 24px rgba(0, 0, 0, 0.16)';

      const topLayerEl = isForward ? currentLayerEl : exitingLayerEl;
      if (topLayerEl) {
        gsap.set(topLayerEl, { boxShadow: shadowValue });
      }

      if (exitingLayerEl) {
        tl.to(
          exitingLayerEl,
          {
            xPercent: exitToXPercent,
            opacity: isForward ? IOS_EXIT_DIM_OPACITY : 1,
            duration: IOS_TRANSITION_DURATION,
            ease: 'power2.out',
            force3D: true,
          },
          0
        );
      }

      tl.to(
        currentLayerEl,
        {
          xPercent: 0,
          opacity: 1,
          duration: IOS_TRANSITION_DURATION,
          ease: 'power2.out',
          force3D: true,
        },
        0
      );
    } else {
      // Exit animation: fade out with slight movement (runs simultaneously)
      if (exitingLayerEl) {
        gsap.set(exitingLayerEl, { y: exitBaseY });
        tl.to(
          exitingLayerEl,
          {
            y: exitBaseY + exitToY,
            opacity: 0,
            duration: VERTICAL_TRANSITION_DURATION,
            ease: 'circ.out',
            force3D: true,
          },
          0
        );
      }

      // Enter animation: fade in with slight movement (runs simultaneously)
      tl.fromTo(
        currentLayerEl,
        { y: enterFromY, opacity: 0 },
        {
          y: 0,
          opacity: 1,
          duration: VERTICAL_TRANSITION_DURATION,
          ease: 'circ.out',
          force3D: true,
          onComplete: () => {
            if (currentLayerEl) {
              gsap.set(currentLayerEl, { clearProps: 'transform,opacity' });
            }
          },
        },
        0
      );
    }

    return () => {
      tl.kill();
      gsap.killTweensOf([currentLayerEl, exitingLayerEl]);
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
