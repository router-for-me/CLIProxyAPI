import { memo, useEffect, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Sheet } from '@/components/ui/Sheet';
import { Skeleton } from '@/components/ui/Skeleton';
import { sharingApi } from '@/services/api/sharing';
import { LoadState } from './shared';
import { compactDate, date, duration, number, useSharingActions } from './hooks';
import type { UsageEntry } from './types';
import { BalancedTag, RequestStatus } from './UsageStatus';
import {
  cacheHitRate,
  credentialDisplayName,
  entryStatus,
  errorMessage,
  servingModelId,
  tokenLine,
  tokensPerSecond,
} from './usageFormat';
import styles from './Usage.module.scss';

const noRefresh = async () => {};

const latencyClass = (entry: UsageEntry) => {
  const kind = entryStatus(entry);
  return kind === 'fail'
    ? styles.latencyFail
    : kind === 'interrupted'
      ? styles.latencyMuted
      : styles.latencyOk;
};

function Latency({ entry, inline = false }: { entry: UsageEntry; inline?: boolean }) {
  const { t } = useTranslation();
  const ttft =
    entry.ttft_ms > 0 ? `${t('sharing.latency_ttft_short')} ${duration(entry.ttft_ms)}` : '';
  const total = `${t('sharing.latency_total_short')} ${duration(entry.latency_ms)}`;
  return (
    <span className={`${styles.latency} ${latencyClass(entry)}`}>
      {inline ? (
        [ttft, total].filter(Boolean).join(' · ')
      ) : (
        <>
          {ttft && <span>{ttft}</span>}
          <span>{total}</span>
        </>
      )}
    </span>
  );
}

function ModelCell({ entry }: { entry: UsageEntry }) {
  const serving = servingModelId(entry);
  return (
    <span className={styles.modelCell}>
      <span className={`${styles.mono} ${styles.modelName}`} title={entry.model}>
        {entry.model}
      </span>
      {(entry.balanced || serving) && (
        <span className={styles.modelMeta}>
          {entry.balanced && <BalancedTag attempts={entry.attempts} />}
          {serving && <span className={`${styles.muted} ${styles.mono}`}>（{serving}）</span>}
        </span>
      )}
    </span>
  );
}

/** Desktop row. Exported for markup tests. */
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
  const rate = tokensPerSecond(entry);
  const error = entryStatus(entry) === 'fail' ? errorMessage(entry.error) : '';
  return (
    <tr
      className={styles.recordRow}
      onClick={onOpen}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          onOpen();
        }
      }}
      tabIndex={0}
    >
      <td>
        <span className={`${styles.mono} ${styles.strong}`}>{compactDate(entry.at)}</span>
        <span className={styles.muted}>
          {t(entry.stream ? 'sharing.stream' : 'sharing.nonstream')}
          {rate !== null ? ` · ${rate} t/s` : ''}
        </span>
      </td>
      <td>
        <span className={styles.strong}>{entry.key_name || '—'}</span>
        {showConsumer && <span className={styles.muted}>@{entry.consumer}</span>}
      </td>
      <td>
        <ModelCell entry={entry} />
      </td>
      <td>
        {entry.tokens_known ? (
          <span className={`${styles.mono} ${styles.strong}`}>{tokenLine(entry)}</span>
        ) : (
          <span className={styles.muted}>{t('sharing.unknown')}</span>
        )}
      </td>
      <td>
        <Latency entry={entry} />
      </td>
      <td>
        <RequestStatus status={entry.status} failed={entry.failed} />
        {error && (
          <span className={styles.rowError} title={error}>
            {error}
          </span>
        )}
      </td>
    </tr>
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
  const rate = tokensPerSecond(entry);
  const error = entryStatus(entry) === 'fail' ? errorMessage(entry.error) : '';
  const field = (label: string, value: ReactNode, wide = false) => (
    <span className={`${styles.cardField} ${wide ? styles.cardFieldWide : ''}`}>
      <span className={styles.cardLabel}>{label}</span>
      <span className={styles.cardValue}>{value}</span>
    </span>
  );
  return (
    <button type="button" className={styles.recordCard} onClick={onOpen}>
      <span className={styles.recordCardHead}>
        <ModelCell entry={entry} />
        <RequestStatus status={entry.status} failed={entry.failed} />
      </span>
      <span className={styles.cardFields}>
        {field(t('sharing.time'), <span className={styles.mono}>{compactDate(entry.at)}</span>)}
        {field(
          t('sharing.key_label'),
          `${entry.key_name || '—'}${showConsumer ? `（@${entry.consumer}）` : ''}`
        )}
        {field(t('sharing.col_latency'), <Latency entry={entry} inline />)}
        {field(
          t('sharing.col_mode'),
          `${t(entry.stream ? 'sharing.stream' : 'sharing.nonstream')}${
            rate !== null ? ` · ${rate} t/s` : ''
          }`
        )}
        {field(
          t('sharing.col_tokens'),
          entry.tokens_known ? (
            <span className={styles.mono}>{tokenLine(entry)}</span>
          ) : (
            t('sharing.unknown')
          ),
          true
        )}
      </span>
      {error && <span className={styles.rowError}>{error}</span>}
    </button>
  );
}

