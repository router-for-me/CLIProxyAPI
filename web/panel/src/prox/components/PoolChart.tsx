/**
 * Pool chart: recorded pool (solid) up to now, projected pool (dashed) after,
 * with the capacity each reset returns labeled on the line.
 */

import { useId, useMemo, useState } from 'react';
import type { ChartEvent, ChartPoint } from '../types';
import { useElementWidth } from '../useElementWidth';
import styles from './PoolChart.module.scss';

const HEIGHT = 320;
const PAD = { top: 34, right: 16, bottom: 46, left: 56 };
const DAY = 86_400_000;

export interface PoolChartProps {
  recorded: ChartPoint[];
  projected: ChartPoint[];
  events: ChartEvent[];
  capacity: number;
  now: string;
  color: string;
  /** Hour ticks instead of day ticks (five-hour view). */
  hourly?: boolean;
}

interface Pt {
  t: number;
  v: number;
}

const toPts = (list: ChartPoint[]) =>
  list.map((p) => ({ t: new Date(p.at).getTime(), v: p.value }));

export function PoolChart({
  recorded,
  projected,
  events,
  capacity,
  now,
  color,
  hourly,
}: PoolChartProps) {
  const [ref, width] = useElementWidth<HTMLDivElement>();
  const gradientId = useId();
  const [hover, setHover] = useState<number | null>(null);

  const rec = useMemo(() => toPts(recorded), [recorded]);
  const proj = useMemo(() => toPts(projected), [projected]);
  const nowMs = new Date(now).getTime();
  const all = rec.concat(proj);
  const t0 = all.length ? all[0].t : nowMs - DAY;
  const t1 = all.length ? all[all.length - 1].t : nowMs + DAY;
  const top = Math.max(capacity, ...all.map((p) => p.v), 100);
  const innerW = Math.max(10, width - PAD.left - PAD.right);
  const innerH = HEIGHT - PAD.top - PAD.bottom;
  const x = (t: number) => PAD.left + ((t - t0) / Math.max(1, t1 - t0)) * innerW;
  const y = (v: number) => PAD.top + innerH - (v / top) * innerH;

  const line = (pts: Pt[]) =>
    pts.map((p, i) => `${i ? 'L' : 'M'}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join('');
  const area = (pts: Pt[]) =>
    pts.length < 2
      ? ''
      : `${line(pts)}L${x(pts[pts.length - 1].t).toFixed(1)},${y(0)}L${x(pts[0].t).toFixed(1)},${y(0)}Z`;

  const ticks = useMemo(() => {
    const out: { t: number; top: string; bottom: string }[] = [];
    if (hourly) {
      const start = new Date(t0);
      start.setMinutes(0, 0, 0);
      const stepH = (t1 - t0) / 3_600_000 > 30 ? 6 : 3;
      for (let t = start.getTime(); t <= t1; t += 3_600_000) {
        const d = new Date(t);
        if (t < t0 || d.getHours() % stepH !== 0) continue;
        out.push({
          t,
          top: d.toLocaleTimeString(undefined, { hour: 'numeric' }),
          bottom: d.getHours() === 0 ? d.toLocaleDateString(undefined, { weekday: 'short' }) : '',
        });
      }
      return out;
    }
    const start = new Date(t0);
    start.setHours(12, 0, 0, 0);
    const spanDays = (t1 - t0) / DAY;
    const every = spanDays > 20 ? 2 : 1;
    let i = 0;
    for (let d = new Date(start); d.getTime() <= t1; d.setDate(d.getDate() + 1), i++) {
      if (d.getTime() < t0 || i % every) continue;
      out.push({
        t: d.getTime(),
        top: d.toLocaleDateString(undefined, { weekday: 'short' }),
        bottom: String(d.getDate()),
      });
    }
    return out;
  }, [t0, t1, hourly]);

  // Reset labels sit just above the line after the step; skip ones that would collide.
  const labels = useMemo(() => {
    const series = rec.concat(proj);
    const valueAt = (t: number) => {
      let v = series.length ? series[0].v : 0;
      for (const p of series) {
        if (p.t > t + 60_000) break;
        v = p.v;
      }
      return v;
    };
    const out: { x: number; y: number; text: string }[] = [];
    let lastX = -Infinity;
    for (const ev of [...events].sort((a, b) => a.at.localeCompare(b.at))) {
      const t = new Date(ev.at).getTime();
      if (t < t0 || t > t1 || ev.gain < 1) continue;
      const px = x(t);
      if (px - lastX < 46) continue;
      lastX = px;
      out.push({ x: px, y: y(valueAt(t)) - 10, text: `+${Math.round(ev.gain)}%` });
    }
    return out;
    // x/y derive from width and the series, which are listed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [events, rec, proj, t0, t1, width, top]);

  const nowX = x(nowMs);
  const current = rec.length ? rec[rec.length - 1] : null;
  const hoverPt = useMemo(() => {
    if (hover === null || !all.length) return null;
    const t = t0 + ((hover - PAD.left) / innerW) * (t1 - t0);
    let best = all[0];
    for (const p of all) if (Math.abs(p.t - t) < Math.abs(best.t - t)) best = p;
    return { ...best, projected: best.t > nowMs };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hover, recorded, projected, width]);

  return (
    <div ref={ref} className={styles.wrap}>
      {width > 0 && (
        <svg
          width={width}
          height={HEIGHT}
          className={styles.svg}
          onMouseMove={(e) => setHover(e.clientX - e.currentTarget.getBoundingClientRect().left)}
          onMouseLeave={() => setHover(null)}
          role="img"
          aria-label="Pool remaining over time"
        >
          <defs>
            <linearGradient id={gradientId} x1="0" x2="0" y1="0" y2="1">
              <stop offset="0%" stopColor={color} stopOpacity={0.28} />
              <stop offset="100%" stopColor={color} stopOpacity={0.02} />
            </linearGradient>
          </defs>
          {[0, 0.5, 1].map((f) => (
            <g key={f}>
              <line
                x1={PAD.left}
                x2={PAD.left + innerW}
                y1={y(top * f)}
                y2={y(top * f)}
                className={styles.grid}
              />
              <text x={PAD.left - 10} y={y(top * f) + 4} className={styles.axisY}>
                {Math.round(top * f)}%
              </text>
            </g>
          ))}
          {ticks.map((tk) => (
            <g key={tk.t}>
              <line
                x1={x(tk.t)}
                x2={x(tk.t)}
                y1={PAD.top}
                y2={PAD.top + innerH}
                className={styles.gridV}
              />
              <text x={x(tk.t)} y={HEIGHT - 26} className={styles.axisX}>
                {tk.top}
              </text>
              <text x={x(tk.t)} y={HEIGHT - 8} className={styles.axisX}>
                {tk.bottom}
              </text>
            </g>
          ))}
          <path d={area(rec)} fill={`url(#${gradientId})`} />
          <path d={area(proj)} fill={`url(#${gradientId})`} opacity={0.35} />
          <path
            d={line(rec)}
            fill="none"
            stroke={color}
            strokeWidth={2.25}
            strokeLinejoin="round"
          />
          <path
            d={line(proj)}
            fill="none"
            stroke={color}
            strokeWidth={2}
            strokeDasharray="7 6"
            opacity={0.9}
          />
          <line x1={nowX} x2={nowX} y1={PAD.top - 8} y2={PAD.top + innerH} className={styles.now} />
          <text x={nowX} y={PAD.top - 14} className={styles.nowLabel}>
            Now
          </text>
          {current && <circle cx={x(current.t)} cy={y(current.v)} r={5} fill={color} />}
          {labels.map((l) => (
            <text key={`${l.x}`} x={l.x} y={Math.max(PAD.top - 2, l.y)} className={styles.gain}>
              {l.text}
            </text>
          ))}
          {hoverPt && (
            <g pointerEvents="none">
              <line
                x1={x(hoverPt.t)}
                x2={x(hoverPt.t)}
                y1={PAD.top}
                y2={PAD.top + innerH}
                className={styles.cross}
              />
              <circle cx={x(hoverPt.t)} cy={y(hoverPt.v)} r={4} fill={color} />
              <g
                transform={`translate(${Math.min(x(hoverPt.t) + 10, width - 170)},${PAD.top + 6})`}
              >
                <rect width={160} height={40} rx={8} className={styles.tip} />
                <text x={10} y={17} className={styles.tipValue}>
                  {Math.round(hoverPt.v)}% {hoverPt.projected ? 'projected' : ''}
                </text>
                <text x={10} y={32} className={styles.tipTime}>
                  {new Date(hoverPt.t).toLocaleString(undefined, {
                    weekday: 'short',
                    month: 'short',
                    day: 'numeric',
                    hour: 'numeric',
                    minute: '2-digit',
                  })}
                </text>
              </g>
            </g>
          )}
        </svg>
      )}
    </div>
  );
}
