import { useState, useCallback, useRef, useEffect, useMemo } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import {
  collectUsageDetails,
  calculateServiceHealthData,
  type ServiceHealthData,
  type StatusBlockDetail,
} from '@/utils/usage';
import type { UsagePayload } from './hooks/useUsageData';
import styles from '@/pages/UsagePage.module.scss';

const COLOR_STOPS = [
  { r: 239, g: 68, b: 68 }, // #ef4444
  { r: 250, g: 204, b: 21 }, // #facc15
  { r: 34, g: 197, b: 94 }, // #22c55e
] as const;

const TOOLTIP_OFFSET = 8;
const TOOLTIP_SAFE_WIDTH = 180;
const TOOLTIP_SAFE_HEIGHT = 72;

type TooltipHorizontalPosition = 'center' | 'left' | 'right';
type TooltipVerticalPosition = 'above' | 'below';

interface ActiveTooltipState {
  idx: number;
  anchorEl: HTMLDivElement;
  horizontal: TooltipHorizontalPosition;
  vertical: TooltipVerticalPosition;
  left: number;
  top: number;
  transform: string;
}

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

function formatDateTime(timestamp: number): string {
  const date = new Date(timestamp);
  const month = (date.getMonth() + 1).toString().padStart(2, '0');
  const day = date.getDate().toString().padStart(2, '0');
  const h = date.getHours().toString().padStart(2, '0');
  const m = date.getMinutes().toString().padStart(2, '0');
  return `${month}/${day} ${h}:${m}`;
}

export interface ServiceHealthCardProps {
  usage: UsagePayload | null;
  loading: boolean;
}

