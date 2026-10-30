import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { AutocompleteInput } from '@/components/ui/AutocompleteInput';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { DatePickerInput } from '@/components/ui/DatePickerInput';
import { EmptyState } from '@/components/ui/EmptyState';
import { Select } from '@/components/ui/Select';
import { Sheet } from '@/components/ui/Sheet';
import { useAuthStore } from '@/stores';
import { sharingApi } from '@/services/api/sharing';
import { formatCompactNumber } from '@/utils/format';
import { normalizePaginationParams, parsePage, parsePageSize } from '@/utils/pagination';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import {
  Table,
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from '@/components/ui/Table';
import type { Account, Channel, PersonalKey, SharedKeyRef, UsageEntry } from './types';
import { DailyTrend, LoadState, PageHeader, Pager } from './shared';
import { compactDate, date, duration, number, useSharingActions, useSharingData } from './hooks';
import {
  buildModelCandidateQuery,
  buildUsageFilterQuery,
  resolveUsageKey,
  resolveUsageModel,
  withoutOutcome,
  type UsageKeyOption,
} from './usageFilters';
import styles from './Sharing.module.scss';

type UsageScope = 'mine' | 'contributed' | 'site';
type UsageOutcome = '' | 'ok' | 'failed';
type RangePreset = 'all' | 'today' | 'yesterday' | '7d' | '30d' | 'custom';

const DAY_MS = 86400000;
const RANGE_PRESETS: RangePreset[] = ['all', 'today', 'yesterday', '7d', '30d', 'custom'];

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

const stripJsonExt = (name: string) => name.replace(/\.json$/i, '');
// Channel-mounted credential files are named "<channel>-<type>#<n>-<internal>.json";
// the pinned prefix alone identifies the credential, the rest is internal.
const credentialDisplayName = (name: string) =>
  stripJsonExt(name).replace(/^(.+#\d+)-[^#]+$/, '$1');
const tokensPerSecond = (entry: UsageEntry) =>
  entry.tokens_known && entry.latency_ms > 0 && entry.output_tokens > 0
    ? Math.round(entry.output_tokens / (entry.latency_ms / 1000))
    : null;

// Cache hit rate: the share of total input tokens served from the upstream cache.
export const cacheHitRate = (
  entry: Pick<UsageEntry, 'input_tokens' | 'cache_read_tokens'>
): number | null =>
  entry.input_tokens > 0 && entry.cache_read_tokens > 0
    ? (entry.cache_read_tokens / entry.input_tokens) * 100
    : null;

const statusKind = (status: number, failed: boolean): 'ok' | 'fail' | 'interrupted' =>
  status === 499 ? 'interrupted' : failed || status >= 400 ? 'fail' : 'ok';

const entryStatus = (entry: UsageEntry): 'ok' | 'fail' | 'interrupted' =>
  statusKind(entry.status, entry.failed);

export function RequestStatus({ status, failed = false }: { status: number; failed?: boolean }) {
  const { t } = useTranslation();
  const kind = statusKind(status, failed);
  const label =
    kind === 'interrupted'
      ? t('sharing.status_interrupted')
      : kind === 'fail'
        ? t('sharing.status_failed')
        : t('sharing.status_success');
  return (
    <span
      className={
        kind === 'interrupted'
          ? styles.pillInterrupted
          : kind === 'fail'
            ? styles.pillFail
            : styles.pillOk
      }
    >
      {label} {status}
    </span>
  );
}

function Field({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className={styles.field}>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

function DetailSection({
  title,
  meta,
  children,
}: {
  title: ReactNode;
  meta?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className={styles.detailSection}>
      <h3 className={styles.detailSectionTitle}>
        {title}
        {meta && <span className={styles.detailSectionMeta}>{meta}</span>}
      </h3>
      {children}
    </section>
  );
}

// Upstream error bodies are usually JSON envelopes; surface the human message
// and keep the raw body available separately.
export function errorMessage(body: string | undefined): string {
  const text = (body || '').trim();
  if (!text) return '';
  const pick = (value: unknown): string => {
    if (typeof value === 'string') return value;
    if (!value || typeof value !== 'object') return '';
    const record = value as Record<string, unknown>;
    return pick(record.message) || pick(record.error) || pick(record.detail);
  };
  try {
    const message = pick(JSON.parse(text)).trim();
    if (message) return message;
  } catch {
    // Not JSON; fall back to the raw text.
  }
  return text;
}

function ErrorBlock({ body }: { body?: string }) {
  const { t } = useTranslation();
  const { copy } = useSharingActions(noRefresh);
  const raw = (body || '').trim();
  if (!raw) return <p className={styles.errorEmpty}>{t('sharing.no_error_body')}</p>;
  const message = errorMessage(raw);
  return (
    <div className={styles.errorBlock}>
      <div className={styles.errorHead}>
        <span>{t('sharing.error_title')}</span>
        <button type="button" className={styles.linkButton} onClick={() => void copy(raw)}>
          {t('common.copy')}
        </button>
      </div>
      <p className={styles.errorMessage}>{message}</p>
      {message !== raw && (
        <details className={styles.errorRaw}>
          <summary>{t('sharing.error_raw')}</summary>
          <pre>{raw}</pre>
        </details>
      )}
    </div>
  );
}

const noRefresh = async () => {};

function AttemptTimeline({ entry }: { entry: UsageEntry }) {
  const { t } = useTranslation();
  const trace = entry.trace_id || entry.request_id;
  const [state, setState] = useState<{ items: UsageEntry[]; error: string; loading: boolean }>({
    items: [],
    error: '',
    loading: true,
  });
  useEffect(() => {
    const controller = new AbortController();
    setState({ items: [], error: '', loading: true });
    sharingApi.usageAttempts(trace, controller.signal).then(
      (result) => {
        if (!controller.signal.aborted)
          setState({ items: result.items ?? [], error: '', loading: false });
      },
      (error) => {
        if (controller.signal.aborted) return;
        setState({
          items: [],
          error: error instanceof Error ? error.message : String(error),
          loading: false,
        });
      }
    );
    return () => controller.abort();
  }, [trace]);
  const items = state.items.length ? state.items : [entry];
  return (
    <DetailSection
      title={t('sharing.attempts_title')}
      meta={t('sharing.balanced_attempts', { count: Math.max(items.length, entry.attempts || 1) })}
    >
      <LoadState loading={state.loading} error={state.error} />
      <ol className={styles.timeline}>
        {items.map((attempt, index) => {
          const kind = entryStatus(attempt);
          const current = attempt.id === entry.id;
          const message = kind === 'ok' ? '' : errorMessage(attempt.error);
          return (
            <li
              key={attempt.id}
              className={`${styles.timelineItem} ${
                kind === 'ok'
                  ? styles.timelineOk
                  : kind === 'fail'
                    ? styles.timelineFail
                    : styles.timelineMuted
              } ${current ? styles.timelineCurrent : ''}`}
            >
              <div className={styles.timelineHead}>
                <span className={styles.timelineIndex}>
                  {t('sharing.attempt_index', { index: index + 1 })}
                </span>
                <span className={`${styles.mono} ${styles.timelineModel}`}>
                  {servingModelId(attempt) || attempt.model}
                </span>
                <RequestStatus status={attempt.status} failed={attempt.failed} />
              </div>
              <div className={styles.cellSecondary}>
                {[
                  attempt.credential_name ? credentialDisplayName(attempt.credential_name) : '',
                  attempt.owner ? `@${attempt.owner}` : '',
                  `${t('sharing.latency_total_short')} ${duration(attempt.latency_ms)}`,
                  attempt.ttft_ms > 0
                    ? `${t('sharing.latency_ttft_short')} ${duration(attempt.ttft_ms)}`
                    : '',
                ]
                  .filter(Boolean)
                  .join(' · ')}
              </div>
              {message && (
                <div className={styles.timelineError} title={message}>
                  {message}
                </div>
              )}
              {kind !== 'ok' && index < items.length - 1 && (
                <div className={styles.timelineNext}>{t('sharing.attempt_switched')}</div>
              )}
            </li>
          );
        })}
      </ol>
    </DetailSection>
  );
}

function UsageDetailSheet({
  entry,
  showConsumer,
  onClose,
}: {
  entry: UsageEntry | null;
  showConsumer: boolean;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const { copy } = useSharingActions(noRefresh);
  const cacheRate = entry ? cacheHitRate(entry) : null;
  const rate = entry ? tokensPerSecond(entry) : null;
  const kind = entry ? entryStatus(entry) : 'ok';
  const serving = entry ? servingModelId(entry) : '';
  const idValue = (value: string) => (
    <span className={styles.idValue}>
      <span className={styles.mono}>{value}</span>
      <button type="button" className={styles.linkButton} onClick={() => void copy(value)}>
        {t('common.copy')}
      </button>
    </span>
  );
  return (
    <Sheet
      open={!!entry}
      onClose={onClose}
      size="md"
      eyebrow={entry ? date(entry.at) : undefined}
      title={entry?.model}
      description={
        entry
          ? `${entry.endpoint} · ${t(entry.stream ? 'sharing.stream' : 'sharing.nonstream')}`
          : undefined
      }
    >
      {entry && (
        <div className={styles.detailBody}>
          <div
            className={`${styles.outcomeBanner} ${
              kind === 'ok'
                ? styles.outcomeOk
                : kind === 'fail'
                  ? styles.outcomeFail
                  : styles.outcomeMuted
            }`}
          >
            <div className={styles.outcomeMain}>
              <RequestStatus status={entry.status} failed={entry.failed} />
              {entry.balanced && <BalancedTag attempts={entry.attempts} />}
            </div>
            <div className={styles.outcomeStats}>
              <span>
                {t('sharing.latency_total_short')} <strong>{duration(entry.latency_ms)}</strong>
              </span>
              {entry.ttft_ms > 0 && (
                <span>
                  {t('sharing.latency_ttft_short')} <strong>{duration(entry.ttft_ms)}</strong>
                </span>
              )}
              {rate !== null && (
                <span>
                  <strong>{rate}</strong> t/s
                </span>
              )}
            </div>
            {kind === 'interrupted' && (
              <p className={styles.cellSecondary}>{t('sharing.status_interrupted_hint')}</p>
            )}
            {kind === 'fail' && <ErrorBlock body={entry.error} />}
          </div>

          {entry.balanced && <AttemptTimeline entry={entry} />}

          <DetailSection title={t('sharing.section_call')}>
            <dl className={styles.fieldGrid}>
              {showConsumer && <Field label={t('sharing.consumer')}>@{entry.consumer}</Field>}
              <Field label={t('sharing.field_key')}>{entry.key_name || '—'}</Field>
              <Field label={t('sharing.field_model')}>
                <span className={styles.mono}>{entry.model}</span>
              </Field>
              {serving && (
                <Field label={t('sharing.field_serving_model')}>
                  <span className={styles.mono}>{serving}</span>
                </Field>
              )}
              {entry.upstream_model && entry.upstream_model !== entry.model && (
                <Field label={t('sharing.field_upstream_model')}>
                  <span className={styles.mono}>{entry.upstream_model}</span>
                </Field>
              )}
              <Field label={t('sharing.channel')}>
                {entry.channel || '—'}
                {entry.owner && entry.owner !== entry.consumer && (
                  <span className={styles.cellSecondary}>（@{entry.owner}）</span>
                )}
              </Field>
              <Field label={t('sharing.field_credential')}>
                {entry.credential_name ? credentialDisplayName(entry.credential_name) : '—'}
              </Field>
              {entry.reasoning_effort && (
                <Field label={t('sharing.field_reasoning_effort')}>
                  <span className={styles.mono}>{entry.reasoning_effort}</span>
                </Field>
              )}
            </dl>
          </DetailSection>

          {(entry.tokens_known || kind === 'ok') && (
            <DetailSection title={t('sharing.field_tokens')}>
              {entry.tokens_known ? (
                <div className={styles.tokenTiles}>
                  <div className={styles.tokenTile}>
                    <span className={styles.statLabel}>{t('sharing.token_input')}</span>
                    <strong>{number(entry.input_tokens)}</strong>
                    {entry.cache_read_tokens > 0 && (
                      <span className={styles.cellSecondary}>
                        {t('sharing.token_cache', { count: number(entry.cache_read_tokens) })}
                      </span>
                    )}
                    {entry.cache_creation_tokens > 0 && (
                      <span className={styles.cellSecondary}>
                        {t('sharing.token_cache_write', {
                          count: number(entry.cache_creation_tokens),
                        })}
                      </span>
                    )}
                  </div>
                  <div className={styles.tokenTile}>
                    <span className={styles.statLabel}>{t('sharing.token_output')}</span>
                    <strong>{number(entry.output_tokens)}</strong>
                    {entry.reasoning_tokens > 0 && (
                      <span className={styles.cellSecondary}>
                        {t('sharing.token_reasoning', { count: number(entry.reasoning_tokens) })}
                      </span>
                    )}
                  </div>
                  {cacheRate !== null && (
                    <div className={styles.tokenTile}>
                      <span className={styles.statLabel}>{t('sharing.field_cache_hit')}</span>
                      <strong>{cacheRate.toFixed(1)}%</strong>
                    </div>
                  )}
                </div>
              ) : (
                <p className={styles.cellSecondary}>{t('sharing.unknown')}</p>
              )}
            </DetailSection>
          )}

          {(entry.trace_id || entry.request_id) && (
            <DetailSection title={t('sharing.section_ids')}>
              <dl className={styles.fieldGrid}>
                {entry.trace_id && (
                  <Field label={t('sharing.field_trace')}>{idValue(entry.trace_id)}</Field>
                )}
                {entry.request_id && (
                  <Field label={t('sharing.field_request')}>{idValue(entry.request_id)}</Field>
                )}
              </dl>
            </DetailSection>
          )}
        </div>
      )}
    </Sheet>
  );
}

export function BalancedTag({ attempts }: { attempts?: number }) {
  const { t } = useTranslation();
  return (
    <span className={styles.balancedTag} title={t('sharing.balanced_tag_hint')}>
      {t('sharing.auto_balance')}
      {attempts && attempts > 1 ? ` · ${t('sharing.balanced_attempts', { count: attempts })}` : ''}
    </span>
  );
}

// Bare base-model calls (keys with even load balancing) are served by one
// channel's pinned model ID; surface it next to the requested name.
export const servingModelId = (entry: Partial<Pick<UsageEntry, 'model' | 'channel'>>): string =>
  entry.model && entry.channel && !entry.model.startsWith(`${entry.channel}/`)
    ? `${entry.channel}/${entry.model}`
    : '';

const tokenLine = (entry: UsageEntry) =>
  `${number(entry.input_tokens)}${
    entry.cache_read_tokens > 0 ? `（${number(entry.cache_read_tokens)}）` : ''
  } / ${number(entry.output_tokens)}${
    entry.reasoning_tokens > 0 ? `（${number(entry.reasoning_tokens)}）` : ''
  }`;

export function UsageLogRow({
  entry,
  showConsumer,
  onOpen,
}: {
  entry: UsageEntry;
  showConsumer: boolean;
  onOpen: () => void;
}) {
  const { t } = useTranslation();
  const status = entryStatus(entry);
  const rate = tokensPerSecond(entry);
  const serving = servingModelId(entry);
  const latencyClass =
    status === 'fail'
      ? styles.latencyFail
      : status === 'interrupted'
        ? styles.latencyInterrupted
        : '';
  const error = status === 'fail' ? errorMessage(entry.error) : '';
  return (
    <TableRow className={styles.clickableRow} onClick={onOpen}>
      <TableCell>
        <div className={`${styles.cellPrimary} ${styles.mono}`}>{compactDate(entry.at)}</div>
        <div className={styles.cellSecondary}>
          {t(entry.stream ? 'sharing.stream' : 'sharing.nonstream')}
          {rate !== null ? ` · ${rate} t/s` : ''}
        </div>
      </TableCell>
      <TableCell>
        <div className={styles.cellPrimary}>{entry.key_name || '—'}</div>
        {showConsumer && <div className={styles.cellSecondary}>@{entry.consumer}</div>}
      </TableCell>
      <TableCell>
        <div className={`${styles.cellPrimary} ${styles.mono}`}>{entry.model}</div>
        <div className={styles.modelMeta}>
          {entry.balanced && <BalancedTag attempts={entry.attempts} />}
          {serving && (
            <span className={`${styles.cellSecondary} ${styles.mono}`}>（{serving}）</span>
          )}
        </div>
      </TableCell>
      <TableCell>
        {entry.tokens_known ? (
          <div className={`${styles.cellPrimary} ${styles.mono}`}>{tokenLine(entry)}</div>
        ) : (
          <span className={styles.cellSecondary}>{t('sharing.unknown')}</span>
        )}
      </TableCell>
      <TableCell>
        <div className={`${styles.latency} ${latencyClass}`}>
          {entry.ttft_ms > 0 && (
            <div>
              {t('sharing.latency_ttft_short')} {duration(entry.ttft_ms)}
            </div>
          )}
          <div>
            {t('sharing.latency_total_short')} {duration(entry.latency_ms)}
          </div>
        </div>
      </TableCell>
      <TableCell>
        <RequestStatus status={entry.status} failed={entry.failed} />
        {error && (
          <div className={styles.rowError} title={error}>
            {error}
          </div>
        )}
      </TableCell>
    </TableRow>
  );
}

function UsageLogCard({
  entry,
  showConsumer,
  onOpen,
}: {
  entry: UsageEntry;
  showConsumer: boolean;
  onOpen: () => void;
}) {
  const { t } = useTranslation();
  const status = entryStatus(entry);
  const rate = tokensPerSecond(entry);
  const serving = servingModelId(entry);
  const error = status === 'fail' ? errorMessage(entry.error) : '';
  return (
    <button type="button" className={`${styles.keyCard} ${styles.usageCard}`} onClick={onOpen}>
      <span className={styles.keyCardHead}>
        <span className={`${styles.cellPrimary} ${styles.mono}`}>{entry.model}</span>
        <RequestStatus status={entry.status} failed={entry.failed} />
      </span>
      {(entry.balanced || serving) && (
        <span className={styles.modelMeta}>
          {entry.balanced && <BalancedTag attempts={entry.attempts} />}
          {serving && (
            <span className={`${styles.cellSecondary} ${styles.mono}`}>（{serving}）</span>
          )}
        </span>
      )}
      <span className={styles.cellSecondary}>
        {compactDate(entry.at)} · {entry.key_name || '—'}
        {showConsumer ? ` · @${entry.consumer}` : ''} · {entry.channel}
      </span>
      <span className={styles.cellSecondary}>
        {duration(entry.latency_ms)}
        {rate !== null ? ` · ${rate} t/s` : ''}
        {entry.tokens_known
          ? ` · ${t('sharing.hint_tokens_io', {
              input: `${number(entry.input_tokens)}${
                entry.cache_read_tokens > 0 ? `（${number(entry.cache_read_tokens)}）` : ''
              }`,
              output: `${number(entry.output_tokens)}${
                entry.reasoning_tokens > 0 ? `（${number(entry.reasoning_tokens)}）` : ''
              }`,
            })}`
          : ''}
        {` · ${t(entry.stream ? 'sharing.stream' : 'sharing.nonstream')}`}
      </span>
      {error && <span className={styles.rowError}>{error}</span>}
    </button>
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
  const appliedKeyID = params.get('key_id') || '';
  const queryString = params.toString();
  const appliedModel = params.get('model') || '';
  const appliedChannel = params.get('channel_id') || '';
  const appliedConsumer = scope === 'mine' ? '' : params.get('consumer') || '';
  const appliedFrom = params.get('from') || '';
  const appliedTo = params.get('to') || '';
  const outcome: UsageOutcome =
    params.get('outcome') === 'ok' || params.get('outcome') === 'failed'
      ? (params.get('outcome') as UsageOutcome)
      : '';

  const [model, setModel] = useState(params.get('model') || '');
  const [channelID, setChannelID] = useState(params.get('channel_id') || '');
  const [keyDraft, setKeyText] = useState<string | null>(null);
  const [preset, setPreset] = useState<RangePreset>(() =>
    detectPreset(params.get('from') || '', params.get('to') || '')
  );
  const [customFrom, setCustomFrom] = useState(() => toDayInput(params.get('from'), 0));
  const [customTo, setCustomTo] = useState(() => toDayInput(params.get('to'), -1));

  useEffect(() => {
    setModel(appliedModel);
    setChannelID(appliedChannel);
    setKeyText(null);
    setPreset(detectPreset(appliedFrom, appliedTo));
    setCustomFrom(toDayInput(appliedFrom, 0));
    setCustomTo(toDayInput(appliedTo, -1));
  }, [appliedModel, appliedChannel, appliedKeyID, appliedConsumer, appliedFrom, appliedTo, scope]);

  const [options, setOptions] = useState<{
    channels: Channel[];
    keys: PersonalKey[];
    users: Account[];
    allKeys: SharedKeyRef[];
  }>({ channels: [], keys: [], users: [], allKeys: [] });
  const [selected, setSelected] = useState<UsageEntry | null>(null);

  // Filter suggestions load per scope and independently: each list lands when
  // it arrives, one failing list never hides the others or the usage records,
  // and pieces that already loaded are not refetched on scope switches.
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
    return () => {
      controller.abort();
    };
  }, [scope]);

  const filterQuery = useMemo(
    () => buildUsageFilterQuery(queryString, scope, me),
    [queryString, scope, me]
  );
  const {
    data: summary,
    loading: summaryLoading,
    error: summaryError,
    refresh: refreshSummary,
  } = useSharingData(
    useCallback(
      (signal: AbortSignal) =>
        sharingApi.summary(
          Object.fromEntries(new URLSearchParams(withoutOutcome(filterQuery))),
          signal
        ),
      [filterQuery]
    ),
    { keepPreviousData: false, headerRefresh: false }
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
          {
            ...Object.fromEntries(new URLSearchParams(filterQuery)),
            page,
            page_size: pageSize,
          },
          signal
        ),
      [filterQuery, page, pageSize]
    ),
    // Same filter scope keeps the previous rows while a new page loads; a
    // scope/filter/account change clears them so no out-of-scope rows show.
    { keepPreviousData: false, headerRefresh: false, dataKey: filterQuery }
  );
  useEffect(() => {
    const next = normalizePaginationParams(params, !loading && !error ? usage?.total : undefined);
    if (next.toString() !== params.toString()) setParams(next, { replace: true });
  }, [params, setParams, usage, loading, error]);

  const groups = useMemo(() => summary?.groups ?? [], [summary]);

  // Model suggestions use a summary that ignores the applied model filter and
  // pagination, so choosing a model never shrinks the dropdown to itself.
  // Without an applied model that query is exactly the main filter query, so
  // the main summary doubles as the candidate source (no duplicate request).
  const candidateQuery = useMemo(
    () => buildModelCandidateQuery(queryString, scope, me),
    [queryString, scope, me]
  );
  const { data: candidateSummary, refresh: refreshCandidates } = useSharingData(
    useCallback(
      (signal: AbortSignal) =>
        appliedModel
          ? sharingApi.summary(Object.fromEntries(new URLSearchParams(candidateQuery)), signal)
          : Promise.resolve(null),
      [appliedModel, candidateQuery]
    ),
    { keepPreviousData: false, headerRefresh: false }
  );
  useHeaderRefresh(
    useCallback(async () => {
      await Promise.all([refreshSummary(), refreshUsage(), refreshCandidates()]);
    }, [refreshSummary, refreshUsage, refreshCandidates])
  );

  // Candidates pair every bare model (even load balancing) with each channel
  // that served it, in the same display form the log entries use; selecting
  // one pins the filter to that exact serving channel server-side.
  const models = useMemo(() => {
    const set = new Set<string>();
    const groups = appliedModel ? candidateSummary?.groups : summary?.groups;
    for (const group of groups ?? []) {
      if (!group.model) continue;
      set.add(group.model);
      const serving = servingModelId(group);
      if (serving) set.add(`${group.model}（${serving}）`);
    }
    return [...set].sort((a, b) => a.localeCompare(b));
  }, [appliedModel, candidateSummary, summary]);

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
  const keyFilterOptions = useMemo(() => keyOptions.map((option) => option.label), [keyOptions]);
  const keyText =
    keyDraft ??
    (appliedKeyID
      ? keyOptions.find((option) => option.keyId === appliedKeyID)?.label || ''
      : appliedConsumer
        ? `@${appliedConsumer}`
        : '');
  const resolvedModel = resolveUsageModel(model, models, params.get('model') || '');
  const resolvedKey =
    keyDraft === null && (appliedKeyID || appliedConsumer)
      ? { label: keyText, keyId: appliedKeyID || undefined, consumer: appliedConsumer || undefined }
      : resolveUsageKey(keyText, keyOptions);
  const canApply = resolvedModel !== null && resolvedKey !== null;

  const apply = () => {
    if (resolvedModel === null || resolvedKey === null) return;
    const next = new URLSearchParams();
    if (scope !== 'mine') next.set('scope', scope);
    if (pageSize !== 50) next.set('page_size', String(pageSize));
    if (outcome) next.set('outcome', outcome);
    if (resolvedModel) next.set('model', resolvedModel);
    if (channelID) next.set('channel_id', channelID);
    if (resolvedKey.keyId) next.set('key_id', resolvedKey.keyId);
    if (resolvedKey.consumer && scope !== 'mine') next.set('consumer', resolvedKey.consumer);
    if (preset === 'custom') {
      if (customFrom) next.set('from', startOfDay(parseDayInput(customFrom)).toISOString());
      if (customTo)
        next.set(
          'to',
          new Date(startOfDay(parseDayInput(customTo)).getTime() + DAY_MS).toISOString()
        );
    } else if (preset !== 'all') {
      const range = presetRange(preset);
      next.set('from', range.from.toISOString());
      next.set('to', range.to.toISOString());
    }
    setParams(next);
  };

  const reset = () => {
    setModel('');
    setChannelID('');
    setKeyText('');
    setPreset('all');
    setCustomFrom('');
    setCustomTo('');
    const next = new URLSearchParams();
    if (scope !== 'mine') next.set('scope', scope);
    if (pageSize !== 50) next.set('page_size', String(pageSize));
    setParams(next);
  };

  const setScope = (value: UsageScope) => {
    const next = new URLSearchParams(params);
    next.delete('page');
    next.delete('key_id');
    next.delete('consumer');
    setKeyText('');
    if (value === 'mine') next.delete('scope');
    else next.set('scope', value);
    setParams(next);
  };

  const setOutcome = (value: UsageOutcome) => {
    const next = new URLSearchParams(params);
    next.delete('page');
    if (value) next.set('outcome', value);
    else next.delete('outcome');
    setParams(next);
  };

  const setPageSize = (size: number) => {
    const next = new URLSearchParams(params);
    if (size === 50) next.delete('page_size');
    else next.set('page_size', String(size));
    next.delete('page');
    setParams(next);
  };

  const total = summary?.total;
  const failureRate =
    total && total.executions > 0 ? (total.failures / total.executions) * 100 : null;
  const tokenHint = total
    ? [
        number(total.total_tokens),
        t('sharing.hint_tokens_io', {
          input: number(total.input_tokens),
          output: number(total.output_tokens),
        }),
        total.unknown_token_records > 0
          ? t('sharing.hint_tokens_unknown', { count: number(total.unknown_token_records) })
          : '',
      ]
        .filter(Boolean)
        .join('；')
    : undefined;
  const cacheRate =
    total && total.input_tokens > 0 && total.cache_read_tokens > 0
      ? (total.cache_read_tokens / total.input_tokens) * 100
      : null;

  const showConsumer = scope !== 'mine';

  return (
    <div className={styles.page}>
      <PageHeader
        title={t('sharing.usage')}
        actions={
          <div className={styles.segmented}>
            {(['mine', 'contributed', 'site'] as const)
              .filter((value) => value !== 'site' || isAdmin)
              .map((value) => (
                <button
                  key={value}
                  type="button"
                  className={`${styles.segmentedItem} ${scope === value ? styles.active : ''}`}
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
        }
      />
      <Card>
        <div className={styles.filters}>
          <div className="form-group">
            <label>{t('sharing.range_label')}</label>
            <Select
              value={preset}
              onChange={(value) => setPreset(value as RangePreset)}
              ariaLabel={t('sharing.range_label')}
              options={RANGE_PRESETS.map((value) => ({
                value,
                label: t('sharing.range_' + value),
              }))}
            />
          </div>
          {preset === 'custom' && (
            <>
              <DatePickerInput
                label={t('sharing.from')}
                value={customFrom}
                onChange={setCustomFrom}
              />
              <DatePickerInput label={t('sharing.to')} value={customTo} onChange={setCustomTo} />
            </>
          )}
          <AutocompleteInput
            label={t('sharing.model')}
            value={model}
            onChange={setModel}
            options={models}
            placeholder={t('sharing.all_models')}
            emptyHint={t('sharing.no_model_options')}
            clearLabel={t('sharing.clear_filter')}
          />
          <div className="form-group">
            <label>{t('sharing.channel')}</label>
            <Select
              value={channelID}
              onChange={setChannelID}
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
          <AutocompleteInput
            label={scope === 'mine' ? t('sharing.key_label') : t('sharing.key_member_label')}
            value={keyText}
            onChange={setKeyText}
            options={keyFilterOptions}
            placeholder={scope === 'mine' ? t('sharing.all_keys') : t('sharing.key_or_member')}
            clearLabel={t('sharing.clear_filter')}
          />
          <div className={styles.actions}>
            <Button onClick={apply} disabled={!canApply}>
              {t('sharing.apply_filter')}
            </Button>
            <Button variant="secondary" onClick={reset}>
              {t('sharing.clear_filter')}
            </Button>
          </div>
        </div>
      </Card>
      <div className={styles.usageStats}>
        <div className={styles.usageStat} title={t('sharing.hint_executions')}>
          <span className={styles.statLabel}>{t('sharing.pill_calls')}</span>
          <strong className={styles.statValue}>{total ? number(total.executions) : '—'}</strong>
          <span className={styles.statHint}>
            {total && total.failovers
              ? t('sharing.stat_failovers', { count: number(total.failovers) })
              : ' '}
          </span>
        </div>
        <div
          className={`${styles.usageStat} ${
            failureRate !== null && failureRate > 0 ? styles.usageStatDanger : ''
          }`}
        >
          <span className={styles.statLabel}>{t('sharing.success_rate')}</span>
          <strong className={styles.statValue}>
            {failureRate !== null ? `${(100 - failureRate).toFixed(1)}%` : '—'}
          </strong>
          <span className={styles.statHint}>
            {total
              ? t('sharing.hint_failures_detail', {
                  failures: number(total.failures),
                  executions: number(total.executions),
                })
              : ' '}
          </span>
        </div>
        <div className={styles.usageStat} title={tokenHint}>
          <span className={styles.statLabel}>{t('sharing.pill_tokens')}</span>
          <strong className={styles.statValue}>
            {total ? formatCompactNumber(total.total_tokens) : '—'}
          </strong>
          <span className={styles.statHint}>
            {total
              ? t('sharing.hint_tokens_io', {
                  input: formatCompactNumber(total.input_tokens),
                  output: formatCompactNumber(total.output_tokens),
                })
              : ' '}
          </span>
        </div>
        <div className={styles.usageStat}>
          <span className={styles.statLabel}>{t('sharing.field_cache_hit')}</span>
          <strong className={styles.statValue}>
            {cacheRate !== null ? `${cacheRate.toFixed(1)}%` : '—'}
          </strong>
          <span className={styles.statHint}>
            {total && total.cache_read_tokens > 0
              ? t('sharing.token_cache', { count: formatCompactNumber(total.cache_read_tokens) })
              : ' '}
          </span>
        </div>
      </div>
      <Card title={t('sharing.trend_title')}>
        {summaryLoading && !summary ? null : <DailyTrend groups={groups} />}
      </Card>
      <div className={styles.listToolbar}>
        <h2 className={styles.listTitle}>{t('sharing.records_title')}</h2>
        <div className={styles.segmented}>
          {(['', 'ok', 'failed'] as const).map((value) => (
            <button
              key={value || 'all'}
              type="button"
              className={`${styles.segmentedItem} ${outcome === value ? styles.active : ''}`}
              onClick={() => setOutcome(value)}
            >
              {t('sharing.outcome_' + (value || 'all'))}
            </button>
          ))}
        </div>
      </div>
      <LoadState loading={loading || summaryLoading} error={error || summaryError} />
      {!loading && !error && !usage?.items.length && (
        <EmptyState title={t('sharing.no_records')} description={t('sharing.no_records_hint')} />
      )}
      {Boolean(usage?.items.length) &&
        (isMobile ? (
          <div className={styles.keyCards}>
            {usage?.items.map((entry) => (
              <UsageLogCard
                key={entry.id}
                entry={entry}
                showConsumer={showConsumer}
                onOpen={() => setSelected(entry)}
              />
            ))}
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                {['time', 'key_label', 'model', 'col_tokens', 'col_latency', 'status_label'].map(
                  (key) => (
                    <TableHead key={key}>{t('sharing.' + key)}</TableHead>
                  )
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {usage?.items.map((entry) => (
                <UsageLogRow
                  key={entry.id}
                  entry={entry}
                  showConsumer={showConsumer}
                  onOpen={() => setSelected(entry)}
                />
              ))}
            </TableBody>
          </Table>
        ))}
      <Pager
        page={page}
        total={usage?.total || 0}
        pageSize={pageSize}
        loading={loading}
        onPageSize={setPageSize}
        onPage={(value) => {
          const next = new URLSearchParams(params);
          if (value === 1) next.delete('page');
          else next.set('page', String(value));
          setParams(next);
        }}
      />
      <UsageDetailSheet
        entry={selected}
        showConsumer={showConsumer}
        onClose={() => setSelected(null)}
      />
    </div>
  );
}
