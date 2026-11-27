import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
  type ReactNode,
} from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import {
  IconDownload,
  IconEye,
  IconEyeOff,
  IconLoader2,
  IconPlus,
  IconSlidersHorizontal,
  IconX,
} from '@/components/ui/icons';
import { Select } from '@/components/ui/Select';
import {
  DISABLE_ALL_RULE,
  ExcludedModelsPicker,
  formatExcludedRulesText,
  parseExcludedRulesText,
  type ExcludedModelsCatalogState,
} from '@/components/excludedModels';
import { hasDisableAllModelsRule } from '@/components/providers/utils';
import {
  LiteralReplacementEditor,
  PatternReplacementEditor,
} from '@/components/providers/SensitiveReplacementEditor';
import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { ModelInfo } from '@/utils/models';
import { PROVIDER_DESCRIPTORS } from '../../descriptors';
import { removeModelAt } from '../../modelEntries';
import { syncDiscoveredModels } from '../../discoverySelection';
import type {
  ApiKeyEntryInput,
  ModelEntryInput,
  NativeSection,
  ProviderBrand,
  ProviderEntryFormInput,
  ProviderResource,
} from '../../types';
import { useConnectivityTest, type ConnectivityErrorMessages } from './useConnectivityTest';
import { useModelDiscovery } from './useModelDiscovery';
import { ModelDiscoveryPanel } from './ModelDiscoveryPanel';
import { ConnectivityStatusIcon } from './ConnectivityStatusIcon';
import { ApiKeyEntriesEditor } from './ApiKeyEntriesEditor';
import { CheckboxField } from './CheckboxField';
import { ModelEntriesEditor } from './ModelEntriesEditor';
import { FormDisclosure, FormSection } from './FormSection';
import styles from './sharedForm.module.scss';
import { MAX_CREDENTIAL_WEIGHT } from '@/utils/credentialWeight';
import { FieldLabel } from '@/components/ui/FieldLabel';
import { readRuntimePolicy, validateRuntimePolicy } from '../../runtimePolicy';
import { readModelOptions, validateModelOptions } from '../../modelOptions';
import { validateConnectionPool } from '../../validateConnectionPool';
import { ConnectionPoolEditor, OptionalBooleanField } from './NativeKeyFields';
import { RuntimePolicyEditor } from './RuntimePolicyEditor';
import { ProviderBehaviorEditor } from './ProviderBehaviorEditor';
import { pickProviderBehavior } from '../../providerBehavior';

import type { MemberField, MemberMode } from '@/services/api/nativeProviderGroups';
import type { NativeProviderFamily } from '@/services/api/providers';
import {
  readMemberForm,
  setMemberMode,
  buildSharedSettings,
  buildSubmittedMember,
  memberDisabled,
  nativeSwitchesDirty,
  memberProbeSettings,
  readSharedForm,
  type NativeConnectionFormOutput,
} from '../../nativeConnectionForm';

const DISABLE_ALL_RULES = [DISABLE_ALL_RULE];

interface CommonFormProps {
  resource: ProviderResource | null;
  mode: 'create' | 'edit';
  mutating: boolean;
  formId: string;
  onDirtyChange?: (dirty: boolean) => void;
}
interface NativeFormProps extends CommonFormProps {
  brand: NativeProviderFamily;
  nativeSection?: NativeSection | null;
  onNativeSubmit: (draft: NativeConnectionFormOutput) => Promise<void>;
}
interface StandardFormProps extends CommonFormProps {
  brand: Exclude<ProviderBrand, NativeProviderFamily>;
  onSubmit: (input: ProviderEntryFormInput) => Promise<void>;
}
const emptyHeader = () => ({ key: '', value: '' });
const emptyModel = (): ModelEntryInput => ({ name: '', alias: '' });
const emptyApiKeyEntry = (): ApiKeyEntryInput => ({
  apiKey: '',
  proxyUrl: '',
  weight: undefined,
});
const META_API_BASE_URL = 'https://api.meta.ai/v1';
const XAI_API_BASE_URL = 'https://api.x.ai/v1';

const stripDisableAllRule = (list?: string[]): string[] =>
  (list ?? []).filter((s) => s.trim() !== '*');

const formatJsonObject = (value?: Record<string, unknown>): string => {
  if (!value || Object.keys(value).length === 0) return '';
  return JSON.stringify(value, null, 2);
};

const isClaudeLikeBrand = (brand: ProviderBrand): boolean => brand === 'claude';

