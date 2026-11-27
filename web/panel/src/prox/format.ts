/** Formatting shared by the prox views. */

export const PROVIDER_META: Record<string, { label: string; color: string }> = {
  claude: { label: 'Claude', color: '#d97757' },
  codex: { label: 'Codex', color: '#7b8cff' },
  kimi: { label: 'Kimi', color: '#3fb6a8' },
  xai: { label: 'xAI', color: '#a1a1aa' },
};

export const providerLabel = (p: string) => PROVIDER_META[p]?.label ?? p;
export const providerColor = (p: string) => PROVIDER_META[p]?.color ?? '#8b8680';

export const WINDOW_LABELS: Record<string, string> = {
  five_hour: '5-hour',
  daily: 'Daily',
  weekly: '7-day',
  monthly: 'Monthly',
};

export const pct = (v: number | null | undefined, digits = 0) =>
  v === null || v === undefined || Number.isNaN(v) ? '--' : `${v.toFixed(digits)}%`;

export function usd(v: number | undefined | null): string {
  if (v === undefined || v === null) return '--';
  if (v === 0) return '$0';
  if (v < 0.01) return '<$0.01';
  if (v < 100) return `$${v.toFixed(2)}`;
  if (v < 10000) return `$${Math.round(v).toLocaleString()}`;
  return `$${(v / 1000).toFixed(1)}k`;
}

export function compact(n: number | undefined | null): string {
  if (n === undefined || n === null) return '--';
  const abs = Math.abs(n);
  if (abs >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (abs >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (abs >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(Math.round(n));
}

const sameDay = (a: Date, b: Date) =>
  a.getFullYear() === b.getFullYear() &&
  a.getMonth() === b.getMonth() &&
  a.getDate() === b.getDate();

/** "3:10 PM", "Thu 3:10 PM", or "Nov 18" depending on distance from now. */
export function when(iso: string | undefined | null, now = new Date()): string {
  if (!iso) return '--';
  const d = new Date(iso);
  const time = d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
  if (sameDay(d, now)) return time;
  const days = Math.abs(d.getTime() - now.getTime()) / 86_400_000;
  if (days < 6.5) return `${d.toLocaleDateString(undefined, { weekday: 'short' })} ${time}`;
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

/** "in 3 hours", "in 2 days", "5 min ago". */
export function relative(iso: string | undefined | null, now = Date.now()): string {
  if (!iso) return '--';
  const diff = new Date(iso).getTime() - now;
  const abs = Math.abs(diff);
  const unit =
    abs < 3_600_000
      ? [Math.max(1, Math.round(abs / 60_000)), 'min']
      : abs < 86_400_000 * 1.5
        ? [Math.round(abs / 3_600_000), 'hour']
        : [Math.round(abs / 86_400_000), 'day'];
  const [n, u] = unit as [number, string];
  const label = `${n} ${u}${n === 1 || u === 'min' ? '' : 's'}`;
  return diff >= 0 ? `in ${label}` : `${label} ago`;
}
