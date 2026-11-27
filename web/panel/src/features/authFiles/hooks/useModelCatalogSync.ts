import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { apiClient, modelsApi } from '@/services/api';
import { useAuthStore, useNotificationStore } from '@/stores';
import { useModelsStore } from '@/stores/useModelsStore';
import { syncModelCatalog as runModelCatalogSync } from '../modelCatalogSync';

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

  const syncModelCatalog = useCallback(
    () =>
      runModelCatalogSync(
        {
          refreshCatalog: () => modelsApi.refreshCatalog(),
          isConnected: () => useAuthStore.getState().connectionStatus === 'connected',
          getConnectionRevision: () => apiClient.getConnectionRevision(),
          isMounted: () => mountedRef.current,
          setSyncing: setSyncingModelCatalog,
          clearModelCache: () => useModelsStore.getState().clearCache(),
          onSynced: () => onSyncedRef.current(),
          showNotification,
          t,
        },
        pendingRef
      ),
    [showNotification, t]
  );

  return { syncingModelCatalog, syncModelCatalog };
}