function buildInitialForm(
  brand: ProviderBrand,
  resource: ProviderResource | null,
  mode: 'create' | 'edit'
): ProviderEntryFormInput {
  if (mode === 'create' || !resource) {
    return {
      apiKey: '',
      name: '',
      baseUrl: brand === 'meta' ? META_API_BASE_URL : brand === 'xai' ? XAI_API_BASE_URL : '',
      proxyUrl: '',
      prefix: '',
      disabled: false,
      disableCooling: undefined,
      runtimePolicy: readRuntimePolicy(),
      priority: undefined,
      weight: undefined,
      models: [emptyModel()],
      headers: [emptyHeader()],
      excludedModelsText: '',
      websockets: brand === 'codex' || brand === 'xai' ? false : undefined,
      cloak: isClaudeLikeBrand(brand)
        ? {
            mode: '',
            strictMode: false,
            sensitiveWordsText: '',
            sensitiveWordsReplace: {},
            sensitivePatternsReplace: [],
            cacheUserId: false,
          }
        : undefined,
      fingerprintProfile: isClaudeLikeBrand(brand) ? '' : undefined,
      testModel:
        brand === 'openaiCompatibility' ||
        brand === 'codex' ||
        brand === 'meta' ||
        brand === 'xai' ||
        isClaudeLikeBrand(brand) ||
        brand === 'gemini' ||
        brand === 'interactions'
          ? ''
          : undefined,
      apiKeyEntries: brand === 'openaiCompatibility' ? [emptyApiKeyEntry()] : undefined,
    };
  }

  const raw = resource.raw;
  if (brand === 'openaiCompatibility') {
    const cfg = raw as OpenAIProviderConfig;
    return {
      apiKey: '',
      name: cfg.name ?? '',
      baseUrl: cfg.baseUrl ?? '',
      proxyUrl: '',
      prefix: cfg.prefix ?? '',
      disabled: cfg.disabled === true,
      disableCooling: cfg.disableCooling,
      runtimePolicy: readRuntimePolicy(cfg),
      ...pickProviderBehavior(cfg, brand),
      priority: cfg.priority,
      models: cfg.models?.length
        ? cfg.models.map((m) => ({
            sourceIndex: m.sourceIndex,
            name: m.name,
            alias: m.alias ?? '',
            priority: m.priority,
            image: m.image === true,
            thinkingJson: formatJsonObject(m.thinking),
            ...readModelOptions(m),
          }))
        : [emptyModel()],
      headers: cfg.headers
        ? Object.entries(cfg.headers).map(([k, v]) => ({ key: k, value: String(v) }))
        : [emptyHeader()],
      excludedModelsText: '',
      testModel: '',
      apiKeyEntries: cfg.apiKeyEntries?.length
        ? cfg.apiKeyEntries.map((entry) => ({
            apiKey: '',
            existingApiKey: entry.apiKey,
            savedProxyUrl: entry.proxyUrl,
            sourceIndex: entry.sourceIndex,
            proxyUrl: entry.proxyUrl ?? '',
            weight: entry.weight,
            authIndex: entry.authIndex,
          }))
        : [emptyApiKeyEntry()],
    };
  }

  const cfg = raw as GeminiKeyConfig & ProviderKeyConfig;
  const disabled = hasDisableAllModelsRule(cfg.excludedModels);
  const excludedList = stripDisableAllRule(cfg.excludedModels);
  return {
    // Keep the API key blank in edit mode. Pre-filling the real key makes this
    // password field a browser-autofill target (the saved management key can
    // overwrite it) and defeats the "leave empty = keep unchanged" contract; an
    // empty field is preserved on save via buildProviderKeyConfig's existing fallback.
    apiKey: '',
    name: '',
    baseUrl: cfg.baseUrl ?? '',
    proxyUrl: cfg.proxyUrl ?? '',
    prefix: cfg.prefix ?? '',
    disabled,
    disableCooling: cfg.disableCooling,
    runtimePolicy: readRuntimePolicy(cfg),
    ...pickProviderBehavior(cfg, brand),
    priority: cfg.priority,
    weight: cfg.weight,
    connectionPool: cfg.connectionPool,
    nativeToolSearch: cfg.nativeToolSearch,
    interactions: cfg.interactions,
    openapi: cfg.openapi,
    openapiLocation: cfg.openapiLocation,
    models: cfg.models?.length
      ? cfg.models.map((m) => ({
          sourceIndex: m.sourceIndex,
          name: m.name,
          alias: m.alias ?? '',
          priority: m.priority,
          thinkingJson: formatJsonObject(m.thinking),
          ...readModelOptions(m),
        }))
      : [emptyModel()],
    headers: cfg.headers
      ? Object.entries(cfg.headers).map(([k, v]) => ({ key: k, value: String(v) }))
      : [emptyHeader()],
    excludedModelsText: excludedList.join('\n'),
    websockets:
      brand === 'codex' || brand === 'xai'
        ? (cfg as ProviderKeyConfig).websockets === true
        : undefined,
    cloak: isClaudeLikeBrand(brand)
      ? {
          mode: (cfg as ProviderKeyConfig).cloak?.mode ?? '',
          strictMode: (cfg as ProviderKeyConfig).cloak?.strictMode === true,
          sensitiveWordsText: (cfg as ProviderKeyConfig).cloak?.sensitiveWords?.join('\n') ?? '',
          sensitiveWordsReplace: cfg.cloak?.sensitiveWordsReplace ?? {},
          sensitivePatternsReplace: cfg.cloak?.sensitivePatternsReplace ?? [],
          cacheUserId: (cfg as ProviderKeyConfig).cloak?.cacheUserId === true,
        }
      : undefined,
    fingerprintProfile: isClaudeLikeBrand(brand)
      ? ((cfg as ProviderKeyConfig).fingerprintProfile ?? '')
      : undefined,
    testModel:
      brand === 'codex' ||
      brand === 'meta' ||
      brand === 'xai' ||
      isClaudeLikeBrand(brand) ||
      brand === 'gemini' ||
      brand === 'interactions'
        ? ''
        : undefined,
  };
}

type Scope = 'shared' | 'member' | 'standard';
interface ProviderFieldsProps {
  brand: ProviderBrand;
  resource: ProviderResource | null;
  mode: 'create' | 'edit';
  mutating: boolean;
  form: ProviderEntryFormInput;
  setForm: Dispatch<SetStateAction<ProviderEntryFormInput>>;
  scope: Scope;
  keyList?: ReactNode;
  probeModels?: ProviderEntryFormInput['models'];
  probeApiKey?: string;
  probeBaseUrl?: string;
  probeProxyUrl?: string;
  probeHeaders?: ProviderEntryFormInput['headers'];
}

