import { calculateStatusBarData } from '@/utils/usage';
import styles from '@/pages/AiProvidersPage.module.scss';

interface ProviderStatusBarProps {
  statusData: ReturnType<typeof calculateStatusBarData>;
}

export function ProviderStatusBar({ statusData }: ProviderStatusBarProps) {
  const hasData = statusData.totalSuccess + statusData.totalFailure > 0;
  const rateClass = !hasData
    ? ''
    : statusData.successRate >= 90
      ? styles.statusRateHigh
      : statusData.successRate >= 50
        ? styles.statusRateMedium
        : styles.statusRateLow;

  return (
    <div className={styles.statusBar}>
      <div className={styles.statusBlocks}>
        {statusData.blocks.map((state, idx) => {
          const blockClass =
            state === 'success'
              ? styles.statusBlockSuccess
              : state === 'failure'
                ? styles.statusBlockFailure
                : state === 'mixed'
                  ? styles.statusBlockMixed
                  : styles.statusBlockIdle;
          return <div key={idx} className={`${styles.statusBlock} ${blockClass}`} />;
        })}
      </div>
      <span className={`${styles.statusRate} ${rateClass}`}>
        {hasData ? `${statusData.successRate.toFixed(1)}%` : '--'}
      </span>
    </div>
  );
}
