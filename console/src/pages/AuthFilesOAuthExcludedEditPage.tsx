import { useCallback, useId, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { EmptyState } from '@/components/ui/EmptyState';
import {
  ExcludedModelChipRow,
  ExcludedModelRuleChip,
  ExcludedModelsPicker,
  isWildcardRule,
  normalizeExcludedRules,
} from '@/components/excludedModels';
import { useAuthStore, useNotificationStore, useThemeStore } from '@/stores';
import { authFilesApi } from '@/services/api';
import {
  getAuthFileIcon,
  getTypeLabel,
  normalizeProviderKey,
} from '@/features/authFiles/constants';
import {
  ModelRulesWorkspace,
  ProviderRail,
} from '@/features/authFiles/modelRules/ModelRulesWorkspace';
import { RuleGroup } from '@/features/authFiles/modelRules/RuleGroup';
import {
  buildExcludedDraft,
  buildExcludedSavePlan,
  getDirtyExcludedProviders,
  getExcludedStats,
  rebaseDraft,
  setExcludedRules,
  type ExcludedDraft,
} from '@/features/authFiles/modelRules/draft';
import {
  runSavePlan,
  useModelRulesData,
  useProviderModelCatalog,
} from '@/features/authFiles/modelRules/useModelRulesData';
import {
  buildRailItems,
  useModelRulesRoute,
} from '@/features/authFiles/modelRules/useModelRulesRoute';
import styles from '@/features/authFiles/modelRules/ModelRules.module.scss';

export function AuthFilesOAuthExcludedEditPage() {
  const { t } = useTranslation();
  const pickerLabelId = useId();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);

  const data = useModelRulesData('excluded');
  const baseline = data.excluded;

  const [draftState, setDraftState] = useState<{ revision: number; draft: ExcludedDraft }>({
    revision: 0,
    draft: {},
  });
  const [keepProviders, setKeepProviders] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);

  const draft = draftState.draft;
  const dirtyProviders = useMemo(
    () => getDirtyExcludedProviders(baseline, draft),
    [baseline, draft]
  );
  // Once initialized, a failed reload must not disable the unsaved-navigation guard.
  const isDirty = data.revision > 0 && dirtyProviders.length > 0;

  const route = useModelRulesRoute('excluded', isDirty);
  const selected = route.provider;

  // 与别名工作台相同：基线到达的同一次渲染里对齐草稿，保存失败的 provider 保留输入。
  if (draftState.revision !== data.revision) {
    setDraftState({
      revision: data.revision,
      draft: rebaseDraft(buildExcludedDraft(baseline), draft, keepProviders),
    });
    if (keepProviders.length) setKeepProviders([]);
  }

  const setDraft = useCallback((update: (current: ExcludedDraft) => ExcludedDraft) => {
    setDraftState((current) => ({ ...current, draft: update(current.draft) }));
  }, []);

  const disabled = connectionStatus !== 'connected' || saving;

  const counts = useMemo(() => {
    const result: Record<string, number> = {};
    Object.entries(draft).forEach(([provider, rules]) => {
      if (rules.length) result[provider] = rules.length;
    });
    return result;
  }, [draft]);
  const stats = useMemo(() => getExcludedStats(draft), [draft]);

  const railItems = useMemo(
    () =>
      buildRailItems(
        {
          files: data.files,
          excluded: baseline,
          modelAlias: data.modelAlias,
          configured: Object.keys(baseline),
          counts,
          dirtyProviders,
          selected,
        },
        t,
        resolvedTheme
      ),
    [baseline, counts, data.files, data.modelAlias, dirtyProviders, resolvedTheme, selected, t]
  );

  const overviewProviders = useMemo(
    () => railItems.filter((item) => item.count > 0).map((item) => item.key),
    [railItems]
  );

  // 目录只为正在编辑的那个 provider 加载；概览只读规则本身，不需要目录。
  const catalogProviders = useMemo(() => (selected ? [selected] : []), [selected]);
  const { catalog, retry: retryCatalog } = useProviderModelCatalog(
    catalogProviders,
    data.status === 'ready'
  );
  const selectedCatalog = selected ? catalog[selected] : undefined;
  const candidates = useMemo(
    () =>
      (selectedCatalog?.models ?? []).map((model) => ({
        id: model.id,
        displayName: model.display_name,
      })),
    [selectedCatalog]
  );

  const selectProvider = useCallback(
    (value: string) => route.openProvider(normalizeProviderKey(value)),
    [route]
  );

  const handleDiscard = useCallback(() => {
    setDraft(() => buildExcludedDraft(baseline));
  }, [baseline, setDraft]);

  const handleSave = useCallback(async () => {
    if (saving) return;
    setSaving(true);
    try {
      const outcome = await runSavePlan(
        buildExcludedSavePlan(baseline, draft),
        authFilesApi.saveOauthExcludedModels,
        authFilesApi.deleteOauthExcludedEntry
      );
      if (!outcome) return;
      setKeepProviders(outcome.failed);
      const current = await data.reload();
      if (!current) return;
      if (outcome.failed.length) {
        showNotification(
          outcome.message
            ? `${t('oauth_excluded.save_failed')}: ${outcome.message}`
            : t('oauth_excluded.save_failed'),
          'error'
        );
      } else {
        showNotification(t('oauth_excluded.save_success'), 'success');
      }
    } finally {
      setSaving(false);
    }
  }, [baseline, data, draft, saving, showNotification, t]);

  const summaryFor = (provider: string) =>
    counts[provider]
      ? t('model_rules.meta_rules', { count: counts[provider] })
      : t('oauth_excluded.no_models');

  const clearAction = (provider: string) =>
    counts[provider] ? (
      <button
        type="button"
        className={styles.quietDanger}
        onClick={() => setDraft((current) => setExcludedRules(current, provider, []))}
        disabled={disabled}
      >
        {t('model_rules.clear_provider')}
      </button>
    ) : null;

  const meta =
    stats.rules > 0
      ? [
          t('model_rules.meta_providers', { count: stats.providers }),
          t('model_rules.meta_rules', { count: stats.rules }),
        ]
      : [t('model_rules.meta_empty')];

  return (
    <ModelRulesWorkspace
      kind="excluded"
      title={t('oauth_excluded.title')}
      meta={data.status === 'ready' ? meta : []}
      status={data.status}
      errorMessage={data.errorMessage}
      unsupportedTitle={t('oauth_excluded.upgrade_required_title')}
      unsupportedDescription={t('oauth_excluded.upgrade_required_desc')}
      onRetry={() => void data.reload()}
      onBack={route.goBack}
      onSwitchKind={route.switchKind}
      rail={
        <ProviderRail
          items={railItems}
          selected={selected}
          total={stats.rules}
          disabled={disabled}
          onSelect={selectProvider}
        />
      }
      dirtyCount={isDirty ? dirtyProviders.length : 0}
      saving={saving}
      canSave={data.status === 'ready' && isDirty && !disabled}
      onSave={() => void handleSave()}
      onDiscard={handleDiscard}
    >
      {selected ? (
        <RuleGroup
          provider={selected}
          label={getTypeLabel(t, selected)}
          icon={getAuthFileIcon(selected, resolvedTheme)}
          summary={summaryFor(selected)}
          dirty={dirtyProviders.includes(selected)}
          dirtyLabel={t('model_rules.unsaved_mark')}
          actions={clearAction(selected)}
        >
          <div className={styles.groupBody}>
            <p className={styles.fieldLabel} id={pickerLabelId}>
              {t('oauth_excluded.models_label')}
            </p>
            <ExcludedModelsPicker
              value={draft[selected] ?? []}
              onChange={(next) => setDraft((current) => setExcludedRules(current, selected, next))}
              candidates={candidates}
              catalogState={selectedCatalog?.state ?? 'loading'}
              onRetryCatalog={() => retryCatalog(selected)}
              disabled={disabled}
              labelledBy={pickerLabelId}
            />
          </div>
        </RuleGroup>
      ) : overviewProviders.length > 0 ? (
        overviewProviders.map((provider) => {
          const rules = normalizeExcludedRules(draft[provider] ?? []);
          return (
            <RuleGroup
              key={provider}
              provider={provider}
              label={getTypeLabel(t, provider)}
              icon={getAuthFileIcon(provider, resolvedTheme)}
              summary={summaryFor(provider)}
              dirty={dirtyProviders.includes(provider)}
              dirtyLabel={t('model_rules.unsaved_mark')}
              actions={
                <>
                  <button
                    type="button"
                    className={styles.quietAction}
                    onClick={() => selectProvider(provider)}
                  >
                    {t('common.edit')}
                  </button>
                  {clearAction(provider)}
                </>
              }
            >
              <div className={styles.groupBody}>
                <ExcludedModelChipRow>
                  {rules.map((rule) => (
                    <ExcludedModelRuleChip
                      key={rule.toLowerCase()}
                      label={rule}
                      variant={isWildcardRule(rule) ? 'wildcard' : 'exact'}
                      onRemove={() =>
                        setDraft((current) =>
                          setExcludedRules(
                            current,
                            provider,
                            rules.filter((item) => item !== rule)
                          )
                        )
                      }
                      removeAriaLabel={t('excluded_models.chip_remove', { rule })}
                      disabled={disabled}
                    />
                  ))}
                </ExcludedModelChipRow>
              </div>
            </RuleGroup>
          );
        })
      ) : (
        <div className={styles.notice}>
          <EmptyState title={t('oauth_excluded.list_empty_all')} />
        </div>
      )}
    </ModelRulesWorkspace>
  );
}