function validate(
  form: ProviderEntryFormInput,
  initialForm: ProviderEntryFormInput,
  brand: ProviderBrand,
  mode: 'create' | 'edit',
  scope: Scope,
  t: TFunction
): string | null {
  const descriptor = PROVIDER_DESCRIPTORS[brand];
  const poolError = validateConnectionPool(form.connectionPool, initialForm.connectionPool);
  if (poolError) return t(poolError);
  const modelError = validateModelOptions(form.models);
  if (modelError) return t(modelError);
  if (form.runtimePolicy) {
    const policyError = validateRuntimePolicy(
      form.runtimePolicy,
      descriptor.supportsRequestScopedErrors
    );
    if (policyError) return t(policyError);
  }
  if (descriptor.supportsName && !form.name.trim()) {
    return t('providersPage.form.validation.nameRequired');
  }
  if (scope !== 'shared' && descriptor.supportsApiKey && mode === 'create' && !form.apiKey.trim()) {
    return t('providersPage.form.validation.apiKeyRequired');
  }
  if (scope !== 'member' && descriptor.baseUrlRequired && !form.baseUrl.trim()) {
    return t('providersPage.form.validation.baseUrlRequired');
  }
  const weights = [
    ...(brand === 'openaiCompatibility'
      ? (form.apiKeyEntries ?? []).map((entry) => entry.weight)
      : []),
    ...(brand !== 'openaiCompatibility' ? [form.weight] : []),
  ];
  if (weights.some((weight) => weight !== undefined && !Number.isSafeInteger(weight))) {
    return t('providersPage.form.validation.weightInteger');
  }
  if (weights.some((weight) => weight !== undefined && weight > MAX_CREDENTIAL_WEIGHT)) {
    return t('providersPage.form.validation.weightMax', { max: MAX_CREDENTIAL_WEIGHT });
  }
  return null;
}
function ProviderFields({
  brand,
  resource,
  mode,
  mutating,
  form,
  setForm,
  scope,
  keyList,
  probeModels = form.models,
  probeApiKey,
  probeBaseUrl,
  probeProxyUrl,
  probeHeaders,
}: ProviderFieldsProps) {
  const { t } = useTranslation();
  const descriptor = PROVIDER_DESCRIPTORS[brand];
  const fid = useId();
  const fallbackApiKey = useMemo(() => {
    if (mode !== 'edit' || !resource) return '';
    if (brand === 'openaiCompatibility') return '';
    return (resource.raw as { apiKey?: string } | undefined)?.apiKey ?? '';
  }, [brand, mode, resource]);

  const fallbackAuthIndex = useMemo(() => {
    if (mode !== 'edit' || !resource) return '';
    return (resource.raw as { authIndex?: string } | undefined)?.authIndex ?? '';
  }, [mode, resource]);

  const connectivityMessages = useMemo<ConnectivityErrorMessages>(
    () => ({
      baseUrlRequired: t('providersPage.connectivity.baseUrlRequired'),
      endpointInvalid: t('providersPage.connectivity.endpointInvalid'),
      apiKeyRequired: t('providersPage.connectivity.apiKeyRequired'),
      modelRequired: t('providersPage.connectivity.modelRequired'),
      timeout: (seconds: number) => t('providersPage.connectivity.timeout', { seconds }),
      requestFailed: t('providersPage.connectivity.requestFailed'),
    }),
    [t]
  );

  const connectivity = useConnectivityTest(
    {
      brand,
      baseUrl: probeBaseUrl ?? form.baseUrl,
      proxyUrl: probeProxyUrl ?? form.proxyUrl,
      savedProxyUrl: resource?.proxyUrl ?? undefined,
      testModel: form.testModel,
      models: probeModels,
      formHeaders: probeHeaders ?? form.headers,
      apiKeyEntries: form.apiKeyEntries,
      apiKey: probeApiKey ?? form.apiKey,
      fallbackApiKey,
      authIndex: fallbackAuthIndex,
    },
    connectivityMessages
  );

  const discovery = useModelDiscovery({
    brand,
    baseUrl: probeBaseUrl ?? form.baseUrl,
    proxyUrl: probeProxyUrl ?? form.proxyUrl,
    savedProxyUrl: resource?.proxyUrl ?? undefined,
    formHeaders: probeHeaders ?? form.headers,
    apiKeyEntries: form.apiKeyEntries,
    apiKey: probeApiKey ?? form.apiKey,
    fallbackApiKey,
    authIndex: fallbackAuthIndex,
  });
  const [discoveryOpen, setDiscoveryOpen] = useState(false);

  const existingModelNames = useMemo(() => {
    const set = new Set<string>();
    form.models.forEach((m) => {
      const name = (m.name ?? '').trim();
      if (name) set.add(name);
    });
    return set;
  }, [form.models]);

  const testModelOptions = useMemo(() => {
    const seen = new Set<string>();
    const names: string[] = [];
    probeModels.forEach((m) => {
      const name = (m.name ?? '').trim();
      if (!name || seen.has(name)) return;
      seen.add(name);
      names.push(name);
    });
    const firstName = names[0];
    const autoLabel = firstName
      ? t('providersPage.form.testModelAutoWith', { name: firstName })
      : t('providersPage.form.testModelAutoEmpty');
    const opts: Array<{ value: string; label: string }> = [{ value: '', label: autoLabel }];
    names.forEach((n) => opts.push({ value: n, label: n }));
    const tm = (form.testModel ?? '').trim();
    if (tm && !seen.has(tm)) {
      opts.push({
        value: tm,
        label: t('providersPage.form.testModelCustom', { name: tm }),
      });
    }
    return opts;
  }, [probeModels, form.testModel, t]);

  const openDiscovery = () => {
    setDiscoveryOpen(true);
    if (!discovery.loading && !discovery.hasFetched) {
      void discovery.fetch();
    }
  };

  const closeDiscovery = () => {
    setDiscoveryOpen(false);
  };

  const applyDiscoveredModels = (picked: ModelInfo[], catalog: ModelInfo[]) => {
    setForm((prev) => ({ ...prev, models: syncDiscoveredModels(prev.models, picked, catalog) }));
  };

  const updateField = <K extends keyof ProviderEntryFormInput>(
    key: K,
    value: ProviderEntryFormInput[K]
  ) => {
    setForm((prev) => ({ ...prev, [key]: value }));
  };

  const updateCloak = <K extends keyof NonNullable<ProviderEntryFormInput['cloak']>>(
    key: K,
    value: NonNullable<ProviderEntryFormInput['cloak']>[K]
  ) => {
    setForm((prev) => ({
      ...prev,
      cloak: {
        ...(prev.cloak ?? {
          mode: '',
          strictMode: false,
          sensitiveWordsText: '',
          sensitiveWordsReplace: {},
          sensitivePatternsReplace: [],
          cacheUserId: false,
        }),
        [key]: value,
      },
    }));
  };

  /* ------------------ entries helpers ------------------ */

  const headersList = useMemo(
    () => (form.headers.length ? form.headers : [emptyHeader()]),
    [form.headers]
  );
  const modelsList = useMemo(
    () => (form.models.length ? form.models : [emptyModel()]),
    [form.models]
  );
  const apiKeyEntries = useMemo(
    () =>
      form.apiKeyEntries && form.apiKeyEntries.length ? form.apiKeyEntries : [emptyApiKeyEntry()],
    [form.apiKeyEntries]
  );

  const excludedRules = useMemo(
    () => parseExcludedRulesText(form.excludedModelsText),
    [form.excludedModelsText]
  );
  /**
   * 候选目录 = discovery 发现的模型 ∪ 表单里已配置的模型名。
   *
   * 两者都可能为空——`vertex` 支持排除模型却不在 MODEL_DISCOVERY_BRANDS 里，永远没有
   * discovery；其余 brand 在用户手动跑一次发现之前也没有。因此**无目录是常态**，
   * picker 必须能在没有目录时退化成纯规则编辑器。
   */
  const excludedCandidates = useMemo(() => {
    const byKey = new Map<string, { id: string; displayName?: string }>();
    discovery.models.forEach((model) => {
      const id = model.name?.trim();
      if (id) byKey.set(id.toLowerCase(), { id, displayName: model.alias || model.displayName });
    });
    form.models.forEach((model) => {
      const id = model.name?.trim();
      if (id && !byKey.has(id.toLowerCase())) byKey.set(id.toLowerCase(), { id });
    });
    return [...byKey.values()].sort((left, right) =>
      left.id.localeCompare(right.id, undefined, { sensitivity: 'base' })
    );
  }, [discovery.models, form.models]);
  const excludedCatalogState: ExcludedModelsCatalogState = discovery.loading
    ? 'loading'
    : discovery.error
      ? 'error'
      : excludedCandidates.length === 0
        ? 'unavailable'
        : 'ready';
  const actualApiKeyEntries = form.apiKeyEntries ?? [];
  const supportsModelImage = brand === 'openaiCompatibility';
  const singleConnectivity =
    brand === 'codex' || brand === 'meta' || brand === 'xai'
      ? { status: connectivity.codexStatus, run: connectivity.runCodex }
      : brand === 'gemini' || brand === 'interactions'
        ? { status: connectivity.geminiStatus, run: connectivity.runGemini }
        : isClaudeLikeBrand(brand)
          ? { status: connectivity.claudeStatus, run: connectivity.runClaude }
          : null;

  const updateModelEntry = (idx: number, patch: Partial<ModelEntryInput>) => {
    updateField(
      'models',
      modelsList.map((it, i) => (i === idx ? { ...it, ...patch } : it))
    );
  };

  const removeModelEntry = (idx: number) => {
    updateField('models', removeModelAt(modelsList, idx));
  };

  const filledApiKeyEntries = apiKeyEntries.filter(
    (e) => e.apiKey.trim() || e.existingApiKey?.trim()
  ).length;

  const memberField = (field: MemberField, label: string, editor: ReactNode, inputId?: string) => {
    const mode = form.memberModes?.[field];
    if (!mode) return editor;
    const modes: MemberMode[] =
      field === 'priority' ? ['inherit', 'override'] : ['inherit', 'empty', 'override'];
    return (
      <div className={styles.field}>
        <Select
          id={mode === 'override' ? undefined : inputId}
          ariaLabel={t('providersPage.nativeKey.valueSource', { field: label })}
          disabled={mutating}
          value={mode}
          options={modes.map((value) => ({
            value,
            label:
              value === 'empty'
                ? t('providersPage.nativeKey.empty')
                : t(`providersPage.runtimePolicy.${value}`),
          }))}
          onChange={(value) => setForm((prev) => setMemberMode(prev, field, value as MemberMode))}
        />
        {mode === 'override' && editor}
      </div>
    );
  };

  return (
    <>
      <FormSection title={t('providersPage.form.sections.connection')}>
        {scope === 'shared' || descriptor.supportsName ? (
          <div className={styles.field}>
            <label className={styles.label} htmlFor={`${fid}-name`}>
              {t(scope === 'shared' ? 'providersPage.nativeGroup.name' : 'providersPage.form.name')}
            </label>
            <input
              id={`${fid}-name`}
              className={styles.input}
              value={form.name}
              onChange={(e) => updateField('name', e.target.value)}
              disabled={mutating}
            />
          </div>
        ) : null}

        {scope !== 'member' && descriptor.supportsBaseUrl ? (
          <div className={styles.field}>
            <label className={styles.label} htmlFor={`${fid}-baseUrl`}>
              {t('providersPage.form.baseUrl')}
            </label>
            <input
              id={`${fid}-baseUrl`}
              className={styles.input}
              value={form.baseUrl}
              onChange={(e) => updateField('baseUrl', e.target.value)}
              placeholder="https://api.example.com"
              aria-required={descriptor.baseUrlRequired || undefined}
              disabled={mutating}
            />
          </div>
        ) : null}

        {descriptor.supportsProxyUrl ? (
          <div className={styles.field}>
            <label className={styles.label} htmlFor={`${fid}-proxy`}>
              {t('providersPage.form.proxyUrl')}
            </label>
            {memberField(
              'proxyUrl',
              t('providersPage.form.proxyUrl'),
              <input
                id={`${fid}-proxy`}
                className={styles.input}
                value={form.proxyUrl}
                onChange={(e) => updateField('proxyUrl', e.target.value)}
                placeholder="http://127.0.0.1:7890"
                disabled={mutating}
              />,
              `${fid}-proxy`
            )}
          </div>
        ) : null}
      </FormSection>

      {keyList}
      {descriptor.supportsApiKeyEntries && form.apiKeyEntries ? (
        <FormSection
          title={`${t('providersPage.form.apiKeyEntriesSection')} · ${filledApiKeyEntries}`}
        >
          <ApiKeyEntriesEditor
            entries={apiKeyEntries}
            removeDisabled={actualApiKeyEntries.length === 0}
            mutating={mutating}
            statuses={connectivity.openaiStatuses}
            isTestingAny={connectivity.isTestingAny}
            onUpdate={(idx, patch) =>
              updateField(
                'apiKeyEntries',
                apiKeyEntries.map((it, i) => (i === idx ? { ...it, ...patch } : it))
              )
            }
            onAdd={() => {
              const next = [...actualApiKeyEntries, emptyApiKeyEntry()];
              updateField('apiKeyEntries', next);
              return next.length - 1;
            }}
            onRemove={(idx) =>
              updateField(
                'apiKeyEntries',
                actualApiKeyEntries.filter((_, i) => i !== idx)
              )
            }
            onTest={(idx) => void connectivity.runOpenAIKey(idx)}
            onTestAll={() => void connectivity.runOpenAIAllKeys()}
          />
        </FormSection>
      ) : null}

      {descriptor.supportsTestModel ? (
        <FormSection title={t('providersPage.form.sections.connectivity')}>
          <div className={styles.field}>
            <FieldLabel
              className={styles.labelLine}
              labelClassName={styles.label}
              htmlFor={`${fid}-testModel`}
              label={t('providersPage.form.testModel')}
              hint={t('providersPage.form.testModelHint')}
              hintId={`${fid}-testModel-hint`}
            />
            <div className={styles.inlineControl}>
              <Select
                id={`${fid}-testModel`}
                value={form.testModel ?? ''}
                options={testModelOptions}
                onChange={(value) => updateField('testModel', value)}
                disabled={mutating}
                ariaLabel={t('providersPage.form.testModel')}
                ariaDescribedBy={`${fid}-testModel-hint`}
              />
              {singleConnectivity ? (
                <button
                  type="button"
                  className={styles.connectivityBtn}
                  disabled={mutating || connectivity.isTestingAny}
                  onClick={() => void singleConnectivity.run()}
                >
                  {singleConnectivity.status.state === 'loading' ? (
                    <span className={`${styles.statusIcon} ${styles.statusIconLoading}`}>
                      <IconLoader2 size={13} />
                    </span>
                  ) : null}
                  <span>{t('providersPage.connectivity.test')}</span>
                </button>
              ) : null}
            </div>
            {singleConnectivity?.status.state === 'success' ? (
              <div className={styles.connectivityRow} role="status">
                <ConnectivityStatusIcon state="success" />
                <span className={styles.connectivityHintSuccess}>
                  {t('providersPage.connectivity.success')}
                </span>
              </div>
            ) : null}
            {singleConnectivity?.status.state === 'error' ? (
              <div className={styles.connectivityError} role="status">
                {singleConnectivity.status.message}
              </div>
            ) : null}
          </div>
        </FormSection>
      ) : null}

      <FormSection title={t('providersPage.form.sections.routing')}>
        {descriptor.supportsPrefix ? (
          <div className={styles.fieldRow}>
            <div className={styles.field}>
              <label className={styles.label} htmlFor={`${fid}-prefix`}>
                {t('providersPage.form.prefix')}
              </label>
              {memberField(
                'prefix',
                t('providersPage.form.prefix'),
                <input
                  id={`${fid}-prefix`}
                  className={styles.input}
                  value={form.prefix}
                  onChange={(e) => updateField('prefix', e.target.value)}
                  disabled={mutating}
                />,
                `${fid}-prefix`
              )}
            </div>
            {descriptor.supportsPriority ? (
              <div className={styles.field}>
                <label className={styles.label} htmlFor={`${fid}-prio`}>
                  {t('providersPage.form.priority')}
                </label>
                {memberField(
                  'priority',
                  t('providersPage.form.priority'),
                  <input
                    id={`${fid}-prio`}
                    type="number"
                    className={styles.input}
                    value={form.priority ?? ''}
                    onChange={(e) =>
                      updateField(
                        'priority',
                        e.target.value === '' ? undefined : Number(e.target.value)
                      )
                    }
                    disabled={mutating}
                  />,
                  `${fid}-prio`
                )}
              </div>
            ) : null}
          </div>
        ) : null}

        {scope !== 'shared' && brand !== 'openaiCompatibility' ? (
          <div className={styles.field}>
            <FieldLabel
              className={styles.labelLine}
              labelClassName={styles.label}
              htmlFor={`${fid}-weight`}
              label={t('providersPage.form.weight')}
              hint={t('providersPage.form.weightHint')}
              hintId={`${fid}-weight-hint`}
            />
            <input
              id={`${fid}-weight`}
              type="number"
              step="1"
              max={MAX_CREDENTIAL_WEIGHT}
              className={styles.input}
              value={form.weight ?? ''}
              placeholder="1"
              aria-describedby={`${fid}-weight-hint`}
              onChange={(e) =>
                updateField('weight', e.target.value === '' ? undefined : Number(e.target.value))
              }
              disabled={mutating}
            />
          </div>
        ) : null}

        {scope !== 'shared' && isClaudeLikeBrand(brand) ? (
          <div className={styles.field}>
            <FieldLabel
              className={styles.labelLine}
              labelClassName={styles.label}
              id={`${fid}-fingerprint-profile-label`}
              label={t('providersPage.form.fingerprintProfile')}
              hint={t('providersPage.form.fingerprintProfileHint')}
              hintId={`${fid}-fingerprint-profile-hint`}
            />
            <Select
              id={`${fid}-fingerprint-profile`}
              value={form.fingerprintProfile ?? ''}
              options={[
                {
                  value: '',
                  label: t('providersPage.form.fingerprintProfileDefault'),
                },
                {
                  value: 'claude-code-cli',
                  label: t('providersPage.form.fingerprintProfileClaudeCodeCli'),
                },
              ]}
              onChange={(value) => updateField('fingerprintProfile', value)}
              disabled={mutating}
              ariaLabelledBy={`${fid}-fingerprint-profile-label`}
              ariaDescribedBy={`${fid}-fingerprint-profile-hint`}
            />
          </div>
        ) : null}

        <div className={styles.switchList}>
          {scope !== 'shared' && descriptor.supportsWebsockets ? (
            <label className={styles.checkboxRow}>
              <input
                type="checkbox"
                role="switch"
                className={styles.checkboxBox}
                checked={form.websockets ?? false}
                disabled={mutating}
                onChange={(e) => updateField('websockets', e.target.checked)}
              />
              <span className={styles.checkboxText}>{t('providersPage.form.websockets')}</span>
            </label>
          ) : null}

          {scope === 'standard' && descriptor.supportsDisabled ? (
            <label className={styles.checkboxRow}>
              <input
                type="checkbox"
                role="switch"
                className={styles.checkboxBox}
                checked={form.disabled}
                disabled={mutating}
                onChange={(e) => updateField('disabled', e.target.checked)}
              />
              <span className={styles.checkboxText}>{t('providersPage.form.disabled')}</span>
            </label>
          ) : null}
        </div>
      </FormSection>

      <FormSection title={t('providersPage.form.sections.advanced')}>
        <div className={styles.disclosureGroup}>
          {descriptor.supportsModels ? (
            <FormDisclosure
              label={t('providersPage.form.modelsSection')}
              count={existingModelNames.size}
            >
              {memberField(
                'models',
                t('providersPage.form.modelsSection'),
                <div className={styles.entriesList}>
                  {discovery.available ? (
                    <div className={styles.entriesToolbar}>
                      <button
                        type="button"
                        className={styles.connectivityBtn}
                        onClick={openDiscovery}
                        disabled={mutating}
                      >
                        <IconDownload size={13} />
                        <span>{t('providersPage.discovery.openButton')}</span>
                      </button>
                    </div>
                  ) : null}
                  {discovery.available && discoveryOpen ? (
                    <ModelDiscoveryPanel
                      loading={discovery.loading}
                      error={discovery.error}
                      models={discovery.models}
                      hasFetched={discovery.hasFetched}
                      existingNames={existingModelNames}
                      mutating={mutating}
                      onApply={applyDiscoveredModels}
                      onReload={() => void discovery.fetch()}
                      onClose={closeDiscovery}
                    />
                  ) : null}
                  <ModelEntriesEditor
                    providerBrand={brand}
                    models={modelsList}
                    supportsImage={supportsModelImage}
                    supportsThinking
                    mutating={mutating}
                    removeDisabled={false}
                    onUpdate={updateModelEntry}
                    onAdd={() => updateField('models', [...modelsList, emptyModel()])}
                    onRemove={removeModelEntry}
                  />
                </div>
              )}
            </FormDisclosure>
          ) : null}

          {descriptor.supportsHeaders ? (
            <FormDisclosure
              label={t('providersPage.form.headersSection')}
              count={headersList.filter((entry) => entry.key.trim()).length || undefined}
            >
              {memberField(
                'headers',
                t('providersPage.form.headersSection'),
                <div className={styles.entriesList}>
                  {headersList.map((entry, idx) => (
                    <div key={idx} className={styles.headerRow}>
                      <input
                        className={`${styles.input} ${styles.mono}`}
                        placeholder="X-Custom-Header"
                        aria-label={t('providersPage.form.headerName')}
                        value={entry.key}
                        onChange={(e) =>
                          updateField(
                            'headers',
                            headersList.map((it, i) =>
                              i === idx ? { ...it, key: e.target.value } : it
                            )
                          )
                        }
                        disabled={mutating}
                      />
                      <input
                        className={styles.input}
                        placeholder="value"
                        aria-label={t('providersPage.form.headerValue')}
                        value={entry.value}
                        onChange={(e) =>
                          updateField(
                            'headers',
                            headersList.map((it, i) =>
                              i === idx ? { ...it, value: e.target.value } : it
                            )
                          )
                        }
                        disabled={mutating}
                      />
                      <button
                        type="button"
                        className={styles.removeBtn}
                        disabled={mutating || headersList.length <= 1}
                        aria-label={t('providersPage.actions.delete')}
                        title={t('providersPage.actions.delete')}
                        onClick={() =>
                          updateField(
                            'headers',
                            headersList.filter((_, i) => i !== idx)
                          )
                        }
                      >
                        <IconX size={14} />
                      </button>
                    </div>
                  ))}
                  <button
                    type="button"
                    className={styles.addBtn}
                    disabled={mutating}
                    onClick={() => updateField('headers', [...headersList, emptyHeader()])}
                  >
                    <IconPlus size={13} />
                    <span>{t('providersPage.form.addHeader')}</span>
                  </button>
                </div>
              )}
            </FormDisclosure>
          ) : null}

          {descriptor.supportsExcludedModels ? (
            <FormDisclosure
              label={t('providersPage.form.excludedSection')}
              count={excludedRules.length || undefined}
            >
              {memberField(
                'excludedModelsText',
                t('providersPage.form.excludedSection'),
                <div className={styles.field}>
                  <ExcludedModelsPicker
                    value={excludedRules}
                    onChange={(next) =>
                      updateField('excludedModelsText', formatExcludedRulesText(next))
                    }
                    candidates={excludedCandidates}
                    catalogState={excludedCatalogState}
                    onRetryCatalog={discovery.available ? () => void discovery.fetch() : undefined}
                    disabled={mutating}
                    // `'*'` = 该 provider 已停用，唯一所有者是上面的停用开关。
                    // 传进来后 picker 双向过滤它，用户手打 `*` 也会被拦下并解释原因。
                    reservedRules={DISABLE_ALL_RULES}
                    reservedRuleMessage={t('providersPage.form.excludedDisabledNote')}
                  />
                </div>
              )}
            </FormDisclosure>
          ) : null}

          {scope !== 'shared' && descriptor.supportsCloak && form.cloak ? (
            <FormDisclosure label={t('providersPage.form.cloakSection')}>
              <div className={styles.field}>
                <label className={styles.label} htmlFor={`${fid}-cloak-mode`}>
                  {t('providersPage.form.cloakMode')}
                </label>
                <input
                  id={`${fid}-cloak-mode`}
                  className={styles.input}
                  value={form.cloak.mode}
                  onChange={(e) => updateCloak('mode', e.target.value)}
                  placeholder="auto / always / never"
                  disabled={mutating}
                />
              </div>
              <div className={styles.switchList}>
                <label className={styles.checkboxRow}>
                  <input
                    type="checkbox"
                    role="switch"
                    className={styles.checkboxBox}
                    checked={form.cloak.strictMode}
                    disabled={mutating}
                    onChange={(e) => updateCloak('strictMode', e.target.checked)}
                  />
                  <span className={styles.checkboxText}>{t('providersPage.form.cloakStrict')}</span>
                </label>
                <CheckboxField
                  label={t('providersPage.form.cloakCacheUserId')}
                  hint={t('providersPage.form.cloakCacheUserIdHint')}
                  checked={form.cloak.cacheUserId}
                  disabled={mutating}
                  onChange={(checked) => updateCloak('cacheUserId', checked)}
                />
              </div>
              <div className={styles.field}>
                <label className={styles.label} htmlFor={`${fid}-cloak-words`}>
                  {t('providersPage.form.cloakSensitiveWords')}
                </label>
                <textarea
                  id={`${fid}-cloak-words`}
                  className={styles.textarea}
                  rows={3}
                  value={form.cloak.sensitiveWordsText}
                  onChange={(e) => updateCloak('sensitiveWordsText', e.target.value)}
                  disabled={mutating}
                />
              </div>
              <div
                className={styles.field}
                role="group"
                aria-labelledby={`${fid}-cloak-literal-label`}
              >
                <FieldLabel
                  className={styles.labelLine}
                  labelClassName={styles.label}
                  id={`${fid}-cloak-literal-label`}
                  label={t('sensitive_replacements.literal_title')}
                  hint={t('sensitive_replacements.literal_hint')}
                  hintId={`${fid}-cloak-literal-hint`}
                />
                <LiteralReplacementEditor
                  value={form.cloak.sensitiveWordsReplace}
                  onChange={(value) => updateCloak('sensitiveWordsReplace', value)}
                  disabled={mutating}
                  describedBy={`${fid}-cloak-literal-hint`}
                  inputClassName={styles.input}
                  addButtonClassName={styles.addBtn}
                  addButtonIcon
                />
              </div>
              <div
                className={styles.field}
                role="group"
                aria-labelledby={`${fid}-cloak-pattern-label`}
              >
                <FieldLabel
                  className={styles.labelLine}
                  labelClassName={styles.label}
                  id={`${fid}-cloak-pattern-label`}
                  label={t('sensitive_replacements.pattern_title')}
                />
                {/* Evaluation order and silently dropped patterns: visible, not an InfoTip. */}
                <p id={`${fid}-cloak-pattern-hint`} className={styles.sectionDesc}>
                  {t('sensitive_replacements.pattern_hint')}
                </p>
                <PatternReplacementEditor
                  value={form.cloak.sensitivePatternsReplace}
                  onChange={(value) => updateCloak('sensitivePatternsReplace', value)}
                  disabled={mutating}
                  describedBy={`${fid}-cloak-pattern-hint`}
                  inputClassName={styles.input}
                  addButtonClassName={styles.addBtn}
                  addButtonIcon
                />
              </div>
            </FormDisclosure>
          ) : null}

          {brand !== 'openaiCompatibility' && (
            <ConnectionPoolEditor
              value={form.connectionPool}
              onChange={(pool) => updateField('connectionPool', pool)}
              disabled={mutating}
            />
          )}
          {scope !== 'shared' && (brand === 'claude' || brand === 'vertex') ? (
            <FormDisclosure label={t('providersPage.nativeKey.routing')}>
              {(brand === 'claude'
                ? (['nativeToolSearch'] as const)
                : (['interactions', 'openapi'] as const)
              ).map((key) => (
                <OptionalBooleanField
                  key={key}
                  label={t(`providersPage.nativeKey.${key}`)}
                  value={form[key]}
                  disabled={mutating}
                  onChange={(value) => updateField(key, value)}
                />
              ))}
              {brand === 'vertex' && (
                <label className={styles.field}>
                  <span className={styles.label}>{t('providersPage.nativeKey.location')}</span>
                  <input
                    className={styles.input}
                    disabled={mutating}
                    value={form.openapiLocation ?? ''}
                    onChange={(e) => updateField('openapiLocation', e.target.value)}
                  />
                </label>
              )}
            </FormDisclosure>
          ) : null}
          {scope !== 'shared' && (
            <ProviderBehaviorEditor
              brand={brand}
              value={form}
              onChange={(patch) => setForm((current) => ({ ...current, ...patch }))}
              disabled={mutating}
            />
          )}
          <RuntimePolicyEditor
            value={form.runtimePolicy ?? readRuntimePolicy()}
            onChange={(value) => updateField('runtimePolicy', value)}
            disabled={mutating}
            supportsErrors={descriptor.supportsRequestScopedErrors}
          />
        </div>
      </FormSection>
    </>
  );
}

