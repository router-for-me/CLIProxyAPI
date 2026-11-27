import type { TFunction } from 'i18next';
import type { NotificationType } from '@/types';

export type ModelCatalogSyncPending = { current: boolean };

export type ModelCatalogSyncDeps = {
  refreshCatalog: () => Promise<{ changedProviders: string[] }>;
  isConnected: () => boolean;
  getConnectionRevision: () => number;
  isMounted: () => boolean;
  setSyncing: (syncing: boolean) => void;
  /** Drops cached model lists; runs even when no provider changed. */
  clearModelCache: () => void;
  onSynced: () => void;
  showNotification: (message: string, type: NotificationType) => void;
  t: TFunction;
};

/** Global catalog operation, deliberately independent of credential refreshes. */
export async function syncModelCatalog(
  deps: ModelCatalogSyncDeps,
  pending: ModelCatalogSyncPending
): Promise<void> {
  if (pending.current || !deps.isConnected()) return;
  const revision = deps.getConnectionRevision();
  const isCurrent = () => deps.isMounted() && revision === deps.getConnectionRevision();
  pending.current = true;
  deps.setSyncing(true);
  try {
    const { changedProviders } = await deps.refreshCatalog();
    if (!isCurrent()) return;
    deps.clearModelCache();
    deps.onSynced();
    deps.showNotification(
      changedProviders.length
        ? deps.t('auth_files.sync_catalog_success', { providers: changedProviders.join(', ') })
        : deps.t('auth_files.sync_catalog_unchanged'),
      'success'
    );
  } catch (error: unknown) {
    if (!isCurrent()) return;
    const message = error instanceof Error ? error.message : String(error);
    deps.showNotification(`${deps.t('auth_files.sync_catalog_failed')}: ${message}`, 'error');
  } finally {
    pending.current = false;
    if (deps.isMounted()) deps.setSyncing(false);
  }
}
