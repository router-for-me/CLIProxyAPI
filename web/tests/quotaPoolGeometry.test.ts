import { describe, expect, test } from 'bun:test';
import {
  areaPath,
  downsampleForDraw,
  linePath,
  niceCeil,
  segmentValueAt,
  timeScale,
  valueAt,
  valueScale,
} from '@/features/quota/pool/geometry';
import { estimateLabelWidth, placeLabels } from '@/features/quota/pool/labels';
import type { RecordedSegment, XY } from '@/features/quota/pool/types';

const identity = (value: number) => value;

describe('paths', () => {
  const steps: XY[] = [
    { t: 0, v: 10 },
    { t: 5, v: 10 },
    { t: 5, v: 30 },
    { t: 10, v: 30 },
  ];

  test('linePath draws a vertical segment at a duplicate t', () => {
    expect(linePath(steps, identity, identity)).toBe('M 0 10 L 5 10 L 5 30 L 10 30');
  });

  test('areaPath closes down to the baseline', () => {
    expect(areaPath(steps, identity, identity, 0)).toBe(
      'M 0 10 L 5 10 L 5 30 L 10 30 L 10 0 L 0 0 Z'
    );
    expect(areaPath([], identity, identity, 0)).toBe('');
  });

  test('scales map time to x and value to y', () => {
    const x = timeScale(100, 200, 60, 1000);
    const y = valueScale(600, 30, 232);

    expect(x(100)).toBe(60);
    expect(x(150)).toBe(560);
    expect(y(600)).toBe(30);
    expect(y(0)).toBe(262);
  });
});

describe('niceCeil', () => {
  test('rounds up to 1, 2, 2.5 or 5 times a power of ten', () => {
    expect(niceCeil(0.3)).toBe(0.5);
    expect(niceCeil(3.7)).toBe(5);
    expect(niceCeil(6)).toBe(10);
    expect(niceCeil(0)).toBe(1);
    expect(niceCeil(-2)).toBe(1);
    expect(niceCeil(2.2)).toBe(2.5);
    expect(niceCeil(20)).toBe(20);
    expect(niceCeil(0.21)).toBe(0.25);
  });
});

describe('valueAt', () => {
  const points: XY[] = [
    { t: 0, v: 0 },
    { t: 10, v: 100 },
    { t: 10, v: 40 },
    { t: 20, v: 60 },
  ];

  test('interpolates linearly and takes the later point at a riser', () => {
    expect(valueAt(points, 5)).toBe(50);
    expect(valueAt(points, 10)).toBe(40);
    expect(valueAt(points, 15)).toBe(50);
    expect(valueAt(points, 20)).toBe(60);
  });

  test('is null outside the points', () => {
    expect(valueAt(points, -1)).toBeNull();
    expect(valueAt(points, 21)).toBeNull();
    expect(valueAt([], 0)).toBeNull();
  });

  test('segmentValueAt reads the segment spanning t, the later one at a shared boundary', () => {
    const segments: RecordedSegment[] = [
      { known: 5, partial: true, points: points.slice(0, 2), stale: false },
      {
        known: 6,
        partial: false,
        points: [
          { t: 10, v: 100 },
          { t: 30, v: 80 },
        ],
        stale: false,
      },
    ];

    expect(segmentValueAt(segments, 5)).toEqual({ known: 5, partial: true, v: 50 });
    expect(segmentValueAt(segments, 10)).toEqual({ known: 6, partial: false, v: 100 });
    expect(segmentValueAt(segments, 20)).toEqual({ known: 6, partial: false, v: 90 });
    expect(segmentValueAt(segments, 40)).toBeNull();
  });
});

describe('downsampleForDraw', () => {
  const columnOf = (point: XY) => Math.floor(point.t);

  test('keeps at most two points per pixel column', () => {
    const points = Array.from({ length: 100 }, (_, index) => ({
      t: index / 10,
      v: Math.sin(index),
    }));
    const kept = downsampleForDraw(points, identity);
    const perColumn = new Map<number, number>();

    for (const point of kept) {
      perColumn.set(columnOf(point), (perColumn.get(columnOf(point)) ?? 0) + 1);
    }

    expect(Math.max(...perColumn.values())).toBeLessThanOrEqual(2);
    expect(perColumn.size).toBe(10);
  });

  test('keeps both ends of a riser and the column extremes', () => {
    const points: XY[] = [
      { t: 3.1, v: 50 },
      { t: 3.2, v: 50 },
      { t: 3.5, v: 50 },
      { t: 3.5, v: 100 },
      { t: 3.8, v: 100 },
      { t: 4.2, v: 100 },
      { t: 4.4, v: 100 },
      { t: 4.4, v: 70 },
      { t: 4.6, v: 70 },
    ];

    expect(downsampleForDraw(points, identity)).toEqual([
      { t: 3.5, v: 50 },
      { t: 3.5, v: 100 },
      { t: 4.4, v: 100 },
      { t: 4.4, v: 70 },
    ]);
  });

  test('keeps the first and last point of a flat column', () => {
    const flat = [0.1, 0.4, 0.7].map((t) => ({ t, v: 5 }));

    expect(downsampleForDraw(flat, identity)).toEqual([flat[0], flat[2]]);
  });
});

describe('labels', () => {
  const bounds = { height: 100, width: 200, x: 0, y: 0 };
  const box = (id: string, priority: number, x: number, y = 10) => ({
    height: 14,
    id,
    priority,
    width: 40,
    x,
    y,
  });

  test('estimates width from the character count', () => {
    expect(estimateLabelWidth('+100%')).toBeCloseTo(5 * 0.58 * 12 + 6);
    expect(estimateLabelWidth('+1', 10)).toBeCloseTo(2 * 0.58 * 10 + 6);
  });

  test('places by priority, hides collisions and respects reserved rects', () => {
    const placed = placeLabels(
      [box('low', 1, 10), box('high', 9, 30), box('free', 5, 120), box('reserved', 8, 160)],
      [{ height: 20, width: 30, x: 170, y: 0 }],
      bounds
    );

    expect([...placed]).toEqual(['high', 'free']);
  });

  test('drops labels outside the bounds and tries alternatives that share an id', () => {
    const placed = placeLabels(
      [box('outside', 9, 180), box('step', 5, 10), box('step', 5, 10, 60), box('blocker', 7, 0)],
      [],
      bounds
    );

    expect([...placed]).toEqual(['blocker', 'step']);
  });
});