/** Keep row identity independent of both list position and user-entered secrets. */
interface NativeRow {
  id: number;
  resource: ProviderResource | null;
  initial: ProviderEntryFormInput;
  input: ProviderEntryFormInput;
  /** On/off chosen for this key; undefined follows the group switch or the saved state. */
  disabled?: boolean;
}
function createRow(
  brand: NativeProviderFamily,
  resource: ProviderResource | null,
  id: number
): NativeRow {
  const initial = readMemberForm(
    buildInitialForm(brand, resource, resource ? 'edit' : 'create'),
    resource
  );
  return { id, resource, initial, input: initial };
}
function untouched(row: NativeRow) {
  return (
    row.initial === row.input ||
    JSON.stringify({ ...row.input, testModel: '' }) ===
      JSON.stringify({ ...row.initial, testModel: '' })
  );
}

function MemberRow({
  row,
  index,
  brand,
  mutating,
  shared,
  disabled,
  update,
  setDisabled,
  remove,
}: {
  row: NativeRow;
  index: number;
  brand: NativeProviderFamily;
  mutating: boolean;
  shared: ProviderEntryFormInput;
  disabled: boolean;
  update: Dispatch<SetStateAction<ProviderEntryFormInput>>;
  setDisabled: (disabled: boolean) => void;
  remove: () => void;
}) {
  const { t } = useTranslation();
  const [advanced, setAdvanced] = useState(false);
  const [visible, setVisible] = useState(false);
  const id = useId();
  const label = t('providersPage.form.apiKeyEntry', { index: index + 1 });
  return (
    <div>
      <div className={styles.inlineKeyRow} data-off={disabled || undefined}>
        <label className={styles.keyIndex} htmlFor={id}>
          <span className="visually-hidden">{label}</span>
          <span aria-hidden="true">{index + 1}</span>
        </label>
        <div className={styles.passwordField}>
          <input
            id={id}
            className={`${styles.input} ${styles.passwordInput}`}
            type={visible ? 'text' : 'password'}
            autoComplete="new-password"
            data-1p-ignore="true"
            data-lpignore="true"
            data-bwignore="true"
            value={row.input.apiKey}
            disabled={mutating}
            placeholder={
              row.resource
                ? `${row.resource.apiKeyPreview ?? ''} · ${t('providersPage.form.apiKeyEditPlaceholder')}`
                : t('providersPage.form.apiKeyCreatePlaceholder')
            }
            onChange={(e) => {
              const apiKey = e.target.value;
              update((prev) => ({ ...prev, apiKey }));
            }}
          />
          <button
            type="button"
            className={styles.passwordToggle}
            aria-label={`${t(visible ? 'providersPage.form.hideApiKey' : 'providersPage.form.showApiKey')} · ${label}`}
            disabled={mutating}
            onClick={() => setVisible((v) => !v)}
          >
            {visible ? <IconEyeOff size={15} /> : <IconEye size={15} />}
          </button>
        </div>
        <label className={styles.keySwitch} title={t('providersPage.nativeKey.disableKey')}>
          <input
            type="checkbox"
            role="switch"
            className={styles.checkboxBox}
            checked={disabled}
            disabled={mutating}
            aria-label={`${t('providersPage.nativeKey.disableKey')} (${label})`}
            onChange={(e) => setDisabled(e.target.checked)}
          />
          <span aria-hidden="true">{t('providersPage.nativeKey.disableShort')}</span>
        </label>
        <button
          type="button"
          className={styles.entryCardIconBtn}
          aria-label={`${t('providersPage.form.sections.advanced')} · ${label}`}
          title={t('providersPage.form.sections.advanced')}
          aria-expanded={advanced}
          aria-controls={`${id}-options`}
          onClick={() => setAdvanced((open) => !open)}
        >
          <IconSlidersHorizontal size={16} />
        </button>
        <button
          type="button"
          className={styles.removeBtn}
          disabled={mutating}
          aria-label={`${t('providersPage.actions.delete')} ${label}`}
          onClick={remove}
        >
          <IconX size={14} />
        </button>
      </div>
      <div id={`${id}-options`} hidden={!advanced}>
        {(advanced || (!row.resource && index === 0)) && (
          <div className={styles.memberPanel}>
            <p className={styles.memberPanelHint}>{t('providersPage.nativeKey.inheritHint')}</p>
            <ProviderFields
              brand={brand}
              scope="member"
              resource={row.resource}
              form={row.input}
              probeBaseUrl={shared.baseUrl}
              {...memberProbeSettings(shared, row)}
              setForm={update}
              mode={row.resource ? 'edit' : 'create'}
              mutating={mutating}
            />
          </div>
        )}
      </div>
    </div>
  );
}

