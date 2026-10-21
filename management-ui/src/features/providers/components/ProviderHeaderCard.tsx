import { useTranslation } from 'react-i18next';
import { IconLoader2, IconPlus, IconRefreshCw } from '@/components/ui/icons';
import styles from './ProviderHeaderCard.module.scss';

interface ProviderHeaderCardProps {
  title?: string;
  totalActive: number;
  totalResources: number;
  providerFamilies: number;
  updatedAtLabel: string;
  isFetching?: boolean;
  isNewDisabled?: boolean;
  showNewAction?: boolean;
  showSummary?: boolean;
  newLabel?: string;
  variant?: 'quickStart';
  onRefresh: () => void;
  onNew: () => void;
}

export function ProviderHeaderCard({
  title,
  totalActive,
  totalResources,
  providerFamilies,
  updatedAtLabel,
  isFetching = false,
  isNewDisabled = false,
  showNewAction = true,
  showSummary = true,
  newLabel,
  variant,
  onRefresh,
  onNew,
}: ProviderHeaderCardProps) {
  const { t } = useTranslation();
  const cardClassName = [styles.card, variant === 'quickStart' ? styles.quickStartCard : '']
    .filter(Boolean)
    .join(' ');

  return (
    <section className={cardClassName}>
      <div className={styles.row}>
        <div className={styles.titleArea}>
          <h1 className={styles.title}>{title ?? t('providersPage.header.title')}</h1>
        </div>
        <div className={styles.actions}>
          <button
            type="button"
            className={`${styles.btn} ${styles.btnOutline}`}
            onClick={onRefresh}
            disabled={isFetching}
            aria-label={
              isFetching ? t('providersPage.actions.syncing') : t('providersPage.actions.refresh')
            }
          >
            <span className={`${styles.btnIcon} ${isFetching ? styles.spin : ''}`.trim()}>
              {isFetching ? <IconLoader2 size={16} /> : <IconRefreshCw size={16} />}
            </span>
            <span>
              {isFetching ? t('providersPage.actions.syncing') : t('providersPage.actions.refresh')}
            </span>
          </button>
          {showNewAction ? (
            <button
              type="button"
              className={`${styles.btn} ${styles.btnPrimary}`}
              onClick={onNew}
              disabled={isNewDisabled}
            >
              <IconPlus size={16} />
              <span>{newLabel ?? t('providersPage.actions.new')}</span>
            </button>
          ) : null}
        </div>
      </div>

      {showSummary ? (
        <div className={styles.chips}>
          <span className={`${styles.chip} ${styles.chipPrimary}`}>
            {t('providersPage.header.activeResources', {
              active: totalActive,
              total: totalResources,
            })}
          </span>
          <span className={styles.chip}>
            {t('providersPage.header.providerFamilies', { count: providerFamilies })}
          </span>
          <span className={styles.chip}>
            {t('providersPage.header.updatedAt', { time: updatedAtLabel })}
          </span>
        </div>
      ) : null}
    </section>
  );
}
