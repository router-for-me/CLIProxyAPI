import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { AutocompleteInput } from '@/components/ui/AutocompleteInput';
import { DatePickerInput } from '@/components/ui/DatePickerInput';
import { Select } from '@/components/ui/Select';
import { Skeleton } from '@/components/ui/Skeleton';
import { IconChevronDown, IconSlidersHorizontal, IconX } from '@/components/ui/icons';
import { useAuthStore } from '@/stores';
import { sharingApi } from '@/services/api/sharing';
import { sharingCredentials, type SharedCredential } from '@/services/api/sharingCredentials';
import { deriveAuthFileIdentity } from '@/features/authFiles/identity';
import { formatCompactNumber } from '@/utils/format';
import { normalizePaginationParams, parsePage, parsePageSize } from '@/utils/pagination';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import type { Account, Channel, PersonalKey, SharedKeyRef, UsageEntry } from './types';
import { Pager } from './shared';
import { duration, number, useSharingData } from './hooks';
import {
  buildUsageFilterQuery,
  resolveUsageKey,
  resolveUsageModel,
  withoutListParams,
  type UsageKeyOption,
} from './usageFilters';
import { buildTrend, deriveKpis, type UsageDimension } from './usageBreakdown';
import { percent } from './usageFormat';
import { UsageTrend } from './UsageTrend';
import { UsageRanking } from './UsageRanking';
import { UsageDetailSheet, UsageRecords } from './UsageRecords';
import styles from './Usage.module.scss';

type UsageScope = 'mine' | 'contributed' | 'site';
type UsageOutcome = '' | 'ok' | 'failed';
type RangePreset = 'all' | 'today' | 'yesterday' | '7d' | '30d' | 'custom';

const DAY_MS = 86_400_000;
const RANGE_PRESETS: RangePreset[] = ['today', 'yesterday', '7d', '30d', 'all', 'custom'];

const startOfDay = (value: Date) =>
  new Date(value.getFullYear(), value.getMonth(), value.getDate());

const parseDayInput = (value: string) => {
  const [year, month, day] = value.split('-').map(Number);
  return new Date(year, (month || 1) - 1, day || 1);
};

const toDayInput = (iso: string | null, offsetDays: number) => {
  if (!iso) return '';
  const time = new Date(iso).getTime();
  if (Number.isNaN(time)) return '';
  const shifted = startOfDay(new Date(time + offsetDays * DAY_MS));
  return `${shifted.getFullYear()}-${String(shifted.getMonth() + 1).padStart(2, '0')}-${String(shifted.getDate()).padStart(2, '0')}`;
};

function presetRange(preset: Exclude<RangePreset, 'all' | 'custom'>) {
  const today = startOfDay(new Date());
  const tomorrow = new Date(today.getTime() + DAY_MS);
  switch (preset) {
    case 'today':
      return { from: today, to: tomorrow };
    case 'yesterday':
      return { from: new Date(today.getTime() - DAY_MS), to: today };
    case '7d':
      return { from: new Date(today.getTime() - 6 * DAY_MS), to: tomorrow };
    case '30d':
      return { from: new Date(today.getTime() - 29 * DAY_MS), to: tomorrow };
  }
}

function detectPreset(from: string, to: string): RangePreset {
  if (!from && !to) return 'all';
  for (const preset of RANGE_PRESETS) {
    if (preset === 'all' || preset === 'custom') continue;
    const range = presetRange(preset);
    if (range.from.toISOString() === from && range.to.toISOString() === to) return preset;
  }
  return 'custom';
}

function Kpi({
  label,
  value,
  title,
  hint,
  tone,
}: {
  label: string;
  value: string;
  title?: string;
  hint?: string;
  tone?: 'danger';
}) {
  return (
    <div className={`${styles.kpi} ${tone ? styles[`kpi_${tone}`] : ''}`} title={title}>
      <span className={styles.kpiLabel}>{label}</span>
      <strong className={styles.kpiValue}>{value}</strong>
      <span className={styles.kpiHint}>{hint || '\u00a0'}</span>
    </div>
  );
}

