import type { AuthFileItem } from '@/types';

/** Runtime actions must not share pending state across same-name credentials. */
export const getAuthFileRefreshKey = (file: AuthFileItem): string =>
  JSON.stringify([file.name, String(file.authIndex ?? '').trim()]);
