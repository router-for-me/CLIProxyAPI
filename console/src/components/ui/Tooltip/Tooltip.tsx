/**
 * Shared tooltip primitives. Import from `@/components/ui/Tooltip`.
 *
 * ── InfoTip ──────────────────────────────────────────────────────────────────────
 * The standard carrier for an explanation that was moved off the page.
 *
 *   <label htmlFor={id}>{label}</label>
 *   <InfoTip content={t('some.hint')} label={label} />
 *
 * Props
 *   content: ReactNode        Required. The explanation. Plain text or light inline markup
 *                             (<code>, <strong>). Renders nothing when empty.
 *   label?: string            The visible label it explains. Gives the button a specific
 *                             accessible name ("More information about <label>") instead of
 *                             the generic "More information". Pass it whenever there is one.
 *   descriptionId?: string    Id of an element that already carries the same text for
 *                             assistive tech (e.g. a visually hidden hint the control points
 *                             at with aria-describedby). The button is described by it instead
 *                             of rendering its own hidden copy. Most call sites omit this.
 *   className?: string        Extra class for the button (spacing tweaks only).
 *
 * Convention
 * - Put it directly after the label/title text, on the same line, as a sibling of the
 *   <label>/<h*> element, NOT inside it (inside a <label> its accessible name would be
 *   appended to the control's name). One InfoTip per label at most.
 * - It brings its own leading space (0.35em) and vertical alignment, so it works both in
 *   normal inline flow and inside an `align-items: center` flex row. Do not add a gap for it.
 * - Small, muted at rest, clearer on hover/focus; it does not grow the line it sits in.
 * - Behaviour: mouse hover opens after a short delay; keyboard focus opens immediately;
 *   click/tap toggles and pins it open until a tap elsewhere, Escape, blur or scroll.
 *   The pointer can move from the button onto the tooltip without it closing.
 * - Assistive tech: the button is always described by the content (via `descriptionId` or a
 *   `hidden` copy), so screen-reader users hear it on focus without opening anything.
 * - Do not put links or other interactive content in `content`: keyboard users cannot reach
 *   anything inside a tooltip. Text that needs a link should stay visible on the page.
 *
 * ── Tooltip ──────────────────────────────────────────────────────────────────────
 * Wraps exactly one focusable element (button, link, input) and shows `content` for it.
 *
 *   <Tooltip content={t('x.explanation')}>
 *     <button type="button" aria-label={t('x.action')}>…</button>
 *   </Tooltip>
 *
 * Props
 *   content: ReactNode        Required. Falsy content renders the child untouched.
 *   children: ReactElement    One element that accepts ref, pointer/focus handlers and
 *                             aria-describedby (DOM elements and forwardRef/ref-prop components).
 *   delay?: number            Hover delay in ms (default 300). Focus always opens immediately.
 *
 * - Hover (mouse/pen) and keyboard focus only; touch does not open it, and pressing the
 *   trigger closes it. Use InfoTip when the text must be reachable on touch devices.
 * - While open, the child gets `aria-describedby` pointing at the tooltip (merged with any
 *   value it already has). The tooltip is supplementary: the child still needs its own
 *   accessible name.
 *
 * ── Both ─────────────────────────────────────────────────────────────────────────
 * Rendered in a portal on document.body with fixed positioning, so it is never clipped by
 * `overflow: hidden` rows and stacks above Sheets, Modals and Select dropdowns. Prefers above
 * the trigger, flips below when there is no room, clamps to the viewport (maths in
 * `tooltipPosition.ts`). Max width ~280px. Escape closes it without closing the Sheet/Modal
 * underneath. Only one tooltip is open at a time; moving between tooltips skips the delay.
 */

import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type ElementType,
  type FocusEvent,
  type MouseEvent,
  type PointerEvent,
  type ReactElement,
  type ReactNode,
  type Ref,
} from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { IconInfo } from '../icons';
import { resolveTooltipPosition, type TooltipPosition } from './tooltipPosition';
import styles from './Tooltip.module.scss';

/** Hover intent delay before opening. */
const OPEN_DELAY = 300;
/** Grace period after the pointer leaves, so it can travel onto the tooltip. */
const CLOSE_GRACE = 100;
/** A tooltip opened within this window after another closed skips the delay and the animation. */
const WARM_WINDOW = 300;

