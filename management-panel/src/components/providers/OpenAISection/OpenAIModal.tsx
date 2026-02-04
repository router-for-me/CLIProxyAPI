import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { modelsToEntries } from '@/components/ui/modelInputListUtils';
import { useNotificationStore } from '@/stores';
import { apiCallApi, getApiCallErrorMessage } from '@/services/api';
import type { OpenAIProviderConfig, ApiKeyEntry } from '@/types';
import { buildHeaderObject, headersToEntries } from '@/utils/headers';
import type { ModelInfo } from '@/utils/models';
import styles from '@/pages/AiProvidersPage.module.scss';
import { buildApiKeyEntry, buildOpenAIChatCompletionsEndpoint } from '../utils';
import type { ModelEntry, OpenAIFormState, ProviderModalProps } from '../types';
import { OpenAIDiscoveryModal } from './OpenAIDiscoveryModal';

const OPENAI_TEST_TIMEOUT_MS = 30_000;

interface OpenAIModalProps extends ProviderModalProps<OpenAIProviderConfig, OpenAIFormState> {
  isSaving: boolean;
}

const buildEmptyForm = (): OpenAIFormState => ({
  name: '',
  prefix: '',
  baseUrl: '',
  headers: [],
  apiKeyEntries: [buildApiKeyEntry()],
  modelEntries: [{ name: '', alias: '' }],
  testModel: undefined,
});

