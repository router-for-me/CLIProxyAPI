import { useTranslation } from 'react-i18next';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import type { AuthFileRefreshResult } from '@/services/api/authFiles';
import styles from './AuthFileRefreshResults.module.scss';

export function AuthFileRefreshResultsContent({ results }: { results: AuthFileRefreshResult[] }) {
  const { t } = useTranslation();
  const success = results.filter((result) => result.success).length;
  return (
    <div>
      <p role="status">
        {results.length === 0
          ? t('auth_files.refresh_all_empty')
          : t('auth_files.refresh_all_summary', { success, failed: results.length - success })}
      </p>
      <ul className={styles.results}>
        {results.map((result, index) => (
          <li key={`${result.id}:${index}`} className={styles.result}>
            <strong className={styles.identity}>{result.id}</strong>
            <span className={result.success ? styles.success : styles.failure}>
              {t(
                result.success
                  ? 'auth_files.refresh_result_success'
                  : 'auth_files.refresh_result_failed'
              )}
            </span>
            {!result.success && result.error && <p className={styles.error}>{result.error}</p>}
          </li>
        ))}
      </ul>
    </div>
  );
}

export function AuthFileRefreshResults({
  results,
  onClose,
}: {
  results: AuthFileRefreshResult[] | null;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Modal
      open={results !== null}
      title={t('auth_files.refresh_all_results')}
      onClose={onClose}
      footer={<Button onClick={onClose}>{t('common.close')}</Button>}
    >
      {results !== null && <AuthFileRefreshResultsContent results={results} />}
    </Modal>
  );
}
