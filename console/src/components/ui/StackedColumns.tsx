import {
  useCallback,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from 'react';
import { packColumns, stackedColumnCount, updateStackedHeights } from './cardLayout';
import styles from './StackedColumns.module.scss';

export interface StackedColumnsItem {
  key: string;
  node: ReactNode;
  /**
   * False while the card's content is loading: preserve its height unless it grows, so a
   * refresh does not collapse the layout and taller loading content cannot overlap cards.
   */
  settled: boolean;
}

export interface StackedColumnsProps {
  items: StackedColumnsItem[];
  /** False renders the items as plain children of `className` (for example a CSS grid). */
  stacked: boolean;
  minColumnWidth: number;
  gap: number;
  className?: string;
}

/**
 * Card container for every card layout. Stacked packs cards into equal-width columns by their
 * measured heights: each card goes to the shortest column so far. Cards stay children of this
 * one container in every layout and are only positioned, so neither repacking nor switching
 * layouts remounts a card, and the DOM (and Tab) order is the list order.
 */
export function StackedColumns({
  items,
  stacked,
  minColumnWidth,
  gap,
  className,
}: StackedColumnsProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const elementsRef = useRef(new Map<string, HTMLElement>());
  const settledRef = useRef<ReadonlyMap<string, boolean>>(new Map());
  const [width, setWidth] = useState(0);
  const [heights, setHeights] = useState<ReadonlyMap<string, number>>(() => new Map());

  // Measure loading cards too: cached heights prevent shrinking, but growth must be packed.
  const recordHeights = useCallback((elements: Iterable<HTMLElement>) => {
    const measured: { key: string; height: number; settled: boolean }[] = [];
    for (const element of elements) {
      const key = element.dataset.stackedKey;
      if (!key) continue;
      measured.push({
        key,
        height: element.getBoundingClientRect().height,
        settled: settledRef.current.get(key) !== false,
      });
    }
    if (measured.length === 0) return;
    setHeights((previous) => updateStackedHeights(previous, measured));
  }, []);

  // After every render, before paint: refresh the settled flags and measure the cards, so a
  // card that settles at its loading height, or was never observed, is packed by its real size.
  useLayoutEffect(() => {
    settledRef.current = new Map(items.map((item) => [item.key, item.settled]));
    if (stacked) recordHeights(elementsRef.current.values());
  });

  // Size changes that do not re-render this component (a card's own state, the window).
  const [observer, setObserver] = useState<ResizeObserver | null>(null);
  useLayoutEffect(() => {
    if (!stacked) return;
    const container = containerRef.current;
    if (container) setWidth(container.getBoundingClientRect().width);
    if (typeof ResizeObserver === 'undefined') return;
    const next = new ResizeObserver((entries) => {
      const cards: HTMLElement[] = [];
      for (const entry of entries) {
        if (entry.target === container) {
          const containerWidth = container.getBoundingClientRect().width;
          if (containerWidth > 0) setWidth(containerWidth);
        } else {
          cards.push(entry.target as HTMLElement);
        }
      }
      recordHeights(cards);
    });
    if (container) next.observe(container);
    elementsRef.current.forEach((element) => next.observe(element));
    setObserver(next);
    return () => {
      next.disconnect();
      setObserver(null);
    };
  }, [stacked, recordHeights]);

  const attachItem = useCallback(
    (element: HTMLDivElement | null) => {
      if (!element) return;
      const key = element.dataset.stackedKey ?? '';
      elementsRef.current.set(key, element);
      observer?.observe(element);
      return () => {
        if (elementsRef.current.get(key) === element) elementsRef.current.delete(key);
        observer?.unobserve(element);
      };
    },
    [observer]
  );

  const positioned = stacked && width > 0;
  const columnCount = positioned ? stackedColumnCount(width, minColumnWidth, gap) : 1;
  const columnWidth = (width - gap * (columnCount - 1)) / columnCount;
  const known = items
    .map((item) => heights.get(item.key))
    .filter((height): height is number => height !== undefined);
  // Not yet measured: assume an average card until the layout effect measures it.
  const fallback = known.length ? known.reduce((sum, height) => sum + height, 0) / known.length : 1;
  const packed = packColumns(
    items.map((item) => heights.get(item.key) ?? fallback),
    columnCount,
    gap
  );

  const classNames = [stacked ? styles.columns : '', className ?? ''].filter(Boolean).join(' ');
  return (
    <div
      ref={containerRef}
      className={classNames || undefined}
      style={
        stacked
          ? ({
              '--stacked-gap': `${gap}px`,
              height: positioned ? `${packed.height}px` : undefined,
            } as CSSProperties)
          : undefined
      }
      data-positioned={positioned ? '' : undefined}
    >
      {items.map((item, index) => {
        const { column, top } = packed.positions[index];
        return (
          <div
            key={item.key}
            ref={attachItem}
            data-stacked-key={item.key}
            className={styles.item}
            style={
              positioned
                ? {
                    width: `${columnWidth}px`,
                    transform: `translate(${column * (columnWidth + gap)}px, ${top}px)`,
                  }
                : undefined
            }
          >
            {item.node}
          </div>
        );
      })}
    </div>
  );
}
