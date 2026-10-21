import { describe, expect, test } from 'bun:test';
import {
  countPendingLogLines,
  findVisibleLogAnchor,
  getLogAnchorAdjustment,
} from '../src/features/logs/hooks/useLogScroller';

describe('log scroll anchors', () => {
  test('captures a partially visible row using its original, sparse stream ID', () => {
    expect(
      findVisibleLogAnchor([
        { id: 101, offset: -40, bottom: 0 },
        { id: 207, offset: -8, bottom: 12 },
        { id: 509, offset: 12, bottom: 32 },
      ])
    ).toEqual({ id: 207, offset: -8 });
  });

  test('preserves the anchor offset after prepending filtered history', () => {
    expect(
      getLogAnchorAdjustment({ id: 509, offset: -8 }, [
        { id: 101, offset: 0, bottom: 20 },
        { id: 207, offset: 20, bottom: 40 },
        { id: 509, offset: 40, bottom: 60 },
      ])
    ).toBe(48);
  });

  test('corrects upward displacement after older buffered rows are evicted', () => {
    expect(
      getLogAnchorAdjustment({ id: 509, offset: -8 }, [{ id: 509, offset: -208, bottom: -188 }])
    ).toBe(-200);
  });

  test('distinguishes an evicted anchor from an unchanged anchor', () => {
    const rows = [{ id: 509, offset: -8, bottom: 12 }];
    expect(getLogAnchorAdjustment({ id: 207, offset: -8 }, rows)).toBeNull();
    expect(getLogAnchorAdjustment({ id: 509, offset: -8 }, rows)).toBe(0);
    expect(findVisibleLogAnchor([])).toBeNull();
  });
});

describe('pending filtered log lines', () => {
  test('counts matching new rows, not gaps between raw IDs', () => {
    expect(countPendingLogLines([101, 207, 509, 900], 207)).toBe(2);
  });

  test('prepending history and repeated snapshots do not count as new rows', () => {
    expect(countPendingLogLines([1, 40, 101, 207], 207)).toBe(0);
    expect(countPendingLogLines([207, 509, 900], 900)).toBe(0);
  });

  test('counts arrivals even when a bounded buffer stays the same size', () => {
    expect(countPendingLogLines([207, 509], 207)).toBe(1);
  });

  test('an empty or initial snapshot has no pending lines', () => {
    expect(countPendingLogLines([], 207)).toBe(0);
    expect(countPendingLogLines([101, 207], null)).toBe(0);
  });
});
