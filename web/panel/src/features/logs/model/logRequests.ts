/** Feature-local ownership guards: invalidated responses must not mutate logs or UI state. */
export function createLogRequestGuard() {
  let generation = 0;

  return {
    capture: () => generation,
    invalidate: () => ++generation,
    isCurrent: (request: number) => request === generation,
  };
}

/** Serialize reads, coalesce full reloads, and let clear supersede an outstanding read. */
export function createLogRequestQueue() {
  const guard = createLogRequestGuard();
  let active: { id: number; clearing: boolean } | null = null;
  let pendingFullReload = false;

  const invalidate = () => {
    guard.invalidate();
    active = null;
    pendingFullReload = false;
  };

  return {
    invalidate,
    startRead(incremental: boolean): number | null {
      if (active) {
        if (!incremental) pendingFullReload = true;
        return null;
      }
      const id = guard.invalidate();
      active = { id, clearing: false };
      return id;
    },
    startClear(): number | null {
      if (active?.clearing) return null;
      invalidate();
      const id = guard.capture();
      active = { id, clearing: true };
      return id;
    },
    isCurrent: guard.isCurrent,
    finish(request: number, reload = false): boolean {
      if (active?.id !== request || !guard.isCurrent(request)) return false;
      active = null;
      const shouldReload = pendingFullReload || reload;
      pendingFullReload = false;
      return shouldReload;
    },
  };
}