export function ServiceHealthCard({ usage, loading }: ServiceHealthCardProps) {
  const { t } = useTranslation();
  const [activeTooltip, setActiveTooltip] = useState<ActiveTooltipState | null>(null);
  const gridRef = useRef<HTMLDivElement>(null);

  const healthData: ServiceHealthData = useMemo(() => {
    const details = usage ? collectUsageDetails(usage) : [];
    return calculateServiceHealthData(details);
  }, [usage]);

  const hasData = healthData.totalSuccess + healthData.totalFailure > 0;

  useEffect(() => {
    if (activeTooltip === null) return;
    const handler = (e: PointerEvent) => {
      if (gridRef.current && !gridRef.current.contains(e.target as Node)) {
        setActiveTooltip(null);
      }
    };
    document.addEventListener('pointerdown', handler);
    return () => document.removeEventListener('pointerdown', handler);
  }, [activeTooltip]);

  const buildTooltipState = useCallback(
    (idx: number, anchorEl: HTMLDivElement): ActiveTooltipState => {
      const rect = anchorEl.getBoundingClientRect();
      const centerX = rect.left + rect.width / 2;

      let horizontal: TooltipHorizontalPosition = 'center';
      let left = centerX;

      if (centerX <= TOOLTIP_SAFE_WIDTH / 2) {
        horizontal = 'left';
        left = rect.left;
      } else if (centerX >= window.innerWidth - TOOLTIP_SAFE_WIDTH / 2) {
        horizontal = 'right';
        left = rect.right;
      }

      const vertical: TooltipVerticalPosition = rect.top <= TOOLTIP_SAFE_HEIGHT ? 'below' : 'above';
      const top = vertical === 'below' ? rect.bottom + TOOLTIP_OFFSET : rect.top - TOOLTIP_OFFSET;
      const translateX = horizontal === 'center' ? '-50%' : horizontal === 'right' ? '-100%' : '0';
      const translateY = vertical === 'below' ? '0' : '-100%';

      return {
        idx,
        anchorEl,
        horizontal,
        vertical,
        left: Math.round(left),
        top: Math.round(top),
        transform: `translate(${translateX}, ${translateY})`,
      };
    },
    []
  );

  useEffect(() => {
    if (!activeTooltip) return;

    const updateTooltipPosition = () => {
      if (!document.body.contains(activeTooltip.anchorEl)) {
        setActiveTooltip(null);
        return;
      }
      setActiveTooltip(buildTooltipState(activeTooltip.idx, activeTooltip.anchorEl));
    };

    window.addEventListener('resize', updateTooltipPosition);
    window.addEventListener('scroll', updateTooltipPosition, true);
    return () => {
      window.removeEventListener('resize', updateTooltipPosition);
      window.removeEventListener('scroll', updateTooltipPosition, true);
    };
  }, [activeTooltip, buildTooltipState]);

  const openTooltip = useCallback(
    (idx: number, anchorEl: HTMLDivElement) => {
      setActiveTooltip(buildTooltipState(idx, anchorEl));
    },
    [buildTooltipState]
  );

  const handlePointerEnter = useCallback(
    (e: React.PointerEvent<HTMLDivElement>, idx: number) => {
      if (e.pointerType === 'mouse') {
        openTooltip(idx, e.currentTarget);
      }
    },
    [openTooltip]
  );

  const handlePointerLeave = useCallback((e: React.PointerEvent) => {
    if (e.pointerType === 'mouse') {
      setActiveTooltip(null);
    }
  }, []);

  const handlePointerDown = useCallback(
    (e: React.PointerEvent<HTMLDivElement>, idx: number) => {
      if (e.pointerType === 'touch') {
        e.preventDefault();
        setActiveTooltip((prev) =>
          prev?.idx === idx ? null : buildTooltipState(idx, e.currentTarget)
        );
      }
    },
    [buildTooltipState]
  );

  const renderTooltip = (detail: StatusBlockDetail, tooltipState: ActiveTooltipState) => {
    const total = detail.success + detail.failure;
    const posClass =
      tooltipState.horizontal === 'left'
        ? styles.healthTooltipLeft
        : tooltipState.horizontal === 'right'
          ? styles.healthTooltipRight
          : '';
    const vertClass = tooltipState.vertical === 'below' ? styles.healthTooltipBelow : '';
    const timeRange = `${formatDateTime(detail.startTime)} – ${formatDateTime(detail.endTime)}`;
    const tooltip = (
      <div
        className={`${styles.healthTooltip} ${posClass} ${vertClass}`}
        style={{
          position: 'fixed',
          left: `${tooltipState.left}px`,
          top: `${tooltipState.top}px`,
          bottom: 'auto',
          right: 'auto',
          transform: tooltipState.transform,
        }}
      >
        <span className={styles.healthTooltipTime}>{timeRange}</span>
        {total > 0 ? (
          <span className={styles.healthTooltipStats}>
            <span className={styles.healthTooltipSuccess}>
              {t('status_bar.success_short')} {detail.success}
            </span>
            <span className={styles.healthTooltipFailure}>
              {t('status_bar.failure_short')} {detail.failure}
            </span>
            <span className={styles.healthTooltipRate}>({(detail.rate * 100).toFixed(1)}%)</span>
          </span>
        ) : (
          <span className={styles.healthTooltipStats}>{t('status_bar.no_requests')}</span>
        )}
      </div>
    );

    return typeof document === 'undefined' ? tooltip : createPortal(tooltip, document.body);
  };

  const rateClass = !hasData
    ? ''
    : healthData.successRate >= 90
      ? styles.healthRateHigh
      : healthData.successRate >= 50
        ? styles.healthRateMedium
        : styles.healthRateLow;

  return (
    <div className={styles.healthCard}>
      <div className={styles.healthHeader}>
        <h3 className={styles.healthTitle}>{t('service_health.title')}</h3>
        <div className={styles.healthMeta}>
          <span className={styles.healthWindow}>{t('service_health.window')}</span>
          <span className={`${styles.healthRate} ${rateClass}`}>
            {loading ? '--' : hasData ? `${healthData.successRate.toFixed(1)}%` : '--'}
          </span>
        </div>
      </div>
      <div className={styles.healthGridScroller}>
        <div className={styles.healthGrid} ref={gridRef}>
          {healthData.blockDetails.map((detail, idx) => {
            const isIdle = detail.rate === -1;
            const blockStyle = isIdle ? undefined : { backgroundColor: rateToColor(detail.rate) };
            const isActive = activeTooltip?.idx === idx;

            return (
              <div
                key={idx}
                className={`${styles.healthBlockWrapper} ${isActive ? styles.healthBlockActive : ''}`}
                onPointerEnter={(e) => handlePointerEnter(e, idx)}
                onPointerLeave={handlePointerLeave}
                onPointerDown={(e) => handlePointerDown(e, idx)}
              >
                <div
                  className={`${styles.healthBlock} ${isIdle ? styles.healthBlockIdle : ''}`}
                  style={blockStyle}
                />
                {isActive && activeTooltip && renderTooltip(detail, activeTooltip)}
              </div>
            );
          })}
        </div>
      </div>
      <div className={styles.healthLegend}>
        <span className={styles.healthLegendLabel}>{t('service_health.oldest')}</span>
        <div className={styles.healthLegendColors}>
          <div className={`${styles.healthLegendBlock} ${styles.healthBlockIdle}`} />
          <div className={styles.healthLegendBlock} style={{ backgroundColor: '#ef4444' }} />
          <div className={styles.healthLegendBlock} style={{ backgroundColor: '#facc15' }} />
          <div className={styles.healthLegendBlock} style={{ backgroundColor: '#22c55e' }} />
        </div>
        <span className={styles.healthLegendLabel}>{t('service_health.newest')}</span>
      </div>
    </div>
  );
}
