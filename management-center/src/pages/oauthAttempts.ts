export interface OAuthAttempt {
  readonly signal: AbortSignal;
  isCurrent: () => boolean;
  invalidate: () => void;
  schedule: (callback: () => void, delay: number) => void;
  poll: <T>(
    request: () => Promise<T>,
    onResult: (result: T) => boolean,
    onError: (error: unknown) => void,
    delay: number
  ) => void;
}

interface Scheduler {
  setTimeout: (callback: () => void, delay: number) => number;
  clearTimeout: (timer: number) => void;
}

// A provider owns one attempt and at most one scheduled task. Invalidating an
// attempt also makes already-dispatched requests inert when they settle.
export function createOAuthAttempts(scheduler: Scheduler) {
  const attempts = new Map<string, OAuthAttempt>();

  const begin = (provider: string): OAuthAttempt => {
    attempts.get(provider)?.invalidate();
    let timer: number | undefined;
    const controller = new AbortController();
    const attempt: OAuthAttempt = {
      signal: controller.signal,
      isCurrent: () => attempts.get(provider) === attempt,
      invalidate: () => {
        if (timer !== undefined) scheduler.clearTimeout(timer);
        timer = undefined;
        if (attempt.isCurrent()) attempts.delete(provider);
        controller.abort();
      },
      schedule: (callback, delay) => {
        if (!attempt.isCurrent()) return;
        if (timer !== undefined) scheduler.clearTimeout(timer);
        timer = scheduler.setTimeout(() => {
          timer = undefined;
          if (attempt.isCurrent()) callback();
        }, delay);
      },
      poll: (request, onResult, onError, delay) => {
        const tick = async () => {
          try {
            const result = await request();
            if (!attempt.isCurrent()) return;
            if (onResult(result)) attempt.schedule(() => void tick(), delay);
          } catch (error: unknown) {
            if (attempt.isCurrent()) onError(error);
          }
        };
        attempt.schedule(() => void tick(), delay);
      },
    };
    attempts.set(provider, attempt);
    return attempt;
  };

  return {
    begin,
    get: (provider: string) => attempts.get(provider),
    invalidateAll: () => {
      for (const attempt of attempts.values()) attempt.invalidate();
    },
  };
}
