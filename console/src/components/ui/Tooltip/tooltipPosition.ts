/**
 * Tooltip placement maths, kept free of the DOM so it can be unit tested
 * (same approach as `selectPosition.ts` for the Select dropdown).
 *
 * Rules:
 * - Prefer the side above the trigger; flip below when the tooltip does not fit above
 *   and there is more room below. When neither side fits, take the roomier side and
 *   clamp into the viewport (it may then overlap the trigger rather than leave the screen).
 * - Centre horizontally on the trigger, then clamp so the tooltip keeps
 *   `TOOLTIP_VIEWPORT_MARGIN` from both viewport edges.
 */

export type TooltipPlacement = 'top' | 'bottom';

export type TooltipPosition = {
  placement: TooltipPlacement;
  /** Viewport coordinates for `position: fixed`. */
  top: number;
  left: number;
};

type TriggerRect = Pick<DOMRect, 'left' | 'top' | 'bottom' | 'width'>;
type Size = { width: number; height: number };

/** Gap between the trigger and the tooltip. */
export const TOOLTIP_OFFSET = 6;
/** Minimum distance kept from every viewport edge. */
export const TOOLTIP_VIEWPORT_MARGIN = 8;

const clamp = (value: number, min: number, max: number) =>
  Math.min(Math.max(value, min), Math.max(min, max));

export const resolveTooltipPosition = (
  trigger: TriggerRect,
  tooltip: Size,
  viewport: Size,
  offset = TOOLTIP_OFFSET,
  margin = TOOLTIP_VIEWPORT_MARGIN
): TooltipPosition => {
  const spaceAbove = trigger.top - offset - margin;
  const spaceBelow = viewport.height - trigger.bottom - offset - margin;
  const fitsAbove = tooltip.height <= spaceAbove;
  const fitsBelow = tooltip.height <= spaceBelow;
  const placement: TooltipPlacement =
    fitsAbove || (!fitsBelow && spaceAbove >= spaceBelow) ? 'top' : 'bottom';

  const preferredTop =
    placement === 'top' ? trigger.top - offset - tooltip.height : trigger.bottom + offset;
  const top = clamp(preferredTop, margin, viewport.height - margin - tooltip.height);

  const centre = trigger.left + trigger.width / 2;
  const left = clamp(centre - tooltip.width / 2, margin, viewport.width - margin - tooltip.width);

  return { placement, top, left };
};
