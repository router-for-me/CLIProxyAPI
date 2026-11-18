/**
 * Runway layout: where each account sits on a 7-day axis of weekly resets.
 *
 * Pure so the placement rules can be tested. Positions are fractions of the
 * axis (0 = now, 1 = seven days out); accounts whose reset is unknown go to a
 * separate gutter after the axis.
 */

import type { LedgerRow } from './ledgerModel';

export const RUNWAY_SPAN_MS = 7 * 24 * 3_600_000;

/** Markers closer than this fraction of the span stack onto separate rows. */
const MIN_GAP = 0.03;

export interface RunwayMarker {
  row: LedgerRow;
  /** 0..1 along the axis, or null for the unknown gutter. */
  x: number | null;
  /** Stacking row inside the lane, 0 = top. */
  level: number;
}

export interface RunwayTick {
  x: number;
  atMs: number;
}

/** Place every row that has a primary window; rows still loading are left out. */
export function placeRunwayMarkers(rows: LedgerRow[], nowMs: number): RunwayMarker[] {
  const known: RunwayMarker[] = [];
  const unknown: RunwayMarker[] = [];
  for (const row of rows) {
    if (!row.primary) continue;
    const at = row.primary.resetAtMs;
    if (at === null || at <= nowMs) {
      unknown.push({ row, x: null, level: unknown.length });
      continue;
    }
    known.push({ row, x: Math.min(1, (at - nowMs) / RUNWAY_SPAN_MS), level: 0 });
  }
  known.sort((a, b) => (a.x as number) - (b.x as number));
  const lastXByLevel: number[] = [];
  for (const marker of known) {
    let level = lastXByLevel.findIndex((lastX) => (marker.x as number) - lastX >= MIN_GAP);
    if (level === -1) level = lastXByLevel.length;
    lastXByLevel[level] = marker.x as number;
    marker.level = level;
  }
  return [...known, ...unknown];
}

/** Local midnights inside the span, for day gridlines and labels. */
export function runwayDayTicks(nowMs: number): RunwayTick[] {
  const ticks: RunwayTick[] = [];
  const day = new Date(nowMs);
  day.setHours(24, 0, 0, 0);
  while (day.getTime() < nowMs + RUNWAY_SPAN_MS) {
    const atMs = day.getTime();
    ticks.push({ x: (atMs - nowMs) / RUNWAY_SPAN_MS, atMs });
    day.setDate(day.getDate() + 1);
  }
  return ticks;
}
