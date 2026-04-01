import styles from '@/pages/AuthFilesPage.module.scss';

export type QuotaProgressBarProps = {
  percent: number | null;
  highThreshold: number;
  mediumThreshold: number;
};

export function QuotaProgressBar({ percent, highThreshold, mediumThreshold }: QuotaProgressBarProps) {
  const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value));
  const normalized = percent === null ? null : clamp(percent, 0, 100);
  const fillClass =
    normalized === null
      ? styles.quotaBarFillMedium
      : normalized >= highThreshold
        ? styles.quotaBarFillHigh
        : normalized >= mediumThreshold
          ? styles.quotaBarFillMedium
          : styles.quotaBarFillLow;
  const widthPercent = Math.round(normalized ?? 0);

  return (
    <div className={styles.quotaBar}>
      <div className={`${styles.quotaBarFill} ${fillClass}`} style={{ width: `${widthPercent}%` }} />
    </div>
  );
}

