import { useTranslation } from 'react-i18next';
import type { SiwcBindingStatus } from '../siwcBinding';
import styles from './SiwcStatusBadge.module.scss';

/**
 * SIWC 绑定状态药丸：状态点 + 简短状态词，mono 小字，与卡片元数据行同一语汇。
 * `withLabel` 在上下文不言自明时（SIWC 对话框内）去掉 “SIWC” 前缀。
 */
export function SiwcStatusBadge({
  status,
  withLabel = true,
}: {
  status: SiwcBindingStatus;
  withLabel?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <span className={styles.badge} data-status={status}>
      <span className={styles.dot} aria-hidden="true" />
      {withLabel && <span className={styles.label}>SIWC</span>}
      <span>{t(`siwc.${status}`)}</span>
    </span>
  );
}