function NativeConnectionForm({
  brand,
  nativeSection,
  mode,
  mutating,
  formId,
  onDirtyChange,
  onNativeSubmit,
}: NativeFormProps) {
  const { t } = useTranslation();
  const [group] = useState(() => nativeSection?.source.group ?? {});
  const [initial] = useState(() => {
    const defaults = buildInitialForm(brand, null, 'create');
    const shared = readSharedForm(group, defaults);
    if (mode === 'create') shared.baseUrl = defaults.baseUrl;
    return shared;
  });
  const [form, setForm] = useState(initial);
  const [initialRows] = useState(() =>
    nativeSection
      ? nativeSection.members.map((r, i) => createRow(brand, r, i))
      : [createRow(brand, null, 0)]
  );
  const [rows, setRows] = useState(initialRows);
  const nextId = useRef(rows.length);
  const [error, setError] = useState('');
  // Group switch intent; undefined until touched. Per-key intents live on the rows.
  const [allDisabled, setAllDisabled] = useState<boolean | undefined>();
  const rowDisabled = (row: NativeRow) => memberDisabled(row, initial.disabled, allDisabled);
  const groupDisabled = rows.length ? rows.every(rowDisabled) : (allDisabled ?? initial.disabled);
  const dirty =
    JSON.stringify(form) !== JSON.stringify(initial) ||
    rows.length !== initialRows.length ||
    nativeSwitchesDirty(rows, initial.disabled, allDisabled) ||
    rows.some((r, i) => r.id !== initialRows[i]?.id || !untouched(r));
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);
  const keyList = (
    <FormSection title={`${t('providersPage.form.apiKeyEntriesSection')} · ${rows.length}`}>
      <div className={styles.entriesList}>
        {rows.map((row, index) => (
          <MemberRow
            key={row.id}
            row={row}
            index={index}
            brand={brand}
            shared={form}
            mutating={mutating}
            disabled={rowDisabled(row)}
            setDisabled={(value) =>
              setRows((current) =>
                current.map((r) => (r.id === row.id ? { ...r, disabled: value } : r))
              )
            }
            remove={() => setRows((current) => current.filter((r) => r.id !== row.id))}
            update={(action) =>
              setRows((current) =>
                current.map((r) => {
                  if (r.id !== row.id) return r;
                  const input = typeof action === 'function' ? action(r.input) : action;
                  const restoringExclusions =
                    input.memberModes?.excludedModelsText === 'inherit' &&
                    r.input.memberModes?.excludedModelsText !== 'inherit';
                  return { ...r, input, disabled: restoringExclusions ? undefined : r.disabled };
                })
              )
            }
          />
        ))}
        <button
          type="button"
          className={styles.addBtn}
          disabled={mutating}
          onClick={() => {
            const row = createRow(brand, null, nextId.current++);
            setRows((current) => [...current, row]);
          }}
        >
          <IconPlus size={13} />
          {t('providersPage.form.addApiKeyEntry')}
        </button>
      </div>
    </FormSection>
  );
  return (
    <form
      id={formId}
      className={styles.form}
      noValidate
      onSubmit={async (e) => {
        e.preventDefault();
        if (mutating) return;
        const issue = validate(form, initial, brand, mode, 'shared', t);
        if (issue) {
          setError(issue);
          return;
        }
        const keys: NativeConnectionFormOutput['keys'] = [];
        for (const [i, row] of rows.entries()) {
          const unchanged = untouched(row);
          if (!row.resource && unchanged && rows.length === 1 && mode === 'create') continue;
          const error =
            (!row.resource || !unchanged) &&
            validate(row.input, row.initial, brand, row.resource ? 'edit' : 'create', 'member', t);
          if (error) {
            setError(`${t('providersPage.form.apiKeyEntry', { index: i + 1 })}: ${error}`);
            return;
          }
          keys.push(buildSubmittedMember(row, unchanged, initial.disabled, allDisabled));
        }
        try {
          setError('');
          await onNativeSubmit({
            group: buildSharedSettings(brand, group, initial, form),
            allDisabled,
            keys,
          });
        } catch (err) {
          setError(err instanceof Error ? t(err.message) : String(err));
        }
      }}
    >
      <label className={styles.groupSwitch}>
        <span className={styles.groupSwitchText}>
          <span className={styles.groupSwitchLabel}>{t('providersPage.nativeGroup.disabled')}</span>
          <span className={styles.groupSwitchHint}>
            {t('providersPage.connections.keysActive', {
              active: rows.filter((row) => !rowDisabled(row)).length,
              count: rows.length,
            })}
          </span>
        </span>
        <input
          type="checkbox"
          role="switch"
          className={styles.checkboxBox}
          checked={groupDisabled}
          disabled={mutating}
          onChange={(e) => {
            setAllDisabled(e.target.checked);
            // The group switch is authoritative: it resets any per-key choices.
            setRows((current) => current.map((r) => ({ ...r, disabled: undefined })));
          }}
        />
      </label>
      <ProviderFields
        brand={brand}
        scope="shared"
        resource={rows[0]?.resource ?? null}
        mode={mode}
        form={form}
        {...memberProbeSettings(form, rows[0])}
        setForm={setForm}
        mutating={mutating}
        keyList={keyList}
      />
      {error && (
        <div role="alert" className={styles.errorBox}>
          {error}
        </div>
      )}
    </form>
  );
}

