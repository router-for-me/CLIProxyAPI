import { useCallback, useEffect, useMemo, useState } from 'react';
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { AutocompleteInput } from '@/components/ui/AutocompleteInput';
import { EmptyState } from '@/components/ui/EmptyState';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconInfo, IconX } from '@/components/ui/icons';
import { SecondaryScreenShell } from '@/components/common/SecondaryScreenShell';
import { useEdgeSwipeBack } from '@/hooks/useEdgeSwipeBack';
import { useAuthStore, useNotificationStore } from '@/stores';
import { authFilesApi } from '@/services/api';
import type { AuthFileItem, OAuthModelAliasEntry } from '@/types';
import { generateId } from '@/utils/helpers';
import styles from './AuthFilesOAuthModelAliasEditPage.module.scss';

type AuthFileModelItem = { id: string; display_name?: string; type?: string; owned_by?: string };

type LocationState = { fromAuthFiles?: boolean } | null;

type OAuthModelMappingFormEntry = OAuthModelAliasEntry & { id: string };

const OAUTH_PROVIDER_PRESETS = [
  'gemini-cli',
  'vertex',
  'aistudio',
  'antigravity',
  'claude',
  'codex',
  'qwen',
  'kimi',
  'iflow',
];

const OAUTH_PROVIDER_EXCLUDES = new Set(['all', 'unknown', 'empty']);

const normalizeProviderKey = (value: string) => value.trim().toLowerCase();

const buildEmptyMappingEntry = (): OAuthModelMappingFormEntry => ({
  id: generateId(),
  name: '',
  alias: '',
  fork: true,
});

const normalizeMappingEntries = (
  entries?: OAuthModelAliasEntry[]
): OAuthModelMappingFormEntry[] => {
  if (!Array.isArray(entries) || entries.length === 0) {
    return [buildEmptyMappingEntry()];
  }
  return entries.map((entry) => ({
    id: generateId(),
    name: entry.name ?? '',
    alias: entry.alias ?? '',
    fork: Boolean(entry.fork),
  }));
};

export function AuthFilesOAuthModelAliasEditPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const { showNotification } = useNotificationStore();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const disableControls = connectionStatus !== 'connected';

  const [searchParams, setSearchParams] = useSearchParams();
  const providerFromParams = searchParams.get('provider') ?? '';

  const [provider, setProvider] = useState(providerFromParams);
  const [files, setFiles] = useState<AuthFileItem[]>([]);
  const [excluded, setExcluded] = useState<Record<string, string[]>>({});
  const [modelAlias, setModelAlias] = useState<Record<string, OAuthModelAliasEntry[]>>({});
  const [initialLoading, setInitialLoading] = useState(true);
  const [modelAliasUnsupported, setModelAliasUnsupported] = useState(false);

  const [mappings, setMappings] = useState<OAuthModelMappingFormEntry[]>([buildEmptyMappingEntry()]);
  const [modelsList, setModelsList] = useState<AuthFileModelItem[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<'unsupported' | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    setProvider(providerFromParams);
  }, [providerFromParams]);

  const providerOptions = useMemo(() => {
    const extraProviders = new Set<string>();
    Object.keys(excluded).forEach((value) => extraProviders.add(value));
    Object.keys(modelAlias).forEach((value) => extraProviders.add(value));
    files.forEach((file) => {
      if (typeof file.type === 'string') {
        extraProviders.add(file.type);
      }
      if (typeof file.provider === 'string') {
        extraProviders.add(file.provider);
      }
    });

    const normalizedExtras = Array.from(extraProviders)
      .map((value) => value.trim())
      .filter((value) => value && !OAUTH_PROVIDER_EXCLUDES.has(value.toLowerCase()));

    const baseSet = new Set(OAUTH_PROVIDER_PRESETS.map((value) => value.toLowerCase()));
    const extraList = normalizedExtras
      .filter((value) => !baseSet.has(value.toLowerCase()))
      .sort((a, b) => a.localeCompare(b));

    return [...OAUTH_PROVIDER_PRESETS, ...extraList];
  }, [excluded, files, modelAlias]);

  const getTypeLabel = useCallback(
    (type: string): string => {
      const key = `auth_files.filter_${type}`;
      const translated = t(key);
      if (translated !== key) return translated;
      if (type.toLowerCase() === 'iflow') return 'iFlow';
      return type.charAt(0).toUpperCase() + type.slice(1);
    },
    [t]
  );

  const resolvedProviderKey = useMemo(() => normalizeProviderKey(provider), [provider]);
  const title = useMemo(() => t('oauth_model_alias.add_title'), [t]);
  const headerHint = useMemo(() => {
    if (!provider.trim()) {
      return t('oauth_model_alias.provider_hint');
    }
    if (modelsLoading) {
      return t('oauth_model_alias.model_source_loading');
    }
    if (modelsError === 'unsupported') {
      return t('oauth_model_alias.model_source_unsupported');
    }
    return t('oauth_model_alias.model_source_loaded', { count: modelsList.length });
  }, [modelsError, modelsList.length, modelsLoading, provider, t]);

  const handleBack = useCallback(() => {
    const state = location.state as LocationState;
    if (state?.fromAuthFiles) {
      navigate(-1);
      return;
    }
    navigate('/auth-files', { replace: true });
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

    const load = async () => {
      setInitialLoading(true);
      setModelAliasUnsupported(false);
      try {
        const [filesResult, excludedResult, aliasResult] = await Promise.allSettled([
          authFilesApi.list(),
          authFilesApi.getOauthExcludedModels(),
          authFilesApi.getOauthModelAlias(),
        ]);

        if (cancelled) return;

        if (filesResult.status === 'fulfilled') {
          setFiles(filesResult.value?.files ?? []);
        }

        if (excludedResult.status === 'fulfilled') {
          setExcluded(excludedResult.value ?? {});
        }

        if (aliasResult.status === 'fulfilled') {
          setModelAlias(aliasResult.value ?? {});
          return;
        }

        const err = aliasResult.status === 'rejected' ? aliasResult.reason : null;
        const status =
          typeof err === 'object' && err !== null && 'status' in err
            ? (err as { status?: unknown }).status
            : undefined;

        if (status === 404) {
          setModelAliasUnsupported(true);
          return;
        }
      } finally {
        if (!cancelled) {
          setInitialLoading(false);
        }
      }
    };

    load().catch(() => {
      if (!cancelled) {
        setInitialLoading(false);
      }
    });

    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!resolvedProviderKey) {
      setMappings([buildEmptyMappingEntry()]);
      return;
    }
    const existing = modelAlias[resolvedProviderKey] ?? [];
    setMappings(normalizeMappingEntries(existing));
  }, [modelAlias, resolvedProviderKey]);

  useEffect(() => {
    if (!resolvedProviderKey || modelAliasUnsupported) {
      setModelsList([]);
      setModelsError(null);
      setModelsLoading(false);
      return;
    }

    let cancelled = false;
    setModelsLoading(true);
    setModelsError(null);

    authFilesApi
      .getModelDefinitions(resolvedProviderKey)
      .then((models) => {
        if (cancelled) return;
        setModelsList(models);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const status =
          typeof err === 'object' && err !== null && 'status' in err
            ? (err as { status?: unknown }).status
            : undefined;

        if (status === 404) {
          setModelsList([]);
          setModelsError('unsupported');
          return;
        }

        const errorMessage = err instanceof Error ? err.message : '';
        showNotification(`${t('notification.load_failed')}: ${errorMessage}`, 'error');
      })
      .finally(() => {
        if (cancelled) return;
        setModelsLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [modelAliasUnsupported, resolvedProviderKey, showNotification, t]);

  const updateProvider = useCallback(
    (value: string) => {
      setProvider(value);
      const next = new URLSearchParams(searchParams);
      const trimmed = value.trim();
      if (trimmed) {
        next.set('provider', trimmed);
      } else {
        next.delete('provider');
      }
      setSearchParams(next, { replace: true });
    },
    [searchParams, setSearchParams]
  );

  const updateMappingEntry = useCallback(
    (index: number, field: keyof OAuthModelAliasEntry, value: string | boolean) => {
      setMappings((prev) =>
        prev.map((entry, idx) => (idx === index ? { ...entry, [field]: value } : entry))
      );
    },
    []
  );

  const addMappingEntry = useCallback(() => {
    setMappings((prev) => [...prev, buildEmptyMappingEntry()]);
  }, []);

  const removeMappingEntry = useCallback((index: number) => {
    setMappings((prev) => {
      const next = prev.filter((_, idx) => idx !== index);
      return next.length ? next : [buildEmptyMappingEntry()];
    });
  }, []);

  const handleSave = useCallback(async () => {
    const channel = provider.trim();
    if (!channel) {
      showNotification(t('oauth_model_alias.provider_required'), 'error');
      return;
    }

    const seen = new Set<string>();
    const normalized = mappings
      .map((entry) => {
        const name = String(entry.name ?? '').trim();
        const alias = String(entry.alias ?? '').trim();
        if (!name || !alias) return null;
        const key = `${name.toLowerCase()}::${alias.toLowerCase()}::${entry.fork ? '1' : '0'}`;
        if (seen.has(key)) return null;
        seen.add(key);
        return entry.fork ? { name, alias, fork: true } : { name, alias };
      })
      .filter(Boolean) as OAuthModelAliasEntry[];

    setSaving(true);
    try {
      if (normalized.length) {
        await authFilesApi.saveOauthModelAlias(channel, normalized);
      } else {
        await authFilesApi.deleteOauthModelAlias(channel);
      }
      showNotification(t('oauth_model_alias.save_success'), 'success');
      handleBack();
    } catch (err: unknown) {
      const errorMessage = err instanceof Error ? err.message : '';
      showNotification(`${t('oauth_model_alias.save_failed')}: ${errorMessage}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [handleBack, mappings, provider, showNotification, t]);

  const canSave = !disableControls && !saving && !modelAliasUnsupported;

  return (
    <SecondaryScreenShell
      ref={swipeRef}
      title={title}
      onBack={handleBack}
      backLabel={t('common.back')}
      backAriaLabel={t('common.back')}
      contentClassName={styles.pageContent}
      rightAction={
        <Button size="sm" onClick={handleSave} loading={saving} disabled={!canSave}>
          {t('oauth_model_alias.save')}
        </Button>
      }
      isLoading={initialLoading}
      loadingLabel={t('common.loading')}
    >
      {modelAliasUnsupported ? (
        <Card>
          <EmptyState
            title={t('oauth_model_alias.upgrade_required_title')}
            description={t('oauth_model_alias.upgrade_required_desc')}
          />
        </Card>
      ) : (
        <>
          <Card className={styles.settingsCard}>
            <div className={styles.settingsHeader}>
              <div className={styles.settingsHeaderTitle}>
                <IconInfo size={16} />
                <span>{t('oauth_model_alias.title')}</span>
              </div>
              <div className={styles.settingsHeaderHint}>{headerHint}</div>
            </div>

            <div className={styles.settingsSection}>
              <div className={styles.settingsRow}>
                <div className={styles.settingsInfo}>
                  <div className={styles.settingsLabel}>{t('oauth_model_alias.provider_label')}</div>
                  <div className={styles.settingsDesc}>{t('oauth_model_alias.provider_hint')}</div>
                </div>
                <div className={styles.settingsControl}>
                  <AutocompleteInput
                    id="oauth-model-alias-provider"
                    placeholder={t('oauth_model_alias.provider_placeholder')}
                    value={provider}
                    onChange={updateProvider}
                    options={providerOptions}
                    disabled={disableControls || saving}
                    wrapperStyle={{ marginBottom: 0 }}
                  />
                </div>
              </div>

              {providerOptions.length > 0 && (
                <div className={styles.tagList}>
                  {providerOptions.map((option) => {
                    const isActive = normalizeProviderKey(provider) === option.toLowerCase();
                    return (
                      <button
                        key={option}
                        type="button"
                        className={`${styles.tag} ${isActive ? styles.tagActive : ''}`}
                        onClick={() => updateProvider(option)}
                        disabled={disableControls || saving}
                      >
                        {getTypeLabel(option)}
                      </button>
                    );
                  })}
                </div>
              )}
            </div>
          </Card>

          <Card className={styles.settingsCard}>
            <div className={styles.mappingsHeader}>
              <div className={styles.mappingsTitle}>{t('oauth_model_alias.alias_label')}</div>
              <Button
                variant="secondary"
                size="sm"
                onClick={addMappingEntry}
                disabled={disableControls || saving || modelAliasUnsupported}
              >
                {t('oauth_model_alias.add_alias')}
              </Button>
            </div>

            <div className={styles.mappingsBody}>
              {mappings.map((entry, index) => (
                <div key={entry.id} className={styles.mappingRow}>
                  <AutocompleteInput
                    wrapperStyle={{ flex: 1, marginBottom: 0 }}
                    placeholder={t('oauth_model_alias.alias_name_placeholder')}
                    value={entry.name}
                    onChange={(val) => updateMappingEntry(index, 'name', val)}
                    disabled={disableControls || saving}
                    options={modelsList.map((model) => ({
                      value: model.id,
                      label:
                        model.display_name && model.display_name !== model.id
                          ? model.display_name
                          : undefined,
                    }))}
                  />
                  <span className={styles.mappingSeparator}>→</span>
                  <input
                    className={`input ${styles.mappingAliasInput}`}
                    placeholder={t('oauth_model_alias.alias_placeholder')}
                    value={entry.alias}
                    onChange={(e) => updateMappingEntry(index, 'alias', e.target.value)}
                    disabled={disableControls || saving}
                  />
                  <div className={styles.mappingFork}>
                    <ToggleSwitch
                      label={t('oauth_model_alias.alias_fork_label')}
                      labelPosition="left"
                      checked={Boolean(entry.fork)}
                      onChange={(value) => updateMappingEntry(index, 'fork', value)}
                      disabled={disableControls || saving}
                    />
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => removeMappingEntry(index)}
                    disabled={disableControls || saving || mappings.length <= 1}
                    title={t('common.delete')}
                    aria-label={t('common.delete')}
                  >
                    <IconX size={14} />
                  </Button>
                </div>
              ))}
            </div>
          </Card>
        </>
      )}
    </SecondaryScreenShell>
  );
}
