import { useEffect, useMemo, useRef, useState } from "react";
import { compact } from "../format";

export interface Series {
  key: string;
  label: string;
  color: string; // CSS variable reference, e.g. "var(--s1)"
  values: number[];
}

const H = 170;
const PAD = { top: 10, right: 8, bottom: 22, left: 40 };

function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [w, setW] = useState(600);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setW(Math.max(240, Math.floor(e.contentRect.width))));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, w] as const;
}

function niceMax(v: number) {
  if (v <= 0) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  const n = v / p;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10) * p;
}

/**
 * Time-series chart. `kind="bars"` stacks series (bottom first) with a 2px
 * surface gap; `kind="lines"` draws 2px lines. One y-axis only.
 */
export function TimeChart({ times, series, kind, format = compact, title }: { times: string[]; series: Series[]; kind: "bars" | "lines"; format?: (n: number) => string; title: string }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number>();
  const n = times.length;
  const innerW = width - PAD.left - PAD.right;
  const innerH = H - PAD.top - PAD.bottom;
  const totals = useMemo(() => times.map((_, i) => (kind === "bars" ? series.reduce((s, x) => s + (x.values[i] ?? 0), 0) : Math.max(0, ...series.map((x) => x.values[i] ?? 0)))), [times, series, kind]);
  const max = niceMax(Math.max(0, ...totals));
  const y = (v: number) => PAD.top + innerH - (v / max) * innerH;
  const step = n > 0 ? innerW / n : innerW;
  const x = (i: number) => PAD.left + i * step + step / 2;
  const ticks = [0, max / 2, max];
  const labelEvery = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(innerW / 70))));
  const empty = totals.every((t) => t === 0);
  const tf = (iso: string) => {
    const d = new Date(iso);
    return n > 30 && times.length > 0 && Date.parse(times[n - 1]) - Date.parse(times[0]) > 36 * 3600e3
      ? d.toLocaleDateString(undefined, { month: "short", day: "numeric" })
      : d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  };

  function onMove(e: React.MouseEvent<SVGRectElement>) {
    const r = e.currentTarget.getBoundingClientRect();
    const i = Math.floor((e.clientX - r.left) / (r.width / n));
    setHover(Math.max(0, Math.min(n - 1, i)));
  }

  return (
    <div className="chart" ref={ref}>
      <svg width={width} height={H} role="img" aria-label={`${title}. ${empty ? "No data in this range." : `Peak ${format(Math.max(...totals))}.`}`}>
        {ticks.map((t) => (
          <g key={t}>
            <line className="gridline" x1={PAD.left} x2={width - PAD.right} y1={y(t)} y2={y(t)} />
            <text className="axis" x={PAD.left - 6} y={y(t) + 3.5} textAnchor="end">{format(t)}</text>
          </g>
        ))}
        {times.map((t, i) =>
          i % labelEvery === 0 ? (
            <text key={t} className="axis" x={x(i)} y={H - 6} textAnchor="middle">{tf(t)}</text>
          ) : null,
        )}
        {kind === "bars" &&
          times.map((_, i) => {
            let acc = 0;
            const bw = Math.max(2, Math.min(18, step - 3));
            return series.map((s) => {
              const v = s.values[i] ?? 0;
              if (v <= 0) return null;
              const y0 = y(acc), y1 = y(acc + v);
              acc += v;
              const h = Math.max(1, y0 - y1 - (acc > v ? 2 : 0)); // 2px gap between stacked segments
              return <rect key={s.key + i} x={x(i) - bw / 2} y={y1} width={bw} height={h} rx={Math.min(2, bw / 2)} fill={s.color} opacity={hover === undefined || hover === i ? 1 : 0.55} />;
            });
          })}
        {kind === "lines" &&
          series.map((s) => (
            <polyline key={s.key} fill="none" stroke={s.color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" points={s.values.map((v, i) => `${x(i)},${y(v ?? 0)}`).join(" ")} />
          ))}
        {hover !== undefined && (
          <>
            <line x1={x(hover)} x2={x(hover)} y1={PAD.top} y2={PAD.top + innerH} stroke="var(--text-3)" strokeDasharray="3 3" />
            {kind === "lines" && series.map((s) => <circle key={s.key} cx={x(hover)} cy={y(s.values[hover] ?? 0)} r={4} fill={s.color} stroke="var(--surface)" strokeWidth={2} />)}
          </>
        )}
        <rect x={PAD.left} y={PAD.top} width={innerW} height={innerH} fill="transparent" onMouseMove={onMove} onMouseLeave={() => setHover(undefined)} />
      </svg>
      {empty && <div className="faint" style={{ position: "absolute", top: 60, width: "100%", textAlign: "center", fontSize: 12 }}>No requests in this range</div>}
      {hover !== undefined && !empty && (
        <div className="chart-tip" style={{ left: Math.min(width - 170, Math.max(0, x(hover) + 10)), top: 8 }}>
          <div className="faint" style={{ marginBottom: 3 }}>{new Date(times[hover]).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })}</div>
          {series.map((s) => (
            <div className="row" key={s.key}>
              <i style={{ width: 8, height: 8, borderRadius: 2, background: s.color, display: "inline-block" }} />
              <span className="muted">{s.label}</span>
              <span className="num" style={{ marginLeft: "auto", paddingLeft: 12, fontWeight: 600 }}>{format(s.values[hover] ?? 0)}</span>
            </div>
          ))}
        </div>
      )}
      {series.length > 1 && (
        <div className="legend" style={{ marginTop: 6 }}>
          {series.map((s) => (
            <span key={s.key}><i style={{ background: s.color }} />{s.label}</span>
          ))}
        </div>
      )}
    </div>
  );
}
