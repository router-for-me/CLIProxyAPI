import type { ReactElement } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import styles from '@/features/quota/components/PoolHistoryNotice.module.scss';
import type { HistoryStatus } from '@/features/quota/pool/types';

export interface PoolHistoryNoticeProps {
  message?: string;
  onRetry?: () => void;
  status: Exclude<HistoryStatus, 'ok' | 'loading'>;
}

/** One muted line explaining why the pool cards show no recorded history. */
export const PoolHistoryNotice = ({
  message,
  onRetry,
  status,
}: PoolHistoryNoticeProps): ReactElement => {
  const { t } = useTranslation();
  const canRetry = status === 'error' && onRetry !== undefined;

  return (
    <div className={styles.notice} role="status">
      <span>
        {t(`quota_pool.history_${status}`, { message: message ?? t('common.unknown_error') })}
      </span>
      {canRetry && (
        <Button onClick={onRetry} size="sm" type="button" variant="ghost">
          {t('quota_pool.history_retry')}
        </Button>
      )}
    </div>
  );
};
