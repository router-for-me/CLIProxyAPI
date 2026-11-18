import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { pluginStoreApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import { getErrorMessage, isRecord } from '@/utils/helpers';
import type { PluginStoreEntry, PluginStoreResponse } from '@/types';
import { isOfficialPlugin, notifyPluginResourcesChanged } from '../pluginResources';
import { waitForPluginStoreState } from '../pluginPolling';
import { getStoreEntryKey, pluginVersionMatches } from '../pluginStoreEntries';
import type { PluginInstallOptionsModalProps } from '../components/PluginInstallOptionsModal';

export interface StoreLoadError {
  kind: 'unsupported' | 'registry' | 'generic';
  message: string;
}

interface SessionTag {
  apiBase: string;
  managementKey: string;
}

type Tagged<T> = SessionTag & { value: T };

const getErrorStatus = (error: unknown): number | undefined =>
  isRecord(error) && typeof error.status === 'number' ? error.status : undefined;

const getErrorDetailMessage = (error: unknown): string => {
  if (!isRecord(error) || !isRecord(error.details)) return '';
  const message = error.details.message;
  return typeof message === 'string' ? message.trim() : '';
};

interface UsePluginStoreOptions {
  connected: boolean;
  /** Installed-plugin mutations also lock store installation and updates. */
  actionBusy: boolean;
  /** Called once an install or update has settled, successful or not. */
  onInstallSettled: () => void;
}

/**
 * The page's single plugin store request plus the install/update flow.
 *
 * Every result is tagged with the connection it was fetched for, so a response that arrives
 * after a connection switch is never shown; a sequence number drops responses overtaken by a
 * newer load.
 */
export function usePluginStore({ connected, actionBusy, onInstallSettled }: UsePluginStoreOptions) {
  const { t } = useTranslation();
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const clearConfigCache = useConfigStore((state) => state.clearCache);
  const showNotification = useNotificationStore((state) => state.showNotification);

  const [store, setStore] = useState<Tagged<PluginStoreResponse> | null>(null);
  const [storeError, setStoreError] = useState<Tagged<StoreLoadError> | null>(null);
  const [loading, setLoading] = useState(true);
  const [installingKey, setInstallingKey] = useState('');
  const [restartRequiredKeys, setRestartRequiredKeys] = useState<string[]>([]);
  const loadSeq = useRef(0);

  // Multi-step install gauntlet, shown only for non-official (third-party) plugins.
  const [gateOpen, setGateOpen] = useState(false);
  const [gateEntry, setGateEntry] = useState<PluginStoreEntry | null>(null);
  const [gateIsUpdate, setGateIsUpdate] = useState(false);
  const [gateRequestedVersion, setGateRequestedVersion] = useState('');

  const [installOptionsEntry, setInstallOptionsEntry] = useState<PluginStoreEntry | null>(null);
  const [installOptionsIsUpdate, setInstallOptionsIsUpdate] = useState(false);
  const [installVersion, setInstallVersion] = useState('');

  const onInstallSettledRef = useRef(onInstallSettled);
  useEffect(() => {
    onInstallSettledRef.current = onInstallSettled;
  }, [onInstallSettled]);

  const isCurrentSession = (tag: SessionTag) =>
    connected && tag.apiBase === apiBase && tag.managementKey === managementKey;

  const data = store && isCurrentSession(store) ? store.value : null;
  const error = storeError && isCurrentSession(storeError) ? storeError.value : null;

  const load = useCallback(async () => {
    const seq = loadSeq.current + 1;
    loadSeq.current = seq;
    const session = { apiBase, managementKey };

    if (!connected) {
      setLoading(false);
      setStoreError({
        ...session,
        value: { kind: 'generic', message: t('notification.connection_required') },
      });
      return;
    }

    setLoading(true);
    try {
      const response = await pluginStoreApi.list();
      if (loadSeq.current !== seq) return;
      setStore({ ...session, value: response });
      setStoreError(null);
    } catch (err: unknown) {
      if (loadSeq.current !== seq) return;
      const status = getErrorStatus(err);
      let value: StoreLoadError;
      if (status === 404) {
        value = { kind: 'unsupported', message: t('plugin_store.unsupported_backend') };
      } else if (status === 502) {
        const detail = getErrorDetailMessage(err);
        value = {
          kind: 'registry',
          message: detail
            ? `${t('plugin_store.registry_failed')}: ${detail}`
            : t('plugin_store.registry_failed'),
        };
      } else {
        value = { kind: 'generic', message: getErrorMessage(err, t('plugin_store.load_failed')) };
      }
      setStoreError({ ...session, value });
    } finally {
      if (loadSeq.current === seq) setLoading(false);
    }
  }, [apiBase, connected, managementKey, t]);

  const installBlocked = !connected || actionBusy || Boolean(installingKey);

  const runInstall = useCallback(
    async (entry: PluginStoreEntry, isUpdate: boolean, requestedVersion = '') => {
      if (installBlocked) return;
      const session = { apiBase, managementKey };
      const applyResponse = (response: PluginStoreResponse) => {
        // Polling results are the freshest store state; supersede any in-flight load.
        loadSeq.current += 1;
        setStore({ ...session, value: response });
        setStoreError(null);
        setLoading(false);
      };
      const entryKey = getStoreEntryKey(entry);
      const failedKey = isUpdate ? 'plugin_store.update_failed' : 'plugin_store.install_failed';
      const version = requestedVersion.trim();
      setInstallingKey(entryKey);
      try {
        const result = await pluginStoreApi.install(entry.id, {
          sourceId: entry.sourceId || undefined,
          version: version || undefined,
        });
        clearConfigCache();
        const sourceId = result.sourceId || entry.sourceId;
        const installedState = await waitForPluginStoreState(
          entry.id,
          sourceId,
          (plugin) =>
            plugin.installed &&
            plugin.configured &&
            (!version || pluginVersionMatches(plugin.installedVersion, version))
        );
        applyResponse(installedState.response);
        if (
          installedState.timedOut ||
          !installedState.plugin?.installed ||
          !installedState.plugin.configured
        ) {
          showNotification(t('plugin_store.status_pending'), 'warning');
          return;
        }

        if (result.restartRequired) {
          setRestartRequiredKeys((current) =>
            current.includes(entryKey) ? current : [...current, entryKey]
          );
          showNotification(
            isUpdate ? t('plugin_store.update_success') : t('plugin_store.install_success'),
            'success'
          );
          showNotification(t('plugin_store.restart_required_notice'), 'warning');
          return;
        }

        if (!installedState.response.pluginsEnabled) {
          showNotification(
            isUpdate ? t('plugin_store.update_success') : t('plugin_store.install_success'),
            'success'
          );
          showNotification(t('plugin_store.global_disabled_hint'), 'warning');
          return;
        }

        if (installedState.plugin.enabled) {
          const registeredState = await waitForPluginStoreState(
            entry.id,
            sourceId,
            (plugin) => plugin.registered && plugin.effectiveEnabled
          );
          applyResponse(registeredState.response);
          if (
            registeredState.timedOut ||
            !registeredState.plugin?.registered ||
            !registeredState.plugin.effectiveEnabled
          ) {
            showNotification(t('plugin_store.registration_pending'), 'warning');
            return;
          }
          notifyPluginResourcesChanged();
        }

        showNotification(
          isUpdate ? t('plugin_store.update_success') : t('plugin_store.install_success'),
          'success'
        );
      } catch (err: unknown) {
        showNotification(`${t(failedKey)}: ${getErrorMessage(err, t(failedKey))}`, 'error');
        throw err;
      } finally {
        setInstallingKey('');
        // The installed list is a separate endpoint; refresh it whatever the outcome.
        onInstallSettledRef.current();
      }
    },
    [apiBase, clearConfigCache, installBlocked, managementKey, showNotification, t]
  );

  const openInstall = useCallback(
    (entry: PluginStoreEntry) => {
      if (installBlocked) return;
      setInstallOptionsEntry(entry);
      setInstallOptionsIsUpdate(entry.installed && entry.updateAvailable);
      setInstallVersion('');
    },
    [installBlocked]
  );

  const handleInstallOptionsClose = useCallback(() => {
    if (installingKey) return;
    setInstallOptionsEntry(null);
    setInstallVersion('');
  }, [installingKey]);

  const handleInstallOptionsConfirm = useCallback(async () => {
    if (installBlocked || !installOptionsEntry) return;
    const requestedVersion = installVersion.trim();

    // Third-party plugins must clear the multi-step confirmation gauntlet first.
    if (!isOfficialPlugin(installOptionsEntry)) {
      setGateEntry(installOptionsEntry);
      setGateIsUpdate(installOptionsIsUpdate);
      setGateRequestedVersion(requestedVersion);
      setGateOpen(true);
      setInstallOptionsEntry(null);
      setInstallVersion('');
      return;
    }

    try {
      await runInstall(installOptionsEntry, installOptionsIsUpdate, requestedVersion);
      setInstallOptionsEntry(null);
      setInstallVersion('');
    } catch {
      // runInstall already surfaced a notification; keep the modal available for correction.
    }
  }, [installBlocked, installOptionsEntry, installOptionsIsUpdate, installVersion, runInstall]);

  const handleGateConfirm = useCallback(async () => {
    if (installBlocked || !gateEntry) return;
    await runInstall(gateEntry, gateIsUpdate, gateRequestedVersion);
    setGateOpen(false);
    setGateRequestedVersion('');
  }, [gateEntry, gateIsUpdate, gateRequestedVersion, installBlocked, runInstall]);

  const handleGateClose = useCallback(() => {
    setGateOpen(false);
    setGateRequestedVersion('');
  }, []);

  const installOptionsProps: PluginInstallOptionsModalProps = {
    entry: installOptionsEntry,
    isUpdate: installOptionsIsUpdate,
    version: installVersion,
    installing: installOptionsEntry
      ? installingKey === getStoreEntryKey(installOptionsEntry)
      : false,
    onVersionChange: setInstallVersion,
    onClose: handleInstallOptionsClose,
    onConfirm: handleInstallOptionsConfirm,
  };

  const gateProps = {
    open: gateOpen,
    entry: gateEntry,
    isUpdate: gateIsUpdate,
    installing: gateEntry ? installingKey === getStoreEntryKey(gateEntry) : false,
    onClose: handleGateClose,
    onConfirm: handleGateConfirm,
  };

  return {
    data,
    error,
    /** True until the first response (or failure) for this connection, and during reloads. */
    loading,
    load,
    installingKey,
    restartRequiredKeys,
    openInstall,
    installOptionsProps,
    gateProps,
  };
}
