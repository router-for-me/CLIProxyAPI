import { describe, expect, test } from 'bun:test';
import { resolveDropdownStyle } from '@/components/ui/selectPosition';

describe('Select dropdown positioning', () => {
  const viewport = { width: 390, height: 844 };
  const trigger = { left: 300, right: 366, top: 19, bottom: 51, width: 66 };

  test('keeps the trigger width when no minimum is requested', () => {
    const style = resolveDropdownStyle(trigger, viewport.width, viewport.height);
    expect(style.width).toBe(66);
    expect(style.left).toBe(300);
  });

  test('widens to the minimum and right-aligns with a trigger near the right edge', () => {
    const style = resolveDropdownStyle(trigger, viewport.width, viewport.height, 180);
    expect(style.width).toBe(180);
    expect(style.left).toBe(366 - 180);
  });

  test('never exceeds the viewport', () => {
    const style = resolveDropdownStyle(trigger, 160, viewport.height, 180);
    expect(style.width).toBe(160 - 16);
    expect(style.left).toBe(8);
  });

  test('left-aligns when there is room to the right', () => {
    const left = { left: 24, right: 90, top: 19, bottom: 51, width: 66 };
    const style = resolveDropdownStyle(left, viewport.width, viewport.height, 180);
    expect(style.left).toBe(24);
    expect(style.width).toBe(180);
  });
});
