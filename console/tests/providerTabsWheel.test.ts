import { describe, expect, test } from 'bun:test';
import { scrollProviderTabs } from '@/features/authFiles/components/providerTabsWheel';

function wheel(overrides: Partial<WheelEvent> = {}) {
  let prevented = false;
  return {
    deltaX: 0,
    deltaY: 80,
    deltaMode: 0,
    ctrlKey: false,
    defaultPrevented: false,
    ...overrides,
    preventDefault: () => {
      prevented = true;
    },
    wasPrevented: () => prevented,
  };
}
const strip = () => ({ scrollWidth: 900, clientWidth: 400, scrollLeft: 100 });

describe('provider tabs wheel scrolling', () => {
  test('scrolls right on down and left on up without scrolling the page', () => {
    const target = strip();
    const down = wheel();
    scrollProviderTabs(target, down);
    expect(target.scrollLeft).toBe(180);
    expect(down.wasPrevented()).toBe(true);
    scrollProviderTabs(target, wheel({ deltaY: -60 }));
    expect(target.scrollLeft).toBe(120);
  });

  test('clamps both ends and still consumes vertical scrolling', () => {
    const target = strip();
    for (const [deltaY, expected] of [
      [1000, 500],
      [-1000, 0],
    ]) {
      const event = wheel({ deltaY });
      scrollProviderTabs(target, event);
      expect(target.scrollLeft).toBe(expected);
      expect(event.wasPrevented()).toBe(true);
    }
  });

  test('converts line and page wheel units', () => {
    const target = strip();
    scrollProviderTabs(target, wheel({ deltaY: 2, deltaMode: 1 }));
    expect(target.scrollLeft).toBe(132);
    scrollProviderTabs(target, wheel({ deltaY: 1, deltaMode: 2 }));
    expect(target.scrollLeft).toBe(500);
  });

  test('preserves zoom, horizontal gestures and already-handled events', () => {
    for (const overrides of [
      { ctrlKey: true },
      { deltaX: 120, deltaY: 20 },
      { deltaX: 80, deltaY: 0 },
      { defaultPrevented: true },
    ]) {
      const target = strip();
      const event = wheel(overrides);
      scrollProviderTabs(target, event);
      expect(target.scrollLeft).toBe(100);
      expect(event.wasPrevented()).toBe(false);
    }
  });

  test('does not trap page scrolling when all providers fit', () => {
    const target = { scrollWidth: 400, clientWidth: 400, scrollLeft: 0 };
    const event = wheel();
    scrollProviderTabs(target, event);
    expect(target.scrollLeft).toBe(0);
    expect(event.wasPrevented()).toBe(false);
  });
});
