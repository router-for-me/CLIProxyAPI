import { useState, useCallback, useRef, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import type { StatusBarData, StatusBlockDetail } from '@/utils/usage';
import defaultStyles from '@/pages/AiProvidersPage.module.scss';

/**
 * 根据成功率 (0–1) 在三个色标之间做 RGB 线性插值
 * 0 → 红 (#ef4444)  →  0.5 → 金黄 (#facc15)  →  1 → 绿 (#22c55e)
 */
const COLOR_STOPS = [
  { r: 239, g: 68, b: 68 },   // #ef4444
  { r: 250, g: 204, b: 21 },  // #facc15
  { r: 34, g: 197, b: 94 },   // #22c55e
] as const;

function rateToColor(rate: number): string {
  const t = Math.max(0, Math.min(1, rate));
  const segment = t < 0.5 ? 0 : 1;
  const localT = segment === 0 ? t * 2 : (t - 0.5) * 2;
  const from = COLOR_STOPS[segment];
  const to = COLOR_STOPS[segment + 1];
  const r = Math.round(from.r + (to.r - from.r) * localT);
  const g = Math.round(from.g + (to.g - from.g) * localT);
  const b = Math.round(from.b + (to.b - from.b) * localT);
  return `rgb(${r}, ${g}, ${b})`;
}

function formatTime(timestamp: number): string {
  const date = new Date(timestamp);
  const h = date.getHours().toString().padStart(2, '0');
  const m = date.getMinutes().toString().padStart(2, '0');
  return `${h}:${m}`;
}

function formatSuccessRate(rate: number): string {
  const rounded = rate.toFixed(1);
  return `${rounded.endsWith('.0') ? rounded.slice(0, -2) : rounded}%`;
}

type StylesModule = Record<string, string>;

interface ProviderStatusBarProps {
  statusData: StatusBarData;
  styles?: StylesModule;
}

export function ProviderStatusBar({ statusData, styles: stylesProp }: ProviderStatusBarProps) {
  const { t } = useTranslation();
  const s = (stylesProp || defaultStyles) as StylesModule;
  const [activeTooltip, setActiveTooltip] = useState<number | null>(null);
  const blocksRef = useRef<HTMLDivElement>(null);

  const hasData = statusData.totalSuccess + statusData.totalFailure > 0;
  const rateClass = !hasData
    ? ''
    : statusData.successRate >= 90
      ? s.statusRateHigh
      : statusData.successRate >= 50
        ? s.statusRateMedium
        : s.statusRateLow;

  // 点击外部关闭 tooltip（移动端）
  useEffect(() => {
    if (activeTooltip === null) return;
    const handler = (e: PointerEvent) => {
      if (blocksRef.current && !blocksRef.current.contains(e.target as Node)) {
        setActiveTooltip(null);
      }
    };
    document.addEventListener('pointerdown', handler);
    return () => document.removeEventListener('pointerdown', handler);
  }, [activeTooltip]);

  const handlePointerEnter = useCallback((e: React.PointerEvent, idx: number) => {
    if (e.pointerType === 'mouse') {
      setActiveTooltip(idx);
    }
  }, []);

  const handlePointerLeave = useCallback((e: React.PointerEvent) => {
    if (e.pointerType === 'mouse') {
      setActiveTooltip(null);
    }
  }, []);

  const handlePointerDown = useCallback((e: React.PointerEvent, idx: number) => {
    if (e.pointerType === 'touch') {
      e.preventDefault();
      setActiveTooltip((prev) => (prev === idx ? null : idx));
    }
  }, []);

  const getTooltipPositionClass = (idx: number, total: number): string => {
    if (idx <= 2) return s.statusTooltipLeft;
    if (idx >= total - 3) return s.statusTooltipRight;
    return '';
  };

  const renderTooltip = (detail: StatusBlockDetail, idx: number) => {
    const total = detail.success + detail.failure;
    const posClass = getTooltipPositionClass(idx, statusData.blockDetails.length);
    const timeRange = `${formatTime(detail.startTime)} – ${formatTime(detail.endTime)}`;

    return (
      <div className={`${s.statusTooltip} ${posClass}`}>
        <span className={s.tooltipTime}>{timeRange}</span>
        {total > 0 ? (
          <span className={s.tooltipStats}>
            <span className={s.tooltipSuccess}>{t('status_bar.success_short')} {detail.success}</span>
            <span className={s.tooltipFailure}>{t('status_bar.failure_short')} {detail.failure}</span>
            <span className={s.tooltipRate}>({(detail.rate * 100).toFixed(1)}%)</span>
          </span>
        ) : (
          <span className={s.tooltipStats}>{t('status_bar.no_requests')}</span>
        )}
      </div>
    );
  };

  return (
    <div className={s.statusBar}>
      <div className={s.statusBlocks} ref={blocksRef}>
        {statusData.blockDetails.map((detail, idx) => {
          const isIdle = detail.rate === -1;
          const blockStyle = isIdle ? undefined : { backgroundColor: rateToColor(detail.rate) };
          const isActive = activeTooltip === idx;

          return (
            <div
              key={idx}
              className={`${s.statusBlockWrapper} ${isActive ? s.statusBlockActive : ''}`}
              onPointerEnter={(e) => handlePointerEnter(e, idx)}
              onPointerLeave={handlePointerLeave}
              onPointerDown={(e) => handlePointerDown(e, idx)}
            >
              <div
                className={`${s.statusBlock} ${isIdle ? s.statusBlockIdle : ''}`}
                style={blockStyle}
              />
              {isActive && renderTooltip(detail, idx)}
            </div>
          );
        })}
      </div>
      <span className={`${s.statusRate} ${rateClass}`}>
        {hasData ? formatSuccessRate(statusData.successRate) : '--'}
      </span>
    </div>
  );
}
