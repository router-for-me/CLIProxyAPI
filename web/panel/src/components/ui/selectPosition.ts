import type { CSSProperties } from 'react';

const VIEWPORT_MARGIN = 8;
const DROPDOWN_OFFSET = 6;
const DROPDOWN_MAX_HEIGHT = 240;
const DROPDOWN_Z_INDEX = 2010;

const clamp = (value: number, min: number, max: number) => Math.min(Math.max(value, min), max);

export const resolveDropdownStyle = (
  rect: Pick<DOMRect, 'left' | 'right' | 'top' | 'bottom' | 'width'>,
  viewportWidth: number,
  viewportHeight: number,
  minWidth = 0
): CSSProperties => {
  const width = Math.min(
    Math.max(rect.width, minWidth),
    Math.max(0, viewportWidth - VIEWPORT_MARGIN * 2)
  );
  const preferredLeft =
    rect.left + width > viewportWidth - VIEWPORT_MARGIN ? rect.right - width : rect.left;
  const left = clamp(
    preferredLeft,
    VIEWPORT_MARGIN,
    Math.max(VIEWPORT_MARGIN, viewportWidth - width - VIEWPORT_MARGIN)
  );
  const spaceBelow = viewportHeight - rect.bottom - VIEWPORT_MARGIN - DROPDOWN_OFFSET;
  const spaceAbove = rect.top - VIEWPORT_MARGIN - DROPDOWN_OFFSET;
  const direction = spaceBelow >= DROPDOWN_MAX_HEIGHT || spaceBelow >= spaceAbove ? 'down' : 'up';
  const maxHeight = Math.max(
    0,
    Math.min(DROPDOWN_MAX_HEIGHT, direction === 'down' ? spaceBelow : spaceAbove)
  );

  return direction === 'down'
    ? {
        position: 'fixed',
        top: rect.bottom + DROPDOWN_OFFSET,
        left,
        width,
        maxHeight,
        zIndex: DROPDOWN_Z_INDEX,
      }
    : {
        position: 'fixed',
        bottom: viewportHeight - rect.top + DROPDOWN_OFFSET,
        left,
        width,
        maxHeight,
        zIndex: DROPDOWN_Z_INDEX,
      };
};
