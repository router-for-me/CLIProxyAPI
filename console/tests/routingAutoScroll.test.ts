import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import {
  AUTO_SCROLL_EDGE,
  AUTO_SCROLL_MAX_SPEED,
  edgeScrollVelocity,
} from '../src/features/routing/useDragAutoScroll';

describe('routing drag auto-scroll', () => {
  test('rests in the middle and scrolls toward the edge the pointer approaches', () => {
    expect(edgeScrollVelocity(500, 0, 1000)).toBe(0);
    expect(edgeScrollVelocity(AUTO_SCROLL_EDGE, 0, 1000)).toBe(0);
    expect(edgeScrollVelocity(20, 0, 1000)).toBeLessThan(0);
    expect(edgeScrollVelocity(980, 0, 1000)).toBeGreaterThan(0);
  });

  test('ramps up toward the edge and caps at the maximum, even past it', () => {
    const near = Math.abs(edgeScrollVelocity(60, 0, 1000));
    const nearer = Math.abs(edgeScrollVelocity(20, 0, 1000));
    expect(nearer).toBeGreaterThan(near);
    expect(edgeScrollVelocity(0, 0, 1000)).toBe(-AUTO_SCROLL_MAX_SPEED);
    expect(edgeScrollVelocity(-40, 0, 1000)).toBe(-AUTO_SCROLL_MAX_SPEED);
    expect(edgeScrollVelocity(1200, 0, 1000)).toBe(AUTO_SCROLL_MAX_SPEED);
  });

  test('measures from the visible band of an offset scroll container', () => {
    expect(edgeScrollVelocity(110, 100, 900)).toBeLessThan(0);
    expect(edgeScrollVelocity(500, 100, 900)).toBe(0);
  });

  test('short viewports shrink the zones so they never overlap', () => {
    expect(edgeScrollVelocity(60, 0, 120)).toBe(0);
    expect(edgeScrollVelocity(0, 0, 0)).toBe(0);
  });

  test('the tier ladder drives auto-scroll while a card is dragged', () => {
    const ladder = readFileSync('src/features/routing/components/TierLadder.tsx', 'utf8');
    expect(ladder).toContain('useDragAutoScroll(dragKey !== null, ladderRef);');
    expect(ladder).toContain('<ol ref={ladderRef}');
  });
});
