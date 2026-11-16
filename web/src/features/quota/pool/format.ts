import { DAY_MS, MINUTE_MS } from '@/utils/time/durations';

const formatters = new Map<string, Intl.DateTimeFormat>();

const getFormatter = (
  locale: string,
  timeZone: string,
  options: Intl.DateTimeFormatOptions
): Intl.DateTimeFormat => {
  const cacheKey = `${locale}|${timeZone}|${JSON.stringify(options)}`;
  const cached = formatters.get(cacheKey);

  if (cached) return cached;

  const formatter = new Intl.DateTimeFormat(locale, { ...options, timeZone });

  formatters.set(cacheKey, formatter);

  return formatter;
};

const WEEKDAY_TIME: Intl.DateTimeFormatOptions = {
  hour: 'numeric',
  minute: '2-digit',
  weekday: 'short',
};
const MONTH_DAY: Intl.DateTimeFormatOptions = { day: 'numeric', month: 'short' };
const MONTH_DAY_TIME: Intl.DateTimeFormatOptions = {
  ...MONTH_DAY,
  hour: 'numeric',
  minute: '2-digit',
};
const DAY_LABEL: Intl.DateTimeFormatOptions = { day: 'numeric', weekday: 'short' };

/** Chrome puts U+202F before AM/PM and some locales use U+00A0; both become plain spaces. */
export const normalizeSpaces = (text: string): string => text.replace(/[\u00a0\u202f]/g, ' ');

const format = (
  ms: number,
  locale: string,
  timeZone: string,
  options: Intl.DateTimeFormatOptions
): string => normalizeSpaces(getFormatter(locale, timeZone, options).format(ms));

/** "Sun 4:39 PM". */
export const formatWeekdayTime = (ms: number, locale: string, timeZone: string): string =>
  format(ms, locale, timeZone, WEEKDAY_TIME);

/** Weekday and time within a week of now, else month, day and time ("Nov 9, 4:39 PM"). */
export const formatWhen = (ms: number, now: number, locale: string, timeZone: string): string =>
  Math.abs(ms - now) < 7 * DAY_MS
    ? formatWeekdayTime(ms, locale, timeZone)
    : format(ms, locale, timeZone, MONTH_DAY_TIME);

/** "Nov 9". */
export const formatMonthDay = (ms: number, locale: string, timeZone: string): string =>
  format(ms, locale, timeZone, MONTH_DAY);

/** { weekday: 'Fri', day: '23' }. */
export const dayLabelParts = (
  ms: number,
  locale: string,
  timeZone: string
): { day: string; weekday: string } => {
  const parts = getFormatter(locale, timeZone, DAY_LABEL).formatToParts(ms);
  const valueOf = (type: Intl.DateTimeFormatPartTypes) =>
    normalizeSpaces(parts.find((part) => part.type === type)?.value ?? '');

  return { day: valueOf('day'), weekday: valueOf('weekday') };
};

/** Whole minutes, split into days, hours and minutes. */
export const splitDuration = (ms: number): { d: number; h: number; m: number } => {
  const total = Math.round(ms / MINUTE_MS);

  return { d: Math.floor(total / 1440), h: Math.floor((total % 1440) / 60), m: total % 60 };
};

export const formatPoolPercent = (v: number): string => String(Math.round(v));

/** One decimal below 10, whole numbers otherwise. */
export const formatRate = (v: number): string => (v < 10 ? v.toFixed(1) : String(Math.round(v)));