/* Only one tooltip is visible at a time across the app. */
let activeTooltip: { id: string; close: () => void } | null = null;
let lastClosedAt = 0;

type OpenReason = 'hover' | 'focus' | 'pinned';

const isFocusVisible = (element: Element) => {
  try {
    return element.matches(':focus-visible');
  } catch {
    return true;
  }
};

const hasContent = (content: ReactNode) =>
  content !== null && content !== undefined && content !== false && content !== '';

/**
 * Shared open/close state machine and the portalled bubble. Both public components are thin
 * wrappers that wire these handlers onto their trigger.
 */
function useTooltipController(content: ReactNode, delay: number) {
  const tooltipId = useId();
  const [open, setOpen] = useState(false);
  const [instant, setInstant] = useState(false);
  const [position, setPosition] = useState<TooltipPosition | null>(null);
  const triggerRef = useRef<HTMLElement | null>(null);
  const tooltipRef = useRef<HTMLDivElement | null>(null);
  const reasonRef = useRef<OpenReason | null>(null);
  const openTimerRef = useRef<number | null>(null);
  const closeTimerRef = useRef<number | null>(null);
  /** Set while a press inside the bubble is moving focus away from the trigger. */
  const pressingBubbleRef = useRef(false);

  const clearTimers = useCallback(() => {
    if (openTimerRef.current !== null) window.clearTimeout(openTimerRef.current);
    if (closeTimerRef.current !== null) window.clearTimeout(closeTimerRef.current);
    openTimerRef.current = null;
    closeTimerRef.current = null;
  }, []);

  const close = useCallback(() => {
    clearTimers();
    if (activeTooltip?.id === tooltipId) activeTooltip = null;
    if (reasonRef.current === null) return;
    reasonRef.current = null;
    lastClosedAt = Date.now();
    setOpen(false);
    setPosition(null);
  }, [clearTimers, tooltipId]);

  const show = useCallback(
    (reason: OpenReason, skipAnimation = false) => {
      clearTimers();
      if (activeTooltip && activeTooltip.id !== tooltipId) activeTooltip.close();
      activeTooltip = { id: tooltipId, close };
      // A stronger reason (pinned > focus > hover) replaces a weaker one, never the reverse.
      const rank = { hover: 0, focus: 1, pinned: 2 } as const;
      if (reasonRef.current === null || rank[reason] > rank[reasonRef.current]) {
        reasonRef.current = reason;
      }
      if (!open) setInstant(skipAnimation);
      setOpen(true);
    },
    [clearTimers, close, open, tooltipId]
  );

  const scheduleOpen = useCallback(() => {
    if (openTimerRef.current !== null) window.clearTimeout(openTimerRef.current);
    if (closeTimerRef.current !== null) {
      window.clearTimeout(closeTimerRef.current);
      closeTimerRef.current = null;
    }
    if (reasonRef.current !== null) return;
    const warm = activeTooltip !== null || Date.now() - lastClosedAt < WARM_WINDOW;
    if (warm) {
      show('hover', true);
      return;
    }
    openTimerRef.current = window.setTimeout(() => show('hover'), delay);
  }, [delay, show]);

  const scheduleClose = useCallback(() => {
    if (openTimerRef.current !== null) {
      window.clearTimeout(openTimerRef.current);
      openTimerRef.current = null;
    }
    if (reasonRef.current !== 'hover') return;
    if (closeTimerRef.current !== null) window.clearTimeout(closeTimerRef.current);
    closeTimerRef.current = window.setTimeout(close, CLOSE_GRACE);
  }, [close]);

  const cancelClose = useCallback(() => {
    if (closeTimerRef.current !== null) window.clearTimeout(closeTimerRef.current);
    closeTimerRef.current = null;
  }, []);

  const reposition = useCallback(() => {
    const trigger = triggerRef.current;
    const tooltip = tooltipRef.current;
    if (!trigger || !tooltip) return;
    setPosition(
      resolveTooltipPosition(
        trigger.getBoundingClientRect(),
        { width: tooltip.offsetWidth, height: tooltip.offsetHeight },
        { width: window.innerWidth, height: window.innerHeight }
      )
    );
  }, []);

  // Measure and place before paint, so the first visible frame is already positioned.
  useLayoutEffect(() => {
    if (open) reposition();
  }, [open, reposition, content]);

  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      // Capture phase on window: runs before the Sheet/Modal document listeners, so the
      // first Escape dismisses only the tooltip.
      event.preventDefault();
      event.stopPropagation();
      close();
    };
    const handlePointerDown = (event: globalThis.PointerEvent) => {
      const target = event.target as Node | null;
      if (target && (triggerRef.current?.contains(target) || tooltipRef.current?.contains(target)))
        return;
      close();
    };
    const handleScroll = (event: Event) => {
      if (event.target instanceof Node && tooltipRef.current?.contains(event.target)) return;
      // Focus-opened tooltips follow their trigger: tabbing to an off-screen trigger scrolls
      // it into view right after the focus event, which must not dismiss the tooltip.
      if (reasonRef.current === 'focus') reposition();
      else close();
    };
    window.addEventListener('keydown', handleKeyDown, true);
    document.addEventListener('pointerdown', handlePointerDown, true);
    window.addEventListener('scroll', handleScroll, true);
    window.addEventListener('resize', reposition);
    return () => {
      window.removeEventListener('keydown', handleKeyDown, true);
      document.removeEventListener('pointerdown', handlePointerDown, true);
      window.removeEventListener('scroll', handleScroll, true);
      window.removeEventListener('resize', reposition);
    };
  }, [close, open, reposition]);

  useEffect(
    () => () => {
      clearTimers();
      if (activeTooltip?.id === tooltipId) activeTooltip = null;
    },
    [clearTimers, tooltipId]
  );

  const triggerHandlers = {
    onPointerEnter: (event: PointerEvent<HTMLElement>) => {
      if (event.pointerType === 'touch') return;
      scheduleOpen();
    },
    onPointerLeave: (event: PointerEvent<HTMLElement>) => {
      if (event.pointerType === 'touch') return;
      scheduleClose();
    },
    onFocus: (event: FocusEvent<HTMLElement>) => {
      // Mouse/touch focus (not :focus-visible) is handled by hover and click instead, which
      // also keeps programmatic focus on dialog open from popping a tooltip.
      if (isFocusVisible(event.currentTarget)) show('focus');
    },
    onBlur: (event: FocusEvent<HTMLElement>) => {
      const next = event.relatedTarget as Node | null;
      if (pressingBubbleRef.current || (next && tooltipRef.current?.contains(next))) return;
      close();
    },
  };

  const bubble = open ? (
    <div
      ref={tooltipRef}
      id={tooltipId}
      role="tooltip"
      className={styles.tooltip}
      data-placement={position?.placement}
      data-measuring={position ? undefined : ''}
      data-instant={instant ? '' : undefined}
      style={position ? { top: position.top, left: position.left } : undefined}
      onPointerEnter={cancelClose}
      onPointerDown={() => {
        // Pressing non-focusable bubble content blurs the trigger; that must not dismiss it.
        pressingBubbleRef.current = true;
        window.setTimeout(() => {
          pressingBubbleRef.current = false;
        }, 0);
      }}
      onPointerLeave={(event) => {
        if (event.pointerType !== 'touch') scheduleClose();
      }}
    >
      {content}
    </div>
  ) : null;

  const portal =
    bubble && (typeof document === 'undefined' ? bubble : createPortal(bubble, document.body));

  return { open, tooltipId, triggerRef, reasonRef, triggerHandlers, show, close, portal };
}

