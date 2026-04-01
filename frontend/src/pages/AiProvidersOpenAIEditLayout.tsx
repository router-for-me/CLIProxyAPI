import type { Dispatch, SetStateAction } from 'react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Outlet, useLocation, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import { providersApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore, useOpenAIEditDraftStore } from '@/stores';
import { entriesToModels, modelsToEntries } from '@/components/ui/modelInputListUtils';
import type { ApiKeyEntry, OpenAIProviderConfig } from '@/types';
import type { ModelInfo } from '@/utils/models';
import { buildHeaderObject, headersToEntries, normalizeHeaderEntries } from '@/utils/headers';
import { buildApiKeyEntry } from '@/components/providers/utils';
import type { ModelEntry, OpenAIFormState } from '@/components/providers/types';
import type { KeyTestStatus } from '@/stores/useOpenAIEditDraftStore';

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
  keyTestStatuses: KeyTestStatus[];
  setDraftKeyTestStatus: (keyIndex: number, status: KeyTestStatus) => void;
  resetDraftKeyTestStatuses: (count: number) => void;
  availableModels: string[];
  handleBack: () => void;
  handleSave: () => Promise<void>;
  mergeDiscoveredModels: (selectedModels: ModelInfo[]) => void;
};

const buildEmptyForm = (): OpenAIFormState => ({
  name: '',
  priority: undefined,
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

const normalizeModelEntries = (entries: ModelEntry[]) =>
  (entries ?? []).reduce<Array<{ name: string; alias: string }>>((acc, entry) => {
    const name = String(entry?.name ?? '').trim();
    let alias = String(entry?.alias ?? '').trim();
    if (name && (alias === '' || alias === name)) {
      alias = '';
    }
    if (!name && !alias) return acc;
    acc.push({ name, alias });
    return acc;
  }, []);

const normalizeKeyHeaders = (headers: ApiKeyEntry['headers']) => {
  if (!headers || typeof headers !== 'object') return [];
  return Object.entries(headers)
    .map(([key, value]) => ({ key: String(key ?? '').trim(), value: String(value ?? '').trim() }))
    .filter((entry) => entry.key || entry.value)
    .sort((a, b) => {
      const byKey = a.key.toLowerCase().localeCompare(b.key.toLowerCase());
      if (byKey !== 0) return byKey;
      return a.value.localeCompare(b.value);
    });
};

const normalizeApiKeyEntries = (entries: ApiKeyEntry[]) =>
  (entries ?? []).reduce<
    Array<{
      apiKey: string;
      proxyUrl: string;
      headers: Array<{ key: string; value: string }>;
    }>
  >((acc, entry) => {
    const apiKey = String(entry?.apiKey ?? '').trim();
    const proxyUrl = String(entry?.proxyUrl ?? '').trim();
    const headers = normalizeKeyHeaders(entry?.headers);
    if (!apiKey && !proxyUrl && headers.length === 0) return acc;
    acc.push({ apiKey, proxyUrl, headers });
    return acc;
  }, []);

const buildOpenAISignature = (form: OpenAIFormState, testModel: string) =>
  JSON.stringify({
    name: String(form.name ?? '').trim(),
    priority:
      form.priority !== undefined && Number.isFinite(form.priority) ? Math.trunc(form.priority) : null,
    prefix: String(form.prefix ?? '').trim(),
    baseUrl: String(form.baseUrl ?? '').trim(),
    headers: normalizeHeaderEntries(form.headers),
    apiKeyEntries: normalizeApiKeyEntries(form.apiKeyEntries),
    models: normalizeModelEntries(form.modelEntries),
    testModel: String(testModel ?? '').trim(),
  });

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
  const acquireDraft = useOpenAIEditDraftStore((state) => state.acquireDraft);
  const releaseDraft = useOpenAIEditDraftStore((state) => state.releaseDraft);
  const initDraft = useOpenAIEditDraftStore((state) => state.initDraft);
  const setDraftBaselineSignature = useOpenAIEditDraftStore((state) => state.setDraftBaselineSignature);
  const setDraftForm = useOpenAIEditDraftStore((state) => state.setDraftForm);
  const setDraftTestModel = useOpenAIEditDraftStore((state) => state.setDraftTestModel);
  const setDraftTestStatus = useOpenAIEditDraftStore((state) => state.setDraftTestStatus);
  const setDraftTestMessage = useOpenAIEditDraftStore((state) => state.setDraftTestMessage);
  const setDraftKeyTestStatus = useOpenAIEditDraftStore((state) => state.setDraftKeyTestStatus);
  const resetDraftKeyTestStatuses = useOpenAIEditDraftStore((state) => state.resetDraftKeyTestStatuses);

  const form = draft?.form ?? buildEmptyForm();
  const testModel = draft?.testModel ?? '';
  const testStatus = draft?.testStatus ?? 'idle';
  const testMessage = draft?.testMessage ?? '';
  const keyTestStatuses = draft?.keyTestStatuses ?? [];

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

  const handleSetDraftKeyTestStatus = useCallback(
    (keyIndex: number, status: KeyTestStatus) => {
      setDraftKeyTestStatus(draftKey, keyIndex, status);
    },
    [draftKey, setDraftKeyTestStatus]
  );

  const handleResetDraftKeyTestStatuses = useCallback(
    (count: number) => {
      resetDraftKeyTestStatuses(draftKey, count);
    },
    [draftKey, resetDraftKeyTestStatuses]
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
    acquireDraft(draftKey);
    return () => releaseDraft(draftKey);
  }, [acquireDraft, draftKey, releaseDraft]);

  const handleBack = useCallback(() => {
    const state = location.state as LocationState;
    if (state?.fromAiProviders) {
      navigate(-1);
      return;
    }
    navigate('/ai-providers', { replace: true });
  }, [location.state, navigate]);

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
        priority: initialData.priority,
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
      const baselineSignature = buildOpenAISignature(seededForm, initialTestModel);
      initDraft(draftKey, {
        baselineSignature,
        form: seededForm,
        testModel: initialTestModel,
        testStatus: 'idle',
        testMessage: '',
        keyTestStatuses: [],
      });
    } else {
      const emptyForm = buildEmptyForm();
      initDraft(draftKey, {
        baselineSignature: buildOpenAISignature(emptyForm, ''),
        form: emptyForm,
        testModel: '',
        testStatus: 'idle',
        testMessage: '',
        keyTestStatuses: [],
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
  }, [availableModels, loading, setTestMessage, setTestModel, setTestStatus, testModel]);

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

  const resolvedLoading = !draft?.initialized;
  const currentSignature = useMemo(() => buildOpenAISignature(form, testModel), [form, testModel]);
  const baselineSignature = draft?.baselineSignature ?? '';
  const isDirty = Boolean(draft?.initialized) && baselineSignature !== currentSignature;
  const editorRootPath = useMemo(() => {
    if (hasIndexParam) {
      return `/ai-providers/openai/${params.index ?? ''}`;
    }
    return '/ai-providers/openai/new';
  }, [hasIndexParam, params.index]);
  const canGuard = !resolvedLoading && !saving && !invalidIndexParam && !invalidIndex;

  const { allowNextNavigation } = useUnsavedChangesGuard({
    enabled: canGuard,
    shouldBlock: ({ nextLocation }) => {
      const nextPath = nextLocation.pathname;
      const isWithinRoot =
        nextPath === editorRootPath || nextPath.startsWith(`${editorRootPath}/`);
      return isDirty && !isWithinRoot;
    },
    dialog: {
      title: t('common.unsaved_changes_title'),
      message: t('common.unsaved_changes_message'),
      confirmText: t('common.leave'),
      cancelText: t('common.stay'),
      variant: 'danger',
    },
  });

  const handleSave = useCallback(async () => {
    const name = form.name.trim();
    const baseUrl = form.baseUrl.trim();

    if (!name || !baseUrl) {
      showNotification(t('notification.openai_provider_required'), 'error');
      return;
    }

    setSaving(true);
    try {
      const payload: OpenAIProviderConfig = {
        name,
        prefix: form.prefix?.trim() || undefined,
        baseUrl,
        headers: buildHeaderObject(form.headers),
        apiKeyEntries: form.apiKeyEntries.map((entry: ApiKeyEntry) => ({
          apiKey: entry.apiKey.trim(),
          proxyUrl: entry.proxyUrl?.trim() || undefined,
          headers: entry.headers,
        })),
      };
      if (form.priority !== undefined && Number.isFinite(form.priority)) {
        payload.priority = Math.trunc(form.priority);
      }
      const resolvedTestModel = testModel.trim();
      if (resolvedTestModel) payload.testModel = resolvedTestModel;
      const models = entriesToModels(form.modelEntries);
      if (models.length) payload.models = models;

      const nextList =
        editIndex !== null
          ? providers.map((item, idx) => (idx === editIndex ? payload : item))
          : [...providers, payload];

      await providersApi.saveOpenAIProviders(nextList);

      let syncedProviders = nextList;
      try {
        const latest = await fetchConfig('openai-compatibility', true);
        if (Array.isArray(latest)) {
          syncedProviders = latest as OpenAIProviderConfig[];
        }
      } catch {
        // 保存成功后刷新失败时，回退到本地计算结果，避免页面数据为空或回退
      }

      setProviders(syncedProviders);
      showNotification(
        editIndex !== null
          ? t('notification.openai_provider_updated')
          : t('notification.openai_provider_added'),
        'success'
      );
      allowNextNavigation();
      setDraftBaselineSignature(draftKey, buildOpenAISignature(form, testModel));
      handleBack();
    } catch (err: unknown) {
      showNotification(`${t('notification.update_failed')}: ${getErrorMessage(err)}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [
    allowNextNavigation,
    draftKey,
    editIndex,
    fetchConfig,
    form,
    handleBack,
    providers,
    setDraftBaselineSignature,
    showNotification,
    t,
    testModel,
  ]);

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
        keyTestStatuses,
        setDraftKeyTestStatus: handleSetDraftKeyTestStatus,
        resetDraftKeyTestStatuses: handleResetDraftKeyTestStatuses,
        availableModels,
        handleBack,
        handleSave,
        mergeDiscoveredModels,
      } satisfies OpenAIEditOutletContext}
    />
  );
}
