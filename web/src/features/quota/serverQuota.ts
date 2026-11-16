/**
 * Quota windows the server already tracks for each credential.
 *
 * The server's earliest-reset routing reads Claude and Codex quota from every
 * response's rate-limit headers and from occasional background usage reads,
 * and returns the result as `quota_windows` on each auth file. Reading it here
 * means opening the page never calls Anthropic or OpenAI, and the page shows
 * exactly the numbers routing uses.
 */

import type { TFunction } from 'i18next';
import type { AuthFileItem, ClaudeQuotaWindow, CodexQuotaWindow } from '@/types';
import { formatQuotaResetTime } from '@/utils/quota/formatters';

export interface ServerQuotaWindow {
  kind: 'five_hour' | 'weekly' | 'weekly_scoped';
  scope?: string;
  /** Used fraction, 0–1. */
  used: number;
  reset_at: string;
  source: 'response' | 'usage';
  observed_at: string;
}

const isServerQuotaWindow = (value: unknown): value is ServerQuotaWindow => {
  if (!value || typeof value !== 'object') return false;
  const window = value as Partial<ServerQuotaWindow>;
  return (
    (window.kind === 'five_hour' || window.kind === 'weekly' || window.kind === 'weekly_scoped') &&
    typeof window.used === 'number' &&
    Number.isFinite(window.used) &&
    typeof window.reset_at === 'string'
  );
};

export const readServerQuotaWindows = (file: AuthFileItem): ServerQuotaWindow[] => {
  const raw = file['quota_windows'];
  return Array.isArray(raw) ? raw.filter(isServerQuotaWindow) : [];
};

/** Go encodes an unknown time as year 1; treat anything before 2000 as unknown. */
export const resetAtMs = (value: string): number | null => {
  const ms = Date.parse(value);
  return Number.isFinite(ms) && ms > Date.UTC(2000, 0, 1) ? ms : null;
};

const usedPercent = (used: number) => Math.round(Math.min(1, Math.max(0, used)) * 1000) / 10;

const windowFor = (
  window: ServerQuotaWindow,
  id: string,
  labelKey: string,
  periodHours: number,
  t: TFunction
): ClaudeQuotaWindow & CodexQuotaWindow => {
  const reset = resetAtMs(window.reset_at);
  return {
    id,
    label: t(labelKey),
    labelKey,
    usedPercent: usedPercent(window.used),
    resetLabel: reset === null ? '-' : formatQuotaResetTime(window.reset_at),
    resetAtMs: reset,
    periodHours,
  };
};

export const claudeWindowsFromServer = (file: AuthFileItem, t: TFunction): ClaudeQuotaWindow[] =>
  readServerQuotaWindows(file).flatMap((window) => {
    if (window.kind === 'five_hour') {
      return [windowFor(window, 'five-hour', 'claude_quota.five_hour', 5, t)];
    }
    if (window.kind === 'weekly') {
      return [windowFor(window, 'seven-day', 'claude_quota.seven_day', 168, t)];
    }
    if (window.scope === 'fable') {
      return [windowFor(window, 'seven-day-fable', 'claude_quota.seven_day_fable', 168, t)];
    }
    return [];
  });

export const codexWindowsFromServer = (file: AuthFileItem, t: TFunction): CodexQuotaWindow[] =>
  readServerQuotaWindows(file).flatMap((window) => {
    if (window.kind === 'five_hour') {
      return [windowFor(window, 'five-hour', 'codex_quota.primary_window', 5, t)];
    }
    if (window.kind === 'weekly') {
      return [windowFor(window, 'weekly', 'codex_quota.secondary_window', 168, t)];
    }
    return [];
  });
