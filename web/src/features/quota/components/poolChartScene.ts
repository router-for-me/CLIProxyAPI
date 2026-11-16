/**
 * Pure drawing data for PoolChart: layout, paths, label candidates and the
 * texts the chart reads out. Everything here is a function of the model.
 */

import type { TFunction } from 'i18next';
import { getTypeLabel } from '@/features/authFiles/constants';
import { addZonedDays, zonedMidnight } from '@/features/quota/pool/calendar';
import type { PoolMetric } from '@/features/quota/pool/constants';
import {
  dayLabelParts,
  formatMonthDay,
  formatPoolPercent,
  formatRate,
  formatWeekdayTime,
  formatWhen,
} from '@/features/quota/pool/format';
import {
  areaPath,
  downsampleForDraw,
  linePath,
  segmentValueAt,
  timeScale,
  valueAt,
  valueScale,
} from '@/features/quota/pool/geometry';
import {
  estimateLabelWidth,
  placeLabels,
  type LabelCandidate,
  type Rect,
} from '@/features/quota/pool/labels';
import type { PoolCardModel, RecordedSegment } from '@/features/quota/pool/types';

type Scale = (value: number) => number;

interface Layout {
  baseY: number;
  gutter: number;
  height: number;
  plotH: number;
  plotRight: number;
  width: number;
  x: Scale;
  y: Scale;
}

interface ChartLabel {
  baseline: number;
  centerX: number;
  rect: LabelCandidate;
  text: string;
}

interface LabelGroup {
  id: string;
  isEmpty: boolean;
  positions: ChartLabel[];
  priority: number;
}

interface CursorReading {
  isProjected: boolean;
  known: number;
  partial: boolean;
  v: number;
}

interface DrawnPath {
  area: string | null;
  line: string;
}

export interface Scene {
  dayLabels: Array<{ key: number; text: string; x: number }>;
  dayLines: number[];
  emptyX: number | null;
  labelGroups: LabelGroup[];
  layout: Layout;
  nowDot: { x: number; y: number } | null;
  nowLabel: ChartLabel | null;
  projected: DrawnPath | null;
  recorded: Array<DrawnPath & { key: string; partial: boolean }>;
  ticks: Array<{ label: string; v: number; y: number }>;
}

const RIGHT = 14;
export const TOP = 30;
export const X_BAND = 26;
const LABEL_FONT_PX = 12;
const EMPTY_FONT_PX = 13;

export const round = (value: number): number => Math.round(value * 100) / 100;

const getLayout = (width: number, model: PoolCardModel, yMax: number): Layout => {
  const gutter = width <= 520 ? 44 : 60;
  const plotH = width <= 768 ? 180 : 232;
  const plotRight = width - RIGHT;
  const y = valueScale(yMax, TOP, plotH);

  return {
    baseY: y(0),
    gutter,
    height: TOP + plotH + X_BAND,
    plotH,
    plotRight,
    width,
    x: timeScale(model.range.start, model.range.end, gutter, plotRight - gutter),
    y,
  };
};

const toLabel = (
  id: string,
  priority: number,
  text: string,
  centerX: number,
  baseline: number,
  fontPx = LABEL_FONT_PX
): ChartLabel => {
  const width = estimateLabelWidth(text, fontPx);
  const top = baseline - fontPx;

  return {
    baseline,
    centerX,
    rect: { height: fontPx + 3, id, priority, width, x: centerX - width / 2, y: top },
    text,
  };
};

const stepPriority = (isRecorded: boolean, gain: number, index: number, nextIndex: number) => {
  if (isRecorded) return 50 + gain / 1000;
  if (index === nextIndex) return 90;

  return 80 - index * 0.01;
};

/** Above the riser, or at its midpoint when the riser is tall or the top spot is taken. */
const stepLabelGroups = (model: PoolCardModel, layout: Layout, t: TFunction): LabelGroup[] => {
  const { projected, recorded, steps } = model.remaining;
  const projectedSteps = steps.filter(({ kind }) => kind === 'projected');
  const nextIndex = projectedSteps.findIndex((step) => step.t > model.now);

  return steps.flatMap((step, index) => {
    const isRecorded = step.kind === 'recorded';
    const post = isRecorded
      ? (segmentValueAt(recorded, step.t)?.v ?? null)
      : valueAt(projected ?? [], step.t);

    if (post === null) return [];

    const id = `step-${index}`;
    const text = t('quota_pool.gain', { value: Math.round(step.gain) });
    const priority = stepPriority(isRecorded, step.gain, projectedSteps.indexOf(step), nextIndex);
    const x = layout.x(step.t);
    const preY = layout.y(post - step.gain);
    const postY = layout.y(post);
    const top = toLabel(id, priority, text, x, Math.max(LABEL_FONT_PX, postY - 8));
    const middle = toLabel(id, priority, text, x, (preY + postY) / 2 + 4);
    const positions = preY - postY > layout.plotH / 2 ? [middle] : [top, middle];

    return [{ id, isEmpty: false, positions, priority }];
  });
};

