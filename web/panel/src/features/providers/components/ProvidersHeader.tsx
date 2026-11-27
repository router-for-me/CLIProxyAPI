import { useTranslation } from 'react-i18next';
import { IconPlus, IconRefreshCw } from '@/components/ui/icons';
import { useRevealGroup } from '@/hooks/motion';
import styles from './ProvidersHeader.module.scss';

export interface ProvidersHeaderSummary {
  totalActive: number;
  totalResources: number;
  providerFamilies: number;
  updatedAtLabel: string;
}

interface ProvidersHeaderProps {
  title: string;
  /** Omitted on the quick-start route, which manages a single bundle. */
  summary?: ProvidersHeaderSummary;
  isFetching: boolean;
  onRefresh: () => void;
  /** Label of the primary action; the action is hidden when omitted. */
  newLabel?: string;
  newDisabled?: boolean;
  onNew?: () => void;
}

/**
 * Page header in the vault vocabulary: tight title, one mono telemetry line,
 * a quiet text action and a single ink pill for the primary action.
 */
export function ProvidersHeader({
  title,
  summary,
  isFetching,
  onRefresh,
  newLabel,
  newDisabled = false,
  onNew,
}: ProvidersHeaderProps) {
  const { t } = useTranslation();
  const revealRef = useRevealGroup<HTMLElement>();
  const refreshLabel = isFetching
    ? t('providersPage.actions.syncing')
    : t('providersPage.actions.refresh');

  return (
    <header className={styles.header} ref={revealRef}>
      <div className={styles.copy}>
        <h1 className={styles.title} data-reveal>
          {title}
        </h1>
        {summary ? (
          <p className={styles.meta} data-reveal>
            <span className={summary.totalActive > 0 ? styles.metaActive : styles.metaMuted}>
              {t('providersPage.header.activeResources', {
                active: summary.totalActive,
                total: summary.totalResources,
              })}
            </span>
            <span className={styles.metaDot} aria-hidden="true">
              ·
            </span>
            <span>
              {t('providersPage.header.providerFamilies', { count: summary.providerFamilies })}
            </span>
            <span className={styles.metaDot} aria-hidden="true">
              ·
            </span>
            <span className={styles.metaMuted}>
              {t('providersPage.header.updatedAt', { time: summary.updatedAtLabel })}
            </span>
          </p>
        ) : null}
      </div>
      <div className={styles.actions} data-reveal>
        <button
          type="button"
          className={styles.ghostAction}
          onClick={onRefresh}
          disabled={isFetching}
          aria-busy={isFetching || undefined}
        >
          <IconRefreshCw size={14} className={isFetching ? styles.spinning : undefined} />
          {refreshLabel}
        </button>
        {newLabel && onNew ? (
          <button
            type="button"
            className={styles.primaryAction}
            onClick={onNew}
            disabled={newDisabled}
          >
            <IconPlus size={15} />
            {newLabel}
          </button>
        ) : null}
      </div>
    </header>
  );
}
