import { useTranslation } from 'react-i18next';
import { statusKind } from './usageFormat';
import styles from './Usage.module.scss';

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
    <span className={`${styles.status} ${styles[`status_${kind}`]}`}>
      <span className={styles.statusDot} aria-hidden="true" />
      {label} {status}
    </span>
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
