import { useTranslation } from 'react-i18next';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { IconRefreshCw, IconUpload } from '@/components/ui/icons';
import { useRevealGroup } from '@/hooks/motion';
import styles from './VaultHeader.module.scss';

export type VaultHeaderProps = {
  totalCount: number;
  activeCount: number;
  problemCount: number;
  loading: boolean;
  refreshing: boolean;
  uploading: boolean;
  disableControls: boolean;
  onUpload: () => void;
  onRefresh: () => void;
};

/**
 * 凭证库头部：eyebrow（▍游标前缀）+ 标题 + mono 遥测 meta 行 + 动作区。
 * meta 行同时承载 VaultPulse 的文字等价信息（谱条本身 aria-hidden）。
 */
export function VaultHeader(props: VaultHeaderProps) {
  const {
    totalCount,
    activeCount,
    problemCount,
    loading,
    refreshing,
    uploading,
    disableControls,
    onUpload,
    onRefresh,
  } = props;
  const { t } = useTranslation();
  const revealRef = useRevealGroup<HTMLElement>();

  return (
    <header className={styles.header} ref={revealRef}>
      <div className={styles.copy}>
        <h1 className={styles.title} data-reveal>
          {t('auth_files.title')}
        </h1>
        <p className={styles.meta} data-reveal>
          <span className={styles.metaTotal}>
            {t('auth_files.meta_total', { count: totalCount })}
          </span>
          <span className={styles.metaDot} aria-hidden="true">
            ·
          </span>
          <span className={activeCount > 0 ? styles.metaActive : styles.metaMuted}>
            {t('auth_files.meta_active', { count: activeCount })}
          </span>
          {problemCount > 0 && (
            <>
              <span className={styles.metaDot} aria-hidden="true">
                ·
              </span>
              <span className={styles.metaProblem}>
                {t('auth_files.meta_problem', { count: problemCount })}
              </span>
            </>
          )}
        </p>
      </div>
      <div className={styles.actions} data-reveal>
        <button
          type="button"
          className={styles.ghostAction}
          onClick={onRefresh}
          disabled={loading || refreshing}
        >
          <IconRefreshCw size={14} className={refreshing ? styles.spinning : undefined} />
          {t('common.refresh')}
        </button>
        <button
          type="button"
          className={styles.primaryAction}
          onClick={onUpload}
          disabled={disableControls || uploading}
        >
          {uploading ? <LoadingSpinner size={14} /> : <IconUpload size={15} />}
          {t('auth_files.upload_button')}
        </button>
      </div>
    </header>
  );
}
