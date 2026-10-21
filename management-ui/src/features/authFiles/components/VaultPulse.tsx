import { useMemo, type CSSProperties } from 'react';
import type { AuthFileItem } from '@/types';
import { hasAuthFileStatusWarning } from '@/features/authFiles/constants';
import type { AuthFileStatusBarData } from '@/features/authFiles/hooks/useAuthFilesStatusBarCache';
import styles from './VaultPulse.module.scss';

/** 谱条最多渲染的凭证数；超出以 mono「+N」尾注表示。 */
const MAX_BARS = 160;
/** 级联入场总预算（与 useRevealGroup / ThroughputChart 同一 360ms 语汇）。 */
const ENTRANCE_BUDGET_MS = 360;

type PulseState = 'live' | 'idle' | 'warning' | 'problem';

type PulseBar = {
  key: string;
  state: PulseState;
  disabled: boolean;
};

export type VaultPulseProps = {
  files: AuthFileItem[];
  statusBarCache: Map<string, AuthFileStatusBarData>;
};

const STATE_CLASS: Record<PulseState, string> = {
  live: styles.barLive,
  idle: styles.barIdle,
  warning: styles.barWarning,
  problem: styles.barProblem,
};

/**
 * VaultPulse —— 凭证谱条，本页的签名元素。
 *
 * 与仪表盘 LiveWire 同族异形：wire 是时间维度的流量脉搏，
 * spectrum 是舰队维度的凭证体检。每根竖条对应一个凭证，
 * 颜色 + 高度双通道编码健康态（色盲可辨）：
 * 翡翠=近期有活流量，灰=启用但无数据，琥珀=告警，红=不可用；停用整体淡化。
 * 纯装饰-信息层（aria-hidden），文字等价信息由头部 meta 行承载。
 */
export function VaultPulse({ files, statusBarCache }: VaultPulseProps) {
  const bars = useMemo<PulseBar[]>(
    () =>
      files.slice(0, MAX_BARS).map((file) => {
        const disabled = file.disabled === true;
        let state: PulseState;
        if (file.unavailable === true) {
          state = 'problem';
        } else if (hasAuthFileStatusWarning(file)) {
          state = 'warning';
        } else {
          const authIndexKey = typeof file.authIndex === 'string' ? file.authIndex : null;
          const statusData = authIndexKey ? statusBarCache.get(authIndexKey) : undefined;
          const hasTraffic =
            Boolean(statusData) &&
            (statusData?.totalSuccess ?? 0) + (statusData?.totalFailure ?? 0) > 0;
          state = hasTraffic ? 'live' : 'idle';
        }
        return { key: file.name, state, disabled };
      }),
    [files, statusBarCache]
  );

  const overflow = files.length - bars.length;
  const delayStep = bars.length > 1 ? ENTRANCE_BUDGET_MS / (bars.length - 1) : 0;

  if (bars.length === 0) {
    return (
      <div className={styles.pulse} aria-hidden="true">
        <span className={styles.idleLine} />
      </div>
    );
  }

  return (
    <div className={styles.pulse} aria-hidden="true">
      <div className={styles.bars}>
        {bars.map((bar, index) => (
          <span
            key={bar.key}
            className={`${styles.bar} ${STATE_CLASS[bar.state]} ${bar.disabled ? styles.barDisabled : ''}`}
            style={{ '--bar-delay': `${Math.round(index * delayStep)}ms` } as CSSProperties}
          />
        ))}
      </div>
      {overflow > 0 && <span className={styles.overflow}>+{overflow}</span>}
    </div>
  );
}
