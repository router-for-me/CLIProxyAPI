/** Drain rate: trailing one-hour pool consumption in % per hour. */

import { useId, useMemo } from 'react';
import type { ChartPoint } from '../types';
import { useElementWidth } from '../useElementWidth';
import styles from './PoolChart.module.scss';

const HEIGHT = 300;
const PAD = { top: 16, right: 16, bottom: 30, left: 44 };

export function DrainChart({
  points,
  color,
  since,
}: {
  points: ChartPoint[];
  color: string;
  since: number;
}) {
  const [ref, width] = useElementWidth<HTMLDivElement>();
  const gradientId = useId();
  const pts = useMemo(
    () =>
      points.map((p) => ({ t: new Date(p.at).getTime(), v: p.value })).filter((p) => p.t >= since),
    [points, since]
  );
  const t0 = pts.length ? pts[0].t : since;
  const t1 = pts.length ? pts[pts.length - 1].t : since + 3_600_000;
  const peak = Math.max(1, ...pts.map((p) => p.v));
  const step = peak > 40 ? 20 : peak > 20 ? 10 : peak > 8 ? 5 : peak > 4 ? 2 : 1;
  const top = Math.ceil(peak / step) * step;
  const innerW = Math.max(10, width - PAD.left - PAD.right);
  const innerH = HEIGHT - PAD.top - PAD.bottom;
  const x = (t: number) => PAD.left + ((t - t0) / Math.max(1, t1 - t0)) * innerW;
  const y = (v: number) => PAD.top + innerH - (v / top) * innerH;
  const line = pts
    .map((p, i) => `${i ? 'L' : 'M'}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`)
    .join('');
  const area =
    pts.length > 1 ? `${line}L${x(t1).toFixed(1)},${y(0)}L${x(t0).toFixed(1)},${y(0)}Z` : '';
  const yTicks: number[] = [];
  for (let v = 0; v <= top; v += step) yTicks.push(v);
  const xTicks = [0, 0.25, 0.5, 0.75, 1].map((f) => t0 + f * (t1 - t0));

  return (
    <div ref={ref} className={styles.wrap}>
      {width > 0 && (
        <svg
          width={width}
          height={HEIGHT}
          className={styles.svg}
          role="img"
          aria-label="Drain rate"
        >
          <defs>
            <linearGradient id={gradientId} x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stopColor={color} stopOpacity={0.3} />
              <stop offset="100%" stopColor={color} stopOpacity={0.02} />
            </linearGradient>
          </defs>
          {yTicks.map((v) => (
            <g key={v}>
              <line
                x1={PAD.left}
                x2={PAD.left + innerW}
                y1={y(v)}
                y2={y(v)}
                className={styles.grid}
              />
              <text x={PAD.left - 10} y={y(v) + 4} className={styles.axisY}>
                {v}
              </text>
            </g>
          ))}
          {xTicks.map((t) => (
            <text key={t} x={x(t)} y={HEIGHT - 8} className={styles.axisX}>
              {new Date(t).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })}
            </text>
          ))}
          <path d={area} fill={`url(#${gradientId})`} />
          <path d={line} fill="none" stroke={color} strokeWidth={2.25} strokeLinejoin="round" />
        </svg>
      )}
    </div>
  );
}
