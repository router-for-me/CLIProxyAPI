import { useEffect, useLayoutEffect, useRef, useState, type RefObject } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { animate } from 'motion/mini';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { IconCheck } from '@/components/ui/icons';
import { prefersReducedMotion } from '@/hooks/motion';
import { useActionBarHeightVar } from '@/hooks/useActionBarHeightVar';
import styles from './RoutingSaveBar.module.scss';

const easeOutQuart = (progress: number) => 1 - (1 - progress) ** 4;
const easeInCubic = (progress: number) => progress ** 3;
const SHOWN = 'translateY(0)';
const HIDDEN = 'translateY(56px)';
/** Bar width when the policy panel stacks under the board. */
const STACKED_MAX_WIDTH = 560;

/** Marks the policy column the bar aligns with. */
export const POLICY_COLUMN_ATTR = 'data-routing-policy-column';

export interface RoutingSaveBarProps {
  /** Visible while the draft differs from the board (same condition as the leave guard). */
  visible: boolean;
  count: number;
  saving: boolean;
  saveDisabled: boolean;
  onSave: () => void;
  onDiscard: () => void;
  /** The board/policy grid; the bar lines up with its policy column. */
  layoutRef: RefObject<HTMLElement | null>;
}

/**
 * Floating save bar, styled after the config page's: a glass toolbar portalled to the body
 * that rises in on the first change and drops out once the draft is clean.
 *
 * Unlike the config bar it is not centred on the page: it takes the exact left edge and width
 * of the policy column, so it never covers the ladder being arranged. When the policy panel
 * stacks under the board it centres over the layout instead. (Sticky positioning cannot be
 * used: the page-transition layer is not the scroll container.)
 */
export function RoutingSaveBar({
  visible,
  count,
  saving,
  saveDisabled,
  onSave,
  onDiscard,
  layoutRef,
}: RoutingSaveBarProps) {
  const { t } = useTranslation();
  const [mounted, setMounted] = useState(visible);
  // Keep the last real count through the exit so the bar does not flash "0".
  const [shownCount, setShownCount] = useState(count);
  const dockRef = useRef<HTMLDivElement | null>(null);
  const barRef = useRef<HTMLDivElement | null>(null);
  const animationRef = useRef<ReturnType<typeof animate> | null>(null);
  const visibleRef = useRef(visible);
  const previousVisibleRef = useRef(false);

  if (visible && !mounted) setMounted(true);
  if (visible && count !== shownCount) setShownCount(count);

  useEffect(() => {
    visibleRef.current = visible;
  }, [visible]);

  useActionBarHeightVar(dockRef, '--routing-action-bar-height', mounted);

  // Follow the policy column through window resizes and sidebar collapse. Styles are written
  // directly so tracking never re-renders the page.
  useLayoutEffect(() => {
    const dock = dockRef.current;
    const layout = layoutRef.current;
    if (!mounted || !dock || !layout) return;
    const place = () => {
      const area = layout.getBoundingClientRect();
      const column = layout.querySelector(`[${POLICY_COLUMN_ATTR}]`)?.getBoundingClientRect();
      const beside = column && column.left > area.left + 1;
      const width = beside ? column.width : Math.min(area.width, STACKED_MAX_WIDTH);
      const left = beside ? column.left : area.left + (area.width - width) / 2;
      dock.style.margin = '0';
      dock.style.right = 'auto';
      dock.style.left = `${left}px`;
      dock.style.width = `${width}px`;
    };
    place();
    window.addEventListener('resize', place);
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(place);
    observer?.observe(layout);
    return () => {
      window.removeEventListener('resize', place);
      observer?.disconnect();
    };
  }, [mounted, layoutRef]);

  useLayoutEffect(() => {
    if (!mounted) return;
    const el = barRef.current;
    if (!el) return;
    const wasVisible = previousVisibleRef.current;
    previousVisibleRef.current = visible;
    if (visible === wasVisible) return;

    animationRef.current?.stop();
    const reduced = prefersReducedMotion();

    if (visible) {
      animationRef.current = reduced
        ? animate(el, { opacity: [0, 1] }, { duration: 0.15, ease: 'linear' })
        : animate(
            el,
            { transform: [HIDDEN, SHOWN], opacity: [0, 1] },
            { duration: 0.28, ease: easeOutQuart }
          );
      return;
    }

    const finishExit = () => {
      if (!visibleRef.current) setMounted(false);
    };
    animationRef.current = reduced
      ? animate(el, { opacity: [1, 0] }, { duration: 0.12, ease: 'linear', onComplete: finishExit })
      : animate(
          el,
          { transform: [SHOWN, HIDDEN], opacity: [1, 0] },
          { duration: 0.22, ease: easeInCubic, onComplete: finishExit }
        );
  }, [mounted, visible]);

  useEffect(
    () => () => {
      animationRef.current?.stop();
    },
    []
  );

  if (!mounted || typeof document === 'undefined') return null;

  return createPortal(
    <div ref={dockRef} className={styles.dock}>
      <div
        ref={barRef}
        className={styles.bar}
        role="region"
        aria-label={t('routing.dirty_region')}
        data-leaving={!visible || undefined}
      >
        <span className={styles.status} aria-live="polite">
          {t('routing.dirty_count', { count: shownCount })}
        </span>
        <div className={styles.actions}>
          <button
            type="button"
            className={styles.ghostAction}
            onClick={onDiscard}
            disabled={!visible || saving}
          >
            {t('routing.discard')}
          </button>
          <button
            type="button"
            className={styles.savePill}
            onClick={onSave}
            disabled={!visible || saving || saveDisabled}
          >
            {saving ? <LoadingSpinner size={14} /> : <IconCheck size={15} />}
            {t('routing.save')}
          </button>
        </div>
      </div>
    </div>,
    document.body
  );
}
