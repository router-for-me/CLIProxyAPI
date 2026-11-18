// 提示气泡定位：优先在触发器上方，放不下翻到下方，水平居中并夹在视口内。

import { describe, expect, test } from 'bun:test';
import {
  TOOLTIP_OFFSET,
  TOOLTIP_VIEWPORT_MARGIN,
  resolveTooltipPosition,
} from '@/components/ui/Tooltip/tooltipPosition';

const viewport = { width: 1024, height: 768 };
const rect = (left: number, top: number, width = 16, height = 16) => ({
  left,
  top,
  width,
  bottom: top + height,
});
const tooltip = { width: 200, height: 60 };

describe('resolveTooltipPosition', () => {
  test('sits above the trigger, centred on it, when there is room', () => {
    const trigger = rect(500, 400);
    expect(resolveTooltipPosition(trigger, tooltip, viewport)).toEqual({
      placement: 'top',
      top: 400 - TOOLTIP_OFFSET - 60,
      left: 508 - 100,
    });
  });

  test('flips below when the tooltip does not fit above', () => {
    const trigger = rect(500, 40);
    expect(resolveTooltipPosition(trigger, tooltip, viewport)).toEqual({
      placement: 'bottom',
      top: 56 + TOOLTIP_OFFSET,
      left: 408,
    });
  });

  test('stays above at the exact fitting boundary', () => {
    const top = TOOLTIP_VIEWPORT_MARGIN + TOOLTIP_OFFSET + tooltip.height;
    const position = resolveTooltipPosition(rect(500, top), tooltip, viewport);
    expect(position.placement).toBe('top');
    expect(position.top).toBe(TOOLTIP_VIEWPORT_MARGIN);
  });

  test('takes the roomier side and clamps into the viewport when neither side fits', () => {
    const short = { width: 1024, height: 120 };
    const tall = { width: 200, height: 100 };
    // 50px above, 50px below: neither fits; ties keep the preferred side, clamped to the margin.
    const tied = resolveTooltipPosition(rect(500, 52), tall, short);
    expect(tied.placement).toBe('top');
    expect(tied.top).toBe(TOOLTIP_VIEWPORT_MARGIN);
    // More room below: flips and is clamped so its bottom keeps the margin.
    const lower = resolveTooltipPosition(rect(500, 30), tall, short);
    expect(lower.placement).toBe('bottom');
    expect(lower.top).toBe(120 - TOOLTIP_VIEWPORT_MARGIN - 100);
  });

  test('clamps to the left edge', () => {
    const position = resolveTooltipPosition(rect(2, 400), tooltip, viewport);
    expect(position.left).toBe(TOOLTIP_VIEWPORT_MARGIN);
    expect(position.placement).toBe('top');
  });

  test('clamps to the right edge', () => {
    const position = resolveTooltipPosition(rect(1010, 400), tooltip, viewport);
    expect(position.left).toBe(1024 - TOOLTIP_VIEWPORT_MARGIN - 200);
  });

  test('centres on a trigger that is wider than the tooltip', () => {
    const trigger = rect(100, 400, 600, 36);
    const position = resolveTooltipPosition(trigger, tooltip, viewport);
    expect(position.left).toBe(400 - 100);
    expect(position.left).toBeGreaterThan(trigger.left);
    expect(position.left + tooltip.width).toBeLessThan(trigger.left + trigger.width);
  });

  test('pins to the margin when the tooltip is wider than the viewport', () => {
    const position = resolveTooltipPosition(
      rect(100, 400),
      { width: 400, height: 40 },
      {
        width: 320,
        height: 640,
      }
    );
    expect(position.left).toBe(TOOLTIP_VIEWPORT_MARGIN);
  });
});
