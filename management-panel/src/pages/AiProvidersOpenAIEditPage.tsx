import { useEffect } from 'react';
import { useNavigate, useOutletContext } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { Input } from '@/components/ui/Input';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { SecondaryScreenShell } from '@/components/common/SecondaryScreenShell';
import { useEdgeSwipeBack } from '@/hooks/useEdgeSwipeBack';
import { useNotificationStore } from '@/stores';
import { apiCallApi, getApiCallErrorMessage } from '@/services/api';
import type { ApiKeyEntry } from '@/types';
import { buildHeaderObject } from '@/utils/headers';
import { buildApiKeyEntry, buildOpenAIChatCompletionsEndpoint } from '@/components/providers/utils';
import type { OpenAIEditOutletContext } from './AiProvidersOpenAIEditLayout';
import styles from './AiProvidersPage.module.scss';
import layoutStyles from './AiProvidersEditLayout.module.scss';

const OPENAI_TEST_TIMEOUT_MS = 30_000;

const getErrorMessage = (err: unknown) => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
};

export function AiProvidersOpenAIEditPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { showNotification } = useNotificationStore();
  const {
    hasIndexParam,
    invalidIndexParam,
    invalidIndex,
    disableControls,
    loading,
    saving,
    form,
    setForm,
    testModel,
    setTestModel,
    testStatus,
    setTestStatus,
    testMessage,
    setTestMessage,
    availableModels,
    handleBack,
    handleSave,
  } = useOutletContext<OpenAIEditOutletContext>();

  const title = hasIndexParam
    ? t('ai_providers.openai_edit_modal_title')
    : t('ai_providers.openai_add_modal_title');

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

  const canSave = !disableControls && !loading && !saving && !invalidIndexParam && !invalidIndex;

  const renderKeyEntries = (entries: ApiKeyEntry[]) => {
    const list = entries.length ? entries : [buildApiKeyEntry()];

    const updateEntry = (idx: number, field: keyof ApiKeyEntry, value: string) => {
      const next = list.map((entry, i) => (i === idx ? { ...entry, [field]: value } : entry));
      setForm((prev) => ({ ...prev, apiKeyEntries: next }));
    };

    const removeEntry = (idx: number) => {
      const next = list.filter((_, i) => i !== idx);
      setForm((prev) => ({
        ...prev,
        apiKeyEntries: next.length ? next : [buildApiKeyEntry()],
      }));
    };

    const addEntry = () => {
      setForm((prev) => ({ ...prev, apiKeyEntries: [...list, buildApiKeyEntry()] }));
    };

    return (
      <div className="stack">
        {list.map((entry, index) => (
          <div key={index} className="item-row">
            <div className="item-meta">
              <Input
                label={`${t('common.api_key')} #${index + 1}`}
                value={entry.apiKey}
                onChange={(e) => updateEntry(index, 'apiKey', e.target.value)}
                disabled={saving || disableControls}
              />
              <Input
                label={t('common.proxy_url')}
                value={entry.proxyUrl ?? ''}
                onChange={(e) => updateEntry(index, 'proxyUrl', e.target.value)}
                disabled={saving || disableControls}
              />
            </div>
            <div className="item-actions">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => removeEntry(index)}
                disabled={saving || disableControls || list.length <= 1}
              >
                {t('common.delete')}
              </Button>
            </div>
          </div>
        ))}
        <Button
          variant="secondary"
          size="sm"
          onClick={addEntry}
          disabled={saving || disableControls}
        >
          {t('ai_providers.openai_keys_add_btn')}
        </Button>
      </div>
    );
  };

  const openOpenaiModelDiscovery = () => {
    const baseUrl = form.baseUrl.trim();
    if (!baseUrl) {
      showNotification(t('ai_providers.openai_models_fetch_invalid_url'), 'error');
      return;
    }
    navigate('models');
  };

  const testOpenaiProviderConnection = async () => {
    const baseUrl = form.baseUrl.trim();
    if (!baseUrl) {
      const message = t('notification.openai_test_url_required');
      setTestStatus('error');
      setTestMessage(message);
      showNotification(message, 'error');
      return;
    }

    const endpoint = buildOpenAIChatCompletionsEndpoint(baseUrl);
    if (!endpoint) {
      const message = t('notification.openai_test_url_required');
      setTestStatus('error');
      setTestMessage(message);
      showNotification(message, 'error');
      return;
    }

    const firstKeyEntry = form.apiKeyEntries.find((entry) => entry.apiKey?.trim());
    if (!firstKeyEntry) {
      const message = t('notification.openai_test_key_required');
      setTestStatus('error');
      setTestMessage(message);
      showNotification(message, 'error');
      return;
    }

    const modelName = testModel.trim() || availableModels[0] || '';
    if (!modelName) {
      const message = t('notification.openai_test_model_required');
      setTestStatus('error');
      setTestMessage(message);
      showNotification(message, 'error');
      return;
    }

    const customHeaders = buildHeaderObject(form.headers);
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      ...customHeaders,
    };
    if (!headers.Authorization && !headers['authorization']) {
      headers.Authorization = `Bearer ${firstKeyEntry.apiKey.trim()}`;
    }

    setTestStatus('loading');
    setTestMessage(t('ai_providers.openai_test_running'));

    try {
      const result = await apiCallApi.request(
        {
          method: 'POST',
          url: endpoint,
          header: Object.keys(headers).length ? headers : undefined,
          data: JSON.stringify({
            model: modelName,
            messages: [{ role: 'user', content: 'Hi' }],
            stream: false,
            max_tokens: 5,
          }),
        },
        { timeout: OPENAI_TEST_TIMEOUT_MS }
      );

      if (result.statusCode < 200 || result.statusCode >= 300) {
        throw new Error(getApiCallErrorMessage(result));
      }

      setTestStatus('success');
      setTestMessage(t('ai_providers.openai_test_success'));
    } catch (err: unknown) {
      setTestStatus('error');
      const message = getErrorMessage(err);
      const errorCode =
        typeof err === 'object' && err !== null && 'code' in err
          ? String((err as { code?: string }).code)
          : '';
      const isTimeout = errorCode === 'ECONNABORTED' || message.toLowerCase().includes('timeout');
      if (isTimeout) {
        setTestMessage(
          t('ai_providers.openai_test_timeout', { seconds: OPENAI_TEST_TIMEOUT_MS / 1000 })
        );
      } else {
        setTestMessage(`${t('ai_providers.openai_test_failed')}: ${message}`);
      }
    }
  };

  return (
    <SecondaryScreenShell
      ref={swipeRef}
      contentClassName={layoutStyles.content}
      title={title}
      onBack={handleBack}
      backLabel={t('common.back')}
      backAriaLabel={t('common.back')}
      rightAction={
        <Button size="sm" onClick={() => void handleSave()} loading={saving} disabled={!canSave}>
          {t('common.save')}
        </Button>
      }
      isLoading={loading}
      loadingLabel={t('common.loading')}
    >
      <Card>
        {invalidIndexParam || invalidIndex ? (
          <div className="hint">Invalid provider index.</div>
        ) : (
          <>
            <Input
              label={t('ai_providers.openai_add_modal_name_label')}
              value={form.name}
              onChange={(e) => setForm((prev) => ({ ...prev, name: e.target.value }))}
              disabled={saving || disableControls}
            />
            <Input
              label={t('ai_providers.prefix_label')}
              placeholder={t('ai_providers.prefix_placeholder')}
              value={form.prefix ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, prefix: e.target.value }))}
              hint={t('ai_providers.prefix_hint')}
              disabled={saving || disableControls}
            />
            <Input
              label={t('ai_providers.openai_add_modal_url_label')}
              value={form.baseUrl}
              onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
              disabled={saving || disableControls}
            />

            <HeaderInputList
              entries={form.headers}
              onChange={(entries) => setForm((prev) => ({ ...prev, headers: entries }))}
              addLabel={t('common.custom_headers_add')}
              keyPlaceholder={t('common.custom_headers_key_placeholder')}
              valuePlaceholder={t('common.custom_headers_value_placeholder')}
              disabled={saving || disableControls}
            />

            <div className="form-group">
              <label>
                {hasIndexParam
                  ? t('ai_providers.openai_edit_modal_models_label')
                  : t('ai_providers.openai_add_modal_models_label')}
              </label>
              <div className="hint">{t('ai_providers.openai_models_hint')}</div>
              <ModelInputList
                entries={form.modelEntries}
                onChange={(entries) => setForm((prev) => ({ ...prev, modelEntries: entries }))}
                addLabel={t('ai_providers.openai_models_add_btn')}
                namePlaceholder={t('common.model_name_placeholder')}
                aliasPlaceholder={t('common.model_alias_placeholder')}
                disabled={saving || disableControls}
              />
              <Button
                variant="secondary"
                size="sm"
                onClick={openOpenaiModelDiscovery}
                disabled={saving || disableControls}
              >
                {t('ai_providers.openai_models_fetch_button')}
              </Button>
            </div>

            <div className="form-group">
              <label>{t('ai_providers.openai_test_title')}</label>
              <div className="hint">{t('ai_providers.openai_test_hint')}</div>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <select
                  className={`input ${styles.openaiTestSelect}`}
                  value={testModel}
                  onChange={(e) => {
                    setTestModel(e.target.value);
                    setTestStatus('idle');
                    setTestMessage('');
                  }}
                  disabled={saving || disableControls || availableModels.length === 0}
                >
                  <option value="">
                    {availableModels.length
                      ? t('ai_providers.openai_test_select_placeholder')
                      : t('ai_providers.openai_test_select_empty')}
                  </option>
                  {form.modelEntries
                    .filter((entry) => entry.name.trim())
                    .map((entry, idx) => {
                      const name = entry.name.trim();
                      const alias = entry.alias.trim();
                      const label = alias && alias !== name ? `${name} (${alias})` : name;
                      return (
                        <option key={`${name}-${idx}`} value={name}>
                          {label}
                        </option>
                      );
                    })}
                </select>
                <Button
                  variant={testStatus === 'error' ? 'danger' : 'secondary'}
                  className={`${styles.openaiTestButton} ${
                    testStatus === 'success' ? styles.openaiTestButtonSuccess : ''
                  }`}
                  onClick={() => void testOpenaiProviderConnection()}
                  loading={testStatus === 'loading'}
                  disabled={saving || disableControls || availableModels.length === 0}
                >
                  {t('ai_providers.openai_test_action')}
                </Button>
              </div>
              {testMessage && (
                <div
                  className={`status-badge ${
                    testStatus === 'error'
                      ? 'error'
                      : testStatus === 'success'
                        ? 'success'
                        : 'muted'
                  }`}
                >
                  {testMessage}
                </div>
              )}
            </div>

            <div className="form-group">
              <label>{t('ai_providers.openai_add_modal_keys_label')}</label>
              {renderKeyEntries(form.apiKeyEntries)}
            </div>
          </>
        )}
      </Card>
    </SecondaryScreenShell>
  );
}