/* ------------------------------------------------------------------------------------- */

type TriggerProps = {
  ref?: Ref<HTMLElement>;
  'aria-describedby'?: string;
  onPointerEnter?: (event: PointerEvent<HTMLElement>) => void;
  onPointerLeave?: (event: PointerEvent<HTMLElement>) => void;
  onPointerDown?: (event: PointerEvent<HTMLElement>) => void;
  onFocus?: (event: FocusEvent<HTMLElement>) => void;
  onBlur?: (event: FocusEvent<HTMLElement>) => void;
};

function assignRef<T>(ref: Ref<T> | undefined, value: T | null) {
  if (typeof ref === 'function') ref(value);
  else if (ref) (ref as { current: T | null }).current = value;
}

export type TooltipProps = {
  content: ReactNode;
  children: ReactElement<TriggerProps>;
  /** Hover delay in ms. Keyboard focus opens immediately regardless. */
  delay?: number;
};

export function Tooltip({ content, children, delay = OPEN_DELAY }: TooltipProps) {
  const tooltip = useTooltipController(content, delay);
  const { triggerRef, triggerHandlers, close } = tooltip;
  const childProps = children.props;
  const childRef = childProps.ref;

  const setRef = useCallback(
    (node: HTMLElement | null) => {
      triggerRef.current = node;
      assignRef(childRef, node);
    },
    [childRef, triggerRef]
  );

  if (!hasContent(content)) return children;

  const describedBy =
    [childProps['aria-describedby'], tooltip.open ? tooltip.tooltipId : '']
      .filter(Boolean)
      .join(' ') || undefined;

  // Re-render the child element through JSX (equivalent to cloneElement: same type, its own
  // props, with the trigger wiring layered on top and its own handlers still called first).
  const Trigger = children.type as ElementType;
  return (
    <>
      <Trigger
        key={children.key ?? undefined}
        {...childProps}
        ref={setRef}
        aria-describedby={describedBy}
        onPointerEnter={(event: PointerEvent<HTMLElement>) => {
          childProps.onPointerEnter?.(event);
          triggerHandlers.onPointerEnter(event);
        }}
        onPointerLeave={(event: PointerEvent<HTMLElement>) => {
          childProps.onPointerLeave?.(event);
          triggerHandlers.onPointerLeave(event);
        }}
        onPointerDown={(event: PointerEvent<HTMLElement>) => {
          childProps.onPointerDown?.(event);
          // Pressing the trigger performs its own action; get out of the way.
          close();
        }}
        onFocus={(event: FocusEvent<HTMLElement>) => {
          childProps.onFocus?.(event);
          triggerHandlers.onFocus(event);
        }}
        onBlur={(event: FocusEvent<HTMLElement>) => {
          childProps.onBlur?.(event);
          triggerHandlers.onBlur(event);
        }}
      />
      {tooltip.portal}
    </>
  );
}

