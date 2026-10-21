import { describe, expect, test } from 'bun:test';
import { shouldExitLogFullscreen } from '../src/features/logs/model/logFullscreen';

describe('log fullscreen Escape handling', () => {
  test('an unhandled Escape exits fullscreen', () => {
    expect(shouldExitLogFullscreen({ key: 'Escape', defaultPrevented: false }, false)).toBe(true);
  });

  test('Escape consumed by a nested Select does not exit fullscreen', () => {
    expect(shouldExitLogFullscreen({ key: 'Escape', defaultPrevented: true }, false)).toBe(false);
    // After the Select closes, the next unhandled Escape can exit fullscreen.
    expect(shouldExitLogFullscreen({ key: 'Escape', defaultPrevented: false }, false)).toBe(true);
  });

  test('an open modal retains Escape priority', () => {
    for (const defaultPrevented of [false, true]) {
      expect(shouldExitLogFullscreen({ key: 'Escape', defaultPrevented }, true)).toBe(false);
    }
  });

  test('other keys never exit fullscreen', () => {
    for (const key of ['Enter', 'Tab', 'ArrowDown']) {
      expect(shouldExitLogFullscreen({ key, defaultPrevented: false }, false)).toBe(false);
    }
  });
});
