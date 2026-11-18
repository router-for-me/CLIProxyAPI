import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { apiClient, modelsApi } from '@/services/api';
import { useAuthStore, useNotificationStore } from '@/stores';
import { useModelsStore } from '@/stores/useModelsStore';

/** Global catalog operation, deliberately independent of credential refreshes. */
export function useModelCatalogSync(onSynced: () => void) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [syncingModelCatalog, setSyncingModelCatalog] = useState(false);
  const pendingRef = useRef(false);
  const mountedRef = useRef(true);
  const onSyncedRef = useRef(onSynced);
  useEffect(() => {
    onSyncedRef.current = onSynced;
  }, [onSynced]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const syncModelCatalog = useCallback(async () => {
    if (pendingRef.current || useAuthStore.getState().connectionStatus !== 'connected') return;
    const revision = apiClient.getConnectionRevision();
    const isCurrent = () => mountedRef.current && revision === apiClient.getConnectionRevision();
    pendingRef.current = true;
    setSyncingModelCatalog(true);
    try {
      const { changedProviders } = await modelsApi.refreshCatalog();
      if (!isCurrent()) return;
      useModelsStore.getState().clearCache();
      onSyncedRef.current();
      showNotification(
        changedProviders.length
          ? t('auth_files.sync_catalog_success', { providers: changedProviders.join(', ') })
          : t('auth_files.sync_catalog_unchanged'),
        'success'
      );
    } catch (error: unknown) {
      if (!isCurrent()) return;
      showNotification(
        `${t('auth_files.sync_catalog_failed')}: ${error instanceof Error ? error.message : String(error)}`,
        'error'
      );
    } finally {
      pendingRef.current = false;
      if (mountedRef.current) setSyncingModelCatalog(false);
    }
  }, [showNotification, t]);

  return { syncingModelCatalog, syncModelCatalog };
}