const emptyLabelGroup = (
  emptyAt: number,
  layout: Layout,
  t: TFunction,
  locale: string,
  timeZone: string
): LabelGroup => {
  const text = t('quota_pool.chart_empty', { when: formatWeekdayTime(emptyAt, locale, timeZone) });
  const halfWidth = estimateLabelWidth(text, EMPTY_FONT_PX) / 2;
  const centerX = Math.min(
    Math.max(layout.x(emptyAt), layout.gutter + halfWidth),
    layout.plotRight - halfWidth
  );
  const label = toLabel('empty', 100, text, centerX, layout.baseY - 14, EMPTY_FONT_PX);

  return { id: 'empty', isEmpty: true, positions: [label], priority: 100 };
};

/** Greedy by priority; each label tries its positions in order before the next label is placed. */
export const placeSceneLabels = (scene: Scene, tooltip: Rect | null) => {
  const bounds = { height: TOP + scene.layout.plotH, width: scene.layout.width, x: 0, y: 0 };
  const occupied = [scene.nowLabel?.rect, tooltip].filter((rect): rect is Rect => rect != null);
  const groups = [...scene.labelGroups].sort((left, right) => right.priority - left.priority);
  const placed: Array<{ isEmpty: boolean; label: ChartLabel }> = [];

  for (const { isEmpty, positions } of groups) {
    const label = positions.find(({ rect }) => placeLabels([rect], occupied, bounds).size > 0);

    if (label) {
      occupied.push(label.rect);
      placed.push({ isEmpty, label });
    }
  }

  return placed;
};

const dayLabelStride = (cellWidth: number): number => {
  if (cellWidth < 22) return 7;
  if (cellWidth < 44) return 2;

  return 1;
};

export const seriesOf = (model: PoolCardModel, metric: PoolMetric) =>
  metric === 'remaining' ? model.remaining : model.drain;

const formatValue = (metric: PoolMetric, v: number): string =>
  metric === 'remaining' ? formatPoolPercent(v) : formatRate(v);

const nowValue = (model: PoolCardModel, metric: PoolMetric): number | null => {
  const projected = seriesOf(model, metric).projected ?? [];

  if (metric === 'drain') return valueAt(projected, model.now) ?? model.burn.value;
  if (model.now === model.dataAt) return model.poolNow;

  return valueAt(projected, model.now) ?? model.poolNow;
};

export const buildScene = (
  model: PoolCardModel,
  metric: PoolMetric,
  width: number,
  locale: string,
  timeZone: string,
  t: TFunction
): Scene => {
  const series = seriesOf(model, metric);
  const isRemaining = metric === 'remaining';
  const layout = getLayout(width, model, series.yMax);
  const { baseY, x, y } = layout;
  const { now, range } = model;
  const emptyAt = isRemaining ? model.remaining.emptyAt : null;
  const stride = dayLabelStride((layout.plotRight - layout.gutter) / range.days.length);
  const nowX = range.start <= now && now <= range.end ? x(now) : null;
  const dotValue = nowX === null ? null : nowValue(model, metric);
  const unit = isRemaining ? '%' : '%/h';
  const steps = isRemaining ? stepLabelGroups(model, layout, t) : [];

  const drawRecorded = (segment: RecordedSegment, index: number) => {
    const points = downsampleForDraw(segment.points, x);
    const hasArea = isRemaining && !segment.partial;

    return {
      area: hasArea ? areaPath(points, x, y, baseY) : null,
      key: `${index}:${segment.points[0].t}`,
      line: linePath(points, x, y),
      partial: segment.partial,
    };
  };

  return {
    dayLabels: range.days
      .filter((_, index) => index % stride === 0)
      .map((day) => ({
        key: day.start,
        text: t('quota_pool.day_label', dayLabelParts(day.start, locale, timeZone)),
        x: x((day.start + day.end) / 2),
      })),
    dayLines: range.days.slice(1).map((day) => x(day.start)),
    emptyX: emptyAt === null ? null : x(emptyAt),
    labelGroups:
      emptyAt === null ? steps : [emptyLabelGroup(emptyAt, layout, t, locale, timeZone), ...steps],
    layout,
    nowDot: nowX === null || dotValue === null ? null : { x: nowX, y: y(dotValue) },
    nowLabel: nowX === null ? null : toLabel('now', 0, t('quota_pool.chart_now'), nowX, TOP - 16),
    projected: series.projected && {
      area: isRemaining ? areaPath(series.projected, x, y, baseY) : null,
      line: linePath(series.projected, x, y),
    },
    recorded: series.recorded.map(drawRecorded),
    ticks: [series.yMax, series.yMax / 2, 0].map((v) => ({
      label: `${formatValue(metric, v)}${unit}`,
      v,
      y: y(v),
    })),
  };
};