export const UsageRecords = memo(function UsageRecords({
  items,
  loading,
  isMobile,
  showConsumer,
  onOpen,
}: {
  items: UsageEntry[] | undefined;
  loading: boolean;
  isMobile: boolean;
  showConsumer: boolean;
  onOpen: (entry: UsageEntry) => void;
}) {
  const { t } = useTranslation();
  if (!items) {
    return loading ? (
      <div className={styles.recordSkeleton} aria-hidden="true">
        {Array.from({ length: 6 }, (_, index) => (
          <Skeleton key={index} height={isMobile ? 132 : 52} rounded={10} />
        ))}
      </div>
    ) : null;
  }
  if (!items.length) {
    return (
      <div className={styles.emptyBlock}>
        <strong>{t('sharing.no_records')}</strong>
        <span>{t('sharing.no_records_hint')}</span>
      </div>
    );
  }
  return isMobile ? (
    <div className={`${styles.recordCards} ${loading ? styles.stale : ''}`}>
      {items.map((entry) => (
        <UsageLogCard
          key={entry.id}
          entry={entry}
          showConsumer={showConsumer}
          onOpen={() => onOpen(entry)}
        />
      ))}
    </div>
  ) : (
    <div className={`${styles.recordTableWrap} ${loading ? styles.stale : ''}`}>
      <table className={styles.recordTable}>
        <colgroup>
          <col className={styles.colTime} />
          <col className={styles.colKey} />
          <col className={styles.colModel} />
          <col className={styles.colTokens} />
          <col className={styles.colLatency} />
          <col className={styles.colStatus} />
        </colgroup>
        <thead>
          <tr>
            {['time', 'key_label', 'model', 'col_tokens', 'col_latency', 'status_label'].map(
              (key) => (
                <th key={key} scope="col">
                  {t('sharing.' + key)}
                </th>
              )
            )}
          </tr>
        </thead>
        <tbody>
          {items.map((entry) => (
            <UsageLogRow
              key={entry.id}
              entry={entry}
              showConsumer={showConsumer}
              onOpen={() => onOpen(entry)}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
});

function Field({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className={styles.detailField}>
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

function ErrorBlock({ body }: { body?: string }) {
  const { t } = useTranslation();
  const { copy } = useSharingActions(noRefresh);
  const raw = (body || '').trim();
  if (!raw) return <p className={styles.muted}>{t('sharing.no_error_body')}</p>;
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
          const message = kind === 'ok' ? '' : errorMessage(attempt.error);
          return (
            <li
              key={attempt.id}
              className={`${styles.timelineItem} ${styles[`timeline_${kind}`]} ${
                attempt.id === entry.id ? styles.timelineCurrent : ''
              }`}
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
              <dl className={styles.timelineMeta}>
                {attempt.credential_name && (
                  <Field label={t('sharing.field_credential')}>
                    {credentialDisplayName(attempt.credential_name)}
                  </Field>
                )}
                <Field label={t('sharing.col_latency')}>
                  <Latency entry={attempt} inline />
                </Field>
              </dl>
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

export function UsageDetailSheet({
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
  // Keep the last entry while the sheet animates closed.
  const [shown, setShown] = useState<UsageEntry | null>(entry);
  useEffect(() => {
    if (entry) setShown(entry);
  }, [entry]);
  const current = entry ?? shown;
  const cacheRate = current ? cacheHitRate(current) : null;
  const rate = current ? tokensPerSecond(current) : null;
  const kind = current ? entryStatus(current) : 'ok';
  const serving = current ? servingModelId(current) : '';
  const idValue = (value: string) => (
    <span className={styles.idValue}>
      <span className={styles.mono} title={value}>
        {value}
      </span>
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
      eyebrow={current ? date(current.at) : undefined}
      title={current?.model}
      description={
        current
          ? `${current.endpoint} · ${t(current.stream ? 'sharing.stream' : 'sharing.nonstream')}`
          : undefined
      }
    >
      {current && (
        <div className={styles.detailBody}>
          <div className={`${styles.outcome} ${styles[`outcome_${kind}`]}`}>
            <div className={styles.outcomeMain}>
              <RequestStatus status={current.status} failed={current.failed} />
              {current.balanced && <BalancedTag attempts={current.attempts} />}
            </div>
            <div className={styles.outcomeStats}>
              <span>
                {t('sharing.latency_total_short')} <strong>{duration(current.latency_ms)}</strong>
              </span>
              {current.ttft_ms > 0 && (
                <span>
                  {t('sharing.latency_ttft_short')} <strong>{duration(current.ttft_ms)}</strong>
                </span>
              )}
              {rate !== null && (
                <span>
                  <strong>{rate}</strong> t/s
                </span>
              )}
            </div>
            {kind === 'interrupted' && (
              <p className={styles.muted}>{t('sharing.status_interrupted_hint')}</p>
            )}
            {kind === 'fail' && <ErrorBlock body={current.error} />}
          </div>

          {current.balanced && <AttemptTimeline entry={current} />}

          {(current.tokens_known || kind === 'ok') && (
            <DetailSection title={t('sharing.field_tokens')}>
              {current.tokens_known ? (
                <div className={styles.tokenTiles}>
                  <div className={styles.tokenTile}>
                    <span className={styles.tokenTileLabel}>{t('sharing.token_input')}</span>
                    <strong>{number(current.input_tokens)}</strong>
                    {current.cache_read_tokens > 0 && (
                      <span className={styles.muted}>
                        {t('sharing.token_cache', { count: number(current.cache_read_tokens) })}
                      </span>
                    )}
                    {current.cache_creation_tokens > 0 && (
                      <span className={styles.muted}>
                        {t('sharing.token_cache_write', {
                          count: number(current.cache_creation_tokens),
                        })}
                      </span>
                    )}
                  </div>
                  <div className={styles.tokenTile}>
                    <span className={styles.tokenTileLabel}>{t('sharing.token_output')}</span>
                    <strong>{number(current.output_tokens)}</strong>
                    {current.reasoning_tokens > 0 && (
                      <span className={styles.muted}>
                        {t('sharing.token_reasoning', { count: number(current.reasoning_tokens) })}
                      </span>
                    )}
                  </div>
                  {cacheRate !== null && (
                    <div className={styles.tokenTile}>
                      <span className={styles.tokenTileLabel}>{t('sharing.field_cache_hit')}</span>
                      <strong>{cacheRate.toFixed(1)}%</strong>
                    </div>
                  )}
                </div>
              ) : (
                <p className={styles.muted}>{t('sharing.unknown')}</p>
              )}
            </DetailSection>
          )}

          <DetailSection title={t('sharing.section_call')}>
            <dl className={styles.detailGrid}>
              {showConsumer && <Field label={t('sharing.consumer')}>@{current.consumer}</Field>}
              <Field label={t('sharing.field_key')}>{current.key_name || '—'}</Field>
              {serving && (
                <Field label={t('sharing.field_serving_model')}>
                  <span className={styles.mono}>{serving}</span>
                </Field>
              )}
              {current.upstream_model && current.upstream_model !== current.model && (
                <Field label={t('sharing.field_upstream_model')}>
                  <span className={styles.mono}>{current.upstream_model}</span>
                </Field>
              )}
              <Field label={t('sharing.channel')}>
                {current.channel || '—'}
                {current.owner && current.owner !== current.consumer && (
                  <span className={styles.muted}>（@{current.owner}）</span>
                )}
              </Field>
              <Field label={t('sharing.field_credential')}>
                {current.credential_name ? credentialDisplayName(current.credential_name) : '—'}
              </Field>
              {current.reasoning_effort && (
                <Field label={t('sharing.field_reasoning_effort')}>
                  <span className={styles.mono}>{current.reasoning_effort}</span>
                </Field>
              )}
            </dl>
          </DetailSection>

          {(current.trace_id || current.request_id) && (
            <DetailSection title={t('sharing.section_ids')}>
              <dl className={styles.detailGrid}>
                {current.trace_id && (
                  <Field label={t('sharing.field_trace')}>{idValue(current.trace_id)}</Field>
                )}
                {current.request_id && current.request_id !== current.trace_id && (
                  <Field label={t('sharing.field_request')}>{idValue(current.request_id)}</Field>
                )}
              </dl>
            </DetailSection>
          )}
        </div>
      )}
    </Sheet>
  );
}
