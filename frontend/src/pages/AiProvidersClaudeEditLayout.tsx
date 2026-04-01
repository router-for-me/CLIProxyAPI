import type { Dispatch, SetStateAction } from 'react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Outlet, useLocation, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import { providersApi } from '@/services/api';
import { useAuthStore, useClaudeEditDraftStore, useConfigStore, useNotificationStore } from '@/stores';
import type { ProviderKeyConfig } from '@/types';
import type { ModelInfo } from '@/utils/models';
import type { ModelEntry, ProviderFormState } from '@/components/providers/types';
import { buildHeaderObject, headersToEntries, normalizeHeaderEntries } from '@/utils/headers';
import { excludedModelsToText, parseExcludedModels } from '@/components/providers/utils';
import { modelsToEntries } from '@/components/ui/modelInputListUtils';

type LocationState = { fromAiProviders?: boolean } | null;

type TestStatus = 'idle' | 'loading' | 'success' | 'error';

export type ClaudeEditOutletContext = {
  hasIndexParam: boolean;
  editIndex: number | null;
  invalidIndexParam: boolean;
  invalidIndex: boolean;
  disableControls: boolean;
  loading: boolean;
  saving: boolean;
  form: ProviderFormState;
  setForm: Dispatch<SetStateAction<ProviderFormState>>;
  testModel: string;
  setTestModel: Dispatch<SetStateAction<string>>;
  testStatus: TestStatus;
  setTestStatus: Dispatch<SetStateAction<TestStatus>>;
  testMessage: string;
  setTestMessage: Dispatch<SetStateAction<string>>;
  availableModels: string[];
  handleBack: () => void;
  handleSave: () => Promise<void>;
  mergeDiscoveredModels: (selectedModels: ModelInfo[]) => void;
};

