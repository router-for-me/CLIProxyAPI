import type { Dispatch, SetStateAction } from 'react';
import type { TFunction } from 'i18next';
import type { AuthFileRefreshResult } from '@/services/api/authFiles';
import type { AuthFileItem, NotificationType } from '@/types';
import { isRuntimeOnlyAuthFile, supportsAuthFileManualRefresh } from './constants';
import { getAuthFileRefreshKey } from './manualRefresh';

/** Single and all-credential refreshes exclude each other. */
export type AuthFileRefreshPending = {
  all: boolean;
  keys: Set<string>;
};

export type AuthFileRefreshDeps = {
  requestManualRefresh: (name: string, authIndex?: string) => Promise<void>;
  requestAllManualRefresh: () => Promise<AuthFileRefreshResult[]>;
  getConnectionRevision: () => number;
  setManualRefreshing: Dispatch<SetStateAction<Record<string, boolean>>>;
  setRefreshingAllCredentials: (refreshing: boolean) => void;
  setRefreshResults: (results: AuthFileRefreshResult[] | null) => void;
  showConfirmation: (options: {
    title: string;
    message: string;
    confirmText: string;
    onConfirm: () => Promise<void>;
  }) => void;
  showNotification: (message: string, type: NotificationType) => void;
  invalidateInFlightLoads: () => void;
  /** Broadcasts the credential change to other views. */
  notifyAuthFilesChanged: () => void;
  /** Credential data changed; derived caches (quota, models, …) must follow. */
  onFilesMutated: (names?: string[]) => void;
  reloadFiles: () => Promise<void>;
  t: TFunction;
};

export const createAuthFileRefreshPending = (): AuthFileRefreshPending => ({
  all: false,
  keys: new Set(),
});

const errorMessageOf = (err: unknown, t: TFunction) =>
  err instanceof Error ? err.message : t('notification.update_failed');

/** Asks the backend to refresh one credential identity, then reloads the list. */
export async function refreshAuthFile(
  deps: AuthFileRefreshDeps,
  pending: AuthFileRefreshPending,
  item: AuthFileItem
): Promise<void> {
  const name = item.name.trim();
  const key = getAuthFileRefreshKey(item);
  const revision = deps.getConnectionRevision();
  const isCurrent = () => revision === deps.getConnectionRevision();
  if (
    !name ||
    item.disabled === true ||
    isRuntimeOnlyAuthFile(item) ||
    !supportsAuthFileManualRefresh(item.type ?? item.provider) ||
    pending.all ||
    pending.keys.has(key)
  ) {
    return;
  }

  pending.keys.add(key);
  deps.setManualRefreshing((prev) => ({ ...prev, [key]: true }));

  try {
    await deps.requestManualRefresh(name, String(item.authIndex ?? '').trim() || undefined);
    if (!isCurrent()) return;
    deps.invalidateInFlightLoads();
    deps.showNotification(deps.t('auth_files.manual_refresh_requested', { name }), 'info');
    deps.notifyAuthFilesChanged();
    deps.onFilesMutated([name]);
    await deps.reloadFiles();
  } catch (err: unknown) {
    if (!isCurrent()) return;
    deps.showNotification(
      deps.t('auth_files.manual_refresh_failed', { name, message: errorMessageOf(err, deps.t) }),
      'error'
    );
  } finally {
    pending.keys.delete(key);
    deps.setManualRefreshing((prev) => {
      if (!isCurrent() || !prev[key]) return prev;
      const next = { ...prev };
      delete next[key];
      return next;
    });
  }
}

/**
 * Confirms, then refreshes every credential. The list is reloaded even after a failure,
 * because a timeout can follow partial backend mutations.
 */
export function refreshAllAuthFiles(
  deps: AuthFileRefreshDeps,
  pending: AuthFileRefreshPending
): void {
  if (pending.all || pending.keys.size > 0) return;
  const revision = deps.getConnectionRevision();
  const isCurrent = () => revision === deps.getConnectionRevision();
  deps.showConfirmation({
    title: deps.t('auth_files.refresh_all_button'),
    message: deps.t('auth_files.refresh_all_confirm'),
    confirmText: deps.t('auth_files.refresh_all_button'),
    onConfirm: async () => {
      if (!isCurrent() || pending.all || pending.keys.size > 0) return;
      pending.all = true;
      deps.setRefreshingAllCredentials(true);
      deps.setRefreshResults(null);
      try {
        const results = await deps.requestAllManualRefresh();
        if (!isCurrent()) return;
        deps.setRefreshResults(results);
      } catch (err: unknown) {
        if (!isCurrent()) return;
        deps.showNotification(
          deps.t('auth_files.refresh_all_failed', { message: errorMessageOf(err, deps.t) }),
          'error'
        );
      } finally {
        pending.all = false;
        if (isCurrent()) {
          deps.setRefreshingAllCredentials(false);
          deps.invalidateInFlightLoads();
          deps.onFilesMutated();
          deps.notifyAuthFilesChanged();
          await deps.reloadFiles();
        }
      }
    },
  });
}
