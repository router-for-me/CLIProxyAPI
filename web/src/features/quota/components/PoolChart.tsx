import {
  memo,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type PointerEvent,
  type ReactElement,
} from 'react';
import { useTranslation } from 'react-i18next';
import { getTypeLabel } from '@/features/authFiles/constants';
import styles from '@/features/quota/components/PoolChart.module.scss';
import {
  buildScene,
  placeSceneLabels,
  readCursor,
  round,
  seriesOf,
  shiftLocalDay,
  summaryText,
  tooltipText,
  TOP,
  X_BAND,
  type Scene,
} from '@/features/quota/components/poolChartScene';
import { useElementWidth } from '@/features/quota/hooks/useElementWidth';
import type { PoolMetric } from '@/features/quota/pool/constants';
import { estimateLabelWidth } from '@/features/quota/pool/labels';
import type { PoolCardModel } from '@/features/quota/pool/types';
import { HOUR_MS, MINUTE_MS } from '@/utils/time/durations';

export interface PoolChartProps {
  locale: string;
  metric: PoolMetric;
  model: PoolCardModel;
  timeZone: string;
  /** Overrides the measured width (tests). */
  width?: number;
}

interface LayerProps {
  idPrefix: string;
  scene: Scene;
}

const FALLBACK_WIDTH = 1100;
const TOOLTIP_FONT_PX = 14;
const TOOLTIP_HEIGHT = 28;
const TOOLTIP_PADDING_X = 10;
const CURSOR_SNAP_MS = 15 * MINUTE_MS;
const ANNOUNCE_DELAY_MS = 300;

const keyTarget = (event: KeyboardEvent, from: number, model: PoolCardModel, timeZone: string) => {
  const direction = event.key === 'ArrowLeft' ? -1 : 1;

  switch (event.key) {
    case 'ArrowLeft':
    case 'ArrowRight':
      return event.shiftKey ? shiftLocalDay(from, direction, timeZone) : from + direction * HOUR_MS;
    case 'Home':
      return model.range.start;
    case 'End':
      return model.range.end;
    case 'n':
    case 'N':
      return model.now;
    default:
      return null;
  }
};

/** Everything painted under the labels: gradients, grid, axis labels, series and the Now line. */
const ChartBackdrop = memo(({ idPrefix, scene }: LayerProps): ReactElement => {
  const { baseY, gutter, plotH, plotRight } = scene.layout;
  const clipId = `${idPrefix}-clip`;
  const recordedFill = `url(#${idPrefix}-recorded)`;
  const projectedFill = `url(#${idPrefix}-projected)`;
  const gradients = [
    { id: `${idPrefix}-recorded`, stopClass: styles.stopRecorded },
    { id: `${idPrefix}-projected`, stopClass: styles.stopProjected },
  ];

  return (
    <>
      <defs>
        {gradients.map(({ id, stopClass }) => (
          <linearGradient
            gradientUnits="userSpaceOnUse"
            id={id}
            key={id}
            x1="0"
            x2="0"
            y1={TOP}
            y2={TOP + plotH}
          >
            <stop className={stopClass} offset="0" />
            <stop className={styles.stopEnd} offset="1" />
          </linearGradient>
        ))}
        <clipPath id={clipId}>
          <rect height={plotH + 4} width={plotRight - gutter} x={gutter} y={TOP - 2} />
        </clipPath>
      </defs>
      <g className={styles.dayGrid}>
        {scene.dayLines.map((x) => (
          <line key={x} x1={round(x)} x2={round(x)} y1={TOP} y2={round(baseY)} />
        ))}
      </g>
      {scene.ticks.map(({ v, y }) => (
        <line
          className={v === 0 ? styles.axis : styles.grid}
          key={v}
          x1={gutter}
          x2={plotRight}
          y1={round(y)}
          y2={round(y)}
        />
      ))}
      {scene.ticks.map(({ label, v, y }) => (
        <text
          className={styles.axisText}
          dominantBaseline="middle"
          key={v}
          textAnchor="end"
          x={gutter - 12}
          y={round(y)}
        >
          {label}
        </text>
      ))}
      <g className={styles.reveal}>
        <g clipPath={`url(#${clipId})`}>
          {scene.recorded.map(
            ({ area, key }) => area && <path d={area} fill={recordedFill} key={key} />
          )}
          {scene.projected?.area && <path d={scene.projected.area} fill={projectedFill} />}
          {scene.recorded.map(({ key, line, partial }) => (
            <path
              className={styles.line}
              d={line}
              data-partial={partial || undefined}
              data-series="recorded"
              key={key}
              opacity={partial ? 0.45 : undefined}
            />
          ))}
          {scene.projected && (
            <path
              className={styles.line}
              d={scene.projected.line}
              data-series="projected"
              strokeDasharray="8 4"
            />
          )}
        </g>
      </g>
      {scene.nowLabel && (
        <>
          <line
            className={styles.nowLine}
            x1={round(scene.nowLabel.centerX)}
            x2={round(scene.nowLabel.centerX)}
            y1={TOP - 10}
            y2={TOP + plotH + X_BAND}
          />
          <text
            className={styles.axisText}
            textAnchor="middle"
            x={round(scene.nowLabel.centerX)}
            y={scene.nowLabel.baseline}
          >
            {scene.nowLabel.text}
          </text>
        </>
      )}
    </>
  );
});

