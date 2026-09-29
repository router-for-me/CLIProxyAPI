/**
 * Relative time beside absolute time.
 *
 * A quota card used to say `08-13 14:30` and leave the arithmetic to the
 * reader. The absolute instant is what you need to plan around; the relative
 * one ("in 11 days") is what you need to *react* to. Showing both costs one
 * short span and removes the mental subtraction, so every date on a card now
 * carries its own countdown.
 *
 * Pure and clock-free — `nowMs` is always passed in, so every case is directly
 * testable and the caller decides how often it ticks (see `useNow`).
 */

import { DAY_MS, HOUR_MS, MINUTE_MS } from '@/utils/time/durations';

export interface RelativeTimeParts {
  /** Signed magnitude: positive is future, negative is past. */
  value: number;
  unit: 'day' | 'hour' | 'minute';
}

/**
 * Coarsest unit that still describes the gap, with a signed magnitude.
 *
 * Signed on purpose: the earlier timeline-local version clamped with
 * `Math.max(0, …)`, so a credit that expired last week read "in 1 minute" —
 * technically monotonic, actively misleading.
 *
 * Truncated, not rounded up, for two reasons. It is the ordinary countdown
 * convention ("2 hours left" holds from 2:59 down to 2:00), and rounding up
 * crosses the unit thresholds: `DAY_MS - 1` picks the hour unit and then
 * ceils to "24 hours", `HOUR_MS - 1` to "60 minutes". For a deadline it also
 * errs in the safe direction — never claiming more time than remains.
 *
 * Sub-minute gaps floor to a magnitude of 1 so nothing renders "in 0 minutes".
 */
export function relativeTimeParts(targetMs: number, nowMs: number): RelativeTimeParts {
  const delta = targetMs - nowMs;
  const sign = delta < 0 ? -1 : 1;
  const abs = Math.abs(delta);

  if (abs >= DAY_MS) return { value: sign * Math.floor(abs / DAY_MS), unit: 'day' };
  if (abs >= HOUR_MS) return { value: sign * Math.floor(abs / HOUR_MS), unit: 'hour' };
  return { value: sign * Math.max(1, Math.floor(abs / MINUTE_MS)), unit: 'minute' };
}

/**
 * Formatters are cached per locale: this runs once per quota row per minute,
 * and constructing an Intl formatter is the expensive part.
 */
const relativeFormatters = new Map<string, Intl.RelativeTimeFormat>();

function getRelativeFormatter(locale?: string): Intl.RelativeTimeFormat {
  const key = locale ?? '';
  const cached = relativeFormatters.get(key);
  if (cached) return cached;

  let formatter: Intl.RelativeTimeFormat;
  try {
    formatter = new Intl.RelativeTimeFormat(locale, { numeric: 'always' });
  } catch {
    // An unexpected `resolvedLanguage` throws RangeError rather than falling
    // back on its own. A countdown in the wrong language beats no card at all.
    formatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'always' });
  }
  relativeFormatters.set(key, formatter);
  return formatter;
}

/** Localized relative phrase, e.g. `in 11 days` / `11 天后` / `11 days ago`. */
export function formatRelativeInstant(targetMs: number, nowMs: number, locale?: string): string {
  const { value, unit } = relativeTimeParts(targetMs, nowMs);
  return getRelativeFormatter(locale).format(value, unit);
}

/**
 * Absolute instant in the shape every quota row already uses (`MM-DD HH:mm`,
 * browser-local, 24-hour). Kept in one place so the reset labels baked at fetch
 * time and the ones formatted at render time can never drift apart.
 */
export function formatInstantShort(ms: number): string {
  if (!Number.isFinite(ms)) return '-';
  return new Date(ms).toLocaleString(undefined, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}

export interface ResetDisplay {
  absolute: string;
  /** Null when no usable instant was available — render the absolute half alone. */
  relative: string | null;
}

/**
 * Pair an already-formatted absolute label with a freshly computed relative one.
 *
 * The absolute label wins when present because providers bake it at fetch time
 * and some of them cannot be reproduced at render (`formatCodexResetLabel`
 * resolves `reset_after_seconds` against the fetch-time clock). The instant is
 * only used for the relative half, and its absence degrades to exactly the
 * pre-existing rendering — which is what a store entry cached by an older
 * build, with no `resetAtMs` field, will hit.
 *
 * Returns null when there is nothing worth rendering at all.
 */
export function buildResetDisplay(
  absoluteLabel: string | undefined | null,
  atMs: number | undefined | null,
  nowMs: number,
  locale?: string
): ResetDisplay | null {
  const trimmed = typeof absoluteLabel === 'string' ? absoluteLabel.trim() : '';
  const absolute = trimmed && trimmed !== '-' ? trimmed : null;
  const usableMs = typeof atMs === 'number' && Number.isFinite(atMs) ? atMs : null;

  if (absolute === null && usableMs === null) return null;

  return {
    absolute: absolute ?? formatInstantShort(usableMs as number),
    relative: usableMs === null ? null : formatRelativeInstant(usableMs, nowMs, locale),
  };
}
