import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { pluginsApi } from '@/services/api';
import { useConfigStore, useNotificationStore } from '@/stores';
import { getErrorMessage, isRecord } from '@/utils/helpers';
import type { PluginListEntry, PluginListResponse } from '@/types';
import {
  buildPluginConfigDraft,
  buildPluginConfigPatch,
  type PluginConfigDraft,
} from '../pluginConfigDraft';
import { getPluginTitle, notifyPluginResourcesChanged } from '../pluginResources';
import { waitForPluginState } from '../pluginPolling';

type PluginRuntimeWaitStatus = 'ready' | 'globalDisabled' | 'timeout';

const hasStatus = (error: unknown, status: number) => isRecord(error) && error.status === status;

const hasRestartRequired = (value: unknown) => isRecord(value) && value.restart_required === true;

const hasRestartRequiredError = (error: unknown) =>
  isRecord(error) && (hasRestartRequired(error.details) || hasRestartRequired(error.data));

interface UseInstalledPluginsOptions {
  connected: boolean;
  /** Called after a toggle, delete or config save reached the backend. */
  onMutated: () => void;
}

/** The installed plugin list with its toggle, delete and config-sheet flows. */
export function useInstalledPlugins({ connected, onMutated }: UseInstalledPluginsOptions) {
  const { t } = useTranslation();
  const clearConfigCache = useConfigStore((state) => state.clearCache);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);

  const [data, setData] = useState<PluginListResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [editingPlugin, setEditingPlugin] = useState<PluginListEntry | null>(null);
  const [draft, setDraft] = useState<PluginConfigDraft | null>(null);
  const [mutatingID, setMutatingID] = useState('');
  const [deletingID, setDeletingID] = useState('');
  const [openingConfigID, setOpeningConfigID] = useState('');
  // The requested toggle position, shown on the switch while the runtime catches up.
  const [pendingToggle, setPendingToggle] = useState<{ id: string; enabled: boolean } | null>(null);
  const configRequestSeq = useRef(0);

  const onMutatedRef = useRef(onMutated);
  useEffect(() => {
    onMutatedRef.current = onMutated;
  }, [onMutated]);

  const load = useCallback(async () => {
    if (!connected) {
      setLoading(false);
      setError(t('notification.connection_required'));
      return;
    }

    setLoading(true);
    setError('');
    try {
      const plugins = await pluginsApi.list();
      setData(plugins);
    } catch (err: unknown) {
      setError(
        hasStatus(err, 404)
          ? t('plugin_management.unsupported_backend')
          : getErrorMessage(err, t('plugin_management.load_failed'))
      );
    } finally {
      setLoading(false);
    }
  }, [connected, t]);

  const waitForPluginRuntimeState = useCallback(
    async (id: string, enabled: boolean): Promise<PluginRuntimeWaitStatus> => {
      const result = await waitForPluginState(id, (item, response) =>
        enabled
          ? !response.pluginsEnabled || (item.registered && item.effectiveEnabled)
          : !item.effectiveEnabled
      );
      setData(result.response);
      if (enabled && !result.response.pluginsEnabled) {
        return 'globalDisabled';
      }
      return result.timedOut ? 'timeout' : 'ready';
    },
    []
  );

  // The sheet opens on press and shows a skeleton until the config arrives.
  const openConfigSheet = async (plugin: PluginListEntry) => {
    if (openingConfigID || mutatingID || deletingID) return;

    const requestSeq = configRequestSeq.current + 1;
    configRequestSeq.current = requestSeq;
    setOpeningConfigID(plugin.id);
    setEditingPlugin(plugin);
    setDraft(null);

    try {
      const currentConfig = await pluginsApi.getConfig(plugin.id);
      if (configRequestSeq.current !== requestSeq) return;

      setDraft(buildPluginConfigDraft(plugin, currentConfig));
    } catch (err: unknown) {
      if (configRequestSeq.current !== requestSeq) return;

      setEditingPlugin(null);
      setDraft(null);
      showNotification(
        hasStatus(err, 404)
          ? t('plugin_management.config_not_found')
          : `${t('plugin_management.config_load_failed')}: ${getErrorMessage(
              err,
              t('plugin_management.config_load_failed')
            )}`,
        'error'
      );
    } finally {
      if (configRequestSeq.current === requestSeq) {
        setOpeningConfigID('');
      }
    }
  };

  const closeConfigSheet = () => {
    if (mutatingID || deletingID) return;
    // Closing while the config is still loading cancels that request's effects.
    configRequestSeq.current += 1;
    setOpeningConfigID('');
    setEditingPlugin(null);
    setDraft(null);
  };

  const updateDraft = (updater: (current: PluginConfigDraft) => PluginConfigDraft) => {
    setDraft((current) => (current ? updater(current) : current));
  };

  const togglePlugin = async (plugin: PluginListEntry, enabled: boolean) => {
    if (deletingID) return;
    setMutatingID(plugin.id);
    setPendingToggle({ id: plugin.id, enabled });
    try {
      await pluginsApi.updateEnabled(plugin.id, enabled);
      clearConfigCache();
      const status = await waitForPluginRuntimeState(plugin.id, enabled);
      if (status === 'ready') {
        notifyPluginResourcesChanged();
        showNotification(t('plugin_management.toggle_success'), 'success');
      } else {
        showNotification(
          t(
            status === 'globalDisabled'
              ? 'plugin_management.global_disabled_hint'
              : 'plugin_management.runtime_pending'
          ),
          'warning'
        );
      }
    } catch (err: unknown) {
      showNotification(
        `${t('plugin_management.toggle_failed')}: ${getErrorMessage(
          err,
          t('plugin_management.toggle_failed')
        )}`,
        'error'
      );
    } finally {
      setPendingToggle(null);
      setMutatingID('');
      onMutatedRef.current();
    }
  };

  const deletePlugin = (plugin: PluginListEntry) => {
    if (!connected || mutatingID || openingConfigID || deletingID) return;

    const name = getPluginTitle(plugin);
    showConfirmation({
      title: t('plugin_management.delete_confirm_title'),
      message: t('plugin_management.delete_confirm_message', { name, id: plugin.id }),
      variant: 'danger',
      confirmText: t('plugin_management.delete_plugin'),
      onConfirm: async () => {
        setDeletingID(plugin.id);
        setMutatingID(plugin.id);
        try {
          const result = await pluginsApi.deletePlugin(plugin.id);
          clearConfigCache();
          if (editingPlugin?.id === plugin.id) {
            setEditingPlugin(null);
            setDraft(null);
          }
          await load();
          onMutatedRef.current();
          notifyPluginResourcesChanged();
          showNotification(t('plugin_management.delete_success'), 'success');
          if (result.restartRequired) {
            showNotification(t('plugin_management.delete_restart_required'), 'warning');
          }
        } catch (err: unknown) {
          const restartRequired = hasRestartRequiredError(err);
          const fallback = restartRequired
            ? t('plugin_management.delete_restart_required')
            : t('plugin_management.delete_failed');
          showNotification(
            `${t('plugin_management.delete_failed')}: ${getErrorMessage(err, fallback)}`,
            restartRequired ? 'warning' : 'error'
          );
        } finally {
          setDeletingID('');
          setMutatingID('');
        }
      },
    });
  };

  const saveConfig = async () => {
    if (!editingPlugin || !draft || openingConfigID || mutatingID || deletingID) return;
    const { patch, errors } = buildPluginConfigPatch(draft, editingPlugin.configFields, t);

    if (Object.keys(errors).length > 0) {
      setDraft({ ...draft, errors });
      showNotification(t('plugin_management.validation_failed'), 'warning');
      return;
    }

    if (Object.keys(patch).length === 0) {
      setEditingPlugin(null);
      setDraft(null);
      showNotification(t('plugin_management.save_success'), 'success');
      return;
    }

    setMutatingID(editingPlugin.id);
    try {
      await pluginsApi.patchConfig(editingPlugin.id, patch);
      clearConfigCache();
      const enabledChanged =
        typeof patch.enabled === 'boolean' && patch.enabled !== editingPlugin.enabled;
      const status = enabledChanged
        ? await waitForPluginRuntimeState(editingPlugin.id, patch.enabled === true)
        : await load().then((): PluginRuntimeWaitStatus => 'ready');
      if (status === 'ready') {
        notifyPluginResourcesChanged();
      }
      onMutatedRef.current();
      setEditingPlugin(null);
      setDraft(null);
      if (status === 'ready') {
        showNotification(t('plugin_management.save_success'), 'success');
      } else {
        showNotification(
          t(
            status === 'globalDisabled'
              ? 'plugin_management.global_disabled_hint'
              : 'plugin_management.runtime_pending'
          ),
          'warning'
        );
      }
    } catch (err: unknown) {
      showNotification(
        `${t('plugin_management.save_failed')}: ${getErrorMessage(
          err,
          t('plugin_management.save_failed')
        )}`,
        'error'
      );
    } finally {
      setMutatingID('');
    }
  };

  return {
    data,
    loading,
    error,
    load,
    editingPlugin,
    draft,
    mutatingID,
    deletingID,
    pendingToggle,
    actionBusy: Boolean(mutatingID || openingConfigID || deletingID),
    savingConfig: Boolean(editingPlugin && mutatingID === editingPlugin.id),
    openConfigSheet,
    closeConfigSheet,
    updateDraft,
    togglePlugin,
    deletePlugin,
    saveConfig,
  };
}