export const readCursor = (
  model: PoolCardModel,
  metric: PoolMetric,
  cursor: number
): CursorReading | null => {
  const series = seriesOf(model, metric);

  if (cursor > model.dataAt) {
    const v = valueAt(series.projected ?? [], cursor);

    return v === null ? null : { isProjected: true, known: model.memberCount, partial: false, v };
  }

  const reading = segmentValueAt(series.recorded, cursor);

  return reading && { ...reading, isProjected: false };
};

export const tooltipText = (
  model: PoolCardModel,
  metric: PoolMetric,
  cursor: number,
  t: TFunction,
  locale: string,
  timeZone: string
): string => {
  const when = formatWeekdayTime(cursor, locale, timeZone);
  const reading = readCursor(model, metric, cursor);

  if (!reading) return t('quota_pool.tooltip_no_data', { when });

  const key = metric === 'remaining' ? 'quota_pool.tooltip_remaining' : 'quota_pool.tooltip_drain';
  const kind = t(reading.isProjected ? 'quota_pool.kind_projected' : 'quota_pool.kind_recorded');
  const text = t(key, { kind, value: formatValue(metric, reading.v), when });

  // A stale-only stretch is partial with full coverage; the faded line marks it.
  if (!reading.partial || reading.known >= model.memberCount) return text;

  const partial = t('quota_pool.tooltip_partial', {
    known: reading.known,
    total: model.memberCount,
  });

  return `${text} · ${partial}`;
};

const poolEmptyText = (model: PoolCardModel, t: TFunction, locale: string, timeZone: string) => {
  const stat = model.stats.poolEmpty;

  switch (stat.kind) {
    case 'at':
      return formatWhen(stat.at, model.now, locale, timeZone);
    case 'now':
      return t('quota_pool.empty_now');
    case 'never':
      return t('quota_pool.not_by', { date: formatMonthDay(stat.horizonEnd, locale, timeZone) });
    case 'unknown':
      return t('quota_pool.unknown');
  }
};

const nextResetText = (model: PoolCardModel, t: TFunction, locale: string, timeZone: string) => {
  const stat = model.stats.nextReset;

  if (stat.kind === 'none') return t('quota_pool.unknown');

  const gain = t(stat.lowerBound ? 'quota_pool.gain_at_least' : 'quota_pool.gain', {
    value: stat.gain,
  });

  return `${gain} ${formatWhen(stat.at, model.now, locale, timeZone)}`;
};

export const summaryText = (
  model: PoolCardModel,
  t: TFunction,
  locale: string,
  timeZone: string
) => {
  const { burn } = model;
  const burnKey = burn.approximate ? 'quota_pool.burn_approx' : 'quota_pool.burn_value';

  return t('quota_pool.chart_summary', {
    burn:
      burn.value === null ? t('quota_pool.unknown') : t(burnKey, { value: formatRate(burn.value) }),
    capacity: formatPoolPercent(model.capacity),
    nextReset: nextResetText(model, t, locale, timeZone),
    poolEmpty: poolEmptyText(model, t, locale, timeZone),
    provider: getTypeLabel(t, model.provider),
    remaining: formatPoolPercent(model.poolNow),
  });
};

export const shiftLocalDay = (ms: number, days: 1 | -1, timeZone: string): number => {
  const midnight = zonedMidnight(ms, timeZone);

  return ms + addZonedDays(midnight, days, timeZone) - midnight;
};
