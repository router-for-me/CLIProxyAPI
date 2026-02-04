import { useCallback, useEffect, useMemo, useState } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { modelsToEntries } from '@/components/ui/modelInputListUtils';
import { useEdgeSwipeBack } from '@/hooks/useEdgeSwipeBack';
import { SecondaryScreenShell } from '@/components/common/SecondaryScreenShell';
import { providersApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import type { ProviderKeyConfig } from '@/types';
import { buildHeaderObject, headersToEntries } from '@/utils/headers';
import type { VertexFormState } from '@/components/providers';
import layoutStyles from './AiProvidersEditLayout.module.scss';

type LocationState = { fromAiProviders?: boolean } | null;

const buildEmptyForm = (): VertexFormState => ({
  apiKey: '',
  prefix: '',
  baseUrl: '',
  proxyUrl: '',
  headers: [],
  models: [],
  modelEntries: [{ name: '', alias: '' }],
});

const parseIndexParam = (value: string | undefined) => {
  if (!value) return null;
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) ? parsed : null;
};

export function AiProvidersVertexEditPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const params = useParams<{ index?: string }>();

  const { showNotification } = useNotificationStore();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const disableControls = connectionStatus !== 'connected';

  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);

  const [configs, setConfigs] = useState<ProviderKeyConfig[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [form, setForm] = useState<VertexFormState>(() => buildEmptyForm());

  const hasIndexParam = typeof params.index === 'string';
  const editIndex = useMemo(() => parseIndexParam(params.index), [params.index]);
  const invalidIndexParam = hasIndexParam && editIndex === null;

  const initialData = useMemo(() => {
    if (editIndex === null) return undefined;
    return configs[editIndex];
  }, [configs, editIndex]);

  const invalidIndex = editIndex !== null && !initialData;

  const title =
    editIndex !== null ? t('ai_providers.vertex_edit_modal_title') : t('ai_providers.vertex_add_modal_title');

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
    let cancelled = false;
    setLoading(true);
    setError('');

    Promise.all([fetchConfig('vertex-api-key'), providersApi.getVertexConfigs()])
      .then(([configResult, vertexResult]) => {
        if (cancelled) return;

        const list = Array.isArray(vertexResult)
          ? (vertexResult as ProviderKeyConfig[])
          : Array.isArray(configResult)
            ? (configResult as ProviderKeyConfig[])
            : [];
        setConfigs(list);
        updateConfigValue('vertex-api-key', list);
        clearCache('vertex-api-key');
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const message = err instanceof Error ? err.message : '';
        setError(message || t('notification.refresh_failed'));
      })
      .finally(() => {
        if (cancelled) return;
        setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [clearCache, fetchConfig, t, updateConfigValue]);

  useEffect(() => {
    if (loading) return;

    if (initialData) {
      setForm({
        ...initialData,
        headers: headersToEntries(initialData.headers),
        modelEntries: modelsToEntries(initialData.models),
      });
      return;
    }
    setForm(buildEmptyForm());
  }, [initialData, loading]);

  const canSave = !disableControls && !saving && !loading && !invalidIndexParam && !invalidIndex;

  const handleSave = useCallback(async () => {
    if (!canSave) return;

    const trimmedBaseUrl = (form.baseUrl ?? '').trim();
    const baseUrl = trimmedBaseUrl || undefined;
    if (!baseUrl) {
      showNotification(t('notification.vertex_base_url_required'), 'error');
      return;
    }

    setSaving(true);
    setError('');
    try {
      const payload: ProviderKeyConfig = {
        apiKey: form.apiKey.trim(),
        prefix: form.prefix?.trim() || undefined,
        baseUrl,
        proxyUrl: form.proxyUrl?.trim() || undefined,
        headers: buildHeaderObject(form.headers),
        models: form.modelEntries
          .map((entry) => {
            const name = entry.name.trim();
            const alias = entry.alias.trim();
            if (!name || !alias) return null;
            return { name, alias };
          })
          .filter(Boolean) as ProviderKeyConfig['models'],
      };

      const nextList =
        editIndex !== null
          ? configs.map((item, idx) => (idx === editIndex ? payload : item))
          : [...configs, payload];

      await providersApi.saveVertexConfigs(nextList);
      updateConfigValue('vertex-api-key', nextList);
      clearCache('vertex-api-key');
      showNotification(
        editIndex !== null ? t('notification.vertex_config_updated') : t('notification.vertex_config_added'),
        'success'
      );
      handleBack();
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : '';
      setError(message);
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [
    canSave,
    clearCache,
    configs,
    editIndex,
    form,
    handleBack,
    showNotification,
    t,
    updateConfigValue,
  ]);

  return (
    <SecondaryScreenShell
      ref={swipeRef}
      contentClassName={layoutStyles.content}
      title={title}
      onBack={handleBack}
      backLabel={t('common.back')}
      backAriaLabel={t('common.back')}
      rightAction={
        <Button size="sm" onClick={handleSave} loading={saving} disabled={!canSave}>
          {t('common.save')}
        </Button>
      }
      isLoading={loading}
      loadingLabel={t('common.loading')}
    >
      <Card>
        {error && <div className="error-box">{error}</div>}
        {invalidIndexParam || invalidIndex ? (
          <div className="hint">Invalid provider index.</div>
        ) : (
          <>
            <Input
              label={t('ai_providers.vertex_add_modal_key_label')}
              placeholder={t('ai_providers.vertex_add_modal_key_placeholder')}
              value={form.apiKey}
              onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
              disabled={disableControls || saving}
            />
            <Input
              label={t('ai_providers.prefix_label')}
              placeholder={t('ai_providers.prefix_placeholder')}
              value={form.prefix ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, prefix: e.target.value }))}
              hint={t('ai_providers.prefix_hint')}
              disabled={disableControls || saving}
            />
            <Input
              label={t('ai_providers.vertex_add_modal_url_label')}
              placeholder={t('ai_providers.vertex_add_modal_url_placeholder')}
              value={form.baseUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
              disabled={disableControls || saving}
            />
            <Input
              label={t('ai_providers.vertex_add_modal_proxy_label')}
              placeholder={t('ai_providers.vertex_add_modal_proxy_placeholder')}
              value={form.proxyUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, proxyUrl: e.target.value }))}
              disabled={disableControls || saving}
            />
            <HeaderInputList
              entries={form.headers}
              onChange={(entries) => setForm((prev) => ({ ...prev, headers: entries }))}
              addLabel={t('common.custom_headers_add')}
              keyPlaceholder={t('common.custom_headers_key_placeholder')}
              valuePlaceholder={t('common.custom_headers_value_placeholder')}
              disabled={disableControls || saving}
            />
            <div className="form-group">
              <label>{t('ai_providers.vertex_models_label')}</label>
              <ModelInputList
                entries={form.modelEntries}
                onChange={(entries) => setForm((prev) => ({ ...prev, modelEntries: entries }))}
                addLabel={t('ai_providers.vertex_models_add_btn')}
                namePlaceholder={t('common.model_name_placeholder')}
                aliasPlaceholder={t('common.model_alias_placeholder')}
                disabled={disableControls || saving}
              />
              <div className="hint">{t('ai_providers.vertex_models_hint')}</div>
            </div>
          </>
        )}
      </Card>
    </SecondaryScreenShell>
  );
}