/* ------------------------------------------------------------------------------------- */

export type InfoTipProps = {
  content: ReactNode;
  /** Visible label being explained; used for the accessible name. */
  label?: string;
  /** Id of an existing element that already carries `content` for assistive tech. */
  descriptionId?: string;
  className?: string;
};

export function InfoTip({ content, label, descriptionId, className }: InfoTipProps) {
  const { t } = useTranslation();
  const ownDescriptionId = useId();
  const tooltip = useTooltipController(content, OPEN_DELAY);
  const { triggerRef, triggerHandlers, reasonRef, show, close } = tooltip;

  const setRef = useCallback(
    (node: HTMLButtonElement | null) => {
      triggerRef.current = node;
    },
    [triggerRef]
  );

  if (!hasContent(content)) return null;

  const handleClick = (event: MouseEvent<HTMLButtonElement>) => {
    // The button sits next to labels/rows that may react to clicks; keep it to itself.
    event.stopPropagation();
    event.preventDefault();
    // Hover- or focus-opened: the click pins it. Already pinned: the click closes it.
    if (reasonRef.current === 'pinned') close();
    else show('pinned');
  };

  return (
    <>
      <button
        ref={setRef}
        type="button"
        className={[styles.infoTip, className].filter(Boolean).join(' ') || undefined}
        // Stable hook for tests and context styling (CSS-module class names are hashed).
        data-info-tip=""
        aria-label={label ? t('common.more_info_about', { label }) : t('common.more_info')}
        aria-describedby={descriptionId ?? ownDescriptionId}
        data-state={tooltip.open ? 'open' : 'closed'}
        onClick={handleClick}
        {...triggerHandlers}
      >
        <IconInfo size={14} />
      </button>
      {descriptionId ? null : (
        // `hidden` keeps it out of layout and the reading order; aria-describedby still
        // resolves hidden elements it references directly.
        <span id={ownDescriptionId} hidden>
          {content}
        </span>
      )}
      {tooltip.portal}
    </>
  );
}