export function OpenAIModal({
  isOpen,
  editIndex,
  initialData,
  onClose,
  onSave,
  isSaving,
}: OpenAIModalProps) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const [form, setForm] = useState<OpenAIFormState>(buildEmptyForm);
  const [discoveryOpen, setDiscoveryOpen] = useState(false);
  const [testModel, setTestModel] = useState('');
  const [testStatus, setTestStatus] = useState<'idle' | 'loading' | 'success' | 'error'>('idle');
  const [testMessage, setTestMessage] = useState('');

  const getErrorMessage = (err: unknown) => {
    if (err instanceof Error) return err.message;
    if (typeof err === 'string') return err;
    return '';
  };

  const availableModels = useMemo(
    () => form.modelEntries.map((entry) => entry.name.trim()).filter(Boolean),
    [form.modelEntries]
  );

  useEffect(() => {
    if (!isOpen) {
      setDiscoveryOpen(false);
      return;
    }

    if (initialData) {
      const modelEntries = modelsToEntries(initialData.models);
      setForm({
        name: initialData.name,
        prefix: initialData.prefix ?? '',
        baseUrl: initialData.baseUrl,
        headers: headersToEntries(initialData.headers),
        testModel: initialData.testModel,
        modelEntries,
        apiKeyEntries: initialData.apiKeyEntries?.length
          ? initialData.apiKeyEntries
          : [buildApiKeyEntry()],
      });
      const available = modelEntries.map((entry) => entry.name.trim()).filter(Boolean);
      const initialModel =
        initialData.testModel && available.includes(initialData.testModel)
          ? initialData.testModel
          : available[0] || '';
      setTestModel(initialModel);
    } else {
      setForm(buildEmptyForm());
      setTestModel('');
    }

    setTestStatus('idle');
    setTestMessage('');
    setDiscoveryOpen(false);
  }, [initialData, isOpen]);

  useEffect(() => {
    if (!isOpen) return;
    if (availableModels.length === 0) {
      if (testModel) {
        setTestModel('');
        setTestStatus('idle');
        setTestMessage('');
      }
      return;
    }

    if (!testModel || !availableModels.includes(testModel)) {
      setTestModel(availableModels[0]);
      setTestStatus('idle');
      setTestMessage('');
    }
  }, [availableModels, isOpen, testModel]);

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
              />
              <Input
                label={t('common.proxy_url')}
                value={entry.proxyUrl ?? ''}
                onChange={(e) => updateEntry(index, 'proxyUrl', e.target.value)}
              />
            </div>
            <div className="item-actions">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => removeEntry(index)}
                disabled={list.length <= 1 || isSaving}
              >
                {t('common.delete')}
              </Button>
            </div>
          </div>
        ))}
        <Button variant="secondary" size="sm" onClick={addEntry} disabled={isSaving}>
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
    setDiscoveryOpen(true);
  };

  const applyOpenaiModelDiscoverySelection = (selectedModels: ModelInfo[]) => {
    if (!selectedModels.length) {
      setDiscoveryOpen(false);
      return;
    }

    const mergedMap = new Map<string, ModelEntry>();
    form.modelEntries.forEach((entry) => {
      const name = entry.name.trim();
      if (!name) return;
      mergedMap.set(name, { name, alias: entry.alias?.trim() || '' });
    });

    let addedCount = 0;
    selectedModels.forEach((model) => {
      const name = model.name.trim();
      if (!name || mergedMap.has(name)) return;
      mergedMap.set(name, { name, alias: model.alias ?? '' });
      addedCount += 1;
    });

    const mergedEntries = Array.from(mergedMap.values());
    setForm((prev) => ({
      ...prev,
      modelEntries: mergedEntries.length ? mergedEntries : [{ name: '', alias: '' }],
    }));

    setDiscoveryOpen(false);
    if (addedCount > 0) {
      showNotification(t('ai_providers.openai_models_fetch_added', { count: addedCount }), 'success');
    }
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
        typeof err === 'object' && err !== null && 'code' in err ? String((err as { code?: string }).code) : '';
      const isTimeout =
        errorCode === 'ECONNABORTED' || message.toLowerCase().includes('timeout');
      if (isTimeout) {
        setTestMessage(t('ai_providers.openai_test_timeout', { seconds: OPENAI_TEST_TIMEOUT_MS / 1000 }));
      } else {
        setTestMessage(`${t('ai_providers.openai_test_failed')}: ${message}`);
      }
    }
  };

  return (
    <>
      <Modal
        open={isOpen}
        onClose={onClose}
        title={
          editIndex !== null
            ? t('ai_providers.openai_edit_modal_title')
            : t('ai_providers.openai_add_modal_title')
        }
        footer={
          <>
            <Button variant="secondary" onClick={onClose} disabled={isSaving}>
              {t('common.cancel')}
            </Button>
            <Button onClick={() => void onSave(form, editIndex)} loading={isSaving}>
              {t('common.save')}
            </Button>
          </>
        }
      >
        <Input
          label={t('ai_providers.openai_add_modal_name_label')}
          value={form.name}
          onChange={(e) => setForm((prev) => ({ ...prev, name: e.target.value }))}
        />
        <Input
          label={t('ai_providers.prefix_label')}
          placeholder={t('ai_providers.prefix_placeholder')}
          value={form.prefix ?? ''}
          onChange={(e) => setForm((prev) => ({ ...prev, prefix: e.target.value }))}
          hint={t('ai_providers.prefix_hint')}
        />
        <Input
          label={t('ai_providers.openai_add_modal_url_label')}
          value={form.baseUrl}
          onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
        />

        <HeaderInputList
          entries={form.headers}
          onChange={(entries) => setForm((prev) => ({ ...prev, headers: entries }))}
          addLabel={t('common.custom_headers_add')}
          keyPlaceholder={t('common.custom_headers_key_placeholder')}
          valuePlaceholder={t('common.custom_headers_value_placeholder')}
        />

        <div className="form-group">
          <label>
            {editIndex !== null
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
            disabled={isSaving}
          />
          <Button variant="secondary" size="sm" onClick={openOpenaiModelDiscovery} disabled={isSaving}>
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
              disabled={isSaving || availableModels.length === 0}
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
              className={`${styles.openaiTestButton} ${testStatus === 'success' ? styles.openaiTestButtonSuccess : ''}`}
              onClick={testOpenaiProviderConnection}
              loading={testStatus === 'loading'}
              disabled={isSaving || availableModels.length === 0}
            >
              {t('ai_providers.openai_test_action')}
            </Button>
          </div>
          {testMessage && (
            <div
              className={`status-badge ${
                testStatus === 'error' ? 'error' : testStatus === 'success' ? 'success' : 'muted'
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
      </Modal>

      <OpenAIDiscoveryModal
        isOpen={discoveryOpen}
        baseUrl={form.baseUrl}
        headers={form.headers}
        apiKeyEntries={form.apiKeyEntries}
        onClose={() => setDiscoveryOpen(false)}
        onApply={applyOpenaiModelDiscoverySelection}
      />
    </>
  );
}
