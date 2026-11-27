import type { Dispatch, SetStateAction } from 'react';
import type { TFunction } from 'i18next';
import type { AuthFileItem, NotificationType } from '@/types';
import { getAuthFileRefreshKey } from './manualRefresh';

/** Pending status writes, scoped to the connection revision that started them. */
export type AuthFileStatusPending = {
  revision: number;
  keys: Set<string>;
  batch: boolean;
};

export type AuthFileStatusDeps = {
  setStatus: (
    name: string,
    disabled: boolean,
    authIndex?: string
  ) => Promise<{ disabled: boolean }>;
  getConnectionRevision: () => number;
  setFiles: Dispatch<SetStateAction<AuthFileItem[]>>;
  setStatusUpdating: Dispatch<SetStateAction<Record<string, boolean>>>;
  setBatchStatusUpdating: (updating: boolean) => void;
  invalidateInFlightLoads: () => void;
  showNotification: (message: string, type: NotificationType) => void;
  t: TFunction;
};

export const createAuthFileStatusPending = (): AuthFileStatusPending => ({
  revision: -1,
  keys: new Set(),
  batch: false,
});

const authIndexOf = (file: AuthFileItem) => String(file.authIndex ?? '').trim() || undefined;

/** A new connection drops pending markers left by requests of the previous one. */
function adoptRevision(pending: AuthFileStatusPending, deps: AuthFileStatusDeps): number {
  const revision = deps.getConnectionRevision();
  if (pending.revision !== revision) {
    pending.revision = revision;
    pending.keys = new Set();
    pending.batch = false;
    deps.setBatchStatusUpdating(false);
    deps.setStatusUpdating({});
  }
  return revision;
}

/** Optimistically toggles one credential identity and reconciles it with the backend. */
export async function toggleAuthFileStatus(
  deps: AuthFileStatusDeps,
  pending: AuthFileStatusPending,
  item: AuthFileItem,
  enabled: boolean
): Promise<void> {
  const revision = adoptRevision(pending, deps);
  const isCurrent = () => revision === deps.getConnectionRevision();
  const key = getAuthFileRefreshKey(item);
  if (pending.keys.has(key)) return;
  pending.keys.add(key);
  const { name } = item;
  const nextDisabled = !enabled;
  const previousDisabled = item.disabled === true;
  const withDisabled = (prev: AuthFileItem[], disabled: boolean) =>
    prev.map((file) => (getAuthFileRefreshKey(file) === key ? { ...file, disabled } : file));
  // Settled responses must not touch a list that already belongs to another connection.
  const setDisabled = (disabled: boolean) =>
    deps.setFiles((prev) => (isCurrent() ? withDisabled(prev, disabled) : prev));

  deps.setStatusUpdating((prev) => ({ ...prev, [key]: true }));
  deps.invalidateInFlightLoads();
  deps.setFiles((prev) => withDisabled(prev, nextDisabled));

  try {
    const res = await deps.setStatus(name, nextDisabled, authIndexOf(item));
    if (!isCurrent()) return;
    deps.invalidateInFlightLoads();
    setDisabled(res.disabled);
    deps.showNotification(
      enabled
        ? deps.t('auth_files.status_enabled_success', { name })
        : deps.t('auth_files.status_disabled_success', { name }),
      'success'
    );
  } catch (err: unknown) {
    if (!isCurrent()) return;
    const errorMessage = err instanceof Error ? err.message : '';
    setDisabled(previousDisabled);
    deps.showNotification(`${deps.t('notification.update_failed')}: ${errorMessage}`, 'error');
  } finally {
    if (isCurrent()) {
      pending.keys.delete(key);
      deps.setStatusUpdating((prev) => {
        if (!isCurrent()) return prev;
        const next = { ...prev };
        delete next[key];
        return next;
      });
    }
  }
}

/**
 * Applies one status to every identity with a selected name. Failures roll back
 * individually; nothing starts while a batch or any target identity is pending.
 */
export async function batchSetAuthFileStatus(
  deps: AuthFileStatusDeps & { deselectAll: () => void },
  pending: AuthFileStatusPending,
  files: AuthFileItem[],
  names: string[],
  enabled: boolean
): Promise<void> {
  const revision = adoptRevision(pending, deps);
  const isCurrent = () => revision === deps.getConnectionRevision();
  if (pending.batch) return;

  const uniqueNames = new Set(names);
  const targets = new Map(
    files
      .filter((file) => uniqueNames.has(file.name))
      .map((file) => [getAuthFileRefreshKey(file), file])
  );
  if ([...targets.keys()].some((key) => pending.keys.has(key))) return;
  const originalDisabled = new Map(
    [...targets].map(([key, file]) => [key, file.disabled === true])
  );
  const targetKeys = Array.from(originalDisabled.keys());
  if (targetKeys.length === 0) return;
  const targetKeySet = new Set(targetKeys);
  const nextDisabled = !enabled;

  pending.batch = true;
  targetKeys.forEach((key) => pending.keys.add(key));
  deps.setBatchStatusUpdating(true);
  deps.invalidateInFlightLoads();
  deps.setStatusUpdating((prev) => {
    const next = { ...prev };
    targetKeys.forEach((key) => {
      next[key] = true;
    });
    return next;
  });
  deps.setFiles((prev) =>
    prev.map((file) =>
      targetKeySet.has(getAuthFileRefreshKey(file)) ? { ...file, disabled: nextDisabled } : file
    )
  );

  try {
    const results = await Promise.allSettled(
      targetKeys.map((key) => {
        const file = targets.get(key)!;
        return deps.setStatus(file.name, nextDisabled, authIndexOf(file));
      })
    );
    if (!isCurrent()) return;
    deps.invalidateInFlightLoads();

    const failed = new Set<string>();
    const confirmedDisabled = new Map<string, boolean>();
    results.forEach((result, index) => {
      const key = targetKeys[index];
      if (result.status === 'fulfilled') confirmedDisabled.set(key, result.value.disabled);
      else failed.add(key);
    });

    deps.setFiles((prev) =>
      !isCurrent()
        ? prev
        : prev.map((file) => {
            const key = getAuthFileRefreshKey(file);
            if (failed.has(key)) return { ...file, disabled: originalDisabled.get(key) === true };
            if (confirmedDisabled.has(key))
              return { ...file, disabled: confirmedDisabled.get(key) };
            return file;
          })
    );

    const successCount = confirmedDisabled.size;
    if (failed.size === 0) {
      deps.showNotification(
        deps.t('auth_files.batch_status_success', { count: successCount }),
        'success'
      );
    } else {
      deps.showNotification(
        deps.t('auth_files.batch_status_partial', { success: successCount, failed: failed.size }),
        'warning'
      );
    }

    deps.deselectAll();
  } finally {
    if (isCurrent()) {
      pending.batch = false;
      targetKeys.forEach((key) => pending.keys.delete(key));
      deps.setBatchStatusUpdating(false);
      deps.setStatusUpdating((prev) => {
        if (!isCurrent()) return prev;
        const next = { ...prev };
        targetKeys.forEach((key) => {
          delete next[key];
        });
        return next;
      });
    }
  }
}
