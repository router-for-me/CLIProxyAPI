interface TimerClock {
  now: () => number;
  schedule: (callback: () => void, delay: number) => unknown;
  cancel: (timer: unknown) => void;
}

const clock: TimerClock = {
  now: () => performance.now(),
  schedule: (callback, delay) => setTimeout(callback, delay),
  cancel: (timer) => clearTimeout(timer as ReturnType<typeof setTimeout>),
};

/** Count only readable time; hover, focus and hidden-tab pauses can overlap. */
export function createNotificationTimer(
  duration: number,
  onExpire: () => void,
  timing: TimerClock = clock
) {
  let remaining = duration;
  let startedAt = 0;
  let timer: unknown;
  let running = false;
  let disposed = false;
  const pauses = new Set<string>();

  function resume() {
    if (disposed || running || pauses.size || duration <= 0) return;
    startedAt = timing.now();
    running = true;
    timer = timing.schedule(
      () => {
        running = false;
        disposed = true;
        onExpire();
      },
      Math.max(0, remaining)
    );
  }

  resume();

  return {
    setPaused(reason: string, paused: boolean) {
      if (disposed) return;
      if (paused) {
        pauses.add(reason);
        if (running) {
          remaining -= timing.now() - startedAt;
          timing.cancel(timer);
          running = false;
        }
      } else {
        pauses.delete(reason);
        resume();
      }
    },
    dispose() {
      disposed = true;
      if (running) timing.cancel(timer);
      running = false;
    },
  };
}
