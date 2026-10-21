const MINUTE_MS = 60_000;

/**
 * 返回下一个可见倒计时文案发生变化的等待时间。
 *
 * 文案以向上取整的分钟展示，所以按最近的分钟边界唤醒即可；已经到期或
 * 无效的时间不再创建定时器。
 */
export function getNextAntigravityCountdownUpdateDelay(
  resetTimestamps: readonly number[],
  nowMs: number
): number | null {
  let nextDelay: number | null = null;

  resetTimestamps.forEach((resetMs) => {
    if (!Number.isFinite(resetMs)) return;
    const deltaMs = resetMs - nowMs;
    if (deltaMs <= 0) return;

    const remainder = deltaMs % MINUTE_MS;
    const delay = Math.max(1, Math.ceil(remainder === 0 ? MINUTE_MS : remainder));
    nextDelay = nextDelay === null ? delay : Math.min(nextDelay, delay);
  });

  return nextDelay;
}
