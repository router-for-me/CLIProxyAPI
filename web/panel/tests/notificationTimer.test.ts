import { describe, expect, test } from 'bun:test';
import { createNotificationTimer } from '@/components/common/notificationTimer';

function setup(duration = 3000) {
  let now = 0;
  let expired = 0;
  let nextId = 0;
  const tasks = new Map<number, { callback: () => void; at: number }>();
  const timer = createNotificationTimer(duration, () => expired++, {
    now: () => now,
    schedule(callback, delay) {
      const id = ++nextId;
      tasks.set(id, { callback, at: now + delay });
      return id;
    },
    cancel(id) {
      tasks.delete(id as number);
    },
  });
  return {
    timer,
    expired: () => expired,
    active: () => tasks.size,
    advance(ms: number) {
      now += ms;
      for (const [id, task] of tasks) {
        if (task.at <= now) {
          tasks.delete(id);
          task.callback();
        }
      }
    },
  };
}

describe('notification readable-time expiry', () => {
  test('expires exactly once after its duration', () => {
    const clock = setup();
    clock.advance(2999);
    expect(clock.expired()).toBe(0);
    clock.advance(1);
    expect(clock.expired()).toBe(1);
    clock.timer.setPaused('hover', false);
    clock.advance(5000);
    expect(clock.expired()).toBe(1);
    expect(clock.active()).toBe(0);
  });

  test('hover resumes the remaining time instead of restarting', () => {
    const clock = setup();
    clock.advance(1000);
    clock.timer.setPaused('hover', true);
    expect(clock.active()).toBe(0);
    clock.advance(10000);
    expect(clock.expired()).toBe(0);
    clock.timer.setPaused('hover', false);
    clock.advance(1999);
    expect(clock.expired()).toBe(0);
    clock.advance(1);
    expect(clock.expired()).toBe(1);
  });

  test('hover, focus and hidden-tab pauses must all end before resuming', () => {
    const clock = setup();
    clock.advance(500);
    for (const reason of ['hover', 'focus', 'hidden']) clock.timer.setPaused(reason, true);
    clock.timer.setPaused('hover', false);
    clock.timer.setPaused('hidden', false);
    clock.advance(10000);
    expect(clock.expired()).toBe(0);
    expect(clock.active()).toBe(0);
    clock.timer.setPaused('focus', false);
    clock.advance(2500);
    expect(clock.expired()).toBe(1);
  });

  test('duplicate events do not create additional timers or consume paused time', () => {
    const clock = setup();
    clock.timer.setPaused('hidden', false);
    expect(clock.active()).toBe(1);
    clock.advance(1000);
    clock.timer.setPaused('hover', true);
    clock.advance(1000);
    clock.timer.setPaused('hover', true);
    clock.timer.setPaused('hover', false);
    clock.timer.setPaused('hover', false);
    expect(clock.active()).toBe(1);
    clock.advance(1999);
    expect(clock.expired()).toBe(0);
    clock.advance(1);
    expect(clock.expired()).toBe(1);
  });

  test.each([0, -1])('duration %s stays until explicitly dismissed', (duration) => {
    const clock = setup(duration);
    clock.timer.setPaused('hover', true);
    clock.timer.setPaused('hover', false);
    clock.advance(100000);
    expect(clock.active()).toBe(0);
    expect(clock.expired()).toBe(0);
  });

  test('dismissal or unmount disposes the timer and prevents restarting', () => {
    const clock = setup();
    clock.timer.dispose();
    clock.timer.dispose();
    clock.timer.setPaused('hidden', false);
    clock.advance(10000);
    expect(clock.active()).toBe(0);
    expect(clock.expired()).toBe(0);
  });

  test('disposing while paused also prevents resuming', () => {
    const clock = setup();
    clock.timer.setPaused('focus', true);
    clock.timer.dispose();
    clock.timer.setPaused('focus', false);
    expect(clock.active()).toBe(0);
  });
});