/** Markers and day labels painted over the step labels. */
const ChartForeground = memo(({ scene }: Pick<LayerProps, 'scene'>): ReactElement => {
  const { baseY, plotH } = scene.layout;

  return (
    <>
      {scene.emptyX !== null && (
        <circle
          className={styles.emptyMarker}
          cx={round(scene.emptyX)}
          cy={round(baseY)}
          data-marker="empty"
          r={6}
        />
      )}
      {scene.nowDot && (
        <circle
          className={styles.nowDot}
          cx={round(scene.nowDot.x)}
          cy={round(scene.nowDot.y)}
          data-marker="now"
          r={4}
        />
      )}
      {scene.dayLabels.map(({ key, text, x }) => (
        <text
          className={styles.axisText}
          key={key}
          textAnchor="middle"
          x={round(x)}
          y={TOP + plotH + 18}
        >
          {text}
        </text>
      ))}
    </>
  );
});

/** The pooled quota over the 14-day range: recorded history, projection, resets and markers. */
export const PoolChart = ({
  locale,
  metric,
  model,
  timeZone,
  width,
}: PoolChartProps): ReactElement => {
  const { t } = useTranslation();
  const [measureRef, measuredWidth] = useElementWidth<HTMLDivElement>(FALLBACK_WIDTH);
  const idPrefix = useId();
  const summaryId = `${idPrefix}-summary`;
  const hintId = `${idPrefix}-hint`;
  const [cursor, setCursor] = useState<number | null>(null);
  const [announcement, setAnnouncement] = useState('');
  const frameRef = useRef<number | null>(null);
  const announceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const chartWidth = width ?? measuredWidth;
  const scene = useMemo(
    () => buildScene(model, metric, chartWidth, locale, timeZone, t),
    [chartWidth, locale, metric, model, t, timeZone]
  );
  const restingLabels = useMemo(() => placeSceneLabels(scene, null), [scene]);
  const { layout } = scene;
  const { range } = model;
  const series = seriesOf(model, metric);
  const chartLabel = t('quota_pool.chart_label', { provider: getTypeLabel(t, model.provider) });
  const isCursorVisible = cursor !== null && cursor >= range.start && cursor <= range.end;
  const tooltip = isCursorVisible ? tooltipText(model, metric, cursor, t, locale, timeZone) : null;
  const tooltipWidth = tooltip
    ? estimateLabelWidth(tooltip, TOOLTIP_FONT_PX) + 2 * TOOLTIP_PADDING_X
    : 0;
  const labels = tooltip
    ? placeSceneLabels(scene, {
        height: TOOLTIP_HEIGHT,
        width: tooltipWidth,
        x: layout.plotRight - tooltipWidth,
        y: 0,
      })
    : restingLabels;
  const cursorReading = isCursorVisible ? readCursor(model, metric, cursor) : null;

  useEffect(
    () => () => {
      if (frameRef.current !== null) cancelAnimationFrame(frameRef.current);
      if (announceRef.current !== null) clearTimeout(announceRef.current);
    },
    []
  );

  const clampToRange = (value: number) => Math.min(Math.max(value, range.start), range.end);

  const cancelFrame = () => {
    if (frameRef.current !== null) cancelAnimationFrame(frameRef.current);

    frameRef.current = null;
  };

  const handlePointer = (event: PointerEvent<SVGRectElement>) => {
    const box = event.currentTarget.getBoundingClientRect();
    const ratio = (event.clientX - box.left) / box.width;
    const raw = range.start + ratio * (range.end - range.start);
    const next = clampToRange(Math.round(raw / CURSOR_SNAP_MS) * CURSOR_SNAP_MS);

    cancelFrame();
    frameRef.current = requestAnimationFrame(() => {
      frameRef.current = null;
      setCursor(next);
    });
  };

  const clearCursor = () => {
    cancelFrame();
    setCursor(null);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    if (event.key === 'Escape') {
      clearCursor();

      return;
    }

    const target = keyTarget(event, cursor ?? clampToRange(model.now), model, timeZone);

    if (target === null) return;

    const next = clampToRange(target);

    event.preventDefault();
    setCursor(next);

    if (announceRef.current !== null) clearTimeout(announceRef.current);

    announceRef.current = setTimeout(() => {
      setAnnouncement(tooltipText(model, metric, next, t, locale, timeZone));
    }, ANNOUNCE_DELAY_MS);
  };

  const handleFocus = () => {
    if (cursor === null) setCursor(clampToRange(model.now));
  };

  return (
    <figure className={styles.figure}>
      <div
        aria-describedby={`${summaryId} ${hintId}`}
        aria-label={chartLabel}
        className={styles.plot}
        onBlur={clearCursor}
        onFocus={handleFocus}
        onKeyDown={handleKeyDown}
        ref={measureRef}
        role="group"
        tabIndex={0}
      >
        <svg
          aria-label={chartLabel}
          className={styles.svg}
          height={layout.height}
          role="img"
          viewBox={`0 0 ${layout.width} ${layout.height}`}
          width={layout.width}
        >
          <ChartBackdrop idPrefix={idPrefix} scene={scene} />
          {labels.map(({ isEmpty, label }) => (
            <text
              className={isEmpty ? styles.emptyLabel : styles.stepLabel}
              key={label.rect.id}
              textAnchor="middle"
              x={round(label.centerX)}
              y={round(label.baseline)}
            >
              {label.text}
            </text>
          ))}
          <ChartForeground scene={scene} />
          {isCursorVisible && (
            <line
              className={styles.crosshair}
              x1={round(layout.x(cursor))}
              x2={round(layout.x(cursor))}
              y1={TOP}
              y2={TOP + layout.plotH}
            />
          )}
          {isCursorVisible && cursorReading && (
            <circle
              className={styles.cursorDot}
              cx={round(layout.x(cursor))}
              cy={round(layout.y(cursorReading.v))}
              r={4.5}
            />
          )}
          <rect
            className={styles.hitArea}
            height={layout.plotH}
            onPointerCancel={clearCursor}
            onPointerDown={handlePointer}
            onPointerLeave={clearCursor}
            onPointerMove={handlePointer}
            width={layout.plotRight - layout.gutter}
            x={layout.gutter}
            y={TOP}
          />
        </svg>
        {tooltip && (
          <div aria-hidden="true" className={styles.tooltip}>
            {tooltip}
          </div>
        )}
      </div>
      <p className={styles.srOnly} id={summaryId}>
        {summaryText(model, t, locale, timeZone)}
      </p>
      <p className={styles.srOnly} id={hintId}>
        {t('quota_pool.chart_hint')}
      </p>
      <p aria-live="polite" className={styles.srOnly}>
        {announcement}
      </p>
      <figcaption className={styles.legend}>
        <span className={styles.legendLabel}>{t('quota_pool.legend_pool')}</span>
        {series.recorded.length > 0 && (
          <span className={styles.legendItem}>
            <span aria-hidden="true" className={styles.swatchRecorded} />
            {t('quota_pool.legend_recorded')}
          </span>
        )}
        {series.projected && (
          <span className={styles.legendItem}>
            <span aria-hidden="true" className={styles.swatchProjected} />
            {t('quota_pool.legend_projected')}
          </span>
        )}
        {series.recorded.some(({ partial }) => partial) && (
          <span className={styles.legendItem}>
            <span aria-hidden="true" className={styles.swatchPartial} />
            {t('quota_pool.legend_partial')}
          </span>
        )}
      </figcaption>
    </figure>
  );
};
