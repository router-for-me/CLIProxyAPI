import type { Dispatch, SetStateAction } from 'react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Outlet, useLocation, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { providersApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore, useOpenAIEditDraftStore } from '@/stores';
import { entriesToModels, modelsToEntries } from '@/components/ui/modelInputListUtils';
import type { ApiKeyEntry, OpenAIProviderConfig } from '@/types';
import type { ModelInfo } from '@/utils/models';
import { buildHeaderObject, headersToEntries } from '@/utils/headers';
import { buildApiKeyEntry } from '@/components/providers/utils';
import type { ModelEntry, OpenAIFormState } from '@/components/providers/types';

type LocationState = { fromAiProviders?: boolean } | null;

export type OpenAIEditOutletContext = {
  hasIndexParam: boolean;
  editIndex: number | null;
  invalidIndexParam: boolean;
  invalidIndex: boolean;
  disableControls: boolean;
  loading: boolean;
  saving: boolean;
  form: OpenAIFormState;
  setForm: Dispatch<SetStateAction<OpenAIFormState>>;
  testModel: string;
  setTestModel: Dispatch<SetStateAction<string>>;
  testStatus: 'idle' | 'loading' | 'success' | 'error';
  setTestStatus: Dispatch<SetStateAction<'idle' | 'loading' | 'success' | 'error'>>;
  testMessage: string;
  setTestMessage: Dispatch<SetStateAction<string>>;
  availableModels: string[];
  handleBack: () => void;
  handleSave: () => Promise<void>;
  mergeDiscoveredModels: (selectedModels: ModelInfo[]) => void;
};

const buildEmptyForm = (): OpenAIFormState => ({
  name: '',
  prefix: '',
  baseUrl: '',
  headers: [],
  apiKeyEntries: [buildApiKeyEntry()],
  modelEntries: [{ name: '', alias: '' }],
  testModel: undefined,
});

const parseIndexParam = (value: string | undefined) => {
  if (!value) return null;
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) ? parsed : null;
};

const getErrorMessage = (err: unknown) => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
};