const buildEmptyForm = (): ProviderFormState => ({
  apiKey: '',
  priority: undefined,
  prefix: '',
  baseUrl: '',
  proxyUrl: '',
  headers: [],
  models: [],
  excludedModels: [],
  modelEntries: [{ name: '', alias: '' }],
  excludedText: '',
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

const normalizeClaudeModelEntries = (entries: Array<{ name: string; alias: string }>) =>
  (entries ?? []).reduce<Array<{ name: string; alias: string }>>((acc, entry) => {
    const name = String(entry?.name ?? '').trim();
    let alias = String(entry?.alias ?? '').trim();
    if (name) {
      alias = alias || name;
    }
    if (!name && !alias) return acc;
    acc.push({ name, alias });
    return acc;
  }, []);

const normalizeCloakConfig = (cloak: ProviderFormState['cloak']) => {
  if (!cloak) return null;
  const mode = String(cloak.mode ?? '').trim().toLowerCase() || 'auto';
  const strictMode = Boolean(cloak.strictMode);
  const sensitiveWords = Array.isArray(cloak.sensitiveWords)
    ? cloak.sensitiveWords.map((word) => String(word ?? '').trim()).filter(Boolean)
    : [];
  return {
    mode,
    strictMode,
    sensitiveWords: sensitiveWords.length ? sensitiveWords : null,
  };
};

const buildClaudeSignature = (form: ProviderFormState) =>
  JSON.stringify({
    apiKey: String(form.apiKey ?? '').trim(),
    priority:
      form.priority !== undefined && Number.isFinite(form.priority) ? Math.trunc(form.priority) : null,
    prefix: String(form.prefix ?? '').trim(),
    baseUrl: String(form.baseUrl ?? '').trim(),
    proxyUrl: String(form.proxyUrl ?? '').trim(),
    headers: normalizeHeaderEntries(form.headers),
    models: normalizeClaudeModelEntries(form.modelEntries),
    excludedModels: parseExcludedModels(form.excludedText ?? ''),
    cloak: normalizeCloakConfig(form.cloak),
  });

export function AiProvidersClaudeEditLayout() {
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
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);

  const [configs, setConfigs] = useState<ProviderKeyConfig[]>(() => config?.claudeApiKeys ?? []);
  const [loading, setLoading] = useState(() => !isCacheValid('claude-api-key'));
  const [saving, setSaving] = useState(false);

  const draftKey = useMemo(() => {
    if (invalidIndexParam) return `claude:invalid:${params.index ?? 'unknown'}`;
    if (editIndex === null) return 'claude:new';
    return `claude:${editIndex}`;
  }, [editIndex, invalidIndexParam, params.index]);

  const draft = useClaudeEditDraftStore((state) => state.drafts[draftKey]);
  const acquireDraft = useClaudeEditDraftStore((state) => state.acquireDraft);
  const releaseDraft = useClaudeEditDraftStore((state) => state.releaseDraft);
  const initDraft = useClaudeEditDraftStore((state) => state.initDraft);
  const setDraftBaselineSignature = useClaudeEditDraftStore((state) => state.setDraftBaselineSignature);
  const setDraftForm = useClaudeEditDraftStore((state) => state.setDraftForm);
  const setDraftTestModel = useClaudeEditDraftStore((state) => state.setDraftTestModel);
  const setDraftTestStatus = useClaudeEditDraftStore((state) => state.setDraftTestStatus);
  const setDraftTestMessage = useClaudeEditDraftStore((state) => state.setDraftTestMessage);

  const form = draft?.form ?? buildEmptyForm();
  const testModel = draft?.testModel ?? '';
  const testStatus = draft?.testStatus ?? 'idle';
  const testMessage = draft?.testMessage ?? '';

  const setForm: Dispatch<SetStateAction<ProviderFormState>> = useCallback(
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

  const setTestStatus: Dispatch<SetStateAction<TestStatus>> = useCallback(
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
    return configs[editIndex];
  }, [configs, editIndex]);

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
    const hasValidCache = isCacheValid('claude-api-key');
    if (!hasValidCache) {
      setLoading(true);
    }

    fetchConfig('claude-api-key')
      .then((value) => {
        if (cancelled) return;
        setConfigs(Array.isArray(value) ? (value as ProviderKeyConfig[]) : []);
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
      const seededForm: ProviderFormState = {
        ...initialData,
        headers: headersToEntries(initialData.headers),
        modelEntries: modelsToEntries(initialData.models),
        excludedText: excludedModelsToText(initialData.excludedModels),
      };
      const available = seededForm.modelEntries.map((entry) => entry.name.trim()).filter(Boolean);
      const baselineSignature = buildClaudeSignature(seededForm);
      initDraft(draftKey, {
        baselineSignature,
        form: seededForm,
        testModel: available[0] || '',
        testStatus: 'idle',
        testMessage: '',
      });
      return;
    }

    const emptyForm = buildEmptyForm();
    initDraft(draftKey, {
      baselineSignature: buildClaudeSignature(emptyForm),
      form: emptyForm,
      testModel: '',
      testStatus: 'idle',
      testMessage: '',
    });
  }, [draft?.initialized, draftKey, initDraft, initialData, loading]);

  const resolvedLoading = !draft?.initialized;
  const currentSignature = useMemo(() => buildClaudeSignature(form), [form]);
  const baselineSignature = draft?.baselineSignature ?? '';
  const isDirty = Boolean(draft?.initialized) && baselineSignature !== currentSignature;
  const editorRootPath = useMemo(() => {
    if (hasIndexParam) {
      return `/ai-providers/claude/${params.index ?? ''}`;
    }
    return '/ai-providers/claude/new';
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

  useEffect(() => {
    if (resolvedLoading) return;

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
  }, [availableModels, resolvedLoading, setTestMessage, setTestModel, setTestStatus, testModel]);

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
        showNotification(t('ai_providers.claude_models_fetch_added', { count: addedCount }), 'success');
      }
    },
    [setForm, showNotification, t]
  );

  const handleSave = useCallback(async () => {
    const canSave =
      !disableControls && !saving && !resolvedLoading && !invalidIndexParam && !invalidIndex;
    if (!canSave) return;

    setSaving(true);
    try {
      const payload: ProviderKeyConfig = {
        apiKey: form.apiKey.trim(),
        priority: form.priority !== undefined ? Math.trunc(form.priority) : undefined,
        prefix: form.prefix?.trim() || undefined,
        baseUrl: (form.baseUrl ?? '').trim() || undefined,
        proxyUrl: form.proxyUrl?.trim() || undefined,
        headers: buildHeaderObject(form.headers),
        models: form.modelEntries
          .map((entry) => {
            const name = entry.name.trim();
            if (!name) return null;
            const alias = entry.alias.trim();
            return { name, alias: alias || name };
          })
          .filter(Boolean) as ProviderKeyConfig['models'],
        excludedModels: parseExcludedModels(form.excludedText),
        cloak: form.cloak,
      };

      const nextList =
        editIndex !== null
          ? configs.map((item, idx) => (idx === editIndex ? payload : item))
          : [...configs, payload];

      await providersApi.saveClaudeConfigs(nextList);
      setConfigs(nextList);
      updateConfigValue('claude-api-key', nextList);
      clearCache('claude-api-key');
      showNotification(
        editIndex !== null ? t('notification.claude_config_updated') : t('notification.claude_config_added'),
        'success'
      );
      allowNextNavigation();
      setDraftBaselineSignature(draftKey, buildClaudeSignature(form));
      handleBack();
    } catch (err: unknown) {
      showNotification(`${t('notification.update_failed')}: ${getErrorMessage(err)}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [
    allowNextNavigation,
    clearCache,
    configs,
    draftKey,
    disableControls,
    editIndex,
    form,
    handleBack,
    invalidIndex,
    invalidIndexParam,
    resolvedLoading,
    setDraftBaselineSignature,
    saving,
    showNotification,
    t,
    updateConfigValue,
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
        availableModels,
        handleBack,
        handleSave,
        mergeDiscoveredModels,
      } satisfies ClaudeEditOutletContext}
    />
  );
}
