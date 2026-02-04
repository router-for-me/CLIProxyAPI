/**
 * Formatting functions for quota display.
 */

import type { CodexUsageWindow } from '@/types';
import { normalizeNumberValue } from './parsers';

export function formatQuotaResetTime(value?: string): string {
  if (!value) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '-';
  return date.toLocaleString(undefined, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false
  });
}

export function formatUnixSeconds(value: number | null): string {
  if (!value) return '-';
  const date = new Date(value * 1000);
  if (Number.isNaN(date.getTime())) return '-';
  return date.toLocaleString(undefined, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false
  });
}

export function formatCodexResetLabel(window?: CodexUsageWindow | null): string {
  if (!window) return '-';
  const resetAt = normalizeNumberValue(window.reset_at ?? window.resetAt);
  if (resetAt !== null && resetAt > 0) {
    return formatUnixSeconds(resetAt);
  }
  const resetAfter = normalizeNumberValue(window.reset_after_seconds ?? window.resetAfterSeconds);
  if (resetAfter !== null && resetAfter > 0) {
    const targetSeconds = Math.floor(Date.now() / 1000 + resetAfter);
    return formatUnixSeconds(targetSeconds);
  }
  return '-';
}

export function createStatusError(message: string, status?: number): Error & { status?: number } {
  const error = new Error(message) as Error & { status?: number };
  if (status !== undefined) {
    error.status = status;
  }
  return error;
}

export function getStatusFromError(err: unknown): number | undefined {
  if (typeof err === 'object' && err !== null && 'status' in err) {
    const rawStatus = (err as { status?: unknown }).status;
    if (typeof rawStatus === 'number' && Number.isFinite(rawStatus)) {
      return rawStatus;
    }
    const asNumber = Number(rawStatus);
    if (Number.isFinite(asNumber) && asNumber > 0) {
      return asNumber;
    }
  }
  return undefined;
}
