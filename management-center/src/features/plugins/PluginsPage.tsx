import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { Sheet } from '@/components/ui/Sheet';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconGithub,
  IconPlug,
  IconRefreshCw,
  IconSearch,
  IconSettings,
  IconSidebarStore,
  IconTrash2,
} from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { pluginsApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import { getErrorMessage, isRecord } from '@/utils/helpers';
import type {
  PluginConfigField,
  PluginListEntry,
  PluginListResponse,
} from '@/types';
import {
  buildPluginConfigDraft,
  buildPluginConfigPatch,
  normalizePluginConfigFieldType,
  type PluginConfigDraft,
} from './pluginConfigDraft';
import {
  getPluginTitle,
  notifyPluginResourcesChanged,
  resolvePluginAssetURL,
} from './pluginResources';
import { waitForPluginState } from './pluginPolling';
import styles from './PluginsPage.module.scss';

type PluginRuntimeWaitStatus = 'ready' | 'globalDisabled' | 'timeout';

function PluginCardLogo({ src }: { src: string }) {
  const [failed, setFailed] = useState(false);
  const showImage = Boolean(src) && !failed;

  return showImage ? (
    <img src={src} alt="" onError={() => setFailed(true)} />
  ) : (
    <IconPlug size={18} />
  );
}

const hasStatus = (error: unknown, status: number) => isRecord(error) && error.status === status;

const hasRestartRequired = (value: unknown) => isRecord(value) && value.restart_required === true;

const hasRestartRequiredError = (error: unknown) =>
  isRecord(error) && (hasRestartRequired(error.details) || hasRestartRequired(error.data));

export function PluginsPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const clearConfigCache = useConfigStore((state) => state.clearCache);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);

  const [data, setData] = useState<PluginListResponse | null>(null);
  const [filter, setFilter] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [editingPlugin, setEditingPlugin] = useState<PluginListEntry | null>(null);
  const [draft, setDraft] = useState<PluginConfigDraft | null>(null);
  const [mutatingID, setMutatingID] = useState('');
  const [deletingID, setDeletingID] = useState('');
  const [openingConfigID, setOpeningConfigID] = useState('');
  const configRequestSeq = useRef(0);

  const connected = connectionStatus === 'connected';

  const loadPlugins = useCallback(async () => {
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

  useHeaderRefresh(loadPlugins, connected);

  useEffect(() => {
    void loadPlugins();
  }, [loadPlugins]);

  const pluginStats = useMemo(() => {
    const plugins = data?.plugins ?? [];
    return {
      discovered: plugins.length,
      registered: plugins.filter((plugin) => plugin.registered).length,
      configured: plugins.filter((plugin) => plugin.configured).length,
      effective: plugins.filter((plugin) => plugin.effectiveEnabled).length,
    };
  }, [data?.plugins]);

  const visiblePlugins = useMemo(() => {
    const query = filter.trim().toLowerCase();
    const plugins = data?.plugins ?? [];
    if (!query) return plugins;

    return plugins.filter((plugin) => {
      const haystack = [
        plugin.id,
        plugin.path,
        plugin.metadata?.name,
        plugin.metadata?.author,
        plugin.metadata?.version,
        plugin.metadata?.githubRepository,
        ...plugin.menus.map((menu) => `${menu.menu} ${menu.path} ${menu.description}`),
      ]
        .filter(Boolean)
        .join(' ')
        .toLowerCase();
      return haystack.includes(query);
    });
  }, [data?.plugins, filter]);

  const resolvePluginAsset = useCallback(
    (value: string) => resolvePluginAssetURL(value, apiBase),
    [apiBase]
  );

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
    if (mutatingID || openingConfigID || deletingID) return;
    setEditingPlugin(null);
    setDraft(null);
  };

  const updateDraft = (updater: (current: PluginConfigDraft) => PluginConfigDraft) => {
    setDraft((current) => (current ? updater(current) : current));
  };

  const handleTogglePlugin = async (plugin: PluginListEntry, enabled: boolean) => {
    if (deletingID) return;
    setMutatingID(plugin.id);
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
      setMutatingID('');
    }
  };

  const handleDeletePlugin = (plugin: PluginListEntry) => {
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
          await loadPlugins();
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

  const handleSaveConfig = async () => {
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
        : await loadPlugins().then((): PluginRuntimeWaitStatus => 'ready');
      if (status === 'ready') {
        notifyPluginResourcesChanged();
      }
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

  const handleFieldTextChange =
    (fieldName: string) => (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
      const value = event.target.value;
      updateDraft((current) => ({
        ...current,
        values: { ...current.values, [fieldName]: value },
        errors: { ...current.errors, [fieldName]: '' },
        touchedFields: { ...current.touchedFields, [fieldName]: true },
      }));
    };

  const handleFieldBooleanChange = (fieldName: string, value: boolean) => {
    updateDraft((current) => ({
      ...current,
      values: { ...current.values, [fieldName]: value },
      errors: { ...current.errors, [fieldName]: '' },
      touchedFields: { ...current.touchedFields, [fieldName]: true },
    }));
  };

  const handlePriorityChange = (event: ChangeEvent<HTMLInputElement>) => {
    const value = event.target.value;
    updateDraft((current) => ({
      ...current,
      priority: value,
      errors: { ...current.errors, priority: '' },
      priorityTouched: true,
    }));
  };

  const renderFieldEditor = (field: PluginConfigField) => {
    if (!draft) return null;
    const fieldType = normalizePluginConfigFieldType(field);
    const value = draft.values[field.name];
    const textValue = typeof value === 'string' ? value : '';
    const errorText = draft.errors[field.name];

    if (fieldType === 'boolean') {
      return (
        <div key={field.name} className={styles.fieldRow}>
          <div className={styles.fieldText}>
            <div className={styles.fieldLabel}>{field.name}</div>
            {field.description ? (
              <div className={styles.fieldDescription}>{field.description}</div>
            ) : null}
          </div>
          <ToggleSwitch
            checked={value === true}
            onChange={(nextValue) => handleFieldBooleanChange(field.name, nextValue)}
            ariaLabel={field.name}
          />
        </div>
      );
    }

    if (fieldType === 'enum' && field.enumValues.length > 0) {
      return (
        <div key={field.name} className={styles.formField}>
          <label htmlFor={`plugin-field-${field.name}`}>{field.name}</label>
          <Select
            id={`plugin-field-${field.name}`}
            value={textValue}
            options={field.enumValues.map((item) => ({ value: item, label: item }))}
            onChange={(nextValue) =>
              updateDraft((current) => ({
                ...current,
                values: { ...current.values, [field.name]: nextValue },
                errors: { ...current.errors, [field.name]: '' },
                touchedFields: { ...current.touchedFields, [field.name]: true },
              }))
            }
            placeholder={t('plugin_management.select_placeholder')}
          />
          {field.description ? <div className={styles.fieldHint}>{field.description}</div> : null}
          {errorText ? <div className={styles.fieldError}>{errorText}</div> : null}
        </div>
      );
    }

    if (fieldType === 'array' || fieldType === 'object') {
      return (
        <div key={field.name} className={styles.formField}>
          <label htmlFor={`plugin-field-${field.name}`}>{field.name}</label>
          <textarea
            id={`plugin-field-${field.name}`}
            className={styles.textarea}
            value={textValue}
            onChange={handleFieldTextChange(field.name)}
            placeholder={fieldType === 'array' ? '[]' : '{}'}
            spellCheck={false}
          />
          {field.description ? <div className={styles.fieldHint}>{field.description}</div> : null}
          {errorText ? <div className={styles.fieldError}>{errorText}</div> : null}
        </div>
      );
    }

    return (
      <Input
        key={field.name}
        id={`plugin-field-${field.name}`}
        label={field.name}
        value={textValue}
        onChange={handleFieldTextChange(field.name)}
        inputMode={fieldType === 'integer' || fieldType === 'number' ? 'decimal' : undefined}
        hint={field.description || undefined}
        error={errorText || undefined}
      />
    );
  };

  const savingConfig = Boolean(editingPlugin && mutatingID === editingPlugin.id);

  return (
    <div className={styles.page}>
      {/* ── Page Header ── */}
      <div className={styles.pageHeader}>
        <h1 className={styles.title}>{t('plugin_management.title')}</h1>
        <p className={styles.description}>{t('plugin_management.description')}</p>
      </div>

      {/* ── Alerts ── */}
      {error ? <div className={styles.errorBox}>{error}</div> : null}

      {data && !data.pluginsEnabled ? (
        <div className={styles.warningBox}>{t('plugin_management.global_disabled_hint')}</div>
      ) : null}

      {/* ── Status Bar ── */}
      {data ? (
        <div className={styles.statusBar}>
          <div className={styles.statusPill}>
            <span
              className={`${styles.statusDot} ${
                data.pluginsEnabled ? styles.statusDotOn : styles.statusDotOff
              }`}
            />
            <span className={styles.statusLabel}>{t('plugin_management.global_status')}</span>
            <span className={styles.statusValue}>
              {data.pluginsEnabled
                ? t('plugin_management.global_enabled')
                : t('plugin_management.global_disabled')}
            </span>
          </div>

          <span className={styles.statusDivider} />

          <div className={styles.statusPill}>
            <span className={styles.statusLabel}>{t('plugin_management.plugins_dir')}</span>
            <span
              className={`${styles.statusValue} ${styles.statusPathValue}`}
              title={data.pluginsDir || 'plugins'}
            >
              {data.pluginsDir || 'plugins'}
            </span>
          </div>

          <span className={styles.statusDivider} />

          <div className={styles.statusPill}>
            <span className={styles.statusLabel}>{t('plugin_management.discovered')}</span>
            <span className={styles.statusValue}>{pluginStats.discovered}</span>
          </div>

          <span className={styles.statusDivider} />

          <div className={styles.statusPill}>
            <span className={styles.statusLabel}>{t('plugin_management.effective')}</span>
            <span className={styles.statusValue}>
              {pluginStats.effective}/{pluginStats.registered}
            </span>
          </div>
        </div>
      ) : null}

      {/* ── Toolbar ── */}
      <div className={styles.toolbar}>
        <Input
          type="search"
          value={filter}
          onChange={(event) => setFilter(event.target.value)}
          placeholder={t('plugin_management.search_placeholder')}
          aria-label={t('plugin_management.search_label')}
          rightElement={<IconSearch size={16} />}
        />
        <Button
          variant="secondary"
          size="sm"
          onClick={loadPlugins}
          disabled={!connected || loading || Boolean(mutatingID || deletingID)}
          loading={loading}
        >
          <IconRefreshCw size={16} />
          {t('plugin_management.refresh')}
        </Button>
        <Button variant="secondary" size="sm" onClick={() => navigate('/plugin-store')}>
          <IconSidebarStore size={16} />
          {t('plugin_store.title')}
        </Button>
      </div>

      {/* ── Plugin List ── */}
      {loading ? (
        <div className={styles.pluginList}>
          {Array.from({ length: 4 }, (_, index) => (
            <div key={index} className={styles.skeletonRow}>
              <div className={styles.skeletonAvatar} />
              <div className={styles.skeletonText}>
                <div className={styles.skeletonLine} />
                <div className={styles.skeletonLine} />
              </div>
            </div>
          ))}
        </div>
      ) : visiblePlugins.length === 0 ? (
        <EmptyState
          title={t('plugin_management.no_plugins')}
          description={t('plugin_management.no_plugins_desc')}
          action={
            <Button variant="secondary" size="sm" onClick={loadPlugins} disabled={!connected}>
              <IconRefreshCw size={16} />
              {t('plugin_management.refresh')}
            </Button>
          }
        />
      ) : (
        <div className={styles.pluginList}>
          {visiblePlugins.map((plugin) => {
            const logo = resolvePluginAsset(plugin.logo || plugin.metadata?.logo || '');
            const github = plugin.metadata?.githubRepository.trim();
            const openingConfig = openingConfigID === plugin.id;
            const deletingPlugin = deletingID === plugin.id;
            const actionBusy = Boolean(mutatingID || openingConfigID || deletingID);
            const version = plugin.metadata?.version;
            const author = plugin.metadata?.author;

            return (
              <article key={plugin.id} className={styles.pluginRow}>
                {/* Logo */}
                <div className={styles.logoBox} aria-hidden="true">
                  <PluginCardLogo src={logo} />
                </div>

                {/* Info */}
                <div className={styles.pluginInfo}>
                  <div className={styles.pluginName}>
                    <h2>{getPluginTitle(plugin)}</h2>
                    <div className={styles.badgeRow}>
                      <span
                        className={
                          plugin.effectiveEnabled ? styles.badgeSuccess : styles.badgeMuted
                        }
                      >
                        {plugin.effectiveEnabled
                          ? t('plugin_management.status_effective')
                          : t('plugin_management.status_inactive')}
                      </span>
                      <span className={plugin.registered ? styles.badge : styles.badgeWarning}>
                        {plugin.registered
                          ? t('plugin_management.registered')
                          : t('plugin_management.not_registered')}
                      </span>
                      <span className={plugin.configured ? styles.badge : styles.badgeMuted}>
                        {plugin.configured
                          ? t('plugin_management.configured')
                          : t('plugin_management.not_configured')}
                      </span>
                      {plugin.supportsOAuth ? (
                        <span className={styles.badge}>{t('plugin_management.oauth')}</span>
                      ) : null}
                    </div>
                  </div>

                  <span className={styles.pluginId}>{plugin.id}</span>

                  {version || author || plugin.path ? (
                    <div className={styles.pluginMeta}>
                      {version ? (
                        <span className={styles.metaItem}>
                          <strong>{version}</strong>
                        </span>
                      ) : null}
                      {version && author ? (
                        <span className={styles.metaDot} aria-hidden="true" />
                      ) : null}
                      {author ? <span className={styles.metaItem}>{author}</span> : null}
                      {(version || author) && plugin.path ? (
                        <span className={styles.metaDot} aria-hidden="true" />
                      ) : null}
                      {plugin.path ? (
                        <span
                          className={`${styles.metaItem} ${styles.metaPath}`}
                          title={plugin.path}
                        >
                          {plugin.path}
                        </span>
                      ) : null}
                    </div>
                  ) : null}
                </div>

                {/* Actions */}
                <div className={styles.rowActions}>
                  <ToggleSwitch
                    checked={plugin.enabled}
                    onChange={(enabled) => handleTogglePlugin(plugin, enabled)}
                    disabled={!connected || actionBusy}
                    ariaLabel={t('plugin_management.enabled')}
                  />
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => openConfigSheet(plugin)}
                    disabled={!connected || actionBusy}
                    loading={openingConfig}
                  >
                    <IconSettings size={14} />
                    {t('plugin_management.edit_config')}
                  </Button>
                  <Button
                    variant="danger"
                    size="sm"
                    onClick={() => handleDeletePlugin(plugin)}
                    disabled={!connected || actionBusy}
                    loading={deletingPlugin}
                    title={t('plugin_management.delete_plugin')}
                    aria-label={t('plugin_management.delete_plugin')}
                  >
                    <IconTrash2 size={14} />
                    {t('plugin_management.delete_plugin')}
                  </Button>
                  {github ? (
                    <a
                      className={styles.iconLink}
                      href={github}
                      target="_blank"
                      rel="noreferrer"
                      title={t('plugin_management.open_repository')}
                      aria-label={t('plugin_management.open_repository')}
                    >
                      <IconGithub size={14} />
                    </a>
                  ) : null}
                </div>
              </article>
            );
          })}
        </div>
      )}

      {/* ── Config Sheet ── */}
      <Sheet
        open={Boolean(editingPlugin && draft)}
        onClose={closeConfigSheet}
        size="lg"
        title={
          editingPlugin
            ? t('plugin_management.config_title', { name: getPluginTitle(editingPlugin) })
            : t('plugin_management.edit_config')
        }
        description={editingPlugin?.id}
        closeDisabled={savingConfig}
        footer={
          <div className={styles.sheetFooter}>
            <Button variant="secondary" onClick={closeConfigSheet} disabled={savingConfig}>
              {t('common.cancel')}
            </Button>
            <Button onClick={handleSaveConfig} loading={savingConfig}>
              {t('common.save')}
            </Button>
          </div>
        }
      >
        {draft && editingPlugin ? (
          <div className={styles.configForm}>
            <section className={styles.formSection}>
              <h3>{t('plugin_management.base_settings')}</h3>
              <div className={styles.fieldRow}>
                <div className={styles.fieldText}>
                  <div className={styles.fieldLabel}>{t('plugin_management.enabled')}</div>
                  <div className={styles.fieldDescription}>
                    {t('plugin_management.enabled_hint')}
                  </div>
                </div>
                <ToggleSwitch
                  checked={draft.enabled}
                  onChange={(enabled) =>
                    updateDraft((current) => ({
                      ...current,
                      enabled,
                      enabledTouched: true,
                    }))
                  }
                  ariaLabel={t('plugin_management.enabled')}
                />
              </div>
              <Input
                label={t('plugin_management.priority')}
                value={draft.priority}
                onChange={handlePriorityChange}
                inputMode="numeric"
                error={draft.errors.priority || undefined}
              />
            </section>

            <section className={styles.formSection}>
              <h3>{t('plugin_management.config_fields')}</h3>
              {editingPlugin.configFields.length > 0 ? (
                editingPlugin.configFields.map((field) => renderFieldEditor(field))
              ) : (
                <div className={styles.emptyConfig}>{t('plugin_management.no_config_fields')}</div>
              )}
            </section>
          </div>
        ) : null}
      </Sheet>
    </div>
  );
}