export function UsagePage() {
  const { t } = useTranslation();
  const account = useAuthStore((state) => state.account);
  const isMobile = useMediaQuery('(max-width: 768px)');
  const me = account?.username || '';
  const isAdmin = account?.role === 'admin';
  const [params, setParams] = useSearchParams();

  const scope: UsageScope =
    params.get('scope') === 'contributed'
      ? 'contributed'
      : params.get('scope') === 'site' && isAdmin
        ? 'site'
        : 'mine';
  const page = parsePage(params.get('page'));
  const pageSize = parsePageSize(params.get('page_size'));
  const queryString = params.toString();
  const appliedKeyID = params.get('key_id') || '';
  const appliedModel = params.get('model') || '';
  const appliedChannel = params.get('channel_id') || '';
  const appliedCredentialID = params.get('credential_id') || '';
  const appliedConsumer = scope === 'mine' ? '' : params.get('consumer') || '';
  const appliedFrom = params.get('from') || '';
  const appliedTo = params.get('to') || '';
  const appliedProvider = params.get('provider') || '';
  const appliedStream = params.get('stream') || '';
  const appliedStatus = params.get('status') || '';
  const appliedEndpoint = params.get('endpoint') || '';
  const appliedEffort = params.get('effort') || '';
  const appliedBalanced = params.get('balanced') || '';
  const appliedSort = params.get('sort') || '';
  const outcome: UsageOutcome =
    params.get('outcome') === 'ok' || params.get('outcome') === 'failed'
      ? (params.get('outcome') as UsageOutcome)
      : '';
  const preset = detectPreset(appliedFrom, appliedTo);

  // URL is the single source of truth; every control writes it directly.
  const update = useCallback(
    (mutate: (next: URLSearchParams) => void, options?: { keepPage?: boolean }) => {
      const next = new URLSearchParams(params);
      mutate(next);
      if (!options?.keepPage) next.delete('page');
      setParams(next);
    },
    [params, setParams]
  );

  const [customOpen, setCustomOpen] = useState(preset === 'custom');
  const [customFrom, setCustomFrom] = useState(() => toDayInput(appliedFrom, 0));
  const [customTo, setCustomTo] = useState(() => toDayInput(appliedTo, -1));
  useEffect(() => {
    setCustomFrom(toDayInput(appliedFrom, 0));
    setCustomTo(toDayInput(appliedTo, -1));
    if (detectPreset(appliedFrom, appliedTo) === 'custom') setCustomOpen(true);
  }, [appliedFrom, appliedTo]);

  const setPreset = (value: RangePreset) => {
    if (value === 'custom') {
      setCustomOpen(true);
      return;
    }
    setCustomOpen(false);
    update((next) => {
      if (value === 'all') {
        next.delete('from');
        next.delete('to');
        return;
      }
      const range = presetRange(value);
      next.set('from', range.from.toISOString());
      next.set('to', range.to.toISOString());
    });
  };

  const applyCustom = (fromDay: string, toDay: string) => {
    if (fromDay && toDay && parseDayInput(toDay) < parseDayInput(fromDay)) return;
    update((next) => {
      if (fromDay) next.set('from', startOfDay(parseDayInput(fromDay)).toISOString());
      else next.delete('from');
      if (toDay)
        next.set('to', new Date(startOfDay(parseDayInput(toDay)).getTime() + DAY_MS).toISOString());
      else next.delete('to');
    });
  };

  // Filter suggestions load per scope and independently: one failing list never
  // hides the others, and loaded lists are not refetched on scope switches.
  const [options, setOptions] = useState<{
    channels: Channel[];
    keys: PersonalKey[];
    users: Account[];
    allKeys: SharedKeyRef[];
  }>({ channels: [], keys: [], users: [], allKeys: [] });
  const optionsLoaded = useRef(new Set<keyof typeof options>());
  useEffect(() => {
    const controller = new AbortController();
    const wanted: [keyof typeof options, (signal: AbortSignal) => Promise<unknown>][] = [
      ['channels', async (signal) => (await sharingApi.channels(signal)).channels],
    ];
    if (scope === 'mine') {
      wanted.push(['keys', async (signal) => (await sharingApi.keys(signal)).keys]);
    } else {
      wanted.push(
        ['users', async (signal) => (await sharingApi.users(signal)).users],
        ['allKeys', async (signal) => (await sharingApi.allKeys(signal)).keys]
      );
    }
    for (const [key, fetcher] of wanted) {
      if (optionsLoaded.current.has(key)) continue;
      void fetcher(controller.signal).then(
        (value) => {
          if (controller.signal.aborted) return;
          optionsLoaded.current.add(key);
          setOptions((current) => ({ ...current, [key]: value }));
        },
        () => {
          // Leave this suggestion list empty; it retries on the next scope change.
        }
      );
    }
    return () => controller.abort();
  }, [scope]);

  const filterQuery = useMemo(
    () => buildUsageFilterQuery(queryString, scope, me),
    [queryString, scope, me]
  );
  const overviewQuery = useMemo(() => withoutListParams(filterQuery), [filterQuery]);
  const {
    data: overview,
    loading: summaryLoading,
    error: summaryError,
    refresh: refreshSummary,
  } = useSharingData(
    useCallback(
      // The fetch time bounds the trend for ranges that end in the future.
      async (signal: AbortSignal) => ({
        ...(await sharingApi.overview(
          Object.fromEntries(new URLSearchParams(overviewQuery)),
          signal
        )),
        fetchedAt: Date.now(),
      }),
      [overviewQuery]
    ),
    // Within one scope the previous figures stay on screen (dimmed) while a new
    // filter loads, so the layout never collapses; a scope switch drops them.
    { keepPreviousData: false, headerRefresh: false, dataKey: `${scope}:${me}` }
  );
  const {
    data: usage,
    loading,
    error,
    refresh: refreshUsage,
  } = useSharingData(
    useCallback(
      (signal: AbortSignal) =>
        sharingApi.usage(
          { ...Object.fromEntries(new URLSearchParams(filterQuery)), page, page_size: pageSize },
          signal
        ),
      [filterQuery, page, pageSize]
    ),
    // Pagination keeps the current rows; a filter change clears them so no
    // out-of-filter rows are ever shown.
    { keepPreviousData: false, headerRefresh: false, dataKey: filterQuery }
  );
  useEffect(() => {
    const next = normalizePaginationParams(params, !loading && !error ? usage?.total : undefined);
    if (next.toString() !== params.toString()) setParams(next, { replace: true });
  }, [params, setParams, usage, loading, error]);

  const { data: credentialsList } = useSharingData(
    useCallback(async (signal: AbortSignal): Promise<SharedCredential[]> => {
      const response = await sharingCredentials.list(signal);
      return response.files as SharedCredential[];
    }, []),
    { keepPreviousData: true, headerRefresh: false, dataKey: 'credentials' }
  );
  const credentialInfo = useMemo(
    () =>
      appliedCredentialID
        ? (credentialsList?.find((file) => file.id === appliedCredentialID) ?? null)
        : null,
    [credentialsList, appliedCredentialID]
  );
  const credentialInfoLoading = Boolean(appliedCredentialID) && credentialsList === undefined;

  // The app header's refresh button reloads statistics, records and candidates together.
  useHeaderRefresh(
    useCallback(async () => {
      await Promise.all([refreshSummary(), refreshUsage()]);
    }, [refreshSummary, refreshUsage])
  );

  const kpis = useMemo(() => deriveKpis(overview?.total), [overview]);
  // A range ending in the future (today, last 7 days) is drawn up to the time
  // the statistics were fetched, never into hours that have not happened yet.
  const trend = useMemo(
    () =>
      buildTrend(overview?.buckets ?? [], {
        from: appliedFrom ? new Date(appliedFrom).getTime() : undefined,
        to: appliedTo ? new Date(appliedTo).getTime() : undefined,
        now: overview?.fetchedAt,
      }),
    [overview, appliedFrom, appliedTo]
  );

  // The overview pairs every bare model with its serving channel so even-load
  // calls can be pinned to the credential that actually served them.
  const models = useMemo(() => overview?.models ?? [], [overview]);
  const facets = overview?.facets;

  const keyLabel = useCallback(
    (key: PersonalKey | SharedKeyRef) =>
      key.name || ('hint' in key ? key.hint : '') || t('sharing.unnamed_key'),
    [t]
  );
  const keyOptions = useMemo<UsageKeyOption[]>(() => {
    if (scope === 'mine')
      return options.keys.map((key) => ({ label: keyLabel(key), keyId: key.id }));
    return [
      ...options.users.map((user) => ({ label: `@${user.username}`, consumer: user.username })),
      ...options.allKeys.map((key) => ({
        label: `${keyLabel(key)}（@${key.owner}）`,
        keyId: key.id,
      })),
    ];
  }, [options, scope, keyLabel]);
  const appliedKeyText = appliedKeyID
    ? keyOptions.find((option) => option.keyId === appliedKeyID)?.label || ''
    : appliedConsumer
      ? `@${appliedConsumer}`
      : '';

  // Typing only searches candidates; a filter applies when the text matches
  // one exactly (picked from the list or typed in full), or clears when emptied.
  const [modelText, setModelText] = useState(appliedModel);
  const [keyText, setKeyText] = useState(appliedKeyText);
  useEffect(() => setModelText(appliedModel), [appliedModel]);
  useEffect(() => setKeyText(appliedKeyText), [appliedKeyText]);

  const changeModel = (text: string) => {
    setModelText(text);
    const resolved = resolveUsageModel(text, models, appliedModel);
    if (resolved === null || resolved === appliedModel) return;
    update((next) => {
      if (resolved) next.set('model', resolved);
      else next.delete('model');
    });
  };
  const changeKey = (text: string) => {
    setKeyText(text);
    const resolved = resolveUsageKey(text, keyOptions);
    if (!resolved) return;
    if ((resolved.keyId || '') === appliedKeyID && (resolved.consumer || '') === appliedConsumer)
      return;
    update((next) => {
      next.delete('key_id');
      next.delete('consumer');
      if (resolved.keyId) next.set('key_id', resolved.keyId);
      if (resolved.consumer && scope !== 'mine') next.set('consumer', resolved.consumer);
    });
  };

  const setScope = (value: UsageScope) =>
    update((next) => {
      next.delete('key_id');
      next.delete('consumer');
      if (value === 'mine') next.delete('scope');
      else next.set('scope', value);
    });

  const credentialIdentity = credentialInfo ? deriveAuthFileIdentity(credentialInfo) : null;
  const credentialIsProvider = credentialInfo?.kind === 'provider';
  const credentialTitle = credentialInfoLoading
    ? t('common.loading')
    : credentialIsProvider
      ? credentialInfo?.label || credentialInfo?.provider_name || t('sharing.history_resource')
      : credentialIdentity?.primary ||
        overview?.rank.credential?.find((row) => row.key === appliedCredentialID)?.label ||
        t('sharing.history_resource');
  const channelTitle = appliedChannel
    ? options.channels.find((channel) => channel.id === appliedChannel)?.slug || appliedChannel
    : '';

  const appliedBaseFilters = useMemo(
    () =>
      [
        { param: 'model', value: appliedModel, label: () => appliedModel },
        { param: 'channel_id', value: appliedChannel, label: () => channelTitle },
        { param: 'key_id', value: appliedKeyID, label: () => appliedKeyText },
        { param: 'consumer', value: appliedConsumer, label: () => `@${appliedConsumer}` },
        { param: 'credential_id', value: appliedCredentialID, label: () => credentialTitle },
        { param: 'provider', value: appliedProvider, label: () => appliedProvider },
        {
          param: 'stream',
          value: appliedStream,
          label: () => t(appliedStream === 'stream' ? 'sharing.stream' : 'sharing.nonstream'),
        },
        { param: 'status', value: appliedStatus, label: () => appliedStatus },
        { param: 'endpoint', value: appliedEndpoint, label: () => appliedEndpoint },
        {
          param: 'effort',
          value: appliedEffort,
          label: () => t('sharing.effort_' + (appliedEffort || 'none')),
        },
        {
          param: 'balanced',
          value: appliedBalanced,
          label: () => t('sharing.balanced_' + appliedBalanced),
        },
      ].filter((item) => item.value),
    [
      appliedModel,
      appliedChannel,
      appliedKeyID,
      appliedConsumer,
      appliedCredentialID,
      appliedProvider,
      appliedStream,
      appliedStatus,
      appliedEndpoint,
      appliedEffort,
      appliedBalanced,
      appliedKeyText,
      credentialTitle,
      channelTitle,
      t,
    ]
  );
  const activeFilterCount = appliedBaseFilters.length;
  // Phones collapse the field filters by default so the figures stay above the fold.
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [moreOpen, setMoreOpen] = useState(
    Boolean(
      appliedCredentialID ||
      appliedProvider ||
      appliedStream ||
      appliedStatus ||
      appliedEndpoint ||
      appliedEffort ||
      appliedBalanced
    )
  );

  const clearParam = (param: string) =>
    update((next) => {
      next.delete(param);
    });
  const setParam = (param: string, value: string) =>
    update((next) => {
      if (value) next.set(param, value);
      else next.delete(param);
    });

  const clearFilters = () =>
    update((next) => {
      for (const key of [
        'model',
        'channel_id',
        'key_id',
        'consumer',
        'credential_id',
        'outcome',
        'provider',
        'stream',
        'status',
        'endpoint',
        'effort',
        'balanced',
        'sort',
      ])
        next.delete(key);
    });

  const selectRank = (dimension: UsageDimension, value: string) =>
    update((next) => {
      const param =
        dimension === 'model' ? 'model' : dimension === 'consumer' ? 'consumer' : 'credential_id';
      if (next.get(param) === value) {
        next.delete(param);
        return;
      }
      next.set(param, value);
      if (dimension === 'consumer') next.delete('key_id');
    });

  const [selected, setSelected] = useState<UsageEntry | null>(null);
  const showConsumer = scope !== 'mine';
  const rankDimensions: UsageDimension[] =
    scope === 'mine' ? ['model', 'credential'] : ['model', 'consumer', 'credential'];

  const staleSummary = summaryLoading && Boolean(overview);
  // This theme's warning and error colors coincide, so only a clearly degraded
  // success rate (below 95%) is highlighted, matching the ranking rows.
  const failureTone =
    kpis?.successRate != null && kpis.successRate < 95 ? ('danger' as const) : undefined;

  return (
    <div className={styles.page}>
      <header className={styles.top}>
        <h1 className={styles.title}>{t('sharing.usage')}</h1>
        <div className={styles.topActions}>
          <div className={styles.scopeTabs} role="tablist" aria-label={t('sharing.usage')}>
            {(['mine', 'contributed', 'site'] as const)
              .filter((value) => value !== 'site' || isAdmin)
              .map((value) => (
                <button
                  key={value}
                  type="button"
                  role="tab"
                  aria-selected={scope === value}
                  className={`${styles.scopeTab} ${scope === value ? styles.scopeTabActive : ''}`}
                  onClick={() => setScope(value)}
                >
                  {value === 'mine'
                    ? t('sharing.my_usage')
                    : value === 'contributed'
                      ? t('sharing.my_contribution')
                      : t('sharing.scope_all')}
                </button>
              ))}
          </div>
        </div>
      </header>

      <section className={styles.filterBar} aria-label={t('sharing.filters_label')}>
        <div className={styles.rangeChips} role="group" aria-label={t('sharing.range_label')}>
          {RANGE_PRESETS.map((value) => {
            const active =
              value === 'custom'
                ? customOpen || preset === 'custom'
                : !customOpen && preset === value;
            return (
              <button
                key={value}
                type="button"
                aria-pressed={active}
                className={`${styles.chip} ${active ? styles.chipActive : ''}`}
                onClick={() => setPreset(value)}
              >
                {t('sharing.range_' + value)}
              </button>
            );
          })}
        </div>
        {customOpen && (
          <div className={styles.customRange}>
            <DatePickerInput
              label={t('sharing.from')}
              value={customFrom}
              onChange={(value) => {
                setCustomFrom(value);
                applyCustom(value, customTo);
              }}
            />
            <DatePickerInput
              label={t('sharing.to')}
              value={customTo}
              onChange={(value) => {
                setCustomTo(value);
                applyCustom(customFrom, value);
              }}
            />
          </div>
        )}
        {isMobile && (
          <button
            type="button"
            className={styles.filterToggle}
            aria-expanded={filtersOpen}
            onClick={() => setFiltersOpen((value) => !value)}
          >
            <IconSlidersHorizontal size={15} />
            {t('sharing.filters_label')}
            {activeFilterCount > 0 && (
              <span className={styles.filterCount}>{activeFilterCount}</span>
            )}
            <IconChevronDown
              size={15}
              className={`${styles.filterChevron} ${filtersOpen ? styles.filterChevronOpen : ''}`}
            />
          </button>
        )}
        <div className={styles.filterFields} hidden={isMobile && !filtersOpen}>
          <AutocompleteInput
            label={t('sharing.model')}
            value={modelText}
            onChange={changeModel}
            options={models}
            placeholder={t('sharing.all_models')}
            emptyHint={t('sharing.no_model_options')}
            clearLabel={t('sharing.clear_filter')}
          />
          <AutocompleteInput
            label={scope === 'mine' ? t('sharing.key_label') : t('sharing.key_member_label')}
            value={keyText}
            onChange={changeKey}
            options={keyOptions.map((option) => option.label)}
            placeholder={scope === 'mine' ? t('sharing.all_keys') : t('sharing.key_or_member')}
            clearLabel={t('sharing.clear_filter')}
          />
          <div className="form-group">
            <label>{t('sharing.channel')}</label>
            <Select
              value={appliedChannel}
              onChange={(value) => setParam('channel_id', value)}
              ariaLabel={t('sharing.channel')}
              options={[
                { value: '', label: t('sharing.all_channels') },
                ...options.channels.map((channel) => ({
                  value: channel.id,
                  label: `${channel.slug}（${channel.owner}）`,
                })),
              ]}
            />
          </div>
        </div>
        <div className={styles.moreBar}>
          <button
            type="button"
            className={styles.filterToggle}
            aria-expanded={moreOpen}
            onClick={() => setMoreOpen((value) => !value)}
          >
            <IconSlidersHorizontal size={15} />
            {t('sharing.more_filters')}
            {activeFilterCount > 0 && (
              <span className={styles.filterCount}>{activeFilterCount}</span>
            )}
            <IconChevronDown
              size={15}
              className={`${styles.filterChevron} ${moreOpen ? styles.filterChevronOpen : ''}`}
            />
          </button>
        </div>
        {moreOpen && (
          <div className={styles.moreFilters}>
            <div className="form-group">
              <label>{t('sharing.field_credential')}</label>
              <Select
                value={appliedCredentialID}
                onChange={(value) => setParam('credential_id', value)}
                ariaLabel={t('sharing.field_credential')}
                options={[
                  { value: '', label: t('sharing.all_credentials') },
                  ...(credentialsList ?? []).map((file) => ({
                    value: file.id,
                    label: deriveAuthFileIdentity(file).primary || file.name,
                  })),
                ]}
              />
            </div>
            <div className="form-group">
              <label>{t('sharing.provider')}</label>
              <Select
                value={appliedProvider}
                onChange={(value) => setParam('provider', value)}
                ariaLabel={t('sharing.provider')}
                options={[
                  { value: '', label: t('sharing.all_providers') },
                  ...(facets?.providers ?? []).map((provider) => ({
                    value: provider,
                    label: provider,
                  })),
                ]}
              />
            </div>
            <div className="form-group">
              <label>{t('sharing.col_mode')}</label>
              <Select
                value={appliedStream}
                onChange={(value) => setParam('stream', value)}
                ariaLabel={t('sharing.col_mode')}
                options={[
                  { value: '', label: t('sharing.all_modes') },
                  { value: 'stream', label: t('sharing.stream') },
                  { value: 'nonstream', label: t('sharing.nonstream') },
                ]}
              />
            </div>
            <div className="form-group">
              <label>{t('sharing.status_label')}</label>
              <Select
                value={appliedStatus}
                onChange={(value) => setParam('status', value)}
                ariaLabel={t('sharing.status_label')}
                options={[
                  { value: '', label: t('sharing.all_statuses') },
                  { value: '2xx', label: '2xx' },
                  { value: '4xx', label: '4xx' },
                  { value: '5xx', label: '5xx' },
                  ...(facets?.statuses ?? []).map((status) => ({
                    value: String(status),
                    label: String(status),
                  })),
                ]}
              />
            </div>
            <div className="form-group">
              <label>{t('sharing.field_endpoint')}</label>
              <Select
                value={appliedEndpoint}
                onChange={(value) => setParam('endpoint', value)}
                ariaLabel={t('sharing.field_endpoint')}
                options={[
                  { value: '', label: t('sharing.all_endpoints') },
                  ...(facets?.endpoints ?? []).map((endpoint) => ({
                    value: endpoint,
                    label: endpoint,
                  })),
                ]}
              />
            </div>
            <div className="form-group">
              <label>{t('sharing.field_reasoning_effort')}</label>
              <Select
                value={appliedEffort}
                onChange={(value) => setParam('effort', value)}
                ariaLabel={t('sharing.field_reasoning_effort')}
                options={[
                  { value: '', label: t('sharing.all_efforts') },
                  ...(facets?.efforts ?? []).map((effort) => ({
                    value: effort,
                    label: t(`sharing.effort_${effort}`),
                  })),
                ]}
              />
            </div>
            <div className="form-group">
              <label>{t('sharing.auto_balance')}</label>
              <Select
                value={appliedBalanced}
                onChange={(value) => setParam('balanced', value)}
                ariaLabel={t('sharing.auto_balance')}
                options={[
                  { value: '', label: t('sharing.balanced_all') },
                  { value: 'yes', label: t('sharing.balanced_yes') },
                  { value: 'no', label: t('sharing.balanced_no') },
                ]}
              />
            </div>
          </div>
        )}
        {activeFilterCount > 0 && (
          <div className={styles.activeFilters}>
            {appliedBaseFilters.map((item) => (
              <span key={item.param} className={styles.filterTag}>
                <span className={styles.filterTagLabel}>{t('sharing.param_' + item.param)}</span>
                <span className={styles.mono}>{item.label()}</span>
                <button
                  type="button"
                  aria-label={t('sharing.filter_remove')}
                  title={t('sharing.filter_remove')}
                  onClick={() => clearParam(item.param)}
                >
                  <IconX size={13} />
                </button>
              </span>
            ))}
            <button type="button" className={styles.clearAll} onClick={clearFilters}>
              {t('sharing.clear_all_filters')}
            </button>
          </div>
        )}
      </section>

      {summaryError && (
        <div className="error-box" role="alert">
          {summaryError}
        </div>
      )}

      <section
        className={`${styles.kpis} ${staleSummary ? styles.stale : ''}`}
        aria-label={t('sharing.kpi_label')}
        aria-busy={summaryLoading}
      >
        {!kpis ? (
          Array.from({ length: 5 }, (_, index) => <Skeleton key={index} height={96} rounded={12} />)
        ) : (
          <>
            <Kpi
              label={t('sharing.requests_label')}
              value={number(kpis.requests)}
              title={t('sharing.hint_requests')}
              hint={
                kpis.executions !== kpis.requests
                  ? t('sharing.stat_upstream_calls', { count: number(kpis.executions) })
                  : undefined
              }
            />
            <Kpi
              label={t('sharing.upstream_success_rate')}
              value={percent(kpis.successRate)}
              title={t('sharing.hint_executions')}
              tone={failureTone}
              hint={
                kpis.failures > 0
                  ? t('sharing.kpi_failures', { count: number(kpis.failures) })
                  : kpis.failovers > 0
                    ? t('sharing.stat_failovers', { count: number(kpis.failovers) })
                    : undefined
              }
            />
            <Kpi
              label={t('sharing.pill_tokens')}
              value={formatCompactNumber(kpis.tokens)}
              title={[
                number(kpis.tokens),
                kpis.unknownTokenRecords > 0
                  ? t('sharing.hint_tokens_unknown', { count: number(kpis.unknownTokenRecords) })
                  : '',
              ]
                .filter(Boolean)
                .join('；')}
              hint={t('sharing.hint_tokens_io', {
                input: formatCompactNumber(kpis.inputTokens),
                output: formatCompactNumber(kpis.outputTokens),
              })}
            />
            <Kpi
              label={t('sharing.field_cache_hit')}
              value={percent(kpis.cacheRate)}
              title={t('sharing.hint_cache_rate')}
            />
            <Kpi
              label={t('sharing.kpi_latency')}
              value={kpis.avgLatency === null ? '—' : duration(Math.round(kpis.avgLatency))}
              title={t('sharing.hint_latency')}
              hint={
                kpis.avgTtft === null
                  ? undefined
                  : `${t('sharing.latency_ttft_short')} ${duration(Math.round(kpis.avgTtft))}`
              }
            />
          </>
        )}
      </section>

      <div className={`${styles.insights} ${staleSummary ? styles.stale : ''}`}>
        {overview ? (
          <>
            <UsageTrend points={trend.points} granularity={trend.granularity} />
            <UsageRanking
              rank={overview?.rank}
              dimensions={rankDimensions}
              compact={isMobile}
              active={{
                model: appliedModel || undefined,
                consumer: appliedConsumer || undefined,
                credential: appliedCredentialID || undefined,
              }}
              onSelect={selectRank}
            />
          </>
        ) : (
          !summaryError && (
            <>
              <Skeleton height={300} rounded={12} />
              <Skeleton height={300} rounded={12} />
            </>
          )
        )}
      </div>

      <section className={styles.records} aria-labelledby="usage-records-title">
        <header className={styles.recordsHead}>
          <h2 className={styles.panelTitle} id="usage-records-title">
            {t('sharing.records_title')}
            {usage && <span className={styles.recordsCount}>{number(usage.total)}</span>}
          </h2>
          <div className={styles.recordsActions}>
            <Select
              value={appliedSort}
              onChange={(value) => setParam('sort', value)}
              ariaLabel={t('sharing.sort_label')}
              size="sm"
              options={[
                { value: '', label: t('sharing.sort_time') },
                { value: 'tokens', label: t('sharing.sort_tokens') },
                { value: 'latency', label: t('sharing.sort_latency') },
              ]}
            />
            <div className={styles.miniTabs} role="tablist" aria-label={t('sharing.status_label')}>
              {(['', 'ok', 'failed'] as const).map((value) => (
                <button
                  key={value || 'all'}
                  type="button"
                  role="tab"
                  aria-selected={outcome === value}
                  className={`${styles.miniTab} ${outcome === value ? styles.miniTabActive : ''}`}
                  onClick={() =>
                    update((next) => {
                      if (value) next.set('outcome', value);
                      else next.delete('outcome');
                    })
                  }
                >
                  {t('sharing.outcome_' + (value || 'all'))}
                </button>
              ))}
            </div>
          </div>
        </header>
        {error && (
          <div className="error-box" role="alert">
            {error}
          </div>
        )}
        <UsageRecords
          items={usage?.items}
          loading={loading}
          isMobile={isMobile}
          showConsumer={showConsumer}
          onOpen={setSelected}
        />
        {Boolean(usage?.total) && (
          <Pager
            page={page}
            total={usage?.total || 0}
            pageSize={pageSize}
            loading={loading}
            onPageSize={(size) =>
              update((next) => {
                if (size === 50) next.delete('page_size');
                else next.set('page_size', String(size));
              })
            }
            onPage={(value) =>
              update(
                (next) => {
                  if (value === 1) next.delete('page');
                  else next.set('page', String(value));
                },
                { keepPage: true }
              )
            }
          />
        )}
      </section>

      <UsageDetailSheet
        entry={selected}
        showConsumer={showConsumer}
        onClose={() => setSelected(null)}
      />
    </div>
  );
}