export function AiProvidersOpenAIEditLayout() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const { showNotification } = useNotificationStore();

  const params = useParams<{ index?: string }>();
  const hasIndexParam = typeof params.index === 'string';
  const editIndex = useMemo(() => parseIndexParam(params.index), [params.index]);
  const invalidIndexParam = hasIndexParam && editIndex === null;

  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const disableControls = connectionStatus !== 'connected';

  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);
  const isCacheValid = useConfigStore((state) => state.isCacheValid);

  const [providers, setProviders] = useState<OpenAIProviderConfig[]>(
    () => config?.openaiCompatibility ?? []
  );
  const [loading, setLoading] = useState(
    () => !isCacheValid('openai-compatibility')
  );
  const [saving, setSaving] = useState(false);

  const draftKey = useMemo(() => {
    if (invalidIndexParam) return `openai:invalid:${params.index ?? 'unknown'}`;
    if (editIndex === null) return 'openai:new';
    return `openai:${editIndex}`;
  }, [editIndex, invalidIndexParam, params.index]);

  const draft = useOpenAIEditDraftStore((state) => state.drafts[draftKey]);
  const ensureDraft = useOpenAIEditDraftStore((state) => state.ensureDraft);
  const initDraft = useOpenAIEditDraftStore((state) => state.initDraft);
  const clearDraft = useOpenAIEditDraftStore((state) => state.clearDraft);
  const setDraftForm = useOpenAIEditDraftStore((state) => state.setDraftForm);
  const setDraftTestModel = useOpenAIEditDraftStore((state) => state.setDraftTestModel);
  const setDraftTestStatus = useOpenAIEditDraftStore((state) => state.setDraftTestStatus);
  const setDraftTestMessage = useOpenAIEditDraftStore((state) => state.setDraftTestMessage);

  const form = draft?.form ?? buildEmptyForm();
  const testModel = draft?.testModel ?? '';
  const testStatus = draft?.testStatus ?? 'idle';
  const testMessage = draft?.testMessage ?? '';

  const setForm: Dispatch<SetStateAction<OpenAIFormState>> = useCallback(
    (action) => {
      setDraftForm(draftKey, action);
    },
    [draftKey, setDraftForm]
  );

  const setTestModel: Dispatch<SetStateAction<string>> = useCallback(
    (action) => {
      setDraftTestModel(draftKey, action);
    },
    [draftKey, setDraftTestModel]
  );

  const setTestStatus: Dispatch<SetStateAction<'idle' | 'loading' | 'success' | 'error'>> =
    useCallback(
      (action) => {
        setDraftTestStatus(draftKey, action);
      },
      [draftKey, setDraftTestStatus]
    );

  const setTestMessage: Dispatch<SetStateAction<string>> = useCallback(
    (action) => {
      setDraftTestMessage(draftKey, action);
    },
    [draftKey, setDraftTestMessage]
  );

  const initialData = useMemo(() => {
    if (editIndex === null) return undefined;
    return providers[editIndex];
  }, [editIndex, providers]);

  const invalidIndex = editIndex !== null && !initialData;

  const availableModels = useMemo(
    () => form.modelEntries.map((entry) => entry.name.trim()).filter(Boolean),
    [form.modelEntries]
  );

  useEffect(() => {
    ensureDraft(draftKey);
  }, [draftKey, ensureDraft]);

  const handleBack = useCallback(() => {
    clearDraft(draftKey);
    const state = location.state as LocationState;
    if (state?.fromAiProviders) {
      navigate(-1);
      return;
    }
    navigate('/ai-providers', { replace: true });
  }, [clearDraft, draftKey, location.state, navigate]);

  useEffect(() => {
    let cancelled = false;
    const hasValidCache = isCacheValid('openai-compatibility');
    if (!hasValidCache) {
      setLoading(true);
    }

    fetchConfig('openai-compatibility')
      .then((value) => {
        if (cancelled) return;
        setProviders(Array.isArray(value) ? (value as OpenAIProviderConfig[]) : []);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const message = getErrorMessage(err) || t('notification.refresh_failed');
        showNotification(`${t('notification.load_failed')}: ${message}`, 'error');
      })
      .finally(() => {
        if (cancelled) return;
        setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [fetchConfig, isCacheValid, showNotification, t]);

  useEffect(() => {
    if (loading) return;
    if (draft?.initialized) return;

    if (initialData) {
      const modelEntries = modelsToEntries(initialData.models);
      const seededForm: OpenAIFormState = {
        name: initialData.name,
        prefix: initialData.prefix ?? '',
        baseUrl: initialData.baseUrl,
        headers: headersToEntries(initialData.headers),
        testModel: initialData.testModel,
        modelEntries,
        apiKeyEntries: initialData.apiKeyEntries?.length
          ? initialData.apiKeyEntries
          : [buildApiKeyEntry()],
      };

      const available = modelEntries.map((entry) => entry.name.trim()).filter(Boolean);
      const initialTestModel =
        initialData.testModel && available.includes(initialData.testModel)
          ? initialData.testModel
          : available[0] || '';
      initDraft(draftKey, {
        form: seededForm,
        testModel: initialTestModel,
        testStatus: 'idle',
        testMessage: '',
      });
    } else {
      initDraft(draftKey, {
        form: buildEmptyForm(),
        testModel: '',
        testStatus: 'idle',
        testMessage: '',
      });
    }
  }, [draft?.initialized, draftKey, initDraft, initialData, loading]);

  useEffect(() => {
    if (loading) return;

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
  }, [availableModels, loading, testModel]);

  const mergeDiscoveredModels = useCallback(
    (selectedModels: ModelInfo[]) => {
      if (!selectedModels.length) return;

      let addedCount = 0;
      setForm((prev) => {
        const mergedMap = new Map<string, ModelEntry>();
        prev.modelEntries.forEach((entry) => {
          const name = entry.name.trim();
          if (!name) return;
          mergedMap.set(name, { name, alias: entry.alias?.trim() || '' });
        });

        selectedModels.forEach((model) => {
          const name = model.name.trim();
          if (!name || mergedMap.has(name)) return;
          mergedMap.set(name, { name, alias: model.alias ?? '' });
          addedCount += 1;
        });

        const mergedEntries = Array.from(mergedMap.values());
        return {
          ...prev,
          modelEntries: mergedEntries.length ? mergedEntries : [{ name: '', alias: '' }],
        };
      });

      if (addedCount > 0) {
        showNotification(t('ai_providers.openai_models_fetch_added', { count: addedCount }), 'success');
      }
    },
    [setForm, showNotification, t]
  );

  const handleSave = useCallback(async () => {
    setSaving(true);
    try {
      const payload: OpenAIProviderConfig = {
        name: form.name.trim(),
        prefix: form.prefix?.trim() || undefined,
        baseUrl: form.baseUrl.trim(),
        headers: buildHeaderObject(form.headers),
        apiKeyEntries: form.apiKeyEntries.map((entry: ApiKeyEntry) => ({
          apiKey: entry.apiKey.trim(),
          proxyUrl: entry.proxyUrl?.trim() || undefined,
          headers: entry.headers,
        })),
      };
      const resolvedTestModel = testModel.trim();
      if (resolvedTestModel) payload.testModel = resolvedTestModel;
      const models = entriesToModels(form.modelEntries);
      if (models.length) payload.models = models;

      const nextList =
        editIndex !== null
          ? providers.map((item, idx) => (idx === editIndex ? payload : item))
          : [...providers, payload];

      await providersApi.saveOpenAIProviders(nextList);
      setProviders(nextList);
      updateConfigValue('openai-compatibility', nextList);
      clearCache('openai-compatibility');
      showNotification(
        editIndex !== null
          ? t('notification.openai_provider_updated')
          : t('notification.openai_provider_added'),
        'success'
      );
      handleBack();
    } catch (err: unknown) {
      showNotification(`${t('notification.update_failed')}: ${getErrorMessage(err)}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [
    clearCache,
    editIndex,
    form,
    handleBack,
    providers,
    testModel,
    showNotification,
    t,
    updateConfigValue,
  ]);

  const resolvedLoading = !draft?.initialized;

  return (
    <Outlet
      context={{
        hasIndexParam,
        editIndex,
        invalidIndexParam,
        invalidIndex,
        disableControls,
        loading: resolvedLoading,
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
        mergeDiscoveredModels,
      } satisfies OpenAIEditOutletContext}
    />
  );
}

