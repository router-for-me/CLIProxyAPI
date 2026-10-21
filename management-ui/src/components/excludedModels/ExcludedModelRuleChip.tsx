import type { ReactNode } from 'react';
import { IconX } from '@/components/ui/icons';
import styles from './ExcludedModelRuleChip.module.scss';

/**
 * 排除项 chip —— 按**来源**区分三种形态，取代两处近乎重复的手写标记
 * （`AuthFileDetailsSheet.module.scss` 的 `.excludedModelChip` 与
 * `AuthFilesOAuthExcludedEditPage.module.scss` 的 `.customRuleChip`）。
 *
 * - `exact`    实线 primary 染色：用户显式勾选的模型，可直接移除。
 * - `wildcard` 虚线：由通配符规则派生出的模型。没有 ✕——要移除得去改那条规则，
 *              直接给个 ✕ 会承诺一件它做不到的事。
 * - `unknown`  虚线弱化：精确规则但目录里没有（如已下线的模型 id），可移除。
 */
export type ExcludedModelChipVariant = 'exact' | 'wildcard' | 'unknown';

export interface ExcludedModelRuleChipProps {
  label: string;
  variant?: ExcludedModelChipVariant;
  /** 次要说明，例如派生该 chip 的规则。 */
  detail?: string;
  /** 省略即不渲染 ✕。 */
  onRemove?: () => void;
  removeAriaLabel?: string;
  disabled?: boolean;
  title?: string;
}

/** chip 的换行容器。单独导出，免得每个消费方各写一遍 flex-wrap。 */
export function ExcludedModelChipRow({ children }: { children: ReactNode }) {
  return <div className={styles.chipRow}>{children}</div>;
}

export function ExcludedModelRuleChip({
  label,
  variant = 'exact',
  detail,
  onRemove,
  removeAriaLabel,
  disabled = false,
  title,
}: ExcludedModelRuleChipProps) {
  return (
    <span
      className={`${styles.chip} ${styles[variant]}`}
      title={title ?? (detail ? `${label} — ${detail}` : label)}
    >
      <span className={styles.label}>{label}</span>
      {detail ? <span className={styles.detail}>{detail}</span> : null}
      {onRemove ? (
        <button
          type="button"
          className={styles.remove}
          onClick={onRemove}
          disabled={disabled}
          aria-label={removeAriaLabel ?? label}
        >
          <IconX size={12} />
        </button>
      ) : null}
    </span>
  );
}
