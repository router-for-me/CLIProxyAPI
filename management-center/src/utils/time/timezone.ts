/**
 * The viewer's UTC offset, as a short label.
 *
 * Timestamps in this app are rendered in the browser's local timezone, which
 * is invisible until it matters — and it matters most for the one number a
 * user might act on. Stating the offset next to those timestamps costs six
 * characters and removes the guess.
 */

/**
 * `GMT`, `GMT+8`, `GMT-5`, `GMT+5:30`.
 *
 * @param offsetMinutes minutes EAST of UTC — i.e. the negation of
 * `Date.prototype.getTimezoneOffset`, which counts westward.
 */
export function formatUtcOffsetLabel(offsetMinutes: number): string {
  if (!Number.isFinite(offsetMinutes) || offsetMinutes === 0) return 'GMT';

  const sign = offsetMinutes < 0 ? '-' : '+';
  const abs = Math.abs(Math.trunc(offsetMinutes));
  const hours = Math.floor(abs / 60);
  const minutes = abs % 60;

  return minutes === 0
    ? `GMT${sign}${hours}`
    : `GMT${sign}${hours}:${String(minutes).padStart(2, '0')}`;
}

/**
 * Short offset label for the browser's timezone at `date`.
 *
 * Deliberately arithmetic rather than `Intl.DateTimeFormat`'s
 * `timeZoneName: 'shortOffset'`: that is ES2022-only, needs its locale pinned
 * to keep the label ASCII in all four UI languages, and disagrees with the
 * offset here at UTC itself (it emits `GMT+0`, this emits `GMT`). Since we want
 * an offset rather than a zone name like `PST`, Intl adds a second source of
 * truth and nothing else.
 *
 * Takes a date because the offset is not constant — a zone observing DST
 * answers differently in January than in July.
 */
export function resolveTimeZoneLabel(date: Date = new Date()): string {
  return formatUtcOffsetLabel(-date.getTimezoneOffset());
}