function StandardProviderForm({
  brand,
  resource,
  mode,
  formId,
  mutating,
  onDirtyChange,
  onSubmit,
}: StandardFormProps) {
  const { t } = useTranslation();
  const [initial] = useState(() => buildInitialForm(brand, resource, mode));
  const [form, setForm] = useState(initial);
  const [error, setError] = useState('');
  const dirty = JSON.stringify(initial) !== JSON.stringify(form);
  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [onDirtyChange, dirty]);
  return (
    <form
      id={formId}
      className={styles.form}
      noValidate
      onSubmit={async (e) => {
        e.preventDefault();
        if (mutating) return;
        const issue = validate(form, initial, brand, mode, 'standard', t);
        if (issue) {
          setError(issue);
          return;
        }
        try {
          setError('');
          await onSubmit(form);
        } catch (err) {
          setError(err instanceof Error ? err.message : String(err));
        }
      }}
    >
      <ProviderFields
        scope="standard"
        brand={brand}
        mode={mode}
        resource={resource}
        form={form}
        setForm={setForm}
        mutating={mutating}
      />
      {error && (
        <div role="alert" className={styles.errorBox}>
          {error}
        </div>
      )}
    </form>
  );
}

export function BaseProviderForm(props: NativeFormProps | StandardFormProps) {
  return 'onNativeSubmit' in props ? (
    <NativeConnectionForm {...props} />
  ) : (
    <StandardProviderForm {...props} />
  );
}
