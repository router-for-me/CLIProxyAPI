import { useTranslation } from 'react-i18next';
import { IconEye, IconEyeOff, IconRefreshCw } from '@/components/ui/icons';
import { useCountUp } from '@/hooks/motion';
import styles from './QuotaHeader.module.scss';

export type QuotaHeaderProps = {
  totalCount: number;
  loadedCount: number;
  attentionCount: number;
  refreshing: boolean;
  disableControls: boolean;
  showEmails: boolean;
  onRefreshAll: () => void;
  onToggleEmails: () => void;
};

/**
 * Quota page header: the title and the credential meta on one baseline, with
 * "Show emails" and "Refresh all credentials" right-aligned.
 *
 * Entrance: the `data-reveal` elements are sequenced by the page's useRevealGroup
 * (title 0ms → meta 70ms → actions 140ms → tabs 210ms).
 */
export function QuotaHeader(props: QuotaHeaderProps) {
  const {
    totalCount,
    loadedCount,
    attentionCount,
    refreshing,
    disableControls,
    showEmails,
    onRefreshAll,
    onToggleEmails,
  } = props;
  const { t } = useTranslation();
  // While batch results land, the loaded count is the only number on the page that ticks
  const displayLoadedCount = useCountUp(loadedCount);
  const EmailsIcon = showEmails ? IconEyeOff : IconEye;

  return (
    <header className={styles.header}>
      <div className={styles.copy}>
        <h1 className={styles.title} data-reveal>
          {t('quota_management.title')}
        </h1>
        <p className={styles.meta} data-reveal>
          <span>{t('quota_management.meta_credentials', { count: totalCount })}</span>
          <span aria-hidden="true">·</span>
          <span>{t('quota_management.meta_loaded', { count: displayLoadedCount })}</span>
          {attentionCount > 0 && (
            <>
              <span aria-hidden="true">·</span>
              <span className={styles.metaAttention}>
                {t('quota_management.meta_attention', { count: attentionCount })}
              </span>
            </>
          )}
        </p>
      </div>
      <div className={styles.actions} data-reveal>
        {/* The label names the action, so no aria-pressed: "Hide emails, pressed" would invert the state */}
        <button
          type="button"
          className={`${styles.action} ${styles.emailsAction}`}
          onClick={onToggleEmails}
        >
          <EmailsIcon size={16} aria-hidden="true" />
          {t(showEmails ? 'quota_management.hide_emails' : 'quota_management.show_emails')}
        </button>
        <button
          type="button"
          className={`${styles.action} ${styles.refreshAction}`}
          onClick={onRefreshAll}
          disabled={disableControls || refreshing}
        >
          <IconRefreshCw
            size={16}
            aria-hidden="true"
            className={refreshing ? styles.spinning : undefined}
          />
          {t('quota_management.refresh_all_credentials')}
        </button>
      </div>
    </header>
  );
}
