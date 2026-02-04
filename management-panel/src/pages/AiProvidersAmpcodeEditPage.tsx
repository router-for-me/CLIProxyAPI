import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { useEdgeSwipeBack } from '@/hooks/useEdgeSwipeBack';
import { SecondaryScreenShell } from '@/components/common/SecondaryScreenShell';
import { ampcodeApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import type { AmpcodeConfig } from '@/types';
import { maskApiKey } from '@/utils/format';
import { buildAmpcodeFormState, entriesToAmpcodeMappings } from '@/components/providers/utils';
import type { AmpcodeFormState } from '@/components/providers';
import layoutStyles from './AiProvidersEditLayout.module.scss';

type LocationState = { fromAiProviders?: boolean } | null;

const getErrorMessage = (err: unknown) => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
};

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
  const [mappingsDirty, setMappingsDirty] = useState(false);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
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
    setMappingsDirty(false);
    setError('');
    setForm(buildAmpcodeFormState(useConfigStore.getState().config?.ampcode ?? null));

    void (async () => {
      try {
        const ampcode = await ampcodeApi.getAmpcode();
        if (!mountedRef.current) return;

        setLoaded(true);
        updateConfigValue('ampcode', ampcode);
        clearCache('ampcode');
        setForm(buildAmpcodeFormState(ampcode));
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
      const modelMappings = entriesToAmpcodeMappings(form.mappingEntries);

      if (upstreamUrl) {
        await ampcodeApi.updateUpstreamUrl(upstreamUrl);
      } else {
        await ampcodeApi.clearUpstreamUrl();
      }

      await ampcodeApi.updateForceModelMappings(form.forceModelMappings);

      if (loaded || mappingsDirty) {
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
        upstreamUrl: upstreamUrl || undefined,
        forceModelMappings: form.forceModelMappings,
      };

      if (previous.upstreamApiKey) {
        next.upstreamApiKey = previous.upstreamApiKey;
      }

      if (Array.isArray(previous.modelMappings)) {
        next.modelMappings = previous.modelMappings;
      }

      if (overrideKey) {
        next.upstreamApiKey = overrideKey;
      }

      if (loaded || mappingsDirty) {
        if (modelMappings.length) {
          next.modelMappings = modelMappings;
        } else {
          delete next.modelMappings;
        }
      }

      updateConfigValue('ampcode', next);
      clearCache('ampcode');
      showNotification(t('notification.ampcode_updated'), 'success');
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
    if (!loaded && mappingsDirty) {
      showConfirmation({
        title: t('ai_providers.ampcode_mappings_overwrite_title', { defaultValue: 'Overwrite Mappings' }),
        message: t('ai_providers.ampcode_mappings_overwrite_confirm'),
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
      rightAction={
        <Button size="sm" onClick={() => void saveAmpcode()} loading={saving} disabled={!canSave}>
          {t('common.save')}
        </Button>
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
        <div
          style={{
            display: 'flex',
            gap: 8,
            alignItems: 'center',
            marginTop: -8,
            marginBottom: 12,
            flexWrap: 'wrap',
          }}
        >
          <div className="hint" style={{ margin: 0 }}>
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
              setMappingsDirty(true);
              setForm((prev) => ({ ...prev, mappingEntries: entries }));
            }}
            addLabel={t('ai_providers.ampcode_model_mappings_add_btn')}
            namePlaceholder={t('ai_providers.ampcode_model_mappings_from_placeholder')}
            aliasPlaceholder={t('ai_providers.ampcode_model_mappings_to_placeholder')}
            disabled={loading || saving || disableControls}
          />
          <div className="hint">{t('ai_providers.ampcode_model_mappings_hint')}</div>
        </div>
      </Card>
    </SecondaryScreenShell>
  );
}
