import { MAX_DRAW_POINTS_PER_PX } from '@/features/quota/pool/constants';
import type { RecordedSegment, XY } from '@/features/quota/pool/types';

type Scale = (value: number) => number;

const isRiserEnd = (points: XY[], index: number): boolean =>
  points[index - 1]?.t === points[index].t || points[index + 1]?.t === points[index].t;

const pickExtremes = (points: XY[], indexes: number[]): number[] => {
  const isPreferred = (candidate: number, best: number, difference: number): boolean =>
    difference < 0 ||
    (difference === 0 && isRiserEnd(points, candidate) && !isRiserEnd(points, best));
  let low = indexes[0];
  let high = indexes[0];

  for (const index of indexes) {
    if (isPreferred(index, low, points[index].v - points[low].v)) low = index;
    if (isPreferred(index, high, points[high].v - points[index].v)) high = index;
  }

  if (low === high) return [indexes[0], indexes[indexes.length - 1]];

  return [Math.min(low, high), Math.max(low, high)];
};

/**
 * Keeps at most `maxPerPx` points per pixel column (the column's minimum and
 * maximum, in time order). On ties riser endpoints win, so a column's riser
 * keeps both ends.
 */
export const downsampleForDraw = (
  points: XY[],
  x: Scale,
  maxPerPx: number = MAX_DRAW_POINTS_PER_PX
): XY[] => {
  const columns: number[][] = [];
  let previousColumn: number | null = null;

  for (const [index, point] of points.entries()) {
    const column = Math.floor(x(point.t));

    if (column === previousColumn) {
      columns[columns.length - 1].push(index);
    } else {
      columns.push([index]);
      previousColumn = column;
    }
  }

  return columns.flatMap((indexes) =>
    (indexes.length <= maxPerPx ? indexes : pickExtremes(points, indexes)).map(
      (index) => points[index]
    )
  );
};

export const timeScale =
  (start: number, end: number, x0: number, width: number): Scale =>
  (t) =>
    x0 + ((t - start) / (end - start)) * width;

export const valueScale =
  (yMax: number, top: number, height: number): Scale =>
  (v) =>
    top + height - (v / yMax) * height;

const round2 = (value: number): number => Math.round(value * 100) / 100;

/** "M x y L x y ..."; equal consecutive t draws a vertical riser. */
export const linePath = (points: XY[], x: Scale, y: Scale): string =>
  points
    .map((point, index) => `${index === 0 ? 'M' : 'L'} ${round2(x(point.t))} ${round2(y(point.v))}`)
    .join(' ');

/** The line closed down to `baseY`. */
export const areaPath = (points: XY[], x: Scale, y: Scale, baseY: number): string => {
  if (points.length === 0) return '';

  const first = round2(x(points[0].t));
  const last = round2(x(points[points.length - 1].t));
  const base = round2(baseY);

  return `${linePath(points, x, y)} L ${last} ${base} L ${first} ${base} Z`;
};

/** Smallest {1, 2, 2.5, 5} x 10^k that is >= n; 1 when n <= 0. */
export const niceCeil = (n: number): number => {
  if (n <= 0) return 1;

  const exponent = Math.floor(Math.log10(n));
  const candidates = [1, 2, 2.5, 5, 10].map((mantissa) =>
    Number((mantissa * 10 ** exponent).toPrecision(12))
  );

  return candidates.find((candidate) => candidate >= n) ?? candidates[candidates.length - 1];
};

/** Index of the last point with point.t <= t, or -1. */
const lastIndexAtOrBefore = (points: XY[], t: number): number => {
  let low = 0;
  let high = points.length;

  while (low < high) {
    const middle = (low + high) >>> 1;

    if (points[middle].t <= t) {
      low = middle + 1;
    } else {
      high = middle;
    }
  }

  return low - 1;
};

/** Linear interpolation; at a riser the later point wins; null outside the points. */
export const valueAt = (points: XY[], t: number): number | null => {
  const index = lastIndexAtOrBefore(points, t);

  if (index < 0 || t > points[points.length - 1].t) return null;

  const left = points[index];
  const right = points[index + 1];

  if (left.t === t) return left.v;

  return left.v + ((right.v - left.v) * (t - left.t)) / (right.t - left.t);
};

/** The value of the last segment that spans t. */
export const segmentValueAt = (
  segments: RecordedSegment[],
  t: number
): { known: number; partial: boolean; v: number } | null => {
  for (const segment of [...segments].reverse()) {
    const v = valueAt(segment.points, t);

    if (v !== null) return { known: segment.known, partial: segment.partial, v };
  }

  return null;
};
