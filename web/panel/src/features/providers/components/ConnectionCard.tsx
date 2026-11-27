import { useId, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { IconPencil } from '@/components/ui/icons';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { ProviderStatusBar } from '@/components/providers/ProviderStatusBar';
import type { ResourceUsage } from '../resourceView';
import styles from './ResourceCard.module.scss';
import statusBarStyles from './providerStatusBar.module.scss';

export interface ConnectionCardData {
  title: string;
  prefix?: string | null;
  /** Claude groups without a base URL talk to the official endpoint. */
  defaultClaudeEndpoint: boolean;
  endpoint: string | null;
  keyCount: number;
  activeKeyCount: number;
  modelCount: number;
  headerCount: number;
  priority: number;
  usage: ResourceUsage | null;
}

interface ConnectionCardProps {
  connection: ConnectionCardData;
  selected: boolean;
  disableMutations: boolean;
  onEdit: () => void;
  onToggleDisabled: (disabled: boolean) => void;
}

const CLAUDE_DEFAULT_BASE_URL = 'https://api.anthropic.com';

/**
 * Connection card. Editing is the primary action, so the whole surface opens the editor;
 * the group switch sits top-right as the one control that acts in place.
 */
export function ConnectionCard({
  connection,
  selected,
  disableMutations,
  onEdit,
  onToggleDisabled,
}: ConnectionCardProps) {
  const { t } = useTranslation();
  const titleId = useId();
  const { keyCount, activeKeyCount, usage } = connection;
  const enabled = activeKeyCount > 0;
  const allOff = keyCount > 0 && !enabled;

  const keySummary =
    activeKeyCount === keyCount
      ? t('providersPage.connections.keyCount', { count: keyCount })
      : allOff
        ? `${t('providersPage.connections.keyCount', { count: keyCount })} · ${t('providersPage.status.disabled')}`
        : t('providersPage.connections.keysActive', { active: activeKeyCount, count: keyCount });

  const endpoint: ReactNode = connection.endpoint ? (
    connection.endpoint
  ) : connection.defaultClaudeEndpoint ? (
    <>
      {CLAUDE_DEFAULT_BASE_URL}{' '}
      <span className={styles.endpointNote}>{t('providersPage.status.defaultSuffix')}</span>
    </>
  ) : (
    <span className={styles.endpointNote}>{t('providersPage.status.notSet')}</span>
  );

  // Zero counts are the default, not information worth a chip.
  const chips: ReactNode[] = [];
  if (connection.prefix) {
    chips.push(
      <span key="prefix" className={`${styles.chip} ${styles.chipPrefix}`}>
        <span className={styles.chipLabel}>{t('providersPage.detail.fields.prefix')}</span>
        {connection.prefix}
      </span>
    );
  }
  const metric = (key: string, label: string, value: number) => {
    if (value === 0) return;
    chips.push(
      <span key={key} className={styles.chip}>
        <span className={styles.chipLabel}>{label}</span>
        {value}
      </span>
    );
  };
  metric('models', t('providersPage.table.metrics.models'), connection.modelCount);
  metric('headers', t('providersPage.table.metrics.headers'), connection.headerCount);
  metric('priority', t('providersPage.toolbar.sort.priority'), connection.priority);

  const cardClass = [
    styles.card,
    styles.connectionCard,
    allOff ? styles.cardDisabled : '',
    selected ? styles.cardSelected : '',
  ]
    .filter(Boolean)
    .join(' ');
  const hasTraffic = usage ? usage.success + usage.failure > 0 : false;
  const editLabel = t('providersPage.connections.edit');

  return (
    <article className={cardClass} aria-labelledby={titleId}>
      <div className={styles.body}>
        <div className={styles.head}>
          <h3 className={styles.titleWrap}>
            <button
              id={titleId}
              type="button"
              className={`${styles.title} ${styles.cardLink}`}
              title={editLabel}
              disabled={disableMutations}
              onClick={onEdit}
            >
              {connection.title}
              <span className="visually-hidden"> · {editLabel}</span>
            </button>
          </h3>
          <IconPencil size={13} className={styles.editHint} aria-hidden="true" />
          <div className={styles.cardControl}>
            <ToggleSwitch
              checked={enabled}
              disabled={disableMutations || keyCount === 0}
              onChange={(value) => onToggleDisabled(!value)}
              ariaLabel={t(
                enabled
                  ? 'providersPage.connections.disableKeys'
                  : 'providersPage.connections.enableKeys',
                { name: connection.title }
              )}
            />
          </div>
        </div>
        <div className={styles.sub}>{keySummary}</div>

        <div
          className={styles.endpoint}
          title={
            connection.endpoint ??
            (connection.defaultClaudeEndpoint ? CLAUDE_DEFAULT_BASE_URL : undefined)
          }
        >
          {endpoint}
        </div>

        {chips.length > 0 ? <div className={styles.chips}>{chips}</div> : null}

        {usage ? (
          <div className={`${styles.health} ${styles.cardControl}`}>
            <div className={styles.healthHead}>
              <span className={styles.healthLabel}>{t('providersPage.card.requests')}</span>
              <span className={styles.healthCounts}>
                <span className={hasTraffic ? styles.countOk : undefined}>
                  {t('stats.success')} {usage.success}
                </span>
                <span className={usage.failure > 0 ? styles.countFail : undefined}>
                  {t('stats.failure')} {usage.failure}
                </span>
              </span>
            </div>
            <ProviderStatusBar statusData={usage.statusData} styles={statusBarStyles} />
          </div>
        ) : null}
      </div>
    </article>
  );
}
