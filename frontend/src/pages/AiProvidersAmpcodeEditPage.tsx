import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { useEdgeSwipeBack } from '@/hooks/useEdgeSwipeBack';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import { SecondaryScreenShell } from '@/components/common/SecondaryScreenShell';
import { ampcodeApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import type { AmpcodeConfig } from '@/types';
import { maskApiKey } from '@/utils/format';
import {
  buildAmpcodeFormState,
  entriesToAmpcodeMappings,
  entriesToAmpcodeUpstreamApiKeys,
} from '@/components/providers/utils';
import type { AmpcodeFormState } from '@/components/providers';
import layoutStyles from './AiProvidersEditLayout.module.scss';

type LocationState = { fromAiProviders?: boolean } | null;

const getErrorMessage = (err: unknown) => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
};

const normalizeMappingEntries = (entries: Array<{ name: string; alias: string }>) =>
  (entries ?? []).reduce<Array<{ from: string; to: string }>>((acc, entry) => {
    const from = String(entry?.name ?? '').trim();
    const to = String(entry?.alias ?? '').trim();
    if (!from && !to) return acc;
    acc.push({ from, to });
    return acc;
  }, []);

const normalizeUpstreamApiKeyEntries = (form: AmpcodeFormState) =>
  entriesToAmpcodeUpstreamApiKeys(form.upstreamApiKeyEntries).map((entry) => ({
    upstreamApiKey: entry.upstreamApiKey,
    apiKeys: entry.apiKeys,
  }));

const buildAmpcodeSignature = (form: AmpcodeFormState) =>
  JSON.stringify({
    upstreamUrl: String(form.upstreamUrl ?? '').trim(),
    upstreamApiKey: String(form.upstreamApiKey ?? '').trim(),
    forceModelMappings: Boolean(form.forceModelMappings),
    upstreamApiKeys: normalizeUpstreamApiKeyEntries(form),
    modelMappings: normalizeMappingEntries(form.mappingEntries),
  });

export function AiProvidersAmpcodeEditPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const disableControls = connectionStatus !== 'connected';

  const config = useConfigStore((state) => state.config);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);

  const [form, setForm] = useState<AmpcodeFormState>(() => buildAmpcodeFormState(null));
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [modelMappingsDirty, setModelMappingsDirty] = useState(false);
  const [upstreamApiKeysDirty, setUpstreamApiKeysDirty] = useState(false);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [baselineSignature, setBaselineSignature] = useState(() =>
    buildAmpcodeSignature(buildAmpcodeFormState(null))
  );
  const initializedRef = useRef(false);
  const mountedRef = useRef(false);

  const title = useMemo(() => t('ai_providers.ampcode_modal_title'), [t]);

  const handleBack = useCallback(() => {
    const state = location.state as LocationState;
    if (state?.fromAiProviders) {
      navigate(-1);
      return;
    }
    navigate('/ai-providers', { replace: true });
  }, [location.state, navigate]);

  const swipeRef = useEdgeSwipeBack({ onBack: handleBack });

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        handleBack();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [handleBack]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    if (initializedRef.current) return;
    initializedRef.current = true;

    setLoading(true);
    setLoaded(false);
    setModelMappingsDirty(false);
    setUpstreamApiKeysDirty(false);
    setError('');
    const initialForm = buildAmpcodeFormState(useConfigStore.getState().config?.ampcode ?? null);
    setForm(initialForm);
    setBaselineSignature(buildAmpcodeSignature(initialForm));

    void (async () => {
      try {
        const ampcode = await ampcodeApi.getAmpcode();
        if (!mountedRef.current) return;

        setLoaded(true);
        updateConfigValue('ampcode', ampcode);
        clearCache('ampcode');
        const nextForm = buildAmpcodeFormState(ampcode);
        setForm(nextForm);
        setBaselineSignature(buildAmpcodeSignature(nextForm));
      } catch (err: unknown) {
        if (!mountedRef.current) return;
        setError(getErrorMessage(err) || t('notification.refresh_failed'));
      } finally {
        if (mountedRef.current) {
          setLoading(false);
        }
      }
    })();
  }, [clearCache, t, updateConfigValue]);

  const currentSignature = useMemo(() => buildAmpcodeSignature(form), [form]);
  const isDirty = baselineSignature !== currentSignature;
  const canGuard = !loading && !saving;

  const { allowNextNavigation } = useUnsavedChangesGuard({
    enabled: canGuard,
    shouldBlock: ({ currentLocation, nextLocation }) =>
      isDirty && currentLocation.pathname !== nextLocation.pathname,
    dialog: {
      title: t('common.unsaved_changes_title'),
      message: t('common.unsaved_changes_message'),
      confirmText: t('common.leave'),
      cancelText: t('common.stay'),
      variant: 'danger',
    },
  });

  const clearAmpcodeUpstreamApiKey = async () => {
    showConfirmation({
      title: t('ai_providers.ampcode_clear_upstream_api_key_title', {
        defaultValue: 'Clear Upstream API Key',
      }),
      message: t('ai_providers.ampcode_clear_upstream_api_key_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        setSaving(true);
        setError('');
        try {
          await ampcodeApi.clearUpstreamApiKey();
          const previous = config?.ampcode ?? {};
          const next: AmpcodeConfig = { ...previous };
          delete next.upstreamApiKey;
          updateConfigValue('ampcode', next);
          clearCache('ampcode');
          showNotification(t('notification.ampcode_upstream_api_key_cleared'), 'success');
        } catch (err: unknown) {
          const message = getErrorMessage(err);
          setError(message);
          showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
        } finally {
          setSaving(false);
        }
      },
    });
  };

  const performSaveAmpcode = async () => {
    setSaving(true);
    setError('');
    try {
      const upstreamUrl = form.upstreamUrl.trim();
      const overrideKey = form.upstreamApiKey.trim();
      const upstreamApiKeys = entriesToAmpcodeUpstreamApiKeys(form.upstreamApiKeyEntries);
      const modelMappings = entriesToAmpcodeMappings(form.mappingEntries);

      if (upstreamUrl) {
        await ampcodeApi.updateUpstreamUrl(upstreamUrl);
      } else {
        await ampcodeApi.clearUpstreamUrl();
      }

      await ampcodeApi.updateForceModelMappings(form.forceModelMappings);

      if (loaded || upstreamApiKeysDirty) {
        if (upstreamApiKeys.length) {
          await ampcodeApi.saveUpstreamApiKeys(upstreamApiKeys);
        } else {
          await ampcodeApi.deleteUpstreamApiKeys([]);
        }
      }

      if (loaded || modelMappingsDirty) {
        if (modelMappings.length) {
          await ampcodeApi.saveModelMappings(modelMappings);
        } else {
          await ampcodeApi.clearModelMappings();
        }
      }

      if (overrideKey) {
        await ampcodeApi.updateUpstreamApiKey(overrideKey);
      }

      const previous = config?.ampcode ?? {};
      const next: AmpcodeConfig = {
        ...previous,
        forceModelMappings: form.forceModelMappings,
      };

      if (upstreamUrl) {
        next.upstreamUrl = upstreamUrl;
      } else {
        delete next.upstreamUrl;
      }

      if (overrideKey) {
        next.upstreamApiKey = overrideKey;
      }

      if (loaded || upstreamApiKeysDirty) {
        if (upstreamApiKeys.length) {
          next.upstreamApiKeys = upstreamApiKeys;
        } else {
          delete next.upstreamApiKeys;
        }
      }

      if (loaded || modelMappingsDirty) {
        if (modelMappings.length) {
          next.modelMappings = modelMappings;
        } else {
          delete next.modelMappings;
        }
      }

      updateConfigValue('ampcode', next);
      clearCache('ampcode');
      showNotification(t('notification.ampcode_updated'), 'success');
      allowNextNavigation();
      setBaselineSignature(buildAmpcodeSignature(form));
      handleBack();
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      setError(message);
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setSaving(false);
    }
  };

  const saveAmpcode = async () => {
    if (!loaded && (modelMappingsDirty || upstreamApiKeysDirty)) {
      showConfirmation({
        title: t('ai_providers.ampcode_lists_overwrite_title'),
        message: t('ai_providers.ampcode_lists_overwrite_confirm'),
        variant: 'secondary',
        confirmText: t('common.confirm'),
        onConfirm: performSaveAmpcode,
      });
      return;
    }

    await performSaveAmpcode();
  };

  const canSave = !disableControls && !saving && !loading;

  return (
    <SecondaryScreenShell
      ref={swipeRef}
      contentClassName={layoutStyles.content}
      title={title}
      onBack={handleBack}
      backLabel={t('common.back')}
      backAriaLabel={t('common.back')}
      hideTopBarBackButton
      hideTopBarRightAction
      floatingAction={
        <div className={layoutStyles.floatingActions}>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleBack}
            className={layoutStyles.floatingBackButton}
          >
            {t('common.back')}
          </Button>
          <Button
            size="sm"
            onClick={() => void saveAmpcode()}
            loading={saving}
            disabled={!canSave}
            className={layoutStyles.floatingSaveButton}
          >
            {t('common.save')}
          </Button>
        </div>
      }
      isLoading={loading}
      loadingLabel={t('common.loading')}
    >
      <Card>
        {error && <div className="error-box">{error}</div>}
        <Input
          label={t('ai_providers.ampcode_upstream_url_label')}
          placeholder={t('ai_providers.ampcode_upstream_url_placeholder')}
          value={form.upstreamUrl}
          onChange={(e) => setForm((prev) => ({ ...prev, upstreamUrl: e.target.value }))}
          disabled={loading || saving || disableControls}
          hint={t('ai_providers.ampcode_upstream_url_hint')}
        />
        <Input
          label={t('ai_providers.ampcode_upstream_api_key_label')}
          placeholder={t('ai_providers.ampcode_upstream_api_key_placeholder')}
          type="password"
          value={form.upstreamApiKey}
          onChange={(e) => setForm((prev) => ({ ...prev, upstreamApiKey: e.target.value }))}
          disabled={loading || saving || disableControls}
          hint={t('ai_providers.ampcode_upstream_api_key_hint')}
        />
        <div className={layoutStyles.upstreamApiKeyRow}>
          <div className={layoutStyles.upstreamApiKeyHint}>
            {t('ai_providers.ampcode_upstream_api_key_current', {
              key: config?.ampcode?.upstreamApiKey
                ? maskApiKey(config.ampcode.upstreamApiKey)
                : t('common.not_set'),
            })}
          </div>
          <Button
            variant="danger"
            size="sm"
            onClick={() => void clearAmpcodeUpstreamApiKey()}
            disabled={loading || saving || disableControls || !config?.ampcode?.upstreamApiKey}
          >
            {t('ai_providers.ampcode_clear_upstream_api_key')}
          </Button>
        </div>

        <div className="form-group">
          <div className={layoutStyles.ampcodeUpstreamMappingsHeader}>
            <label>{t('ai_providers.ampcode_upstream_api_keys_label')}</label>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => {
                setUpstreamApiKeysDirty(true);
                setForm((prev) => ({
                  ...prev,
                  upstreamApiKeyEntries: [
                    ...prev.upstreamApiKeyEntries,
                    { upstreamApiKey: '', clientApiKeysText: '' },
                  ],
                }));
              }}
              disabled={loading || saving || disableControls}
            >
              {t('ai_providers.ampcode_upstream_api_keys_add_btn')}
            </Button>
          </div>
          <div className={layoutStyles.ampcodeUpstreamMappingsList}>
            {(form.upstreamApiKeyEntries.length
              ? form.upstreamApiKeyEntries
              : [{ upstreamApiKey: '', clientApiKeysText: '' }]
            ).map((entry, index, entries) => (
              <div key={index} className={layoutStyles.ampcodeUpstreamMappingCard}>
                <div className={layoutStyles.ampcodeUpstreamMappingCardTop}>
                  <span className={layoutStyles.ampcodeUpstreamMappingTitle}>
                    {t('ai_providers.ampcode_upstream_api_keys_item_title', { index: index + 1 })}
                  </span>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => {
                      setUpstreamApiKeysDirty(true);
                      setForm((prev) => {
                        const nextEntries = prev.upstreamApiKeyEntries.filter((_, entryIndex) => entryIndex !== index);
                        return {
                          ...prev,
                          upstreamApiKeyEntries: nextEntries.length
                            ? nextEntries
                            : [{ upstreamApiKey: '', clientApiKeysText: '' }],
                        };
                      });
                    }}
                    disabled={loading || saving || disableControls || entries.length <= 1}
                  >
                    {t('common.delete')}
                  </Button>
                </div>
                <input
                  className="input"
                  placeholder={t('ai_providers.ampcode_upstream_api_keys_upstream_placeholder')}
                  aria-label={t('ai_providers.ampcode_upstream_api_keys_upstream_placeholder')}
                  value={entry.upstreamApiKey}
                  onChange={(e) => {
                    const value = e.target.value;
                    setUpstreamApiKeysDirty(true);
                    setForm((prev) => ({
                      ...prev,
                      upstreamApiKeyEntries: prev.upstreamApiKeyEntries.map((item, itemIndex) =>
                        itemIndex === index ? { ...item, upstreamApiKey: value } : item
                      ),
                    }));
                  }}
                  disabled={loading || saving || disableControls}
                />
                <textarea
                  className="input"
                  placeholder={t('ai_providers.ampcode_upstream_api_keys_clients_placeholder')}
                  aria-label={t('ai_providers.ampcode_upstream_api_keys_clients_placeholder')}
                  value={entry.clientApiKeysText}
                  onChange={(e) => {
                    const value = e.target.value;
                    setUpstreamApiKeysDirty(true);
                    setForm((prev) => ({
                      ...prev,
                      upstreamApiKeyEntries: prev.upstreamApiKeyEntries.map((item, itemIndex) =>
                        itemIndex === index ? { ...item, clientApiKeysText: value } : item
                      ),
                    }));
                  }}
                  rows={3}
                  disabled={loading || saving || disableControls}
                />
              </div>
            ))}
          </div>
          <div className="hint">{t('ai_providers.ampcode_upstream_api_keys_hint')}</div>
        </div>

        <div className="form-group">
          <ToggleSwitch
            label={t('ai_providers.ampcode_force_model_mappings_label')}
            checked={form.forceModelMappings}
            onChange={(value) => setForm((prev) => ({ ...prev, forceModelMappings: value }))}
            disabled={loading || saving || disableControls}
          />
          <div className="hint">{t('ai_providers.ampcode_force_model_mappings_hint')}</div>
        </div>

        <div className="form-group">
          <label>{t('ai_providers.ampcode_model_mappings_label')}</label>
          <ModelInputList
            entries={form.mappingEntries}
            onChange={(entries) => {
              setModelMappingsDirty(true);
              setForm((prev) => ({ ...prev, mappingEntries: entries }));
            }}
            addLabel={t('ai_providers.ampcode_model_mappings_add_btn')}
            namePlaceholder={t('ai_providers.ampcode_model_mappings_from_placeholder')}
            aliasPlaceholder={t('ai_providers.ampcode_model_mappings_to_placeholder')}
            removeButtonTitle={t('common.delete')}
            removeButtonAriaLabel={t('common.delete')}
            disabled={loading || saving || disableControls}
          />
          <div className="hint">{t('ai_providers.ampcode_model_mappings_hint')}</div>
        </div>
      </Card>
    </SecondaryScreenShell>
  );
}
